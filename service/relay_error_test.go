package service

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestShouldRetryRelayErrorHonorsChannelPinOnChannelError(t *testing.T) {
	err := types.NewError(errors.New("channel failed"), types.ErrorCodeChannelNoAvailableKey)
	for _, test := range []struct {
		name      string
		pin       *dto.ChannelPin
		wantRetry bool
	}{
		{name: "unrestricted channel error", wantRetry: true},
		{
			name: "single attempt pin suppresses channel error retry",
			pin: &dto.ChannelPin{
				ChannelId: 1, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt,
			},
			wantRetry: false,
		},
		{
			name: "origin task pin permits retry on the same channel",
			pin: &dto.ChannelPin{
				ChannelId: 1, Source: dto.PinSourceOriginTask, Rank: dto.PinRankOriginTask, RetryMode: dto.PinRetrySameChannel,
			},
			wantRetry: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if test.pin != nil {
				GetChannelConstraints(c).AddPin(*test.pin)
			}
			assert.Equal(t, test.wantRetry, ShouldRetryRelayError(c, err, 1))
		})
	}
}

func TestProcessChannelErrorMasksDisableReasonAndNotification(t *testing.T) {
	previousDB, previousType := model.DB, common.MainDatabaseType()
	previousCache, previousRedis := common.MemoryCacheEnabled, common.RedisEnabled
	previousAutoDisable, previousErrorLog := common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled
	previousNotifyLimit := constant.NotifyLimitCount
	previousClient, previousWorker := httpClient, system_setting.WorkerUrl
	fetch := system_setting.GetFetchSetting()
	previousFetch := *fetch
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.MemoryCacheEnabled, common.RedisEnabled = previousCache, previousRedis
		common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled = previousAutoDisable, previousErrorLog
		constant.NotifyLimitCount = previousNotifyLimit
		httpClient, system_setting.WorkerUrl = previousClient, previousWorker
		*fetch = previousFetch
	})
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.User{}))
	model.DB = database
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled, common.RedisEnabled = false, false
	common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled = true, false
	constant.NotifyLimitCount = 10
	channel := &model.Channel{Name: "relay-review", Key: "fixture-key", Type: 1, Status: common.ChannelStatusEnabled, Group: "default", Models: "test-model"}
	require.NoError(t, channel.Insert())
	notifications := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		notifications <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	httpClient, system_setting.WorkerUrl = server.Client(), ""
	fetch.EnableSSRFProtection = false
	settings, err := common.Marshal(kitdto.UserSetting{NotifyType: kitdto.NotifyTypeWebhook, WebhookUrl: server.URL})
	require.NoError(t, err)
	root := &model.User{Username: "notification-test-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled, Setting: string(settings)}
	require.NoError(t, database.Create(root).Error)
	notifyKey := fmt.Sprintf("%d:%s:%s", root.Id, formatNotifyType(channel.Id, common.ChannelStatusAutoDisabled), time.Now().Format("2006010215"))
	notifyLimitStore.Delete(notifyKey)
	t.Cleanup(func() { notifyLimitStore.Delete(notifyKey) })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, "https://private.example.com")
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream https://private.example.com/path?token=review-token api_key:review-secret"), types.ErrorCodeChannelNoAvailableKey, http.StatusUnauthorized)
	ProcessChannelError(c, types.ChannelError{ChannelId: channel.Id, ChannelName: channel.Name, AutoBan: true}, apiErr, nil)
	var notification WebhookPayload
	select {
	case payload := <-notifications:
		require.NoError(t, common.Unmarshal(payload, &notification))
	case <-time.After(5 * time.Second):
		t.Fatal("automatic channel-disable notification was not delivered")
	}
	loaded, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusAutoDisabled, loaded.Status)
	wantReason := "status_code=401, upstream https://***.com/***?token=*** api_key:***"
	assert.Equal(t, wantReason, loaded.GetOtherInfo()["status_reason"])
	assert.Contains(t, notification.Content, wantReason)
	assert.NotContains(t, notification.Content, "review-token")
	assert.NotContains(t, notification.Content, "review-secret")
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
}

// Only addresses the admin configured are masked: a channel on the base URL
// new-api presets reaches a public official endpoint, and so do adaptors that
// build official endpoints themselves.
func TestUpstreamAddressesMaskOnlyConfiguredAddresses(t *testing.T) {
	for _, tc := range []struct {
		name        string
		channelType int
		baseURL     string
		origin      string
		proxyEnv    string
		message     string
		want        string
	}{
		{
			name:        "preset base URL stays readable",
			channelType: constant.ChannelTypeAnthropic,
			baseURL:     "https://api.anthropic.com",
			origin:      "https://api.anthropic.com",
			message:     "request to https://api.anthropic.com/v1/messages failed, see https://platform.claude.com/docs/en/build-with-claude/task-budgets",
			want:        "request to https://api.anthropic.com/v1/messages failed, see https://platform.claude.com/docs/en/build-with-claude/task-budgets",
		},
		{
			name:        "official endpoint built by the adaptor stays readable",
			channelType: constant.ChannelTypeVertexAi,
			origin:      "https://us-central1-aiplatform.googleapis.com",
			message:     "request to https://us-central1-aiplatform.googleapis.com/v1/projects failed",
			want:        "request to https://us-central1-aiplatform.googleapis.com/v1/projects failed",
		},
		{
			name:        "configured base URL is masked",
			channelType: constant.ChannelTypeOpenAI,
			baseURL:     "https://relay.private.example",
			origin:      "https://relay.private.example",
			message:     "request to https://relay.private.example/v1/chat/completions failed",
			want:        "request to https://***.example/***/***/*** failed",
		},
		{
			name:        "absolute Advanced Custom route is masked",
			channelType: constant.ChannelTypeAdvancedCustom,
			origin:      "https://route.private.example",
			message:     "dial tcp: lookup route.private.example: no such host",
			want:        "dial tcp: lookup ***.***.example: no such host",
		},
		{
			name:        "environment proxy is masked on a preset channel",
			channelType: constant.ChannelTypeAnthropic,
			baseURL:     "https://api.anthropic.com",
			proxyEnv:    "http://egress.private.invalid:8080",
			message:     "proxyconnect tcp: dial tcp: lookup egress.private.invalid: no such host",
			want:        "proxyconnect tcp: dial tcp: lookup ***.***.invalid: no such host",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HTTPS_PROXY", tc.proxyEnv)
			t.Setenv("HTTP_PROXY", "")
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			common.SetContextKey(c, constant.ContextKeyChannelType, tc.channelType)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, tc.baseURL)
			common.SetContextKey(c, constant.ContextKeyUpstreamRequestOrigin, tc.origin)
			apiErr := types.NewErrorWithStatusCode(errors.New(tc.message), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)

			assert.Equal(t, tc.want, apiErr.ToOpenAIError(common.UpstreamAddresses(c)...).Message)
		})
	}
}

func TestDecideRelayRetryReasons(t *testing.T) {
	upstream := func(status int) *types.NewAPIError {
		return types.NewOpenAIError(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, status)
	}
	for _, tc := range []struct {
		name    string
		err     *types.NewAPIError
		retries int
		setup   func(*gin.Context)
		want    PolicyDecision
	}{
		{name: "retry status matched", err: upstream(http.StatusTooManyRequests), retries: 1, want: PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"}},
		{name: "status outside retry rules", err: upstream(http.StatusBadRequest), retries: 1, want: PolicyDecision{Action: "stop", Reason: "status_not_retryable", Source: "global"}},
		{name: "attempt budget exhausted", err: upstream(http.StatusTooManyRequests), retries: 0, want: PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}},
		{name: "always skipped status", err: upstream(http.StatusGatewayTimeout), retries: 1, want: PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}},
		{name: "524 without fallback stays excluded", err: upstream(524), retries: 1, want: PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}},
		{name: "success status never retries", err: upstream(http.StatusOK), retries: 1, want: PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}},
		{name: "skip retry error", err: types.NewErrorWithStatusCode(errors.New("local"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry()), retries: 1, want: PolicyDecision{Action: "stop", Reason: "non_retryable_error", Source: "system"}},
		{name: "channel error retries without budget", err: types.NewError(errors.New("no key"), types.ErrorCodeChannelNoAvailableKey), retries: 0, want: PolicyDecision{Action: "retry", Reason: "channel_error", Source: "system"}},
		{name: "single attempt pin", err: upstream(http.StatusTooManyRequests), retries: 1, setup: func(c *gin.Context) {
			GetChannelConstraints(c).AddPin(dto.ChannelPin{ChannelId: 1, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt})
		}, want: PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}},
		{name: "strict session", err: upstream(http.StatusTooManyRequests), retries: 1, setup: func(c *gin.Context) {
			c.Set(ginKeyChannelAffinitySkipRetry, true)
			RequestPolicy(c).SessionModeSource = "global"
		}, want: PolicyDecision{Action: "stop", Reason: "strict_session", Source: "global"}},
		{name: "nil error", retries: 1, want: PolicyDecision{Action: "stop", Reason: "request_completed", Source: "system"}},
		{name: "media failure ignores eight retry budget", err: upstream(http.StatusBadGateway), retries: 8, setup: func(c *gin.Context) {
			MarkMediaGenerationRequest(c, &relaycommon.RelayInfo{OriginModelName: "gpt-image-2.5-sunburst"})
		}, want: PolicyDecision{Action: "stop", Reason: "media_generation_no_retry", Source: "request_type"}},
		{name: "media channel error cannot bypass gate", err: types.NewError(errors.New("no key"), types.ErrorCodeChannelNoAvailableKey), retries: 8, setup: func(c *gin.Context) {
			MarkMediaGenerationRequest(c, &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAIImage})
		}, want: PolicyDecision{Action: "stop", Reason: "media_generation_no_retry", Source: "request_type"}},
		{name: "media gate precedes failover", err: upstream(http.StatusServiceUnavailable), retries: 8, setup: func(c *gin.Context) {
			c.Set(channelFailoverContextKey, &channelFailoverRequest{order: []int{1, 2}, visited: map[int]bool{1: true}, current: 1})
			MarkMediaGenerationRequest(c, &relaycommon.RelayInfo{OriginModelName: "sora-2"})
		}, want: PolicyDecision{Action: "stop", Reason: "media_generation_no_retry", Source: "request_type"}},
		{name: "media success keeps completion reason", retries: 8, setup: func(c *gin.Context) {
			MarkMediaGenerationRequest(c, &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAIImage})
		}, want: PolicyDecision{Action: "stop", Reason: "request_completed", Source: "system"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if tc.setup != nil {
				tc.setup(c)
			}
			decision := DecideRelayRetry(c, tc.err, tc.retries)
			assert.Equal(t, tc.want, decision)
			assert.Equal(t, tc.want.Action == "retry", ShouldRetryRelayError(c, tc.err, tc.retries))
		})
	}
}

func TestMarkMediaGenerationRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		info    relaycommon.RelayInfo
		request kitdto.Request
		raw     string
		want    bool
	}{
		{name: "image protocol", info: relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAIImage}, want: true},
		{name: "image edits mode", info: relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeImagesEdits}, want: true},
		{name: "video submission mode", info: relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeVideoSubmit}, want: true},
		{name: "midjourney generation", info: relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeMidjourneyImagine}, want: true},
		{name: "midjourney describe is understanding", info: relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeMidjourneyDescribe}},
		{name: "task video action", info: relaycommon.RelayInfo{RelayFormat: types.RelayFormatTask, TaskRelayInfo: &relaycommon.TaskRelayInfo{Action: constant.TaskActionTextToVideo}}, want: true},
		{name: "music shares video submit enum", info: relaycommon.RelayInfo{RelayFormat: types.RelayFormatTask, RelayMode: relayconstant.RelayModeVideoSubmit, OriginModelName: "suno_music"}},
		{name: "legacy task constructor leaves format empty", info: relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeVideoSubmit, TaskRelayInfo: &relaycommon.TaskRelayInfo{Action: "generate"}, OriginModelName: "suno_music"}},
		{name: "lyrics task", info: relaycommon.RelayInfo{RelayFormat: types.RelayFormatTask, OriginModelName: "suno_lyrics"}},
		{name: "speech stays retryable", info: relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAIAudio, RelayMode: relayconstant.RelayModeAudioSpeech, OriginModelName: "gpt-4o-mini-tts"}},
		{name: "responses image tool", request: &kitdto.OpenAIResponsesRequest{}, raw: `{"tools":[{"type":"image_generation"}]}`, want: true},
		{name: "responses video tool", request: &kitdto.OpenAIResponsesRequest{}, raw: `{"tools":[{"type":"video_generation"}]}`, want: true},
		{name: "responses function name is not builtin", request: &kitdto.OpenAIResponsesRequest{}, raw: `{"tools":[{"type":"function","name":"image_generation"}]}`},
		{name: "responses input image and prompt ignored", request: &kitdto.OpenAIResponsesRequest{}, raw: `{"model":"gpt-4o","input":[{"type":"message","content":[{"type":"input_image","image_url":"data:image/png;base64,AA"},{"type":"input_text","text":"image_generation video_generation"}]}]}`},
		{name: "chat output image", request: &kitdto.GeneralOpenAIRequest{}, raw: `{"modalities":["text","image"]}`, want: true},
		{name: "chat output video", request: &kitdto.GeneralOpenAIRequest{}, raw: `{"modalities":["VIDEO"]}`, want: true},
		{name: "chat output audio", request: &kitdto.GeneralOpenAIRequest{}, raw: `{"modalities":["text","audio"]}`},
		{name: "chat image tool", request: &kitdto.GeneralOpenAIRequest{}, raw: `{"tools":[{"type":"image_generation"}]}`, want: true},
		{name: "chat function name ignored", request: &kitdto.GeneralOpenAIRequest{}, raw: `{"tools":[{"type":"function","function":{"name":"image_generation"}}]}`},
		{name: "chat image config", request: &kitdto.GeneralOpenAIRequest{}, raw: `{"imageConfig":{}}`, want: true},
		{name: "chat input video", request: &kitdto.GeneralOpenAIRequest{}, raw: `{"model":"video-understanding","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"https://example.invalid/input.mp4"}}]}]}`},
		{name: "gemini image output", request: &kitdto.GeminiChatRequest{}, raw: `{"generationConfig":{"responseModalities":["TEXT","IMAGE"]}}`, want: true},
		{name: "gemini snake image config", request: &kitdto.GeminiChatRequest{}, raw: `{"generationConfig":{"image_config":{}}}`, want: true},
		{name: "gemini null image config", request: &kitdto.GeminiChatRequest{}, raw: `{"generationConfig":{"responseModalities":["TEXT"],"imageConfig":null}}`},
		{name: "gemini input media", request: &kitdto.GeminiChatRequest{}, raw: `{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"AA"}},{"fileData":{"mimeType":"video/mp4","fileUri":"https://example.invalid/input.mp4"}}]}]}`},
		{name: "typed nil request", request: (*kitdto.OpenAIResponsesRequest)(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.raw != "" {
				require.NoError(t, common.UnmarshalJsonStr(tc.raw, tc.request))
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			tc.info.Request = tc.request
			MarkMediaGenerationRequest(c, &tc.info)
			assert.Equal(t, tc.want, IsMediaGenerationRequest(c))
			MarkMediaGenerationRequest(c, &relaycommon.RelayInfo{OriginModelName: "gpt-4o"})
			MarkMediaGenerationRequest(c, nil)
			assert.Equal(t, tc.want, IsMediaGenerationRequest(c), "later conversions cannot clear generation intent")
		})
	}
	assert.False(t, IsMediaGenerationRequest(nil))
	MarkMediaGenerationRequest(nil, nil)
}

func TestMediaGenerationModelClassification(t *testing.T) {
	previousDB := model.DB
	model.DB = nil
	t.Cleanup(func() { model.DB = previousDB })
	for _, tc := range []struct {
		model string
		want  bool
	}{
		{"gpt-image-2", true}, {"gpt-image-2.5-flare", true}, {"gpt-image-2.5-sunburst", true},
		{"dall-e-3", true}, {"black-forest-labs/FLUX.1-dev", true}, {"qwen-image-edit", true},
		{"google/gemini-3.1-flash-image-preview", true}, {"gemini-2.0-flash-exp", true},
		{"doubao-seedream-4-0-250828", true}, {"seedance-1-0-pro", true}, {"imagen-4.0-generate-001", true},
		{"grok-imagine-image", true}, {"grok-imagine-video-1.5", true}, {"sora-2-pro", true},
		{"veo-3.1-generate-preview", true}, {"kling-v3", true}, {"wan2.7-i2v", true},
		{"gpt-4o", false}, {"gemini-2.5-pro", false}, {"qwen2.5-vl", false},
		{"image-understanding", false}, {"video-understanding", false}, {"suno_music", false},
		{"gpt-4o-mini-tts", false}, {"unknown-model-with-empty-catalog", false},
	} {
		t.Run(tc.model, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			info := &relaycommon.RelayInfo{OriginModelName: "custom-alias", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: tc.model}}
			MarkMediaGenerationRequest(c, info)
			assert.Equal(t, tc.want, IsMediaGenerationRequest(c), "mapped generation identity works without a database")
		})
	}
}

func TestMarkMediaGenerationRequestBody(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"override adds image tool", `{"model":"gpt-4o","tools":[{"type":"image_generation"}]}`, true},
		{"override adds video tool", `{"tools":[{"type":"video_generation"}]}`, true},
		{"override maps model", `{"model":"gpt-image-2.5-sunburst"}`, true},
		{"chat modalities", `{"modalities":["text","image"]}`, true},
		{"gemini output modalities", `{"generationConfig":{"responseModalities":["IMAGE"]}}`, true},
		{"gemini output video", `{"generationConfig":{"responseModalities":["VIDEO"]}}`, true},
		{"gemini image config", `{"generationConfig":{"imageConfig":{}}}`, true},
		{"gemini snake modalities", `{"generation_config":{"response_modalities":["IMAGE"]}}`, true},
		{"gemini snake config", `{"generation_config":{"image_config":{}}}`, true},
		{"chat image config", `{"imageConfig":{}}`, true},
		{"chat google image config", `{"extra_body":{"google":{"image_config":{"aspect_ratio":"1:1"}}}}`, true},
		{"function name does not generate", `{"tools":[{"type":"function","name":"image_generation","function":{"name":"video_generation"}}]}`, false},
		{"input media and prompt ignored", `{"model":"gpt-4o","prompt":"image_generation","messages":[{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}},{"type":"video_url","video_url":{"url":"https://example.invalid/input.mp4"}}]}]}`, false},
		{"audio output unchanged", `{"model":"gpt-4o-audio-preview","modalities":["text","audio"]}`, false},
		{"generation model remains single attempt for text output", `{"model":"gemini-3-pro-image","modalities":["text"]}`, true},
		{"null image config", `{"generationConfig":{"responseModalities":["TEXT"],"imageConfig":null}}`, false},
		{"empty body", ``, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			info := &relaycommon.RelayInfo{OriginModelName: "gpt-4o"}
			MarkMediaGenerationRequestBody(c, info, []byte(tc.body))
			assert.Equal(t, tc.want, IsMediaGenerationRequest(c))
			MarkMediaGenerationRequestBody(c, info, []byte(`{"modalities":["text"]}`))
			assert.Equal(t, tc.want, IsMediaGenerationRequest(c), "an override cannot undo an earlier generation match")
		})
	}
}

func TestMediaGenerationPluginModelScopes(t *testing.T) {
	registry := jsplugin.NewRegistry()
	plugin := &jsplugin.LoadedPlugin{Meta: jsplugin.Meta{
		Key: "mixed-media", Models: []string{"custom-picture", "custom-movie", "custom-music"},
		Protocols: []jsplugin.ProtocolClaim{
			{Name: jsplugin.ProtocolOpenAIImage, Models: []string{"custom-picture"}},
			{Name: "openai_video", Models: []string{"custom-movie"}},
		},
	}}
	require.NoError(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{plugin}))
	for _, tc := range []struct {
		model string
		want  bool
	}{{"custom-picture", true}, {"CUSTOM-MOVIE", true}, {"custom-music", false}} {
		t.Run(tc.model, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set(jsplugin.ContextKeyPinnedPlugin, jsplugin.PinnedPlugin{Plugin: plugin, Generation: registry.Generation()})
			MarkMediaGenerationRequest(c, &relaycommon.RelayInfo{RelayFormat: types.RelayFormatTask, RelayMode: relayconstant.RelayModeVideoSubmit, OriginModelName: tc.model})
			assert.Equal(t, tc.want, IsMediaGenerationRequest(c))
			bodyContext, _ := gin.CreateTestContext(httptest.NewRecorder())
			bodyContext.Set(jsplugin.ContextKeyPinnedPlugin, jsplugin.PinnedPlugin{Plugin: plugin, Generation: registry.Generation()})
			body, err := common.Marshal(map[string]string{"model": tc.model})
			require.NoError(t, err)
			MarkMediaGenerationRequestBody(bodyContext, nil, body)
			assert.Equal(t, tc.want, IsMediaGenerationRequest(bodyContext), "final model uses the request's pinned plugin generation")
		})
	}
}

func TestRequestPolicyEventsReachLogAdminInfo(t *testing.T) {
	previousAutoDisable := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = previousAutoDisable })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("auto_ban", true)
	c.Set("channel_id", 7)
	state := RequestPolicy(c)
	state.BeginAttempt(&model.Channel{Id: 7}, "default")
	apiErr := types.NewOpenAIError(errors.New("invalid credential"), types.ErrorCodeBadResponseStatusCode, http.StatusUnauthorized)
	RecordPolicyFailure(c, 7, apiErr, DecideRelayRetry(c, apiErr, 0))

	failed := model.NewLogOther()
	AppendRelayLogAdminInfo(c, nil, failed)
	events, ok := failed.Snapshot()["admin_info"].(map[string]any)["request_policy"].([]PolicyEvent)
	require.True(t, ok, "a failed relay exposes its decision events to administrators")
	require.Len(t, events, 3)
	assert.Equal(t, PolicyDecision{Action: "attempt", Reason: "channel_selected", Source: "routing"}, events[0].Decision)
	assert.Equal(t, "default", events[0].Group)
	assert.Equal(t, PolicyDecision{Action: "failure", Reason: "upstream_failure", Source: "upstream"}, events[1].Decision)
	assert.Equal(t, http.StatusUnauthorized, events[1].Status)
	assert.Equal(t, PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}, events[2].Decision)
	assert.Equal(t, "channel_disable_requested", events[2].Health, "the health entry follows the automatic disable rules")
	common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, true)
	RecordPolicyFailure(c, 7, apiErr, DecideRelayRetry(c, apiErr, 0))
	assert.Equal(t, "key_disable_requested", state.Events()[4].Health)

	state.BeginAttempt(&model.Channel{Id: 8}, "default")
	c.Set("channel_id", 8)
	MarkRequestPolicySuccess(c, nil)
	MarkRequestPolicySuccess(c, nil)
	succeeded := model.NewLogOther()
	AppendRelayLogAdminInfo(c, nil, succeeded)
	events, ok = succeeded.Snapshot()["admin_info"].(map[string]any)["request_policy"].([]PolicyEvent)
	require.True(t, ok, "a successful relay exposes its decision events to administrators")
	require.Len(t, events, 7, "the outcome is recorded once")
	assert.Equal(t, PolicyDecision{Action: "success", Reason: "request_completed", Source: "upstream"}, events[6].Decision)
	assert.Equal(t, 8, events[6].ChannelID)
	assert.Equal(t, 2, events[6].Attempt)
	assert.True(t, state.Successful)

	untouched, _ := gin.CreateTestContext(httptest.NewRecorder())
	other := model.NewLogOther()
	AppendRelayLogAdminInfo(untouched, nil, other)
	assert.NotContains(t, other.Snapshot()["admin_info"], "request_policy", "requests without decisions do not carry an empty record")
}
