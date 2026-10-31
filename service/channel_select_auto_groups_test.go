package service

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	appdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelSelectAutoGroupsTest(t *testing.T) *gorm.DB {
	t.Helper()

	originalDB := model.DB
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalRetryTimes := common.RetryTimes
	originalAutoGroups := setting.AutoGroups2JsonString()
	originalUsableGroups := setting.UserUsableGroups2JSONString()
	originalGroupRatios := ratio_setting.GroupRatio2JSONString()
	originalMaxTokenAutoGroups := setting.GetMaxTokenAutoGroups()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	common.MemoryCacheEnabled = true
	common.RetryTimes = 0

	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`[]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":2}`))
	require.NoError(t, setting.UpdateMaxTokenAutoGroups("2"))

	t.Cleanup(func() {
		model.DB = originalDB
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		common.RetryTimes = originalRetryTimes
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalGroupRatios))
		require.NoError(t, setting.UpdateMaxTokenAutoGroups(fmt.Sprintf("%d", originalMaxTokenAutoGroups)))

		if originalMemoryCacheEnabled && originalDB != nil &&
			originalDB.Migrator().HasTable(&model.Channel{}) && originalDB.Migrator().HasTable(&model.Ability{}) {
			model.InitChannelCache()
		}
		sqlDB, err := db.DB()
		if err == nil {
			require.NoError(t, sqlDB.Close())
		}
	})

	return db
}

func createChannelSelectAutoGroupsChannel(t *testing.T, db *gorm.DB, id int, group, modelName string) {
	t.Helper()
	priority := int64(0)
	weight := uint(100)
	require.NoError(t, db.Create(&model.Channel{
		Id:       id,
		Type:     constant.ChannelTypeOpenAI,
		Key:      fmt.Sprintf("key-%d", id),
		Status:   common.ChannelStatusEnabled,
		Name:     fmt.Sprintf("channel-%d", id),
		Weight:   &weight,
		Models:   modelName,
		Group:    group,
		Priority: &priority,
	}).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group:     group,
		Model:     modelName,
		ChannelId: id,
		Enabled:   true,
		Priority:  &priority,
		Weight:    weight,
	}).Error)
}

func TestCacheGetRandomSatisfiedChannelUsesTokenAutoGroupsWhenGlobalAutoIsEmpty(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	const modelName = "auto-groups-runtime-model"
	createChannelSelectAutoGroupsChannel(t, db, 2101, "vip", modelName)
	createChannelSelectAutoGroupsChannel(t, db, 2102, "default", modelName)
	model.InitChannelCache()

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"vip", "default"})
	common.SetContextKey(ctx, constant.ContextKeyTokenCrossGroupRetry, true)

	retry := 0
	param := &RetryParam{
		Ctx:         ctx,
		TokenGroup:  "auto",
		ModelName:   modelName,
		RequestPath: "/v1/chat/completions",
		Retry:       &retry,
	}

	first, selectedGroup, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, 2101, first.Id)
	assert.Equal(t, "vip", selectedGroup)
	assert.Equal(t, "vip", common.GetContextKeyString(ctx, constant.ContextKeyAutoGroup))
	assert.Empty(t, setting.GetAutoGroups(), "the selection must not depend on the global Auto list")

	param.IncreaseRetry()
	second, selectedGroup, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.Equal(t, 2102, second.Id)
	assert.Equal(t, "default", selectedGroup)
	assert.Equal(t, "default", common.GetContextKeyString(ctx, constant.ContextKeyAutoGroup))
}

func TestSelectChannelServiceTierBeforePriorityAndBindings(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	const modelName = "gpt-6"
	for _, spec := range []struct {
		id       int
		group    string
		priority int64
		tiers    []string
	}{
		{2201, "vip", 100, []string{"default", "priority"}},
		{2202, "default", 100, []string{"default", "priority"}},
		{2203, "default", 0, []string{"ultrafast"}},
	} {
		createChannelSelectAutoGroupsChannel(t, db, spec.id, spec.group, modelName)
		var channel model.Channel
		require.NoError(t, db.First(&channel, spec.id).Error)
		channel.SetOtherSettings(dto.ChannelOtherSettings{SupportedServiceTiers: spec.tiers})
		require.NoError(t, db.Model(&channel).Updates(map[string]any{"settings": channel.OtherSettings, "priority": spec.priority}).Error)
		require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", spec.id).Update("priority", spec.priority).Error)
	}
	model.InitChannelCache()
	for _, tc := range []struct {
		name, group, tier, policy string
		want                      int
		retry                     int
		pin                       bool
	}{
		{name: "ultrafast bypasses higher priority", group: "default", tier: "ultrafast", want: 2203},
		{name: "ultrafast retry stays eligible", group: "default", tier: "ultrafast", retry: 1, want: 2203},
		{name: "auto skips incompatible first group", group: "auto", tier: "ultrafast", want: 2203},
		{name: "key lock overrides default", group: "default", tier: "default", policy: "ultrafast", want: 2203},
		{name: "ordinary requests require default", group: "default", want: 2202},
		{name: "fast alias", group: "default", tier: "fast", want: 2202},
		{name: "unknown preserves existing selection", group: "default", tier: "future-tier", want: 2202},
		{name: "pin cannot bypass tier", group: "default", tier: "ultrafast", pin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			common.SetContextKey(c, constant.ContextKeyUsingGroup, tc.group)
			common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
			common.SetContextKey(c, constant.ContextKeyTokenAutoGroups, []string{"vip", "default"})
			common.SetContextKey(c, constant.ContextKeyTokenOpenAITierPolicy, tc.policy)
			SetRequestServiceTierFilter(c, modelName, tc.tier)
			if tc.pin {
				GetChannelConstraints(c).AddPin(appdto.ChannelPin{ChannelId: 2202, Source: appdto.PinSourceToken})
			}
			channel, group, err := SelectChannelForRequest(c, modelName, &RetryParam{Ctx: c, ModelName: modelName, TokenGroup: tc.group, Retry: &tc.retry})
			if tc.want == 0 {
				require.NotNil(t, err)
				assert.Equal(t, appdto.FilterServiceTier, err.FilterKind)
				assert.Nil(t, channel)
				return
			}
			require.Nil(t, err)
			require.NotNil(t, channel)
			assert.Equal(t, tc.want, channel.Id)
			assert.Equal(t, "default", group)
		})
	}

	// A stored binding cannot reintroduce a channel filtered out by the tier.
	affinity := operation_setting.GetChannelAffinitySetting()
	previousAffinity := *affinity
	t.Cleanup(func() { *affinity = previousAffinity })
	snapshot, err := model.BuildRequestPolicy(map[string]string{
		"channel_affinity_setting.enabled":      "true",
		"channel_affinity_setting.session_mode": "prefer",
		"channel_affinity_setting.rules":        `[{"name":"tier-session","model_regex":[".*"],"key_sources":[{"type":"request_header","key":"X-Session"}],"session_mode":"inherit"}]`,
	})
	require.NoError(t, err)
	*affinity = snapshot.Affinity
	for _, mode := range []string{"prefer", "strict"} {
		t.Run("affinity "+mode, func(t *testing.T) {
			affinity.SessionMode = mode
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			c.Request.Header.Set("X-Session", t.Name())
			_, found := GetPreferredChannelByAffinity(c, modelName, "default")
			require.False(t, found)
			RecordChannelAffinity(c, 2202)
			t.Cleanup(func() { ClearCurrentChannelAffinityCache(c) })
			SetRequestServiceTierFilter(c, modelName, "ultrafast")
			channel, _, selectErr := SelectChannelForRequest(c, modelName, &RetryParam{Ctx: c, ModelName: modelName, TokenGroup: "default"})
			if mode == "strict" {
				require.NotNil(t, selectErr)
				assert.Equal(t, "strict_session_binding_unavailable", selectErr.Message)
				assert.Nil(t, channel)
				return
			}
			require.Nil(t, selectErr)
			require.NotNil(t, channel)
			assert.Equal(t, 2203, channel.Id)
		})
	}
}

func TestChannelAffinityFallbackRouting(t *testing.T) {
	const modelName = "gpt-6"
	for _, tc := range []struct {
		name                      string
		bound                     int
		want                      []int
		legacy, empty, noIdentity bool
		token, strict, pin        bool
		filtered, otherGroup      bool
		auto                      bool
		disabled                  []int
		stopReason                string
		errorCode                 types.ErrorCode
		explicitSkip, noBudget    bool
	}{
		{name: "new chain", want: []int{3101, 3102, 3103}},
		{name: "existing B goes directly to C", bound: 3102, want: []int{3102, 3103}},
		{name: "existing C is preserved", bound: 3103, want: []int{3103}},
		{name: "binding outside chain is preserved without restarting", bound: 3199, want: []int{3199}},
		{name: "missing field keeps legacy priorities", legacy: true, bound: 3102, want: []int{3102, 3199}},
		{name: "empty array keeps legacy priorities", empty: true, bound: 3102, want: []int{3102, 3199}},
		{name: "no identity keeps legacy routing", noIdentity: true, want: []int{3199}},
		{name: "token identity fallback", token: true, want: []int{3101, 3102, 3103}},
		{name: "disabled members are skipped", disabled: []int{3101}, want: []int{3102, 3103}},
		{name: "unavailable tail stops on upstream error", disabled: []int{3102, 3103}, want: []int{3101}},
		{name: "filters reject old binding and chain members", bound: 3102, filtered: true, want: []int{3103}},
		{name: "group membership cannot be bypassed", otherGroup: true, want: []int{3101, 3103}},
		{name: "auto groups cannot restart the chain", auto: true, want: []int{3101, 3102, 3103}},
		{name: "strict does not fall back", strict: true, bound: 3102, want: []int{3102}, stopReason: "strict_session"},
		{name: "pin wins over chain", pin: true, want: []int{3199}, stopReason: "pinned_channel"},
		{name: "bad response body stays excluded", errorCode: types.ErrorCodeBadResponseBody, want: []int{3101}, stopReason: "system_retry_exclusion"},
		{name: "explicit skip stays excluded", explicitSkip: true, want: []int{3101}, stopReason: "non_retryable_error"},
		{name: "attempt budget still applies", noBudget: true, want: []int{3101}, stopReason: "attempt_budget_exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupChannelSelectAutoGroupsTest(t)
			for i, id := range []int{3101, 3102, 3103, 3199} {
				createChannelSelectAutoGroupsChannel(t, db, id, "default", modelName)
				priority := int64(24 - i)
				if id == 3199 {
					priority = 99 // Ordinary routing must never escape to this channel.
				}
				require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", id).Update("priority", priority).Error)
				require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", id).Update("priority", priority).Error)
			}
			for _, id := range tc.disabled {
				require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", id).Update("status", common.ChannelStatusManuallyDisabled).Error)
				require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", id).Update("enabled", false).Error)
			}
			if tc.otherGroup {
				require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 3102).Update("group", "vip").Error)
				require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", 3102).Update("group", "vip").Error)
			}
			if tc.filtered {
				for _, id := range []int{3101, 3102, 3103} {
					var channel model.Channel
					require.NoError(t, db.First(&channel, id).Error)
					tier := "default"
					if id == 3103 {
						tier = "ultrafast"
					}
					channel.SetOtherSettings(dto.ChannelOtherSettings{SupportedServiceTiers: []string{tier}})
					require.NoError(t, db.Model(&channel).Update("settings", channel.OtherSettings).Error)
				}
			}
			model.InitChannelCache()
			setting := operation_setting.GetChannelAffinitySetting()
			previous, previousRedis := *setting, common.RedisEnabled
			t.Cleanup(func() { *setting, common.RedisEnabled = previous, previousRedis })
			common.RedisEnabled = false
			rule := operation_setting.ChannelAffinityRule{
				Name: "ordered-session", ModelRegex: []string{"^gpt-6$"}, SessionMode: "prefer",
				KeySources:         []operation_setting.ChannelAffinityKeySource{{Type: "request_header", Key: "X-Session"}},
				FallbackChannelIDs: []int{3101, 3102, 3103},
				IncludeRuleName:    true, IncludeModelName: true, IncludeUsingGroup: true,
			}
			if tc.legacy {
				rule.FallbackChannelIDs = nil
			} else if tc.empty {
				rule.FallbackChannelIDs = []int{}
			}
			if tc.strict {
				rule.SessionMode = "strict"
			}
			rules := []operation_setting.ChannelAffinityRule{rule}
			if tc.token {
				token := rule
				token.Name = "ordered-token"
				token.KeySources = []operation_setting.ChannelAffinityKeySource{{Type: "context_int", Key: "token_id"}}
				rules = append(rules, token)
			}
			*setting = operation_setting.ChannelAffinitySetting{Enabled: true, SwitchOnSuccess: true, Rules: rules, DefaultTTLSeconds: 3600}
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			if !tc.noIdentity && !tc.token {
				ctx.Request.Header.Set("X-Session", t.Name())
			}
			if tc.token {
				ctx.Set("token_id", 77)
			}
			group := "default"
			if tc.auto {
				group = "auto"
				common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
				common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"vip", "default"})
			}
			legacyRule := rule
			legacyRule.FallbackChannelIDs = nil
			cacheKey := buildChannelAffinityCacheKeySuffix(legacyRule, modelName, group, t.Name())
			assert.Equal(t, cacheKey, buildChannelAffinityCacheKeySuffix(rule, modelName, group, t.Name()))
			cache := getChannelAffinityCache()
			t.Cleanup(func() { _, err := cache.DeleteMany([]string{cacheKey}); assert.NoError(t, err) })
			if tc.token {
				tokenKey := buildChannelAffinityCacheKeySuffix(rules[1], modelName, group, "77")
				t.Cleanup(func() { _, err := cache.DeleteMany([]string{tokenKey}); assert.NoError(t, err) })
			}
			if tc.bound > 0 {
				require.NoError(t, cache.SetWithTTL(cacheKey, tc.bound, time.Minute))
			}
			if tc.filtered {
				SetRequestServiceTierFilter(ctx, modelName, "ultrafast")
			}
			if tc.pin {
				GetChannelConstraints(ctx).AddPin(appdto.ChannelPin{ChannelId: 3199, Source: appdto.PinSourceToken, RetryMode: appdto.PinRetrySingleAttempt})
			}
			param := &RetryParam{Ctx: ctx, TokenGroup: group, ModelName: modelName}
			channel, selectedGroup, selectErr := SelectChannelForRequest(ctx, modelName, param)
			require.Nil(t, selectErr)
			for i, want := range tc.want {
				require.NotNil(t, channel)
				assert.Equal(t, want, channel.Id)
				if !tc.pin {
					assert.Equal(t, "default", selectedGroup)
				}
				code := tc.errorCode
				if code == "" {
					code = types.ErrorCodeBadResponseStatusCode
				}
				status := 502
				if !tc.legacy && !tc.empty {
					status = []int{524, 504}[i%2]
				}
				upstream := types.NewOpenAIError(fmt.Errorf("original upstream failure"), code, status)
				if tc.explicitSkip {
					types.ErrOptionWithSkipRetry()(upstream)
				}
				budget := 5 - i
				if tc.noBudget {
					budget = 0
				}
				decision := DecideRelayRetry(ctx, upstream, budget)
				assert.Equal(t, status, upstream.StatusCode, "keep the real upstream HTTP status")
				assert.Equal(t, code, upstream.GetErrorCode())
				if i+1 < len(tc.want) {
					require.Equal(t, "retry", decision.Action)
					param.IncreaseRetry()
					var err error
					channel, selectedGroup, err = CacheGetRandomSatisfiedChannel(param)
					require.NoError(t, err)
					continue
				}
				reason := tc.stopReason
				if reason == "" && !tc.legacy && !tc.empty {
					reason = "fallback_chain_exhausted"
					if tc.noIdentity {
						reason = "system_retry_exclusion"
					}
				}
				if reason != "" {
					assert.Equal(t, "stop", decision.Action)
					assert.Equal(t, reason, decision.Reason)
				}
				if reason == "fallback_chain_exhausted" {
					param.IncreaseRetry()
					next, _, err := CacheGetRandomSatisfiedChannel(param)
					assert.Nil(t, next)
					require.EqualError(t, err, "channel_affinity_fallback_exhausted")
				}
			}
		})
	}
}

func TestChannelAffinityFallbackValidation(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		valid       bool
	}{
		{name: "missing", valid: true},
		{name: "empty", value: `,"fallback_channel_ids":[]`, valid: true},
		{name: "null", value: `,"fallback_channel_ids":null`, valid: true},
		{name: "ordered positive IDs", value: `,"fallback_channel_ids":[56,40,19]`, valid: true},
		{name: "zero", value: `,"fallback_channel_ids":[0]`},
		{name: "negative", value: `,"fallback_channel_ids":[-1]`},
		{name: "duplicate", value: `,"fallback_channel_ids":[56,40,56]`},
		{name: "fractional", value: `,"fallback_channel_ids":[1.5]`},
		{name: "string", value: `,"fallback_channel_ids":["56"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := `[{"name":"validation","session_mode":"prefer"` + tc.value + `}]`
			snapshot, err := model.BuildRequestPolicy(map[string]string{"channel_affinity_setting.rules": rules})
			if tc.valid {
				require.NoError(t, err)
				assert.Equal(t, rules, snapshot.Options["channel_affinity_setting.rules"], "management JSON must round-trip unchanged")
			} else {
				require.Error(t, err)
			}
		})
	}
}
