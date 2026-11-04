package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	appdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relaychannel "github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// normalizeResponsesWSTestMessage runs the read-loop envelope parse followed by
// request normalization, exactly as the session does for one response.create.
func normalizeResponsesWSTestMessage(message []byte) (responsesWSCreateRequest, string, error) {
	envelope, streamID, err := parseResponsesWSEnvelope(message)
	if err != nil {
		return responsesWSCreateRequest{StreamID: streamID}, envelope.EventID, err
	}
	create, err := normalizeResponsesWSCreateEvent(message, envelope, streamID)
	return create, envelope.EventID, err
}

func TestNormalizeResponsesWSMaxOutputTokens(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{value: "0", valid: true},
		{value: "1073741823", valid: true},
		{value: "1073741824"},
		{value: "18446744073686646784"},
		{value: "-1"},
	} {
		for _, wrapped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wrapped=%t", tc.value, wrapped), func(t *testing.T) {
				fields := `"model":"gpt-5.1","input":"hi","max_output_tokens":` + tc.value
				payload := `{"type":"response.create",` + fields + `}`
				if wrapped {
					payload = `{"type":"response.create","response":{` + fields + `}}`
				}
				create, _, err := normalizeResponsesWSTestMessage([]byte(payload))
				if !tc.valid {
					require.Error(t, err)
					assert.Equal(t, http.StatusBadRequest, newResponsesWSInvalidRequestError(err).StatusCode)
					return
				}
				require.NoError(t, err)
				require.NotNil(t, create.Request.MaxOutputTokens)
				assert.Equal(t, tc.value, fmt.Sprint(*create.Request.MaxOutputTokens))
			})
		}
	}
}

func TestSelectResponsesWSChannelHonorsPinsAndFilters(t *testing.T) {
	require.NoError(t, i18n.Init())
	database := setupRelayChannelDB(t)
	enabled := &model.Channel{Name: "enabled", Key: "sk-test", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI}
	enabled.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
	wsDisabled := &model.Channel{Name: "ws-disabled", Key: "sk-test", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI}
	disabled := &model.Channel{Name: "disabled", Key: "sk-test", Status: common.ChannelStatusManuallyDisabled, Type: constant.ChannelTypeOpenAI}
	filtered := &model.Channel{Name: "filtered", Key: "sk-test", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeAdvancedCustom}
	for _, channel := range []*model.Channel{enabled, disabled, filtered, wsDisabled} {
		require.NoError(t, database.Create(channel).Error)
	}
	for _, tc := range []struct {
		name      string
		channelID int
		status    int
	}{
		{name: "token pin overrides origin pin", channelID: enabled.Id},
		{name: "disabled pin rejects", channelID: disabled.Id, status: http.StatusForbidden},
		{name: "pin cannot bypass websocket switch", channelID: wsDisabled.Id, status: http.StatusBadRequest},
		{name: "pin cannot bypass path filter", channelID: filtered.Id, status: http.StatusBadRequest},
		{name: "missing pin rejects", channelID: 99999, status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			constraints := service.GetChannelConstraints(c)
			constraints.AddPin(appdto.ChannelPin{ChannelId: disabled.Id, Source: appdto.PinSourceOriginTask, Rank: appdto.PinRankOriginTask, RetryMode: appdto.PinRetrySameChannel})
			constraints.AddPin(appdto.ChannelPin{ChannelId: tc.channelID, Source: appdto.PinSourceToken, Rank: appdto.PinRankToken, RetryMode: appdto.PinRetrySingleAttempt})
			constraints.AddFilter(appdto.ChannelFilter{Kind: appdto.FilterRequestPath, RequestPath: c.Request.URL.Path})
			channel, apiErr := selectResponsesWSChannel(c, "gpt-5.1", &service.RetryParam{Ctx: c, ModelName: "gpt-5.1", TokenGroup: "default"})
			if tc.status != 0 {
				require.NotNil(t, apiErr)
				assert.Equal(t, tc.status, apiErr.StatusCode)
				assert.Nil(t, channel)
				assert.False(t, service.ShouldRetryRelayError(c, apiErr, 2))
				return
			}
			require.Nil(t, apiErr)
			require.NotNil(t, channel)
			assert.Equal(t, enabled.Id, channel.Id)
			assert.Equal(t, enabled.Id, common.GetContextKeyInt(c, constant.ContextKeyChannelId))
			assert.False(t, service.ShouldRetryRelayError(c, types.NewErrorWithStatusCode(errors.New("upstream failed"), types.ErrorCodeDoRequestFailed, 503), 2))
		})
	}
}

// The WebSocket relay must admit the same model names as the HTTP distributor
// when a token restricts models (reasoning suffixes and @modifiers included).
func TestCheckResponsesWSModelAccessMatchesHTTPTokenLimits(t *testing.T) {
	for _, tc := range []struct {
		model  string
		status int
	}{
		{model: "gpt-5.1"},
		{model: "gpt-5.1-high"},
		{model: "gpt-5.1@thinking:on"},
		{model: "gpt-4o", status: http.StatusForbidden},
	} {
		t.Run(tc.model, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			common.SetContextKey(c, constant.ContextKeyTokenModelLimitEnabled, true)
			common.SetContextKey(c, constant.ContextKeyTokenModelLimit, map[string]bool{"gpt-5.1": true})
			apiErr := checkResponsesWSModelAccess(c, tc.model)
			if tc.status == 0 {
				assert.Nil(t, apiErr)
				return
			}
			require.NotNil(t, apiErr)
			assert.Equal(t, tc.status, apiErr.StatusCode)
			assert.False(t, service.ShouldRetryRelayError(c, apiErr, 2))
		})
	}
}

func TestSelectResponsesWSChannelAcceptsNativeResponsesChannelTypes(t *testing.T) {
	require.NoError(t, i18n.Init())
	database := setupRelayChannelDB(t)
	nativeRoute := &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", UpstreamPath: "/v1/responses"}}}
	noneRoute := &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", UpstreamPath: "/v1/responses", Converter: "none"}}}
	convertedRoute := &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", UpstreamPath: "/v1/chat/completions", Converter: "openai_responses_to_openai_chat_completions"}}}
	chatRoute := &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/chat/completions", UpstreamPath: "/v1/chat/completions"}}}
	for _, tc := range []struct {
		name        string
		channelType int
		advanced    *dto.AdvancedCustomConfig
		rejectedBy  appdto.ChannelFilterKind
	}{
		{name: "azure", channelType: constant.ChannelTypeAzure},
		{name: "new api", channelType: constant.ChannelTypeNewAPI},
		{name: "sub2api", channelType: constant.ChannelTypeSub2API},
		{name: "advanced custom native responses route", channelType: constant.ChannelTypeAdvancedCustom, advanced: nativeRoute},
		{name: "advanced custom explicit none converter", channelType: constant.ChannelTypeAdvancedCustom, advanced: noneRoute},
		{name: "advanced custom converter route", channelType: constant.ChannelTypeAdvancedCustom, advanced: convertedRoute, rejectedBy: appdto.FilterResponsesWebSocket},
		{name: "advanced custom without responses route", channelType: constant.ChannelTypeAdvancedCustom, advanced: chatRoute, rejectedBy: appdto.FilterRequestPath},
		{name: "anthropic", channelType: constant.ChannelTypeAnthropic, rejectedBy: appdto.FilterResponsesWebSocket},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := &model.Channel{Name: tc.name, Key: "sk-test", Status: common.ChannelStatusEnabled, Type: tc.channelType}
			channel.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
			channel.SetOtherSettings(dto.ChannelOtherSettings{AdvancedCustom: tc.advanced})
			require.NoError(t, database.Create(channel).Error)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			constraints := service.GetChannelConstraints(c)
			constraints.AddPin(appdto.ChannelPin{ChannelId: channel.Id, Source: appdto.PinSourceToken, Rank: appdto.PinRankToken, RetryMode: appdto.PinRetrySingleAttempt})
			constraints.AddFilter(appdto.ChannelFilter{Kind: appdto.FilterRequestPath, RequestPath: c.Request.URL.Path})
			selected, apiErr := selectResponsesWSChannel(c, "ws-model", &service.RetryParam{Ctx: c, ModelName: "ws-model", TokenGroup: "default"})
			if tc.rejectedBy != "" {
				require.NotNil(t, apiErr)
				assert.Nil(t, selected)
				assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
				assert.Equal(t, types.ErrorCode(tc.rejectedBy), apiErr.GetErrorCode())
				return
			}
			require.Nil(t, apiErr)
			require.NotNil(t, selected)
			assert.Equal(t, channel.Id, selected.Id)
		})
	}
}

func TestResponsesWSAzureDialUsesAzureResponsesURLAndAPIKey(t *testing.T) {
	for _, tc := range []struct {
		name           string
		host           string
		responsesVer   string
		wantPath       string
		wantAPIVersion string
		channelAPIVer  string
	}{
		{name: "openai v1 default preview", wantPath: "/openai/v1/responses", wantAPIVersion: "preview"},
		{name: "channel responses version", responsesVer: "2025-04-01-preview", wantPath: "/openai/v1/responses", wantAPIVersion: "2025-04-01-preview"},
		{name: "cognitiveservices uses channel api version", host: "cognitiveservices.azure.com", channelAPIVer: "2025-03-01-preview", wantPath: "/openai/responses", wantAPIVersion: "2025-03-01-preview"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handshake := make(chan *http.Request, 1)
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handshake <- r
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				_ = conn.Close()
			}))
			defer server.Close()

			// The host check only inspects the base URL text, so a path segment
			// is enough to select the cognitiveservices URL form.
			baseURL := server.URL
			if tc.host != "" {
				baseURL += "/" + tc.host
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			info := &relaycommon.RelayInfo{
				RelayMode:      relayconstant.RelayModeResponses,
				RequestURLPath: "/v1/responses",
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelType:          constant.ChannelTypeAzure,
					ChannelBaseUrl:       baseURL,
					ApiType:              constant.APITypeOpenAI,
					ApiVersion:           tc.channelAPIVer,
					ApiKey:               "azure-key",
					UpstreamModelName:    "gpt-test",
					ChannelOtherSettings: dto.ChannelOtherSettings{AzureResponsesVersion: tc.responsesVer},
				},
			}
			adaptor := GetAdaptor(info.ApiType)
			adaptor.Init(info)
			target, err := relaychannel.DoWssRequest(adaptor, c, info, nil)
			require.NoError(t, err)
			_ = target.Close()

			r := <-handshake
			assert.Equal(t, strings.TrimPrefix(baseURL, server.URL)+tc.wantPath, r.URL.Path)
			assert.Equal(t, tc.wantAPIVersion, r.URL.Query().Get("api-version"))
			assert.Equal(t, "azure-key", r.Header.Get("api-key"))
			assert.Empty(t, r.Header.Get("Authorization"))
		})
	}
}

func TestRestoreConnectionContextRejectsChangedAdvancedCustomRoute(t *testing.T) {
	database := setupRelayChannelDB(t)
	baseURL := "http://upstream.example"
	channel := &model.Channel{Name: "advanced", Key: "sk-test", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeAdvancedCustom, BaseURL: &baseURL}
	channel.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
	channel.SetOtherSettings(dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", UpstreamPath: "/v1/responses"}}}})
	require.NoError(t, database.Create(channel).Error)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	service.GetChannelConstraints(c).AddPin(appdto.ChannelPin{ChannelId: channel.Id, Source: appdto.PinSourceToken, Rank: appdto.PinRankToken, RetryMode: appdto.PinRetrySingleAttempt})
	route, ok := channel.GetOtherSettings().AdvancedCustom.MatchPathForModel("/v1/responses", "ws-model")
	require.True(t, ok)
	session := &responsesWSSession{lockedChannelID: channel.Id, lockedModel: "ws-model", lockedKey: "sk-test", lockedRoute: route,
		lockedContext: map[constant.ContextKey]any{constant.ContextKeyChannelBaseUrl: channel.GetBaseURL(), constant.ContextKeyChannelHeaderOverride: channel.GetHeaderOverride()}}
	require.Nil(t, session.restoreConnectionContext(c, "ws-model"))

	// Request-level edits (model list, explicit none converter) keep the connection.
	channel.SetOtherSettings(dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", UpstreamPath: "/v1/responses", Converter: "none", Models: []string{"ws-model", "other-model"}}}}})
	require.NoError(t, database.Model(channel).Update("settings", channel.OtherSettings).Error)
	require.Nil(t, session.restoreConnectionContext(c, "ws-model"))

	channel.SetOtherSettings(dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", UpstreamPath: "/v2/responses"}}}})
	require.NoError(t, database.Model(channel).Update("settings", channel.OtherSettings).Error)
	apiErr := session.restoreConnectionContext(c, "ws-model")
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	assert.ErrorContains(t, apiErr, "upstream route changed")
}

func TestResponsesWSChannelRoutingRequiresExplicitOptIn(t *testing.T) {
	require.NoError(t, i18n.Init())
	database := setupRelayChannelDB(t)
	require.NoError(t, database.AutoMigrate(&model.Ability{}))
	legacy := &model.Channel{Name: "legacy-http", Key: "sk-test", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Group: "default", Models: "ws-model", Priority: common.GetPointer(int64(10))}
	enabled := &model.Channel{Name: "websocket", Key: "sk-test", Type: constant.ChannelTypeCodex, Status: common.ChannelStatusEnabled, Group: "default", Models: "ws-model", Priority: common.GetPointer(int64(0))}
	unsupported := &model.Channel{Name: "unsupported", Key: "sk-test", Type: constant.ChannelTypeAnthropic, Status: common.ChannelStatusEnabled, Group: "default", Models: "ws-model", Priority: common.GetPointer(int64(5))}
	enabled.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
	unsupported.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
	for _, channel := range []*model.Channel{legacy, enabled, unsupported} {
		require.NoError(t, database.Create(channel).Error)
		require.NoError(t, database.Create(&model.Ability{ChannelId: channel.Id, Model: "ws-model", Group: "default", Enabled: true, Priority: channel.Priority}).Error)
	}
	previousCache := common.MemoryCacheEnabled
	t.Cleanup(func() {
		defer func() { common.MemoryCacheEnabled = previousCache }()
		ids := []int{legacy.Id, enabled.Id, unsupported.Id}
		require.NoError(t, database.Where("channel_id IN ?", ids).Delete(&model.Ability{}).Error)
		require.NoError(t, database.Where("id IN ?", ids).Delete(&model.Channel{}).Error)
		model.InitChannelCache()
	})
	common.MemoryCacheEnabled = true
	model.InitChannelCache()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	params := &service.RetryParam{Ctx: c, ModelName: "ws-model", TokenGroup: "default"}
	channel, apiErr := selectResponsesWSChannel(c, "ws-model", params)
	require.Nil(t, apiErr)
	require.NotNil(t, channel)
	assert.Equal(t, enabled.Id, channel.Id)
	httpChannel, err := model.GetRandomSatisfiedChannel("default", "ws-model", 0, nil)
	require.NoError(t, err)
	require.NotNil(t, httpChannel)
	assert.Equal(t, legacy.Id, httpChannel.Id)

	// Disabling the saved setting takes effect for the next create on an existing session.
	enabled.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: false})
	require.NoError(t, database.Model(enabled).Update("setting", enabled.Setting).Error)
	model.InitChannelCache()
	session := &responsesWSSession{lockedChannelID: enabled.Id, lockedModel: "ws-model"}
	apiErr = session.restoreConnectionContext(c, "ws-model")
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	channel, apiErr = selectResponsesWSChannel(c, "ws-model", params)
	require.NotNil(t, apiErr)
	assert.Nil(t, channel)
}

// Session affinity follows the HTTP distributor: a strict binding to an
// unusable channel fails the request, a prefer binding falls back, and an
// unusable binding is dropped from the cache either way.
func TestSelectResponsesWSChannelHonorsStrictSessionBinding(t *testing.T) {
	require.NoError(t, i18n.Init())
	database := setupRelayChannelDB(t)
	require.NoError(t, database.AutoMigrate(&model.Ability{}))
	bound := &model.Channel{Name: "bound", Key: "sk-test", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusAutoDisabled, Group: "default", Models: "ws-model", Priority: common.GetPointer(int64(10))}
	fallback := &model.Channel{Name: "fallback", Key: "sk-test", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Group: "default", Models: "ws-model", Priority: common.GetPointer(int64(0))}
	for _, channel := range []*model.Channel{bound, fallback} {
		channel.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
		require.NoError(t, database.Create(channel).Error)
		require.NoError(t, database.Create(&model.Ability{ChannelId: channel.Id, Model: "ws-model", Group: "default", Enabled: channel.Status == common.ChannelStatusEnabled, Priority: channel.Priority}).Error)
	}
	previousCache := common.MemoryCacheEnabled
	t.Cleanup(func() {
		defer func() { common.MemoryCacheEnabled = previousCache }()
		ids := []int{bound.Id, fallback.Id}
		require.NoError(t, database.Where("channel_id IN ?", ids).Delete(&model.Ability{}).Error)
		require.NoError(t, database.Where("id IN ?", ids).Delete(&model.Channel{}).Error)
		model.InitChannelCache()
	})
	common.MemoryCacheEnabled = true
	model.InitChannelCache()
	affinity := operation_setting.GetChannelAffinitySetting()
	previousAffinity := *affinity
	t.Cleanup(func() { *affinity = previousAffinity })
	snapshot, err := model.BuildRequestPolicy(map[string]string{
		"channel_affinity_setting.enabled":      "true",
		"channel_affinity_setting.session_mode": "strict",
		"channel_affinity_setting.rules":        `[{"name":"session","model_regex":[".*"],"key_sources":[{"type":"request_header","key":"X-Session"}],"session_mode":"inherit"}]`,
	})
	require.NoError(t, err)
	*affinity = snapshot.Affinity
	newSessionContext := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
		c.Request.Header.Set("X-Session", t.Name())
		return c
	}
	seed := newSessionContext()
	_, found := service.GetPreferredChannelByAffinity(seed, "ws-model", "default")
	require.False(t, found)
	seed.Set("channel_id", bound.Id)
	service.RecordChannelAffinity(seed, bound.Id)
	t.Cleanup(func() { service.ClearCurrentChannelAffinityCache(seed) })

	strict := newSessionContext()
	channel, apiErr := selectResponsesWSChannel(strict, "ws-model", &service.RetryParam{Ctx: strict, ModelName: "ws-model", TokenGroup: "default", Retry: common.GetPointer(0)})
	require.NotNil(t, apiErr)
	assert.Nil(t, channel)
	assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
	assert.False(t, service.ShouldRetryRelayError(strict, apiErr, 2))
	_, found = service.GetPreferredChannelByAffinity(seed, "ws-model", "default")
	assert.False(t, found, "an unusable binding is cleared unless keep_on_channel_disabled is set")

	affinity.SessionMode = "prefer"
	service.RecordChannelAffinity(seed, bound.Id)
	prefer := newSessionContext()
	channel, apiErr = selectResponsesWSChannel(prefer, "ws-model", &service.RetryParam{Ctx: prefer, ModelName: "ws-model", TokenGroup: "default", Retry: common.GetPointer(0)})
	require.Nil(t, apiErr)
	require.NotNil(t, channel)
	assert.Equal(t, fallback.Id, channel.Id)
}

func TestNormalizeResponsesWSCreateEventWrapper(t *testing.T) {
	message := []byte(`{
		"type": "response.create",
		"event_id": "evt_1",
		"generate": false,
		"response": {
			"model": "gpt-5.3-codex-spark",
			"input": "hi",
			"store": false,
			"stream": true,
			"stream_options": {"include_usage": true}
		}
	}`)

	create, eventID, err := normalizeResponsesWSTestMessage(message)
	if err != nil {
		t.Fatalf("normalizeResponsesWSTestMessage() error = %v", err)
	}
	req := create.Request
	if eventID != "evt_1" {
		t.Fatalf("eventID = %q, want evt_1", eventID)
	}
	if req.Model != "gpt-5.3-codex-spark" {
		t.Fatalf("model = %q", req.Model)
	}
	if strings.TrimSpace(string(create.Generate)) != "false" {
		t.Fatalf("generate = %s, want false", create.Generate)
	}
	if req.Stream != nil {
		t.Fatalf("stream = %v, want nil", req.Stream)
	}
	if req.StreamOptions != nil {
		t.Fatalf("stream_options = %#v, want nil", req.StreamOptions)
	}
	if strings.TrimSpace(string(req.Store)) != "false" {
		t.Fatalf("store = %s, want false", req.Store)
	}
}

func TestNormalizeResponsesWSCreateEventFlat(t *testing.T) {
	message := []byte(`{
		"type": "response.create",
		"event_id": "evt_2",
		"model": "gpt-5.3-codex-spark",
		"input": "hi",
		"generate": false,
		"stream": true,
		"background": true,
		"stream_options": {"include_usage": true}
	}`)

	create, eventID, err := normalizeResponsesWSTestMessage(message)
	if err != nil {
		t.Fatalf("normalizeResponsesWSTestMessage() error = %v", err)
	}
	req := create.Request
	if eventID != "evt_2" {
		t.Fatalf("eventID = %q, want evt_2", eventID)
	}
	if req.Model != "gpt-5.3-codex-spark" {
		t.Fatalf("model = %q", req.Model)
	}
	if strings.TrimSpace(string(create.Generate)) != "false" {
		t.Fatalf("generate = %s, want false", create.Generate)
	}
	if req.Stream != nil {
		t.Fatalf("stream = %v, want nil", req.Stream)
	}
	if req.StreamOptions != nil {
		t.Fatalf("stream_options = %#v, want nil", req.StreamOptions)
	}
}

// The HTTP body and the upstream event keep the client's member order and
// number spelling; only the WebSocket members change.
func TestResponsesWSCreateEventKeepsClientBytes(t *testing.T) {
	message := []byte(`{"type":"response.create","model":"m","text":{"format":{"type":"json_schema","name":"r","schema":{"properties":{"b":{},"a":{}}}}},"temperature":1.0,"stream":true,"generate":false}`)

	create, _, err := normalizeResponsesWSTestMessage(message)
	require.NoError(t, err)
	assert.Equal(t, `{"model":"m","text":{"format":{"type":"json_schema","name":"r","schema":{"properties":{"b":{},"a":{}}}}},"temperature":1.0}`, string(create.Body))

	event, err := buildResponsesWSCreateEvent(create.Body, create.Generate, "s1")
	require.NoError(t, err)
	assert.Equal(t, `{"model":"m","text":{"format":{"type":"json_schema","name":"r","schema":{"properties":{"b":{},"a":{}}}}},"temperature":1.0,"type":"response.create","stream_id":"s1","generate":false}`, string(event))

	_, _, err = normalizeResponsesWSTestMessage([]byte(`{"type":"response.create","response":["m"]}`))
	assert.Error(t, err)
}

func TestBuildResponsesWSCreateEventIsFlat(t *testing.T) {
	payload := []byte(`{
		"model": "gpt-5.3-codex-spark",
		"input": "hi",
		"store": false,
		"event_id": "evt_upstream",
		"stream": true,
		"background": true,
		"stream_options": {"include_usage": true}
	}`)

	got, err := buildResponsesWSCreateEvent(payload, common.RawMessage(`false`), "")
	if err != nil {
		t.Fatalf("buildResponsesWSCreateEvent() error = %v", err)
	}
	var data map[string]any
	if err := common.Unmarshal(got, &data); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if data["type"] != responsesWSEventTypeResponseCreate {
		t.Fatalf("type = %#v", data["type"])
	}
	if data["model"] != "gpt-5.3-codex-spark" || data["input"] != "hi" || data["store"] != false {
		t.Fatalf("unexpected flat event fields: %s", got)
	}
	if data["generate"] != false {
		t.Fatalf("generate = %#v, want false", data["generate"])
	}
	for _, key := range []string{"response", "event_id", "stream", "background", "stream_options"} {
		if _, ok := data[key]; ok {
			t.Fatalf("field %q should not be present in upstream event: %s", key, got)
		}
	}
}

func TestHTTPResponsesRequestOmitsWebSocketMetadata(t *testing.T) {
	var req dto.OpenAIResponsesRequest
	require.NoError(t, common.Unmarshal([]byte(`{"model":"gpt-5.3-codex-spark","input":"hi","generate":false,"stream_id":"planner"}`), &req))
	got, err := common.Marshal(req)
	require.NoError(t, err)
	var data map[string]any
	require.NoError(t, common.Unmarshal(got, &data))
	assert.NotContains(t, data, "stream_id")
	assert.NotContains(t, data, "generate")
}

func TestBuildResponsesWSErrorPayloadIncludesStatus(t *testing.T) {
	payload, err := buildResponsesWSErrorPayload("evt_err", "", types.NewErrorWithStatusCode(
		errors.New("model is required"),
		types.ErrorCodeInvalidRequest,
		http.StatusBadRequest,
		types.ErrOptionWithSkipRetry(),
	))
	if err != nil {
		t.Fatalf("buildResponsesWSErrorPayload() error = %v", err)
	}
	var data struct {
		Type    string             `json:"type"`
		Status  int                `json:"status"`
		EventID string             `json:"event_id"`
		Error   *types.OpenAIError `json:"error"`
	}
	if err := common.Unmarshal(payload, &data); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if data.Type != "error" || data.Status != http.StatusBadRequest || data.EventID != "evt_err" {
		t.Fatalf("unexpected error event: %s", payload)
	}
	if data.Error == nil || data.Error.Code != string(types.ErrorCodeInvalidRequest) {
		t.Fatalf("unexpected error body: %#v", data.Error)
	}
}

func TestResponsesWSInvalidRequestErrorUsesBadRequestStatus(t *testing.T) {
	payload, err := buildResponsesWSErrorPayload("", "", newResponsesWSInvalidRequestError(errors.New("bad event")))
	if err != nil {
		t.Fatalf("buildResponsesWSErrorPayload() error = %v", err)
	}
	var data struct {
		Status int `json:"status"`
	}
	if err := common.Unmarshal(payload, &data); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if data.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", data.Status, http.StatusBadRequest)
	}
}

func newTestResponsesWSTarget(t *testing.T) (*websocket.Conn, func()) {
	t.Helper()
	target, _, cleanup := newTestWebSocketPair(t)
	return target, cleanup
}

func newTestWebSocketPair(t *testing.T) (*websocket.Conn, *websocket.Conn, func()) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	serverConnCh := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		serverConnCh <- conn
	}))

	targetURL := "ws" + strings.TrimPrefix(server.URL, "http")
	target, _, err := websocket.DefaultDialer.Dial(targetURL, nil)
	if err != nil {
		server.Close()
		t.Fatalf("dial websocket: %v", err)
	}
	serverConn := <-serverConnCh
	cleanup := func() {
		_ = target.Close()
		_ = serverConn.Close()
		server.Close()
	}
	return target, serverConn, cleanup
}

func TestResponsesWSChannelConcurrencyPerCreate(t *testing.T) {
	require.NoError(t, i18n.Init())
	database := setupRelayChannelDB(t)
	require.NoError(t, database.AutoMigrate(&model.User{}))
	user := &model.User{Username: "ws-concurrency-user"}
	require.NoError(t, database.Create(user).Error)
	previousRedis, previousCountTokens, previousLog := common.RedisEnabled, constant.CountToken, common.LogConsumeEnabled
	previousPrices := ratio_setting.ModelPrice2JSONString()
	quotaSetting := operation_setting.GetQuotaSetting()
	previousFreePreConsume := quotaSetting.EnableFreeModelPreConsume
	common.RedisEnabled, constant.CountToken, common.LogConsumeEnabled = false, false, false
	quotaSetting.EnableFreeModelPreConsume = false
	t.Cleanup(func() {
		common.RedisEnabled, constant.CountToken, common.LogConsumeEnabled = previousRedis, previousCountTokens, previousLog
		quotaSetting.EnableFreeModelPreConsume = previousFreePreConsume
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousPrices))
	})
	const modelName = "ws-concurrency-model"
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"ws-concurrency-model":0}`))
	var handshakes atomic.Int32
	peers := make(chan *websocket.Conn, 1)
	upstreamErrors := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			upstreamErrors <- err
			return
		}
		handshakes.Add(1)
		peers <- peer
	}))
	t.Cleanup(upstream.Close)
	channel := &model.Channel{Name: "ws-concurrency", Key: "sk-test", BaseURL: &upstream.URL, Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI}
	channel.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true, MaxConcurrency: 1})
	require.NoError(t, database.Create(channel).Error)
	client, server, cleanup := newTestWebSocketPair(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	guardCtx, cancelGuard := context.WithCancelCause(t.Context())
	t.Cleanup(func() { cancelGuard(context.Canceled) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		ResponsesWebSocketHelper(c, server, func(request *http.Request, requestID string, run func(*gin.Context) *types.NewAPIError) *types.NewAPIError {
			call, _ := gin.CreateTestContext(httptest.NewRecorder())
			requestCtx, cancelRequest := context.WithCancelCause(request.Context())
			stopGuard := context.AfterFunc(guardCtx, func() { cancelRequest(context.Cause(guardCtx)) })
			defer stopGuard()
			defer cancelRequest(context.Canceled)
			call.Request = request.WithContext(context.WithValue(requestCtx, constant.ContextKeyChannelConcurrencyGuard, guardCtx))
			defer common.CleanupBodyStorage(call)
			common.SetContextKey(call, common.RequestIdKey, requestID)
			common.SetContextKey(call, constant.ContextKeyUserId, user.Id)
			common.SetContextKey(call, constant.ContextKeyUsingGroup, "default")
			service.GetChannelConstraints(call).AddPin(appdto.ChannelPin{ChannelId: channel.Id, Source: appdto.PinSourceToken})
			return run(call)
		})
	}()
	t.Cleanup(func() {
		cleanup()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("websocket relay did not finish")
		}
	})
	probe, _ := gin.CreateTestContext(httptest.NewRecorder())
	probe.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	t.Cleanup(func() { service.ReleaseChannelConcurrency(probe) })
	var peer *websocket.Conn
	for index, status := range []string{"completed", "failed", "cancelled"} {
		streamID := fmt.Sprintf("stream-%d", index)
		responseID := fmt.Sprintf("resp-%d", index)
		create := fmt.Sprintf(`{"type":"response.create","stream_id":%q,"model":%q,"input":"hi"}`, streamID, modelName)
		require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(create)))
		if peer == nil {
			select {
			case peer = <-peers:
				t.Cleanup(func() { _ = peer.Close() })
			case err := <-upstreamErrors:
				t.Fatalf("upstream handshake failed: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("relay did not connect to upstream")
			}
		}
		require.NoError(t, peer.SetReadDeadline(time.Now().Add(5*time.Second)))
		_, body, err := peer.ReadMessage()
		require.NoError(t, err)
		var forwarded struct {
			Type     string `json:"type"`
			StreamID string `json:"stream_id"`
		}
		require.NoError(t, common.Unmarshal(body, &forwarded))
		assert.Equal(t, "response.create", forwarded.Type)
		assert.Equal(t, streamID, forwarded.StreamID, "a previously rejected create must not reach upstream")
		created := fmt.Sprintf(`{"type":"response.created","stream_id":%q,"response":{"id":%q,"model":%q}}`, streamID, responseID, modelName)
		require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(created)))
		require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))
		_, body, err = client.ReadMessage()
		require.NoError(t, err)
		assert.JSONEq(t, created, string(body))
		acquired, err := service.TryAcquireChannelConcurrency(probe, channel)
		require.NoError(t, err)
		require.False(t, acquired, "every active create must occupy capacity")

		terminal := fmt.Sprintf(`{"type":%q,"stream_id":%q,"response":{"id":%q,"model":%q,"status":%q,"output":[],"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}}`, "response."+status, streamID, responseID, modelName, status)
		require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(terminal)))
		_, body, err = client.ReadMessage()
		require.NoError(t, err)
		assert.JSONEq(t, terminal, string(body))
		acquired, err = service.TryAcquireChannelConcurrency(probe, channel)
		require.NoError(t, err)
		require.True(t, acquired, "%s must release capacity before publishing its terminal frame", status)

		// An idle established upstream socket cannot bypass another request's slot.
		require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","event_id":"busy-create","stream_id":"busy","model":"ws-concurrency-model","input":"hi"}`)))
		_, body, err = client.ReadMessage()
		require.NoError(t, err)
		var rejected responsesWSErrorEvent
		require.NoError(t, common.Unmarshal(body, &rejected))
		assert.Equal(t, "error", rejected.Type)
		assert.Equal(t, http.StatusTooManyRequests, rejected.Status)
		assert.Equal(t, "busy-create", rejected.EventID)
		assert.Equal(t, "busy", rejected.StreamID)
		require.NotNil(t, rejected.Error)
		assert.Equal(t, "channel_concurrency_limit", rejected.Error.Code)
		service.ReleaseChannelConcurrency(probe)
	}
	assert.Equal(t, int32(1), handshakes.Load(), "all creates must reuse one physical upstream connection")
	// Lease loss must stop an active generation even while the client is connected.
	require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","stream_id":"guard-loss","model":"ws-concurrency-model","input":"hi"}`)))
	require.NoError(t, peer.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, body, err := peer.ReadMessage()
	require.NoError(t, err)
	assert.Contains(t, string(body), `"stream_id":"guard-loss"`)
	created := `{"type":"response.created","stream_id":"guard-loss","response":{"id":"resp-guard-loss","model":"ws-concurrency-model"}}`
	require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(created)))
	require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, body, err = client.ReadMessage()
	require.NoError(t, err)
	assert.JSONEq(t, created, string(body))
	acquired, err := service.TryAcquireChannelConcurrency(probe, channel)
	require.NoError(t, err)
	require.False(t, acquired)
	cancelGuard(errors.New("channel concurrency lease lost"))
	for _, conn := range []*websocket.Conn{client, peer} {
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
		_, _, err := conn.ReadMessage()
		assert.True(t, websocket.IsCloseError(err, websocket.CloseInternalServerErr), "lease loss must close both peers with 1011: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("websocket relay did not stop after lease loss")
	}
	acquired, err = service.TryAcquireChannelConcurrency(probe, channel)
	require.NoError(t, err)
	assert.True(t, acquired, "the stopped generation must return capacity")
}

func TestResponsesWSCyberSessionBlock(t *testing.T) {
	require.NoError(t, i18n.Init())
	t.Setenv("CYBER_SESSION_BLOCK_ENABLED", "true")
	t.Setenv("CYBER_SESSION_BLOCK_TTL_SECONDS", "3600")
	database := setupRelayChannelDB(t)
	require.NoError(t, database.AutoMigrate(&model.User{}))
	user := &model.User{Username: "ws-cyber-user"}
	require.NoError(t, database.Create(user).Error)
	previousRedis, previousCountTokens, previousLog := common.RedisEnabled, constant.CountToken, common.LogConsumeEnabled
	previousPrices := ratio_setting.ModelPrice2JSONString()
	quotaSetting := operation_setting.GetQuotaSetting()
	previousFreePreConsume := quotaSetting.EnableFreeModelPreConsume
	common.RedisEnabled, constant.CountToken, common.LogConsumeEnabled = false, false, false
	quotaSetting.EnableFreeModelPreConsume = false
	t.Cleanup(func() {
		common.RedisEnabled, constant.CountToken, common.LogConsumeEnabled = previousRedis, previousCountTokens, previousLog
		quotaSetting.EnableFreeModelPreConsume = previousFreePreConsume
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousPrices))
	})
	const modelName = "ws-cyber-model"
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"ws-cyber-model":0}`))
	for _, eventType := range []string{"error", "response.failed"} {
		for _, explicit := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/explicit=%t", eventType, explicit), func(t *testing.T) {
				type upstreamCreate struct {
					peer *websocket.Conn
					body []byte
				}
				var handshakes, creates atomic.Int32
				requests := make(chan upstreamCreate, 8)
				upgrader := websocket.Upgrader{}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					peer, err := upgrader.Upgrade(w, r, nil)
					if err != nil {
						t.Errorf("upgrade upstream websocket: %v", err)
						return
					}
					defer peer.Close()
					handshakes.Add(1)
					for {
						_, body, err := peer.ReadMessage()
						if err != nil {
							return
						}
						creates.Add(1)
						requests <- upstreamCreate{peer: peer, body: body}
					}
				}))
				t.Cleanup(upstream.Close)
				channel := &model.Channel{Name: "ws-cyber", Key: "sk-test", BaseURL: &upstream.URL, Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI}
				channel.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
				require.NoError(t, database.Create(channel).Error)
				type callResult struct {
					context *gin.Context
					err     *types.NewAPIError
				}
				openSession := func(identity string) (*websocket.Conn, <-chan callResult) {
					client, server, cleanup := newTestWebSocketPair(t)
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
					if explicit && eventType == "error" {
						c.Request.Header.Set("thread_id", identity)
					}
					results := make(chan callResult, 4)
					done := make(chan struct{})
					go func() {
						defer close(done)
						ResponsesWebSocketHelper(c, server, func(request *http.Request, requestID string, run func(*gin.Context) *types.NewAPIError) *types.NewAPIError {
							call, _ := gin.CreateTestContext(httptest.NewRecorder())
							call.Request = request
							defer common.CleanupBodyStorage(call)
							common.SetContextKey(call, common.RequestIdKey, requestID)
							common.SetContextKey(call, constant.ContextKeyUserId, user.Id)
							common.SetContextKey(call, constant.ContextKeyTokenId, 1001)
							common.SetContextKey(call, constant.ContextKeyUsingGroup, "default")
							service.GetChannelConstraints(call).AddPin(appdto.ChannelPin{ChannelId: channel.Id, Source: appdto.PinSourceToken})
							apiErr := run(call)
							results <- callResult{context: call, err: apiErr}
							return apiErr
						})
					}()
					t.Cleanup(func() {
						cleanup()
						select {
						case <-done:
						case <-time.After(5 * time.Second):
							t.Error("cyber websocket relay did not finish")
						}
					})
					return client, results
				}
				sendCreate := func(client *websocket.Conn, identity, streamID, input string) {
					metadata := ""
					if explicit && eventType == "response.failed" {
						metadata = fmt.Sprintf(`,"client_metadata":{"session_id":%q}`, identity)
					}
					payload := fmt.Sprintf(`{"type":"response.create","event_id":%q,"stream_id":%q,"response":{"model":%q,"input":%q%s}}`, streamID, streamID, modelName, input, metadata)
					require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(payload)))
				}
				readClient := func(client *websocket.Conn) []byte {
					require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))
					_, body, err := client.ReadMessage()
					require.NoError(t, err)
					return body
				}
				receiveCreate := func(streamID string) *websocket.Conn {
					select {
					case request := <-requests:
						var forwarded struct {
							Type     string `json:"type"`
							StreamID string `json:"stream_id"`
						}
						require.NoError(t, common.Unmarshal(request.body, &forwarded))
						require.Equal(t, "response.create", forwarded.Type)
						require.Equal(t, streamID, forwarded.StreamID, "a locally blocked create must never reach upstream")
						return request.peer
					case <-time.After(5 * time.Second):
						t.Fatal("create did not reach upstream")
						return nil
					}
				}
				identity := common.GetUUID()
				followUp := "follow-up " + identity
				client, results := openSession(identity)
				sendCreate(client, identity, "unrelated-check", identity)
				peer := receiveCreate("unrelated-check")
				cyber := `{"code":"cyber_policy","message":"blocked by upstream policy","type":"invalid_request_error"}`
				failure := fmt.Sprintf(`{"type":"error","stream_id":"other","status":503,"error":%s}`, cyber)
				if eventType == "response.failed" {
					failure = fmt.Sprintf(`{"type":"response.failed","stream_id":"other","response":{"id":"other","status":"failed","error":%s}}`, cyber)
				}
				require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(failure)))
				assert.JSONEq(t, failure, string(readClient(client)))
				completed := `{"type":"response.completed","stream_id":"unrelated-check","response":{"id":"unrelated-check","status":"completed","output":[]}}`
				require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(completed)))
				assert.JSONEq(t, completed, string(readClient(client)))
				require.Nil(t, (<-results).err)

				// A different stream's cyber frame must not poison this connection or identity.
				sendCreate(client, identity, "cyber-hit", identity)
				require.Same(t, peer, receiveCreate("cyber-hit"))
				failure = strings.ReplaceAll(failure, `"other"`, `"cyber-hit"`)
				require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(failure)))
				body := readClient(client)
				hit := <-results
				if eventType == "error" {
					var rejected responsesWSErrorEvent
					require.NoError(t, common.Unmarshal(body, &rejected))
					require.NotNil(t, rejected.Error)
					assert.Equal(t, "cyber_policy", rejected.Error.Code)
					assert.Equal(t, http.StatusServiceUnavailable, rejected.Status)
					require.NotNil(t, hit.err)
					assert.False(t, service.ShouldRetryRelayError(hit.context, hit.err, 2))
					assert.False(t, service.ShouldDisableChannel(hit.err))
				} else {
					assert.JSONEq(t, failure, string(body))
					require.Nil(t, hit.err)
				}
				otherIdentity := common.GetUUID()
				other, otherResults := client, results
				wantHandshakes := int32(1)
				// Body metadata can select a new session on the same socket; HTTP headers are fixed.
				if !explicit || eventType == "error" {
					other, otherResults = openSession(otherIdentity)
					wantHandshakes++
				}
				otherInput := identity
				if !explicit {
					otherInput = followUp
				}
				sendCreate(other, otherIdentity, "allowed", otherInput)
				otherPeer := receiveCreate("allowed")
				if explicit && eventType == "response.failed" {
					require.Same(t, peer, otherPeer, "a different metadata session must reuse the upstream socket")
				}
				completed = `{"type":"response.completed","stream_id":"allowed","response":{"id":"allowed","status":"completed","output":[]}}`
				require.NoError(t, otherPeer.WriteMessage(websocket.TextMessage, []byte(completed)))
				assert.JSONEq(t, completed, string(readClient(other)))
				require.Nil(t, (<-otherResults).err)
				assert.Equal(t, int32(3), creates.Load())
				assert.Equal(t, wantHandshakes, handshakes.Load())

				var before model.User
				require.NoError(t, database.First(&before, user.Id).Error)
				if explicit {
					// Persistent session blocks must survive a new client WebSocket.
					client, results = openSession(identity)
				}
				// Changing the content proves anonymous sockets use connection state, not a fingerprint hit.
				sendCreate(client, identity, "blocked", followUp)
				var blocked responsesWSErrorEvent
				require.NoError(t, common.Unmarshal(readClient(client), &blocked))
				require.NotNil(t, blocked.Error)
				assert.Equal(t, http.StatusForbidden, blocked.Status)
				assert.Equal(t, "session_blocked_by_cyber_policy", blocked.Error.Code)
				assert.Equal(t, "blocked", blocked.StreamID)
				local := <-results
				require.NotNil(t, local.err)
				assert.Zero(t, service.RequestPolicy(local.context).Attempts, "local rejection must precede routing and billing")
				assert.Zero(t, common.GetContextKeyInt(local.context, constant.ContextKeyChannelId))
				assert.Equal(t, int32(3), creates.Load())
				assert.Equal(t, wantHandshakes, handshakes.Load(), "local rejection must not open another upstream socket")
				var after model.User
				require.NoError(t, database.First(&after, user.Id).Error)
				assert.Equal(t, before.Quota, after.Quota)
				assert.Equal(t, before.UsedQuota, after.UsedQuota)
				assert.Equal(t, before.RequestCount, after.RequestCount)

				require.NoError(t, database.First(channel, channel.Id).Error)
				assert.Equal(t, common.ChannelStatusEnabled, channel.Status)
			})
		}
	}
}

func TestResponsesWSMessageSizeLimit(t *testing.T) {
	previous := constant.MaxRequestBodyMB
	constant.MaxRequestBodyMB = 1
	t.Cleanup(func() { constant.MaxRequestBodyMB = previous })
	client, server, cleanup := newTestWebSocketPair(t)
	defer cleanup()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	var admitted atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		ResponsesWebSocketHelper(c, server, func(*http.Request, string, func(*gin.Context) *types.NewAPIError) *types.NewAPIError {
			admitted.Store(true)
			return nil
		})
	}()
	_ = client.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", (1<<20)+1)))
	require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, _, err := client.ReadMessage()
	assert.True(t, websocket.IsCloseError(err, websocket.CloseMessageTooBig), "oversized message must be rejected before admission: %v", err)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("websocket request did not exit after oversized message")
	}
	assert.False(t, admitted.Load())
}

func TestResponsesWSShutdownInterruptsBusyWriter(t *testing.T) {
	client, server, cleanupClient := newTestWebSocketPair(t)
	defer cleanupClient()
	target, peer, cleanupTarget := newTestWebSocketPair(t)
	defer cleanupTarget()
	ctx, cancel := context.WithCancel(context.Background())
	s := &responsesWSSession{ctx: ctx, cancel: cancel, client: server, target: target}
	// Blocked network writers own these locks. Shutdown must not wait for
	// them: its close frames are bounded control writes, and closing the
	// sockets is what interrupts those writers.
	s.clientWriteMu.Lock()
	defer s.clientWriteMu.Unlock()
	s.targetWriteMu.Lock()
	defer s.targetWriteMu.Unlock()
	done := make(chan struct{})
	go func() { s.shutdown(websocket.CloseInternalServerErr, "upstream connection closed"); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown waited for a network writer")
	}
	// A dropped socket would read as a synthetic 1006; 1011 is the frame.
	for _, conn := range []*websocket.Conn{client, peer} {
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
		_, _, err := conn.ReadMessage()
		assert.True(t, websocket.IsCloseError(err, websocket.CloseInternalServerErr), "want a 1011 close frame, got %v", err)
	}
	assert.Nil(t, s.getTarget())

	// An upstream dial that completes after shutdown is closed with a frame too.
	late, latePeer, cleanupLate := newTestWebSocketPair(t)
	defer cleanupLate()
	assert.False(t, s.setTarget(late))
	require.NoError(t, latePeer.SetReadDeadline(time.Now().Add(time.Second)))
	_, _, err := latePeer.ReadMessage()
	assert.True(t, websocket.IsCloseError(err, websocket.CloseNormalClosure), "want a 1000 close frame, got %v", err)
}

func TestResponsesWSPassthroughPreservesRawPricingParameters(t *testing.T) {
	create, _, err := normalizeResponsesWSTestMessage([]byte(`{"type":"response.create","generate":false,"response":{"model":"gpt-5.1","input":"hi","vendor":{"tier":"premium"},"stream":true}}`))
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(create.Body)))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, create.Request.Model)
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
	common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: true})
	info := relaycommon.GenRelayInfoResponses(c, &create.Request)
	payload, apiErr := buildResponsesWSCreatePayload(c, info, create.Request, create.Generate, create.StreamID)
	require.Nil(t, apiErr)
	assert.JSONEq(t, `{"type":"response.create","generate":false,"model":"gpt-5.1","input":"hi","vendor":{"tier":"premium"}}`, string(payload))
	storage, err := common.GetBodyStorage(c)
	require.NoError(t, err)
	require.NoError(t, storage.Close())
}

func TestResponsesWSStreamIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, fields, want string
		invalid            bool
	}{
		{name: "default", fields: `"model":"gpt-4o"`},
		{name: "named", fields: `"model":"gpt-4o","stream_id":"planner.A-1_2"`, want: "planner.A-1_2"},
		{name: "wrapped", fields: `"response":{"model":"gpt-4o","stream_id":"wrapped"}`, want: "wrapped"},
		{name: "top-level-wins", fields: `"stream_id":"outer","response":{"model":"gpt-4o","stream_id":"inner"}`, want: "outer"},
		{name: "maximum", fields: `"model":"gpt-4o","stream_id":"` + strings.Repeat("a", 256) + `"`, want: strings.Repeat("a", 256)},
		{name: "too-long", fields: `"stream_id":"` + strings.Repeat("a", 257) + `"`, invalid: true},
		{name: "empty", fields: `"stream_id":""`, invalid: true},
		{name: "null", fields: `"stream_id":null`, invalid: true},
		{name: "number", fields: `"stream_id":123`, invalid: true},
		{name: "unicode", fields: `"stream_id":"计划"`, invalid: true},
		{name: "spaces", fields: `"stream_id":"a b"`, invalid: true},
		{name: "invalid-top-level", fields: `"stream_id":"","response":{"stream_id":"inner"}`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			create, eventID, err := normalizeResponsesWSTestMessage([]byte(`{"type":"response.create","event_id":"test",` + tc.fields + `}`))
			assert.Equal(t, "test", eventID)
			if tc.invalid {
				require.ErrorContains(t, err, "stream_id")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, create.StreamID)
			assert.NotContains(t, string(create.Body), "stream_id")
			for _, passthrough := range []bool{false, true} {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(create.Body)))
				common.SetContextKey(c, constant.ContextKeyOriginalModel, create.Request.Model)
				common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
				common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: passthrough})
				info := relaycommon.GenRelayInfoResponses(c, &create.Request)
				payload, apiErr := buildResponsesWSCreatePayload(c, info, create.Request, create.Generate, create.StreamID)
				require.Nil(t, apiErr)
				var event map[string]any
				require.NoError(t, common.Unmarshal(payload, &event))
				if tc.want == "" {
					assert.NotContains(t, event, "stream_id")
				} else {
					assert.Equal(t, tc.want, event["stream_id"])
				}
				if storage, err := common.GetBodyStorage(c); err == nil {
					require.NoError(t, storage.Close())
				}
			}
		})
	}
}

func TestResponsesWSErrorAttribution(t *testing.T) {
	for _, tc := range []struct {
		name, payload, control            string
		terminal, ambiguous, controlError bool
	}{
		{name: "active-server-failure", payload: `{"status":500,"error":{"type":"server_error"}}`, terminal: true},
		{name: "other-stream", payload: `{"stream_id":"other","status":500}`},
		{name: "previous-response", payload: `{"response_id":"previous","status":500}`},
		{name: "cancel-event-id", payload: `{"event_id":"cancel","status":500}`, control: `{"event_id":"cancel"}`, controlError: true},
		{name: "cancel-target", payload: `{"response_id":"missing","status":400}`, control: `{"response_id":"missing"}`, controlError: true},
		{name: "cancel-code", payload: `{"error":{"type":"invalid_request_error","code":"response_not_found"}}`, control: `{"response_id":"missing"}`, controlError: true},
		{name: "ambiguous-after-cancel", payload: `{"status":500}`, control: `{"response_id":"active"}`, terminal: true, ambiguous: true},
		{name: "explicit-generation-failure", payload: `{"response_id":"active","status":500}`, control: `{"response_id":"active"}`, terminal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var event responsesWSErrorEvent
			require.NoError(t, common.Unmarshal([]byte(tc.payload), &event))
			terminal, ambiguous, controlError := responsesWSErrorEndsRequest(event, "planner", "active", []byte(tc.control))
			assert.Equal(t, tc.terminal, terminal)
			assert.Equal(t, tc.ambiguous, ambiguous)
			assert.Equal(t, tc.controlError, controlError)
		})
	}
}

func TestServiceTierHTTPRoutingAndOutboundValidation(t *testing.T) {
	require.NoError(t, i18n.Init())
	database := setupRelayChannelDB(t)
	require.NoError(t, database.AutoMigrate(&model.Ability{}))
	var hits atomic.Int32
	var forwardedTier string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var request struct {
			ServiceTier string `json:"service_tier"`
		}
		require.NoError(t, common.Unmarshal(body, &request))
		forwardedTier = request.ServiceTier
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	var high, low *model.Channel
	for _, spec := range []struct {
		name     string
		priority int64
		tiers    []string
	}{
		{"normal", 100, []string{"default", "priority"}},
		{"ultrafast", 0, []string{"ultrafast"}},
	} {
		channel := &model.Channel{Name: spec.name, Key: "sk-test", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-6,deepseek-chat", Priority: &spec.priority, Weight: common.GetPointer(uint(100))}
		channel.SetOtherSettings(dto.ChannelOtherSettings{SupportedServiceTiers: spec.tiers})
		require.NoError(t, database.Create(channel).Error)
		for _, name := range []string{"gpt-6", "deepseek-chat"} {
			require.NoError(t, database.Create(&model.Ability{ChannelId: channel.Id, Model: name, Group: "default", Enabled: true, Priority: &spec.priority}).Error)
		}
		if spec.name == "normal" {
			high = channel
		} else {
			low = channel
		}
	}
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		ids := []int{high.Id, low.Id}
		require.NoError(t, database.Where("channel_id IN ?", ids).Delete(&model.Ability{}).Error)
		require.NoError(t, database.Where("id IN ?", ids).Delete(&model.Channel{}).Error)
		model.InitChannelCache()
	})
	for _, tc := range []struct {
		name, model, tier, policy, override, wantTier string
		wantChannel                                   int
		blocked                                       bool
	}{
		{name: "default", model: "gpt-6", wantChannel: high.Id},
		{name: "fast alias passes without legacy switch", model: "gpt-6", tier: "fast", wantTier: "priority", wantChannel: high.Id},
		{name: "UF bypasses higher priority", model: "gpt-6", tier: "ultrafast", wantTier: "ultrafast", wantChannel: low.Id},
		{name: "key lock applies with absent tier", model: "gpt-6", policy: "ultrafast", wantTier: "ultrafast", wantChannel: low.Id},
		{name: "override conflict stops before dispatch", model: "gpt-6", tier: "ultrafast", override: `{"service_tier":"priority"}`, wantChannel: low.Id, blocked: true},
		{name: "key lock wins over override", model: "gpt-6", policy: "ultrafast", override: `{"service_tier":"priority"}`, wantTier: "ultrafast", wantChannel: low.Id},
		{name: "dynamic model cannot silently drop UF", model: "deepseek-chat", tier: "ultrafast", override: `{"operations":[{"path":"model","mode":"set","value":"gpt-6"}]}`, wantChannel: high.Id, blocked: true},
		{name: "non GPT keeps legacy filtering", model: "deepseek-chat", tier: "ultrafast", policy: "ultrafast", wantChannel: high.Id},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, database.Model(&model.Channel{}).Where("id IN ?", []int{high.Id, low.Id}).Update("param_override", tc.override).Error)
			model.InitChannelCache()
			before := hits.Load()
			router := gin.New()
			router.POST("/v1/responses", func(c *gin.Context) {
				common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
				common.SetContextKey(c, constant.ContextKeyTokenOpenAITierPolicy, tc.policy)
			}, middleware.Distribute(), func(c *gin.Context) {
				assert.Equal(t, tc.wantChannel, common.GetContextKeyInt(c, constant.ContextKeyChannelId))
				var request dto.OpenAIResponsesRequest
				require.NoError(t, common.UnmarshalBodyReusable(c, &request))
				info := relaycommon.GenRelayInfoResponses(c, &request)
				_, body, closer, apiErr := PrepareResponsesRequest(c, info, &request)
				require.Nil(t, apiErr)
				defer closer.Close()
				upstreamRequest, err := http.NewRequest(http.MethodPost, upstream.URL, body)
				require.NoError(t, err)
				relaychannel.ApplyUpstreamBodyMetadata(upstreamRequest, body)
				response, err := relaychannel.DoRequest(c, upstreamRequest, info)
				assert.Equal(t, tc.tier, info.RequestedServiceTier, "original request remains frozen")
				if tc.blocked {
					var tierErr *types.NewAPIError
					require.ErrorAs(t, err, &tierErr)
					assert.Equal(t, http.StatusBadRequest, tierErr.StatusCode)
					assert.False(t, service.ShouldRetryRelayError(c, tierErr, 2))
					c.Status(tierErr.StatusCode)
					return
				}
				require.NoError(t, err)
				defer response.Body.Close()
				c.Status(response.StatusCode)
			})
			fields := map[string]any{"model": tc.model, "input": "hi"}
			if tc.tier != "" {
				fields["service_tier"] = tc.tier
			}
			body, err := common.Marshal(fields)
			require.NoError(t, err)
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if tc.blocked {
				assert.Equal(t, http.StatusBadRequest, recorder.Code)
				assert.Equal(t, before, hits.Load())
				return
			}
			assert.Equal(t, http.StatusNoContent, recorder.Code)
			assert.Equal(t, before+1, hits.Load())
			assert.Equal(t, tc.wantTier, forwardedTier)
		})
	}
}

func TestResponsesWSServiceTierRevalidatesEachCreate(t *testing.T) {
	database := setupRelayChannelDB(t)
	baseURL := "http://upstream.example"
	channel := &model.Channel{Name: "ws-tier", Key: "sk-test", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI, BaseURL: &baseURL}
	channel.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
	channel.SetOtherSettings(dto.ChannelOtherSettings{SupportedServiceTiers: []string{"default", "priority"}})
	require.NoError(t, database.Create(channel).Error)
	session := &responsesWSSession{lockedChannelID: channel.Id, lockedModel: "gpt-6", lockedKey: channel.Key,
		lockedContext: map[constant.ContextKey]any{constant.ContextKeyChannelBaseUrl: channel.GetBaseURL(), constant.ContextKeyChannelHeaderOverride: channel.GetHeaderOverride()}}
	for _, tc := range []struct {
		tier, policy string
		rejected     bool
	}{
		{tier: "default"},
		{tier: "ultrafast", rejected: true},
		{tier: "priority"},
		{tier: "default", policy: "ultrafast", rejected: true},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
		common.SetContextKey(c, constant.ContextKeyOriginalModel, "gpt-6")
		common.SetContextKey(c, constant.ContextKeyTokenOpenAITierPolicy, tc.policy)
		service.GetChannelConstraints(c).AddPin(appdto.ChannelPin{ChannelId: channel.Id, Source: appdto.PinSourceToken})
		service.SetRequestServiceTierFilter(c, "gpt-6", tc.tier)
		apiErr := session.restoreConnectionContext(c, "gpt-6")
		if tc.rejected {
			require.NotNil(t, apiErr)
			assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			assert.ErrorContains(t, apiErr, "reconnect required")
			continue
		}
		require.Nil(t, apiErr)
		common.SetContextKey(c, constant.ContextKeyChannelType, channel.Type)
		request := dto.OpenAIResponsesRequest{Model: "gpt-6", ServiceTier: tc.tier, Input: common.RawMessage(`"hi"`)}
		info := relaycommon.GenRelayInfoResponses(c, &request)
		payload, apiErr := buildResponsesWSCreatePayload(c, info, request, nil, "")
		require.Nil(t, apiErr)
		var outgoing struct {
			ServiceTier string `json:"service_tier"`
		}
		require.NoError(t, common.Unmarshal(payload, &outgoing))
		assert.Equal(t, tc.tier, outgoing.ServiceTier)
		assert.Equal(t, tc.tier, info.OutboundServiceTier)
	}
}
