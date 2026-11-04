package controller

import (
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestCyberSessionBlockRelayHTTPAndSSE(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")
	require.NoError(t, db.AutoMigrate(&model.Token{}))
	previousRetries, previousCount := common.RetryTimes, constant.CountToken
	previousErrorLog, previousAutoDisable := constant.ErrorLogEnabled, common.AutomaticDisableChannelEnabled
	previousConsumeLog, previousBatch := common.LogConsumeEnabled, common.BatchUpdateEnabled
	previousRetryStatuses := operation_setting.AutomaticRetryStatusCodeRanges
	quotaSetting := operation_setting.GetQuotaSetting()
	previousQuotaSetting := *quotaSetting
	previousGroupRatio := ratio_setting.GroupRatio2JSONString()
	common.RetryTimes, constant.CountToken = 2, false
	constant.ErrorLogEnabled, common.AutomaticDisableChannelEnabled = false, false
	common.LogConsumeEnabled, common.BatchUpdateEnabled = false, false
	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 403, End: 403}, {Start: 502, End: 502}}
	quotaSetting.TrustQuotaUSD = 0
	t.Cleanup(func() {
		common.RetryTimes, constant.CountToken = previousRetries, previousCount
		constant.ErrorLogEnabled, common.AutomaticDisableChannelEnabled = previousErrorLog, previousAutoDisable
		common.LogConsumeEnabled, common.BatchUpdateEnabled = previousConsumeLog, previousBatch
		operation_setting.AutomaticRetryStatusCodeRanges = previousRetryStatuses
		*quotaSetting = previousQuotaSetting
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroupRatio))
	})
	require.NoError(t, i18n.Init())
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	const modelName, initialQuota = "cyber-session-test", 100000
	const blockedCode = "session_blocked_by_cyber_policy"
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"cyber-session-test":0.001}`))
	service.InitHttpClient()

	// Count debits, including ones that a failed request later refunds. A final
	// unchanged balance alone would miss a block checked after pre-consumption.
	var reservations atomic.Int32
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:cyber_reservations", func(tx *gorm.DB) {
		if tx.Statement.Table != "users" && tx.Statement.Table != "tokens" {
			return
		}
		updates, _ := tx.Statement.Dest.(map[string]any)
		for _, field := range []string{"quota", "remain_quota"} {
			if value, ok := updates[field].(clause.Expr); ok && strings.Contains(value.SQL, " - ") {
				reservations.Add(1)
			}
		}
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Update().Remove("test:cyber_reservations")) })

	for _, tc := range []struct {
		name, payload string
		status        int
		stream, block bool
	}{
		{name: "HTTP 403 cyber", status: 403, block: true, payload: `{"error":{"message":"request rejected","type":"invalid_request_error","code":"cyber_policy"}}`},
		{name: "HTTP 502 wrapped cyber", status: 502, block: true, payload: `{"error":{"message":"request rejected","type":"upstream_error","code":"cyber_policy"}}`},
		{name: "HTTP upstream session already blocked", status: 403, block: true, payload: `{"error":{"message":"session unavailable","type":"invalid_request_error","code":"session_blocked_by_cyber_policy"}}`},
		{name: "SSE response.failed", status: 200, stream: true, block: true, payload: "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_cyber\",\"status\":\"failed\",\"error\":{\"message\":\"request rejected\",\"type\":\"invalid_request_error\",\"code\":\"cyber_policy\"}}}\n\n"},
		{name: "SSE error", status: 200, stream: true, block: true, payload: "event: error\ndata: {\"type\":\"error\",\"code\":\"cyber_policy\",\"message\":\"request rejected\"}\n\n"},
		{name: "ordinary HTTP 403", status: 403, payload: `{"error":{"message":"access denied","type":"invalid_request_error","code":"permission_denied"}}`},
		{name: "cyber mentioned in code and message", status: 403, payload: `{"error":{"message":"cyber model is unavailable","type":"upstream_error","code":"cyber_model_unavailable"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			users := []*model.User{
				{Username: common.GetUUID(), Group: "default", Status: common.UserStatusEnabled, Quota: initialQuota},
				{Username: common.GetUUID(), Group: "default", Status: common.UserStatusEnabled, Quota: initialQuota},
			}
			for _, user := range users {
				user.AffCode = common.GetUUID()
				require.NoError(t, db.Create(user).Error)
			}
			tokens := []*model.Token{
				{UserId: users[0].Id, Key: common.GetUUID(), Status: common.TokenStatusEnabled, RemainQuota: initialQuota},
				{UserId: users[0].Id, Key: common.GetUUID(), Status: common.TokenStatusEnabled, RemainQuota: initialQuota},
				{UserId: users[1].Id, Key: common.GetUUID(), Status: common.TokenStatusEnabled, RemainQuota: initialQuota},
			}
			for _, token := range tokens {
				require.NoError(t, db.Create(token).Error)
			}
			// Failed relays refund asynchronously; drain those refunds before the
			// shared fixture restores model.DB and the billing configuration.
			t.Cleanup(func() {
				require.EventuallyWithT(t, func(collect *assert.CollectT) {
					for _, user := range users {
						var quota int
						if assert.NoError(collect, db.Model(&model.User{}).Where("id = ?", user.Id).Select("quota").Scan(&quota).Error) {
							assert.Equal(collect, initialQuota, quota)
						}
					}
					for _, token := range tokens {
						var quota int
						if assert.NoError(collect, db.Model(&model.Token{}).Where("id = ?", token.Id).Select("remain_quota").Scan(&quota).Error) {
							assert.Equal(collect, initialQuota, quota)
						}
					}
				}, 3*time.Second, time.Millisecond)
			})
			var attempts atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				assert.Equal(t, "/v1/responses", r.URL.Path)
				contentType := "application/json"
				if tc.stream {
					contentType = "text/event-stream"
				}
				w.Header().Set("Content-Type", contentType)
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.payload)
			}))
			t.Cleanup(upstream.Close)
			channel := &model.Channel{Type: constant.ChannelTypeOpenAI, Name: tc.name, Key: "test-key", Status: common.ChannelStatusEnabled, Group: "default", Models: modelName, BaseURL: &upstream.URL}
			require.NoError(t, db.Create(channel).Error)
			require.NoError(t, channel.AddAbilities(db))
			t.Cleanup(func() {
				require.NoError(t, db.Where("channel_id = ?", channel.Id).Delete(&model.Ability{}).Error)
				require.NoError(t, db.Delete(channel).Error)
			})
			request := func(user *model.User, token *model.Token, session string) (*gin.Context, *httptest.ResponseRecorder) {
				t.Helper()
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				body := fmt.Sprintf(`{"model":%q,"input":"hello","stream":%t}`, modelName, tc.stream)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				c.Request.Header.Set("session_id", session)
				common.SetContextKey(c, constant.ContextKeyUserId, user.Id)
				user.ToBaseUser().WriteContext(c)
				common.SetContextKey(c, constant.ContextKeyUserSetting, kitdto.UserSetting{BillingPreference: "wallet_only"})
				common.SetContextKey(c, constant.ContextKeyTokenId, token.Id)
				common.SetContextKey(c, constant.ContextKeyTokenKey, token.Key)
				common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
				common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
				require.Nil(t, middleware.SetupContextForSelectedChannel(c, channel, modelName))
				defer service.ReleaseChannelConcurrency(c)
				Relay(c, types.RelayFormatOpenAIResponses)
				return c, recorder
			}

			session := common.GetUUID()
			beforeReservations := reservations.Load()
			first, firstResponse := request(users[0], tokens[0], session)
			wantAttempts := common.RetryTimes + 1
			if tc.block {
				wantAttempts = 1
			}
			assert.Equal(t, tc.status, firstResponse.Code, firstResponse.Body.String())
			assert.EqualValues(t, wantAttempts, attempts.Load(), "cyber must stop the current request even when 403/502 retries are configured")
			assert.Len(t, first.GetStringSlice("use_channel"), wantAttempts)
			assert.Greater(t, reservations.Load(), beforeReservations, "the paid fixture must actually exercise pre-consumption")
			if tc.stream {
				assert.Contains(t, firstResponse.Body.String(), "cyber_policy", "the upstream terminal event must reach the client")
			}

			beforeAttempts, beforeReservations := attempts.Load(), reservations.Load()
			second, secondResponse := request(users[0], tokens[0], session)
			if !tc.block {
				assert.Equal(t, tc.status, secondResponse.Code, secondResponse.Body.String())
				assert.EqualValues(t, wantAttempts, attempts.Load()-beforeAttempts, "ordinary errors must not poison the session")
				assert.Len(t, second.GetStringSlice("use_channel"), wantAttempts)
				return
			}
			assert.Equal(t, http.StatusForbidden, secondResponse.Code, secondResponse.Body.String())
			var blocked struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, common.Unmarshal(secondResponse.Body.Bytes(), &blocked))
			assert.Equal(t, blockedCode, blocked.Error.Code)
			assert.Equal(t, beforeAttempts, attempts.Load(), "a blocked session must not contact upstream")
			assert.Equal(t, beforeReservations, reservations.Load(), "a blocked session must not reserve user or token quota, even temporarily")
			assert.Empty(t, second.GetStringSlice("use_channel"))

			for _, identity := range []struct {
				name    string
				user    *model.User
				token   *model.Token
				session string
			}{
				{name: "different session", user: users[0], token: tokens[0], session: common.GetUUID()},
				{name: "different token", user: users[0], token: tokens[1], session: session},
				{name: "different user", user: users[1], token: tokens[2], session: session},
			} {
				before := attempts.Load()
				c, _ := request(identity.user, identity.token, identity.session)
				assert.EqualValues(t, 1, attempts.Load()-before, identity.name)
				assert.Len(t, c.GetStringSlice("use_channel"), 1, identity.name)
			}
		})
	}
}

func TestMediaGenerationRelaySingleAttempt(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")
	require.NoError(t, db.AutoMigrate(&model.UserSubscription{}, &model.SubscriptionPlan{}))
	previousRetries, previousCount := common.RetryTimes, constant.CountToken
	previousErrorLog, previousAutoDisable := constant.ErrorLogEnabled, common.AutomaticDisableChannelEnabled
	common.RetryTimes, constant.CountToken = 2, false
	constant.ErrorLogEnabled, common.AutomaticDisableChannelEnabled = false, false
	t.Cleanup(func() {
		common.RetryTimes, constant.CountToken = previousRetries, previousCount
		constant.ErrorLogEnabled, common.AutomaticDisableChannelEnabled = previousErrorLog, previousAutoDisable
	})
	require.NoError(t, i18n.Init())
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	user := &model.User{Username: "media-retry-test", Group: "default", Status: common.UserStatusEnabled, Quota: 100000}
	require.NoError(t, db.Create(user).Error)
	service.InitHttpClient()

	for _, tc := range []struct {
		name, path, model, body, mapped string
		override                        string
		format                          types.RelayFormat
		status, attempts                int
	}{
		{name: "image generation 502", path: "/v1/images/generations", model: "custom-image", body: `{"model":"custom-image","prompt":"draw a tree","n":1}`, format: types.RelayFormatOpenAIImage, status: 502, attempts: 1},
		{name: "image edit 429", path: "/v1/images/edits", model: "gpt-image-2", body: `{"model":"gpt-image-2","prompt":"edit","image":"https://example.invalid/image.png"}`, format: types.RelayFormatOpenAIImage, status: 429, attempts: 1},
		{name: "responses image tool", path: "/v1/responses", model: "gpt-4o", body: `{"model":"gpt-4o","input":"draw a tree","tools":[{"type":"image_generation"}]}`, format: types.RelayFormatOpenAIResponses, status: 502, attempts: 1},
		{name: "channel injects image tool", path: "/v1/responses", model: "gpt-4o", body: `{"model":"gpt-4o","input":"draw a tree"}`, override: `{"tools":[{"type":"image_generation"}]}`, format: types.RelayFormatOpenAIResponses, status: 503, attempts: 1},
		{name: "mapped image model over chat", path: "/v1/chat/completions", model: "drawing-alias", mapped: "gpt-image-2.5-sunburst", body: `{"model":"drawing-alias","messages":[{"role":"user","content":"draw a tree"}]}`, format: types.RelayFormatOpenAI, status: 502, attempts: 1},
		{name: "text keeps retries", path: "/v1/chat/completions", model: "gpt-4o", body: `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`, format: types.RelayFormatOpenAI, status: 502, attempts: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				if tc.override != "" {
					var body map[string]any
					if !assert.NoError(t, common.DecodeJson(r.Body, &body)) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					assert.Equal(t, []any{map[string]any{"type": "image_generation"}}, body["tools"])
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":{"message":"generation unavailable","type":"upstream_error","code":"upstream_unavailable"}}`))
			}))
			t.Cleanup(upstream.Close)
			ch := &model.Channel{Type: constant.ChannelTypeOpenAI, Name: tc.name, Key: "test-key", Status: common.ChannelStatusEnabled, Group: "default", Models: tc.model, BaseURL: &upstream.URL}
			if tc.override != "" {
				ch.ParamOverride = &tc.override
			}
			if tc.mapped != "" {
				mapping, err := common.Marshal(map[string]string{tc.model: tc.mapped})
				require.NoError(t, err)
				ch.ModelMapping = common.GetPointer(string(mapping))
			}
			require.NoError(t, db.Create(ch).Error)
			require.NoError(t, ch.AddAbilities(db))
			t.Cleanup(func() {
				require.NoError(t, db.Where("channel_id = ?", ch.Id).Delete(&model.Ability{}).Error)
				require.NoError(t, db.Delete(ch).Error)
			})
			prices, err := common.Marshal(map[string]float64{tc.model: 0})
			require.NoError(t, err)
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(prices)))
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			common.SetContextKey(c, constant.ContextKeyOriginalModel, tc.model)
			common.SetContextKey(c, constant.ContextKeyUserId, user.Id)
			common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
			common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
			common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
			require.Nil(t, middleware.SetupContextForSelectedChannel(c, ch, tc.model))
			t.Cleanup(func() { service.ReleaseChannelConcurrency(c) })

			Relay(c, tc.format)

			assert.Equal(t, tc.status, recorder.Code, recorder.Body.String())
			assert.Contains(t, recorder.Body.String(), "generation unavailable")
			assert.EqualValues(t, tc.attempts, attempts.Load(), "failed generation must not be submitted again")
			assert.Len(t, c.GetStringSlice("use_channel"), tc.attempts)
			if tc.attempts == 1 {
				events := service.RequestPolicy(c).Events()
				require.NotEmpty(t, events)
				assert.Equal(t, "media_generation_no_retry", events[len(events)-1].Decision.Reason)
			}
		})
	}
}

func TestMediaGenerationTaskSubmissionSingleAttempt(t *testing.T) {
	previousRetries, previousErrorLog := common.RetryTimes, constant.ErrorLogEnabled
	common.RetryTimes, constant.ErrorLogEnabled = 2, false
	t.Cleanup(func() { common.RetryTimes, constant.ErrorLogEnabled = previousRetries, previousErrorLog })
	for _, tc := range []struct {
		name, path, model string
		locked            bool
		attempts          int
	}{
		{name: "video create", path: "/v1/videos", model: "custom-model", attempts: 1},
		{name: "video remix on origin channel", path: "/v1/videos/task_original/remix", model: "sora-2", locked: true, attempts: 1},
		{name: "native image task", path: "/vendor/jobs", model: "gpt-image-2", locked: true, attempts: 1},
		{name: "music keeps retries", path: "/vendor/jobs", model: "suno-v4", attempts: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := taskSubmissionTestContext()
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{}`))
			events := []string{}
			billing := &taskSubmissionTestBilling{events: &events}
			info := taskSubmissionRelayInfo(billing)
			info.RelayFormat, info.OriginModelName = types.RelayFormatTask, tc.model
			if !tc.locked {
				info.ChannelMeta, info.LockedChannel = nil, nil
			}
			t.Cleanup(func() { service.ReleaseChannelConcurrency(c) })
			upstreamErr := &dto.TaskError{StatusCode: 502, Code: "upstream_unavailable", Message: "generation unavailable"}
			attempts := 0

			outcome, taskErr := executeTaskSubmissionWith(c, info, func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
				attempts++
				return nil, upstreamErr
			})

			assert.Nil(t, outcome)
			assert.Same(t, upstreamErr, taskErr)
			assert.Equal(t, tc.attempts, attempts)
			assert.Equal(t, 1, billing.refunds, "failed submission refunds its reservation exactly once")
			assert.Equal(t, []string{"refund"}, events)
		})
	}
}

func TestShouldRetryHonorsPinRetryMode(t *testing.T) {
	openaiErr := types.NewOpenAIError(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError)

	c := newPinRetryContext()
	assert.True(t, service.ShouldRetryRelayError(c, openaiErr, 1))

	origin := newPinRetryContext()
	service.GetChannelConstraints(origin).AddPin(dto.ChannelPin{
		ChannelId: 2,
		Source:    dto.PinSourceOriginTask,
		Rank:      dto.PinRankOriginTask,
		RetryMode: dto.PinRetrySameChannel,
	})
	assert.True(t, service.ShouldRetryRelayError(origin, openaiErr, 1), "origin pin retries on the same channel")

	token := newPinRetryContext()
	service.GetChannelConstraints(token).AddPin(dto.ChannelPin{
		ChannelId: 1,
		Source:    dto.PinSourceToken,
		Rank:      dto.PinRankToken,
		RetryMode: dto.PinRetrySingleAttempt,
	})
	assert.Equal(t, service.PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}, service.DecideRelayRetry(token, openaiErr, 1), "token pin suppresses retry")
}

func TestShouldRetryTaskRelayHonorsPinRetryMode(t *testing.T) {
	taskErr := &dto.TaskError{StatusCode: http.StatusInternalServerError}

	c := newPinRetryContext()
	assert.Equal(t, "retry", decideTaskRetry(c, taskErr, 1).Action)

	origin := newPinRetryContext()
	service.GetChannelConstraints(origin).AddPin(dto.ChannelPin{
		ChannelId: 2,
		Source:    dto.PinSourceOriginTask,
		Rank:      dto.PinRankOriginTask,
		RetryMode: dto.PinRetrySameChannel,
	})
	assert.Equal(t, "retry", decideTaskRetry(origin, taskErr, 1).Action)

	token := newPinRetryContext()
	service.GetChannelConstraints(token).AddPin(dto.ChannelPin{
		ChannelId: 1,
		Source:    dto.PinSourceToken,
		Rank:      dto.PinRankToken,
		RetryMode: dto.PinRetrySingleAttempt,
	})
	assert.Equal(t, service.PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}, decideTaskRetry(token, taskErr, 1))
}

func TestSameChannelPinsMergeToStricterRetryMode(t *testing.T) {
	c := newPinRetryContext()
	constraints := service.GetChannelConstraints(c)
	constraints.AddPin(dto.ChannelPin{
		ChannelId: 7,
		Source:    dto.PinSourceOriginTask,
		Rank:      dto.PinRankOriginTask,
		RetryMode: dto.PinRetrySameChannel,
	})
	constraints.AddPin(dto.ChannelPin{
		ChannelId: 7,
		Source:    dto.PinSourceToken,
		Rank:      dto.PinRankToken,
		RetryMode: dto.PinRetrySingleAttempt,
	})
	pin, found, overridden := constraints.ResolvedPin()
	require.True(t, found)
	assert.Equal(t, 7, pin.ChannelId)
	assert.Equal(t, dto.PinRetrySingleAttempt, pin.RetryMode)
	assert.Empty(t, overridden)
	assert.False(t, service.ShouldRetryRelayError(c, types.NewOpenAIError(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError), 1))
}

func TestLockedTaskFirstAttemptHonorsOriginChannelConcurrency(t *testing.T) {
	t.Setenv("TEST_TASK_DB_DIALECT", "sqlite")
	db, _ := openTaskDialectDatabase(t, &model.Channel{})
	previousDB, previousCache, previousRedis, previousRetries := model.DB, common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes
	model.DB, common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes = db, false, false, 0
	t.Cleanup(func() {
		model.DB, common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes = previousDB, previousCache, previousRedis, previousRetries
	})
	require.NoError(t, i18n.Init())
	for _, tc := range []struct {
		name       string
		full, same bool
	}{
		{name: "full origin rejects the first submit", full: true},
		{name: "switch reserves the origin and releases the initial channel"},
		{name: "same channel reuses its existing slot", same: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := &model.Channel{Type: constant.ChannelTypeOpenAI, Key: "test", Name: "initial", Status: common.ChannelStatusEnabled}
			initial.SetSetting(kitdto.ChannelSettings{MaxConcurrency: 1})
			require.NoError(t, db.Create(initial).Error)
			origin := initial
			if !tc.same {
				origin = &model.Channel{Type: constant.ChannelTypeOpenAI, Key: "test", Name: "origin", Status: common.ChannelStatusEnabled}
				origin.SetSetting(kitdto.ChannelSettings{MaxConcurrency: 1})
				require.NoError(t, db.Create(origin).Error)
			}
			c, other, initialProbe := taskSubmissionTestContext(), taskSubmissionTestContext(), taskSubmissionTestContext()
			t.Cleanup(func() {
				service.ReleaseChannelConcurrency(c)
				service.ReleaseChannelConcurrency(other)
				service.ReleaseChannelConcurrency(initialProbe)
			})
			require.Nil(t, service.AcquireChannelConcurrency(c, initial))
			require.Nil(t, middleware.SetupContextForSelectedChannel(c, initial, "plugin-model"))
			if tc.full {
				require.Nil(t, service.AcquireChannelConcurrency(other, origin))
			}
			service.GetChannelConstraints(c).AddPin(dto.ChannelPin{ChannelId: origin.Id, Source: dto.PinSourceOriginTask, Rank: dto.PinRankOriginTask, RetryMode: dto.PinRetrySameChannel})
			info := taskSubmissionRelayInfo(nil)
			require.Nil(t, relay.ApplyOriginTaskAffinity(c, info))
			submitCalls := 0
			stop := &dto.TaskError{Message: "stop after admission", StatusCode: http.StatusBadRequest, LocalError: true, NoRetry: true}
			outcome, taskErr := executeTaskSubmissionWith(c, info, func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
				submitCalls++
				if !tc.full {
					acquired, err := service.TryAcquireChannelConcurrency(other, origin)
					require.NoError(t, err)
					assert.False(t, acquired, "the origin slot must be held before invoking submit")
				}
				return nil, stop
			})
			assert.Nil(t, outcome)
			require.NotNil(t, taskErr)
			if tc.full {
				assert.Zero(t, submitCalls)
				assert.Equal(t, http.StatusTooManyRequests, taskErr.StatusCode)
				assert.Equal(t, "channel_concurrency_limit", taskErr.Code)
			} else {
				assert.Equal(t, 1, submitCalls)
				assert.Same(t, stop, taskErr)
				service.ReleaseChannelConcurrency(c)
				acquired, err := service.TryAcquireChannelConcurrency(other, origin)
				require.NoError(t, err)
				assert.True(t, acquired, "releasing one attempt must return its only slot")
			}
			if !tc.same {
				acquired, err := service.TryAcquireChannelConcurrency(initialProbe, initial)
				require.NoError(t, err)
				assert.True(t, acquired, "switching to the origin must release the initial channel")
			}
		})
	}
}

func newPinRetryContext() *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c
}

func TestRequestPolicyConfigReturnsSettingsWithoutMigration(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	GetRequestPolicy(ctx)
	assert.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.True(t, response.Success)
	assert.Contains(t, response.Data, "options")
	assert.NotContains(t, response.Data, "migration")
	assert.NotContains(t, response.Data, "differences")
}

func TestChannelAffinityRetryPriorities(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			dsn := ""
			if dialect.env != "" {
				dsn = os.Getenv(dialect.env)
				if dsn == "" {
					t.Skipf("%s not configured", dialect.env)
				}
			}
			db := modelManagementDB(t, dialect.kind, dsn)
			previousRetryTimes := common.RetryTimes
			previousGroups := setting.UserUsableGroups2JSONString()
			previousMaxGroups := setting.GetMaxTokenAutoGroups()
			affinity := operation_setting.GetChannelAffinitySetting()
			previousAffinity := *affinity
			t.Cleanup(func() {
				common.RetryTimes = previousRetryTimes
				*affinity = previousAffinity
				require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousGroups))
				require.NoError(t, setting.UpdateMaxTokenAutoGroups(fmt.Sprint(previousMaxGroups)))
			})
			require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP","empty":"Empty"}`))
			require.NoError(t, setting.UpdateMaxTokenAutoGroups("2"))
			require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":1,"empty":1}`))
			const modelName = "affinity-retry-test"
			for _, spec := range []struct {
				id       int
				priority int64
				group    string
			}{{24, 4, "default"}, {27, 3, "default"}, {30, 2, "default"}, {34, 4, "vip"}, {37, 3, "vip"}} {
				channel := model.Channel{
					Id: spec.id, Name: fmt.Sprintf("channel-%d", spec.id), Type: constant.ChannelTypeOpenAI,
					Key: "test-only", Status: common.ChannelStatusEnabled, Models: modelName, Group: spec.group,
					Priority: common.GetPointer(spec.priority), Weight: common.GetPointer(uint(1)),
				}
				require.NoError(t, db.Create(&channel).Error)
				require.NoError(t, channel.AddAbilities(db))
			}
			for _, cached := range []bool{false, true} {
				common.MemoryCacheEnabled = cached
				model.InitChannelCache()
				for _, tc := range []struct {
					name, mode string
					binding    int
					retries    int
					autoGroups []string
					crossGroup bool
					want       []int
				}{
					{name: "ordinary routing", mode: "off", retries: 2, want: []int{24, 27, 30}},
					{name: "affinity miss", mode: "prefer", retries: 2, want: []int{24, 27, 30}},
					{name: "affinity hit", mode: "prefer", binding: 27, retries: 2, want: []int{27, 24, 27}},
					{name: "no retry budget", mode: "prefer", binding: 27, want: []int{27}},
					{name: "one retry", mode: "prefer", binding: 27, retries: 1, want: []int{27, 24}},
					{name: "strict session", mode: "strict", binding: 27, retries: 2, want: []int{27}},
					{name: "auto group", mode: "prefer", binding: 27, retries: 1, autoGroups: []string{"default", "vip"}, want: []int{27, 24}},
					{name: "cross group", mode: "prefer", binding: 27, retries: 1, autoGroups: []string{"default", "vip"}, crossGroup: true, want: []int{27, 24, 34, 37}},
					{name: "skip empty group", mode: "prefer", binding: 27, retries: 1, autoGroups: []string{"empty", "default"}, want: []int{27, 24, 27}},
				} {
					t.Run(fmt.Sprintf("cache=%t/%s", cached, tc.name), func(t *testing.T) {
						common.RetryTimes = tc.retries
						*affinity = operation_setting.ChannelAffinitySetting{
							Enabled: true, DefaultTTLSeconds: 60, MaxEntries: 100,
							Rules: []operation_setting.ChannelAffinityRule{{
								Name: t.Name(), ModelRegex: []string{"^" + modelName + "$"}, SessionMode: tc.mode,
								KeySources: []operation_setting.ChannelAffinityKeySource{{Type: "request_header", Key: "X-Session"}},
							}},
						}
						ctx := newPinRetryContext()
						ctx.Request.Header.Set("X-Session", t.Name())
						group := "default"
						common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
						if tc.autoGroups != nil {
							group = "auto"
							common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, tc.autoGroups)
							common.SetContextKey(ctx, constant.ContextKeyTokenCrossGroupRetry, tc.crossGroup)
						}
						if tc.binding != 0 {
							_, found := service.GetPreferredChannelByAffinity(ctx, modelName, group)
							require.False(t, found)
							service.RecordChannelAffinity(ctx, tc.binding)
							t.Cleanup(func() { service.ClearCurrentChannelAffinityCache(ctx) })
						}
						// HTTP distribution and the relay loop use separate RetryParam values.
						channel, _, selectionErr := service.SelectChannelForRequest(ctx, modelName, &service.RetryParam{Ctx: ctx, TokenGroup: group, ModelName: modelName})
						require.Nil(t, selectionErr)
						require.NotNil(t, channel)
						retry := &service.RetryParam{Ctx: ctx, TokenGroup: group, ModelName: modelName}
						upstreamErr := types.NewOpenAIError(errors.New("upstream failure"), types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError)
						var attempts []int
						for ; retry.GetRetry() <= common.RetryTimes; retry.IncreaseRetry() {
							if len(attempts) > 0 {
								var err error
								channel, _, err = service.CacheGetRandomSatisfiedChannel(retry)
								require.NoError(t, err)
								if channel == nil {
									break
								}
							}
							service.AppendUsedChannel(ctx, channel.Id)
							attempts = append(attempts, channel.Id)
							require.LessOrEqual(t, len(attempts), len(tc.want), "retry budget must not increase")
							if service.DecideRelayRetry(ctx, upstreamErr, common.RetryTimes-retry.GetRetry()).Action != "retry" {
								break
							}
						}
						assert.Equal(t, tc.want, attempts)
					})
				}
			}
		})
	}
}

func TestRequestPolicyRoutingDatabaseMatrix(t *testing.T) {
	require.NoError(t, i18n.Init())
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			dsn := ""
			if dialect.env != "" {
				dsn = os.Getenv(dialect.env)
				if dsn == "" {
					t.Skipf("%s not configured", dialect.env)
				}
			}
			db := modelManagementDB(t, dialect.kind, dsn)
			previousGroups := setting.UserUsableGroups2JSONString()
			previousRatios, err := common.Marshal(ratio_setting.GetGroupRatioCopy())
			require.NoError(t, err)
			require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default"}`))
			require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
			t.Cleanup(func() {
				require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousGroups))
				require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(string(previousRatios)))
			})
			// Channel 1 holds the session binding but is no longer usable.
			channels := []model.Channel{
				{Id: 1, Name: "A", Type: 1, Key: "test-only", Status: common.ChannelStatusAutoDisabled, Models: "policy-test", Group: "default", Priority: common.GetPointer(int64(10))},
				{Id: 2, Name: "B", Type: 1, Key: "test-only", Status: common.ChannelStatusEnabled, Models: "policy-test", Group: "default", Priority: common.GetPointer(int64(5))},
			}
			for i := range channels {
				require.NoError(t, db.Create(&channels[i]).Error)
				require.NoError(t, channels[i].AddAbilities(db))
			}
			affinity := operation_setting.GetChannelAffinitySetting()
			previousAffinity := *affinity
			t.Cleanup(func() { *affinity = previousAffinity })
			for _, cached := range []bool{false, true} {
				for _, keep := range []bool{false, true} {
					for _, tc := range []struct {
						globalMode, ruleMode string
						blocked              bool
					}{
						{"strict", "inherit", true},
						{"prefer", "strict", true},
						{"prefer", "inherit", false},
						{"strict", "prefer", false},
					} {
						t.Run(fmt.Sprintf("cache=%t/keep=%t/global=%s/rule=%s", cached, keep, tc.globalMode, tc.ruleMode), func(t *testing.T) {
							common.MemoryCacheEnabled = cached
							model.InitChannelCache()
							snapshot, err := model.BuildRequestPolicy(map[string]string{
								"channel_affinity_setting.enabled":                  "true",
								"channel_affinity_setting.session_mode":             tc.globalMode,
								"channel_affinity_setting.keep_on_channel_disabled": fmt.Sprint(keep),
								"channel_affinity_setting.rules":                    fmt.Sprintf(`[{"name":"session","model_regex":[".*"],"key_sources":[{"type":"request_header","key":"X-Session"}],"session_mode":%q}]`, tc.ruleMode),
							})
							require.NoError(t, err)
							*affinity = snapshot.Affinity
							seed := newPinRetryContext()
							seed.Request.Header.Set("X-Session", t.Name())
							_, found := service.GetPreferredChannelByAffinity(seed, "policy-test", "default")
							require.False(t, found)
							seed.Set("channel_id", 1)
							service.RecordChannelAffinity(seed, 1)
							t.Cleanup(func() { service.ClearCurrentChannelAffinityCache(seed) })
							bound, found := service.GetPreferredChannelByAffinity(seed, "policy-test", "default")
							require.True(t, found)
							require.Equal(t, 1, bound)

							request := newPinRetryContext()
							request.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"policy-test"}`))
							request.Request.Header.Set("Content-Type", "application/json")
							request.Request.Header.Set("X-Session", t.Name())
							common.SetContextKey(request, constant.ContextKeyUsingGroup, "default")
							middleware.Distribute()(request)
							events := service.RequestPolicy(request).Events()
							assert.True(t, slices.ContainsFunc(events, func(event service.PolicyEvent) bool { return event.Decision.Reason == "session_rule_matched" }), "decision events are recorded for every request")
							bound, found = service.GetPreferredChannelByAffinity(seed, "policy-test", "default")
							if tc.blocked {
								assert.Equal(t, http.StatusServiceUnavailable, request.Writer.Status())
								assert.True(t, request.IsAborted())
								assert.Equal(t, keep, found, "binding retention is independent of strict request handling")
								if found {
									assert.Equal(t, 1, bound)
								}
								return
							}
							assert.False(t, request.IsAborted())
							assert.Equal(t, 2, common.GetContextKeyInt(request, constant.ContextKeyChannelId), "prefer falls back to the next eligible channel")
							require.True(t, found)
							assert.Equal(t, 2, bound, "a successful fallback rebinds the session")
						})
					}
				}
			}
		})
	}
}
