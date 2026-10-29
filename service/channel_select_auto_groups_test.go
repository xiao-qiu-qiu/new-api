package service

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	appdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
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
