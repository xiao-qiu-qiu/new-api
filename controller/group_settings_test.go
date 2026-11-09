package controller

import (
	"context"
	"errors"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGroupSettingsLifecycle(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			if dialect != "sqlite" && os.Getenv("TEST_"+strings.ToUpper(dialect)+"_DSN") == "" {
				if os.Getenv("TEST_GROUP_SETTINGS_REQUIRE_ALL_DB") == "1" {
					t.Fatal("three-database verification requires TEST_" + strings.ToUpper(dialect) + "_DSN")
				}
				t.Skip("set TEST_" + strings.ToUpper(dialect) + "_DSN to exercise this database")
			}
			t.Setenv("TEST_MANAGE_USER_DIALECT", dialect)
			runGroupSettingsLifecycle(t)
		})
	}
}

func TestUserGroupIdentityAndRouteSelection(t *testing.T) {
	groups, err := setting.ParseUserGroups(`{"default":"","members":"","legacy":"private"}`)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"default": "", "members": "", "legacy": "private"}, groups)
	for _, invalid := range []string{`null`, `[]`, `{"members":null}`, `{"members":1}`, `{"auto":""}`, `{"*":""}`, `{" members":""}`, `{"members":"auto"}`, `{"members":" private"}`} {
		_, err := setting.ParseUserGroups(invalid)
		assert.Error(t, err, invalid)
	}

	snapshot := &setting.GroupSettingsSnapshot{
		UserGroups:          groups,
		Ratios:              map[string]float64{"default": 1, "members": 2, "private": 3, "public": 4},
		UsableGroups:        map[string]string{"public": "Public", "auto": "Automatic"},
		SpecialUsableGroups: map[string]map[string]string{"members": {"+:default": "Default"}},
	}
	assert.Equal(t, "private", snapshot.DefaultGroup("legacy"), "historical non-Key consumers retain explicit mappings")
	assert.Equal(t, "members", snapshot.DefaultGroup("members"), "pure identities retain the historical same-name fallback outside Key selection")
	assert.Equal(t, "default", snapshot.DefaultGroup("missing"))
	assert.Equal(t, "default", snapshot.DefaultGroup("default"))
	assert.Equal(t, map[string]string{"public": "Public", "auto": "Automatic"}, snapshot.Usable("legacy"))
	assert.Equal(t, map[string]string{"public": "Public", "auto": "Automatic", "default": "Default"}, snapshot.Usable("members"))
	assert.False(t, snapshot.Selectable("legacy", "private"))
	assert.False(t, snapshot.Selectable("members", "members"))
	assert.Equal(t, []string{"public"}, snapshot.FilterAutoGroups("members", []string{"members", "private", "public"}, 3))
	snapshot.UsableGroups["private"] = "Private"
	snapshot.SpecialUsableGroups["legacy"] = map[string]string{"-:private": ""}
	assert.NotContains(t, snapshot.Usable("legacy"), "private", "legacy defaults cannot undo an explicit visibility removal")
}

func TestSpecialPricingResolution(t *testing.T) {
	rules, err := setting.ParseSpecialPricingRules(`[
		{"user_group":"*","model":"gpt-image-2","price":0.1,"billing_unit":"request"},
		{"user_group":"*","model":"gpt-image-*","price":0.03,"billing_unit":"image"},
		{"user_group":"student","model":"*","price":0.02,"billing_unit":"request"},
		{"user_group":"student","model":"gpt-image-*","price":0,"billing_unit":"image"},
		{"user_group":"student","model":"gpt-image-2*","price":0.01,"billing_unit":"image"},
		{"user_group":"student","model":"gpt-image-2","price":0.012,"billing_unit":"request"},
		{"user_group":"student","model":"ab*cd","price":0.04,"billing_unit":"request"},
		{"user_group":"student","model":"abc*d","price":0.05,"billing_unit":"request"},
		{"user_group":"student","model":"provider/*image-*","price":0.06,"billing_unit":"image"},
		{"user_group":"student","model":"q?[*","price":0.07,"billing_unit":"request"},
		{"user_group":"student","model":"prefix**suffix","price":0.08,"billing_unit":"request"},
		{"user_group":"student","model":"汉字*","price":0.09,"billing_unit":"request"},
		{"user_group":"student","model":"*abc","price":0.11,"billing_unit":"request"}
	]`)
	require.NoError(t, err)
	snapshot := &setting.GroupSettingsSnapshot{SpecialPricingRules: rules}
	for _, tc := range []struct {
		name, userGroup, model string
		index                  int
	}{
		{"specific group beats fallback exact", "student", "gpt-image-3", 3},
		{"group wildcard beats fallback exact", "student", "not-an-image", 2},
		{"exact model beats earlier wildcard", "student", "gpt-image-2", 5},
		{"more literal characters win", "student", "gpt-image-2-preview", 4},
		{"same specificity keeps configuration order", "student", "abcd", 6},
		{"fallback exact wins", "default", "gpt-image-2", 0},
		{"fallback wildcard wins", "default", "gpt-image-3", 1},
		{"wildcard crosses slashes", "student", "provider/nested/gpt-image-2", 8},
		{"other glob syntax is literal", "student", "q?[anything", 9},
		{"question mark is not a wildcard", "student", "qa[anything", 2},
		{"consecutive stars match empty sequences", "student", "prefixsuffix", 10},
		{"suffix remains anchored", "student", "prefixsuffix-extra", 2},
		{"specificity counts characters not UTF8 bytes", "student", "汉字abc", 12},
		{"case remains unchanged", "default", "GPT-IMAGE-2", -1},
		{"no model normalization", "default", "alias/gpt-image-2", -1},
		{"no match", "default", "other-model", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule, matched := snapshot.ResolveSpecialPricing(tc.userGroup, tc.model)
			assert.Equal(t, tc.index >= 0, matched)
			if tc.index >= 0 {
				assert.Equal(t, rules[tc.index], rule)
			}
		})
	}
	// A group-specific broad rule must beat even an exact all-user rule.
	groupFirst := &setting.GroupSettingsSnapshot{SpecialPricingRules: []setting.SpecialPricingRule{rules[0], rules[3]}}
	rule, matched := groupFirst.ResolveSpecialPricing("student", "gpt-image-2")
	require.True(t, matched)
	assert.Zero(t, rule.Price)
}

func TestSpecialPricingBillingExpressionAndBounds(t *testing.T) {
	previousQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() { common.QuotaPerUnit = previousQuotaPerUnit })
	for _, tc := range []struct {
		name, unit, expression string
		price                  float64
		count, quota           int
	}{
		{"free images", "image", `tier("special", fixed(0)) * image_count`, 0, 128, 0},
		{"per image", "image", `tier("special", fixed(0.03)) * image_count`, 0.03, 3, 45000},
		{"per request", "request", `tier("special", fixed(0.05))`, 0.05, 128, 25000},
		{"scientific notation", "image", `tier("special", fixed(1e-07)) * image_count`, 1e-7, 128, 6},
		{"request price uses quota not million scaling", "request", `tier("special", fixed(1000))`, 1000, 1, 500000000},
		{"large safe image price", "image", `tier("special", fixed(30)) * image_count`, 30, 128, 1920000000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule := setting.SpecialPricingRule{UserGroup: "*", Model: "model", Price: tc.price, BillingUnit: tc.unit}
			require.NoError(t, rule.Validate())
			expression := rule.BillingExpression()
			assert.Equal(t, tc.expression, expression)
			result, err := billingexpr.ComputeTieredQuotaWithRequest(&billingexpr.BillingSnapshot{
				ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: common.QuotaPerUnit,
			}, billingexpr.TokenParams{}, billingexpr.RequestInput{ImageCount: &tc.count})
			require.NoError(t, err)
			assert.Equal(t, tc.quota, result.ActualQuotaAfterGroup)
			assert.Equal(t, "special", result.MatchedTier)
			assert.Nil(t, result.Clamp)
		})
	}
	for _, price := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1), math.MaxFloat64} {
		rule := setting.SpecialPricingRule{UserGroup: "*", Model: "model", Price: price, BillingUnit: "image"}
		assert.Error(t, rule.Validate())
	}
	imageLimit := float64(common.MaxQuota) / float64(dto.MaxImageN) / common.QuotaPerUnit
	for _, tc := range []struct {
		unit  string
		price float64
		valid bool
	}{
		{"image", imageLimit - 0.000001, true},
		{"image", imageLimit + 0.000001, false},
		{"request", imageLimit + 0.000001, true},
		{"request", float64(common.MaxQuota)/common.QuotaPerUnit + 1, false},
	} {
		rule := setting.SpecialPricingRule{UserGroup: "*", Model: "model", Price: tc.price, BillingUnit: tc.unit}
		assert.Equal(t, tc.valid, rule.Validate() == nil, "%s price %g", tc.unit, tc.price)
	}
}

func runGroupSettingsLifecycle(t *testing.T) {
	t.Helper()
	db := setupManageUserTestDB(t)
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitLogDB())
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.Token{}, &model.Channel{}))
	oldGroups, oldConfigured := setting.UserGroups2JSONString(), setting.UserGroupsConfigured()
	oldRatios := ratio_setting.GroupRatio2JSONString()
	oldTopup := common.TopupGroupRatio2JSONString()
	oldSpecial := ratio_setting.GroupGroupRatio2JSONString()
	oldSpecialPricing := setting.SpecialPricingRules2JSONString()
	oldUsable := setting.UserUsableGroups2JSONString()
	oldSpecialUsable := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.MarshalJSONString()
	oldOptions := maps.Clone(common.OptionMap)
	common.OptionMap = make(map[string]string)
	setting.ResetUserGroups()
	setting.ResetSpecialPricingRules()
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"legacy":2,"route_only":3}`))
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"default":1,"topup_only":1}`))
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(`{"special_source":{"legacy":0.7}}`))
	require.NoError(t, ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.UnmarshalJSON([]byte(`{}`)))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","route_only":"Route"}`))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateUserGroupsByJSONString(oldGroups))
		if !oldConfigured {
			setting.ResetUserGroups()
		}
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldRatios))
		require.NoError(t, common.UpdateTopupGroupRatioByJSONString(oldTopup))
		require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(oldSpecial))
		require.NoError(t, setting.UpdateSpecialPricingRulesByJSONString(oldSpecialPricing))
		require.NoError(t, ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.UnmarshalJSON([]byte(oldSpecialUsable)))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(oldUsable))
		common.OptionMap = oldOptions
	})
	legacy := model.User{Username: "legacy-account", Password: "placeholder", Group: "legacy", Status: common.UserStatusEnabled, AffCode: "group-legacy"}
	require.NoError(t, db.Create(&legacy).Error)
	plan := model.SubscriptionPlan{Title: "Group plan", UpgradeGroup: "plan_member", DowngradeGroup: "plan_return"}
	require.NoError(t, db.Create(&plan).Error)
	require.NoError(t, db.Create(&model.UserSubscription{UserId: legacy.Id, PlanId: plan.Id, Status: "active", PrevUserGroup: "snapshot_return"}).Error)
	groups, err := model.GetUserGroupRegistry()
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"default": "", "legacy": "legacy", "plan_member": "default", "plan_return": "default", "snapshot_return": "default", "topup_only": "default", "special_source": "default"}, groups)
	assert.NotContains(t, groups, "route_only")
	assert.False(t, setting.UserGroupsConfigured(), "a read must not persist an inferred registry")

	ctx, recorder := newAuthenticatedContext(t, http.MethodGet, "/api/group/user", nil, 1)
	GetAdminUserGroups(ctx)
	var list struct {
		Success bool     `json:"success"`
		Data    []string `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &list))
	assert.True(t, list.Success)
	assert.ElementsMatch(t, []string{"default", "legacy", "plan_member", "plan_return", "snapshot_return", "topup_only", "special_source"}, list.Data)
	ctx, recorder = newAuthenticatedContext(t, http.MethodGet, "/api/option/", nil, 1)
	GetOptions(ctx)
	var optionsResponse struct {
		Data []model.Option `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &optionsResponse))
	var baseline string
	for _, option := range optionsResponse.Data {
		if option.Key == "UserGroups" {
			baseline = option.Value
		}
	}
	encoded, err := common.Marshal(groups)
	require.NoError(t, err)
	assert.JSONEq(t, string(encoded), baseline)

	groups["student"] = "standard"
	encoded, err = common.Marshal(groups)
	require.NoError(t, err)
	ctx, recorder = newAuthenticatedContext(t, http.MethodPut, "/api/group/settings", map[string]any{"options": map[string]string{
		"UserGroups": string(encoded), "GroupRatio": `{"default":1,"legacy":2,"standard":3,"route_only":4}`,
		"TopupGroupRatio": `{"default":1,"topup_only":1,"student":0.8}`,
		"GroupGroupRatio": `{"student":{"standard":0.5}}`,
	}}, 1)
	UpdateGroupSettings(ctx)
	require.True(t, decodeAPIResponse(t, recorder).Success, recorder.Body.String())
	assert.Equal(t, "standard", setting.GetUserDefaultRatioGroup("student"))
	assert.Equal(t, "legacy", setting.GetUserDefaultRatioGroup("legacy"))
	assert.Equal(t, 0.5, service.GetUserGroupRatio("student", "standard"), "special ratio replaces the base ratio")
	assert.Equal(t, 0.8, common.GetTopupGroupRatio("student"))
	require.NoError(t, model.UpdateGroupSettingsOptions(map[string]string{"TopupGroupRatio": `{"default":1,"topup_only":1,"student":0}`}))
	assert.Zero(t, common.GetTopupGroupRatio("student"), "zero retains its existing top-up semantics")
	require.NoError(t, model.UpdateGroupSettingsOptions(map[string]string{"TopupGroupRatio": `{"default":1,"topup_only":1,"student":0.8}`}))
	assert.NotContains(t, ratio_setting.GetGroupRatioCopy(), "student")
	assert.NotContains(t, service.GetUserUsableGroups("student"), "student")
	assert.NotContains(t, service.GetUserUsableGroups("student"), "standard", "an identity's legacy default does not grant Key routing access")
	t.Run("pure identity persistence and reference protection", func(t *testing.T) {
		withIdentity := maps.Clone(groups)
		withIdentity["identity_only"] = ""
		encoded, err := common.Marshal(withIdentity)
		require.NoError(t, err)
		ctx, recorder := newAuthenticatedContext(t, http.MethodPut, "/api/group/settings", map[string]any{"options": map[string]string{
			"UserGroups": string(encoded), "TopupGroupRatio": `{"default":1,"topup_only":1,"student":0.8,"identity_only":0.75}`,
		}}, 1)
		UpdateGroupSettings(ctx)
		require.True(t, decodeAPIResponse(t, recorder).Success, recorder.Body.String())
		for range 2 {
			model.InitOptionMap()
			assert.Equal(t, withIdentity, setting.GetUserGroupsCopy())
			assert.Equal(t, "standard", setting.GetUserDefaultRatioGroup("student"))
			assert.Equal(t, "default", setting.GetUserDefaultRatioGroup("identity_only"))
			assert.Equal(t, 0.75, common.GetTopupGroupRatio("identity_only"))
		}
		var persisted model.Option
		require.NoError(t, db.Where(&model.Option{Key: "UserGroups"}).First(&persisted).Error)
		assert.JSONEq(t, string(encoded), persisted.Value)
		identityUser := model.User{Username: "identity-only", Group: "identity_only", AffCode: "identity-only"}
		require.NoError(t, db.Create(&identityUser).Error)
		ctx, recorder = newAuthenticatedContext(t, http.MethodGet, "/api/user/self/groups", nil, identityUser.Id)
		GetUserGroups(ctx)
		var response struct {
			Success bool                      `json:"success"`
			Data    map[string]map[string]any `json:"data"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		require.True(t, response.Success)
		assert.Len(t, response.Data, 2)
		assert.Contains(t, response.Data, "default")
		assert.Contains(t, response.Data, "route_only")
		assert.NotContains(t, response.Data, "identity_only")
		assert.NotContains(t, response.Data, "auto", "real routes are available without enabling the compatibility Auto entry")
		require.NoError(t, db.AutoMigrate(&model.Ability{}))
		require.NoError(t, db.Create(&[]model.Ability{
			{Group: "default", Model: "identity-public-a", ChannelId: 1, Enabled: true},
			{Group: "default", Model: "identity-shared", ChannelId: 1, Enabled: true},
			{Group: "route_only", Model: "identity-public-b", ChannelId: 1, Enabled: true},
			{Group: "route_only", Model: "identity-shared", ChannelId: 1, Enabled: true},
			{Group: "standard", Model: "identity-private", ChannelId: 1, Enabled: true},
			{Group: "identity_only", Model: "identity-same-name", ChannelId: 1, Enabled: true},
		}).Error)
		for _, tc := range []struct {
			query  string
			models []string
		}{
			{"", []string{"identity-public-a", "identity-public-b", "identity-shared"}},
			{"?group=route_only", []string{"identity-public-b", "identity-shared"}},
			{"?group=standard", []string{}},
		} {
			ctx, recorder = newAuthenticatedContext(t, http.MethodGet, "/api/user/models"+tc.query, nil, identityUser.Id)
			GetUserModels(ctx)
			assert.ElementsMatch(t, tc.models, decodeUserModelsResponse(t, recorder), tc.query)
		}
		encoded, err = common.Marshal(groups)
		require.NoError(t, err)
		restore := map[string]string{"UserGroups": string(encoded), "TopupGroupRatio": `{"default":1,"topup_only":1,"student":0.8}`}
		assert.ErrorContains(t, model.UpdateGroupSettingsOptions(restore), "User group is still referenced")
		assert.Equal(t, withIdentity, setting.GetUserGroupsCopy())
		require.NoError(t, db.Delete(&identityUser).Error)
		require.NoError(t, model.UpdateGroupSettingsOptions(restore))
	})
	require.NoError(t, model.ValidateUserGroup("student"))
	assert.Error(t, model.ValidateUserGroup("route_only"))
	require.NoError(t, model.UpdateGroupSettingsOptions(map[string]string{"GroupRatio": `{"default":1,"legacy":2,"standard":0,"route_only":4}`, "GroupGroupRatio": `{"student":{"standard":0}}`}))
	assert.Zero(t, ratio_setting.GetGroupRatio("standard"))
	assert.Zero(t, service.GetUserGroupRatio("student", "standard"))
	require.NoError(t, model.UpdateGroupSettingsOptions(map[string]string{"GroupRatio": `{"default":1,"legacy":2,"standard":3,"route_only":4}`, "GroupGroupRatio": `{"student":{"standard":0.5}}`}))
	t.Run("special pricing persistence validation and references", func(t *testing.T) {
		model.InitOptionMap()
		assert.JSONEq(t, `[]`, common.OptionMap["SpecialPricingRules"])
		assert.NotNil(t, setting.CaptureGroupSettings().SpecialPricingRules)
		rules := `[{"user_group":"*","model":"gpt-image-*","price":0.03,"billing_unit":"image"},{"user_group":"student","model":"gpt-image-*","price":0,"billing_unit":"image"},{"user_group":"student","model":"request-model","price":0.05,"billing_unit":"request"}]`
		ctx, recorder := newAuthenticatedContext(t, http.MethodPut, "/api/group/settings", map[string]any{"options": map[string]string{"SpecialPricingRules": rules}}, 1)
		UpdateGroupSettings(ctx)
		require.True(t, decodeAPIResponse(t, recorder).Success, recorder.Body.String())
		var persisted model.Option
		require.NoError(t, db.Where(&model.Option{Key: "SpecialPricingRules"}).First(&persisted).Error)
		assert.JSONEq(t, rules, persisted.Value)
		for range 2 {
			model.InitOptionMap()
			assert.JSONEq(t, rules, common.OptionMap["SpecialPricingRules"])
			assert.JSONEq(t, rules, setting.SpecialPricingRules2JSONString())
			free, matched := setting.CaptureGroupSettings().ResolveSpecialPricing("student", "gpt-image-2")
			require.True(t, matched)
			assert.Zero(t, free.Price)
			fallback, matched := setting.CaptureGroupSettings().ResolveSpecialPricing("default", "gpt-image-2")
			require.True(t, matched)
			assert.Equal(t, 0.03, fallback.Price)
		}
		ctx, recorder = newAuthenticatedContext(t, http.MethodGet, "/api/option/", nil, 1)
		GetOptions(ctx)
		var response struct {
			Data []model.Option `json:"data"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		found := false
		for _, option := range response.Data {
			if option.Key == "SpecialPricingRules" {
				found = true
				assert.JSONEq(t, rules, option.Value)
			}
		}
		assert.True(t, found, "the settings endpoint must expose special pricing")
		for _, invalid := range []string{
			`null`, `{}`, `[null]`, `[{}]`,
			`[{"user_group":"student","model":"gpt-image-*","billing_unit":"image"}]`,
			`[{"user_group":"student","model":"gpt-image-*","price":null,"billing_unit":"image"}]`,
			`[{"user_group":"student","model":"gpt-image-*","price":"0","billing_unit":"image"}]`,
			`[{"user_group":"student","model":"gpt-image-*","price":-0.01,"billing_unit":"image"}]`,
			`[{"user_group":"student","model":"gpt-image-*","price":1e309,"billing_unit":"image"}]`,
			`[{"user_group":"student","model":"gpt-image-*","price":1e20,"billing_unit":"image"}]`,
			`[{"user_group":"student","model":"gpt-image-*","price":0,"billing_unit":"token"}]`,
			`[{"user_group":"student","model":"gpt-image-*","price":0}]`,
			`[{"user_group":"student","model":"","price":0,"billing_unit":"image"}]`,
			`[{"user_group":"student","model":" gpt-image-*","price":0,"billing_unit":"image"}]`,
			`[{"user_group":"","model":"gpt-image-*","price":0,"billing_unit":"image"}]`,
			`[{"user_group":"auto","model":"gpt-image-*","price":0,"billing_unit":"image"}]`,
			`[{"user_group":" student","model":"gpt-image-*","price":0,"billing_unit":"image"}]`,
			`[{"user_group":"route_only","model":"gpt-image-*","price":0,"billing_unit":"image"}]`,
			`[{"user_group":"missing","model":"gpt-image-*","price":0,"billing_unit":"image"}]`,
			`[{"user_group":"student","model":"gpt-image-*","price":0,"billing_unit":"image"},{"user_group":"student","model":"gpt-image-*","price":0.02,"billing_unit":"request"}]`,
		} {
			assert.Error(t, model.UpdateOption("SpecialPricingRules", invalid), invalid)
			assert.JSONEq(t, rules, setting.SpecialPricingRules2JSONString())
			persisted = model.Option{}
			require.NoError(t, db.Where(&model.Option{Key: "SpecialPricingRules"}).First(&persisted).Error)
			assert.JSONEq(t, rules, persisted.Value)
		}
		assert.Error(t, model.UpdateGroupSettingsOptions(map[string]string{"UserGroups": `{"default":"default","*":"default"}`}))
		copy := setting.CaptureGroupSettings()
		copy.SpecialPricingRules[0].Price = 99
		assert.JSONEq(t, rules, setting.SpecialPricingRules2JSONString(), "a captured rule list must not alias the live configuration")

		withPricingGroup := maps.Clone(groups)
		withPricingGroup["pricing_only"] = "standard"
		encoded, err := common.Marshal(withPricingGroup)
		require.NoError(t, err)
		pricingOnly := `[{"user_group":"pricing_only","model":"gpt-image-*","price":0,"billing_unit":"image"}]`
		require.NoError(t, model.UpdateGroupSettingsOptions(map[string]string{"UserGroups": string(encoded), "SpecialPricingRules": pricingOnly}))
		encoded, err = common.Marshal(groups)
		require.NoError(t, err)
		assert.ErrorContains(t, model.UpdateGroupSettingsOptions(map[string]string{"UserGroups": string(encoded)}), "User group is still referenced")
		assert.Contains(t, setting.GetUserGroupsCopy(), "pricing_only")
		require.NoError(t, model.UpdateGroupSettingsOptions(map[string]string{"UserGroups": string(encoded), "SpecialPricingRules": rules}))
		assert.NotContains(t, setting.GetUserGroupsCopy(), "pricing_only")

		require.NoError(t, db.Callback().Update().Before("gorm:update").Register("special_pricing_write_failure", func(tx *gorm.DB) {
			if option, ok := tx.Statement.Dest.(*model.Option); ok && option.Key == "SpecialPricingRules" {
				tx.AddError(errors.New("injected special pricing write failure"))
			}
		}))
		assert.Error(t, model.UpdateGroupSettingsOptions(map[string]string{"SpecialPricingRules": `[]`, "TopupGroupRatio": `{"student":0.4}`}))
		require.NoError(t, db.Callback().Update().Remove("special_pricing_write_failure"))
		assert.JSONEq(t, rules, setting.SpecialPricingRules2JSONString())
		assert.Equal(t, 0.8, common.GetTopupGroupRatio("student"))
		persisted = model.Option{}
		require.NoError(t, db.Where(&model.Option{Key: "TopupGroupRatio"}).First(&persisted).Error)
		assert.JSONEq(t, `{"default":1,"topup_only":1,"student":0.8}`, persisted.Value)
		model.InitOptionMap()
		assert.JSONEq(t, rules, setting.SpecialPricingRules2JSONString())

		require.NoError(t, model.UpdateOption("SpecialPricingRules", `[]`))
		model.InitOptionMap()
		assert.JSONEq(t, `[]`, setting.SpecialPricingRules2JSONString())
		require.NoError(t, model.UpdateOption("SpecialPricingRules", rules))
		require.NoError(t, db.Where(&model.Option{Key: "SpecialPricingRules"}).Delete(&model.Option{}).Error)
		model.InitOptionMap()
		assert.JSONEq(t, `[]`, common.OptionMap["SpecialPricingRules"], "an absent persisted option resets to the default")
		require.NoError(t, model.UpdateOption("SpecialPricingRules", rules))
	})
	withoutRouteOnly := `{"default":1,"legacy":2,"standard":3}`
	assert.Error(t, model.UpdateGroupSettingsOptions(map[string]string{"GroupRatio": withoutRouteOnly}), "visible routing groups cannot be orphaned")
	channel := model.Channel{Name: "group-channel", Group: "default,route_only"}
	require.NoError(t, db.Create(&channel).Error)
	assert.Error(t, model.UpdateGroupSettingsOptions(map[string]string{"GroupRatio": withoutRouteOnly, "UserUsableGroups": `{"default":"Default"}`}))
	require.NoError(t, db.Delete(&channel).Error)
	boundToken := model.Token{UserId: legacy.Id, Key: "boundrouteonly", Group: "route_only"}
	require.NoError(t, db.Create(&boundToken).Error)
	assert.Error(t, model.UpdateGroupSettingsOptions(map[string]string{"GroupRatio": withoutRouteOnly, "UserUsableGroups": `{"default":"Default"}`}))
	require.NoError(t, db.Delete(&boundToken).Error)
	autoToken := model.Token{UserId: legacy.Id, Key: "boundcustomautoroute", Group: "auto", AutoGroups: `["route_only"]`}
	require.NoError(t, db.Create(&autoToken).Error)
	assert.Error(t, model.UpdateGroupSettingsOptions(map[string]string{"GroupRatio": withoutRouteOnly, "UserUsableGroups": `{"default":"Default"}`}), "custom token Auto snapshots must not be orphaned")
	require.NoError(t, db.Delete(&autoToken).Error)
	require.NoError(t, model.UpdateGroupSettingsOptions(map[string]string{"GroupRatio": withoutRouteOnly, "UserUsableGroups": `{"default":"Default"}`}))
	require.NoError(t, model.UpdateGroupSettingsOptions(map[string]string{"GroupRatio": `{"default":1,"legacy":2,"standard":3,"route_only":4}`, "UserUsableGroups": `{"default":"Default","route_only":"Route"}`}))

	for _, referenced := range []string{"legacy", "plan_member", "plan_return", "snapshot_return", "default"} {
		t.Run("cannot delete referenced "+referenced, func(t *testing.T) {
			candidate := maps.Clone(groups)
			delete(candidate, referenced)
			encoded, err := common.Marshal(candidate)
			require.NoError(t, err)
			assert.Error(t, model.UpdateGroupSettingsOptions(map[string]string{"UserGroups": string(encoded)}))
			assert.Equal(t, groups, setting.GetUserGroupsCopy())
		})
	}
	before := setting.UserGroups2JSONString()
	for _, invalid := range []map[string]string{
		{"UserGroups": `null`},
		{"UserGroups": `{"default":"default","student":"missing"}`},
		{"UserGroups": `{"auto":"default"}`},
		{"GroupRatio": `{"default":-1}`},
		{"GroupRatio": `{"default":1,"legacy":2,"standard":null,"route_only":4}`},
		{"group_ratio_setting.group_ratio": `{"default":null}`},
		{"GroupGroupRatio": `{"student":{"standard":null}}`},
		{"GroupGroupRatio": `{"student":null}`},
		{"GroupGroupRatio": `{"student":[]}`},
		{"TopupGroupRatio": `{"student":null}`},
		{"TopupGroupRatio": `{"student":false}`},
		{"UserUsableGroups": `{"default":null}`},
		{"UserUsableGroups": `{"default":{}}`},
		{"group_ratio_setting.group_special_usable_group": `{"student":null}`},
		{"group_ratio_setting.group_special_usable_group": `{"student":{"+:standard":null}}`},
		{"AutoGroups": `[null]`},
		{"TopupGroupRatio": `{"student":-1}`},
		{"TopupGroupRatio": `{"route_only":1}`},
		{"GroupGroupRatio": `{"student":{"missing":2}}`},
		{"UserGroups": before, "Notice": "not a group setting"},
	} {
		assert.Error(t, model.UpdateGroupSettingsOptions(invalid))
		assert.JSONEq(t, before, setting.UserGroups2JSONString())
		assert.Equal(t, 3.0, ratio_setting.GetGroupRatio("standard"))
		assert.Equal(t, 0.5, service.GetUserGroupRatio("student", "standard"))
	}

	// Persistence failure rolls every key back and publishes no in-memory change.
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("group_test_failure", func(tx *gorm.DB) {
		if option, ok := tx.Statement.Dest.(*model.Option); ok && option.Key == "TopupGroupRatio" {
			tx.AddError(errors.New("injected option write failure"))
		}
	}))
	assert.Error(t, model.UpdateGroupSettingsOptions(map[string]string{"GroupRatio": `{"default":1,"legacy":2,"standard":5,"route_only":4}`, "TopupGroupRatio": `{"student":0.6}`}))
	require.NoError(t, db.Callback().Update().Remove("group_test_failure"))
	assert.Equal(t, 3.0, ratio_setting.GetGroupRatio("standard"))
	var persisted model.Option
	require.NoError(t, db.Where(&model.Option{Key: "GroupRatio"}).First(&persisted).Error)
	assert.JSONEq(t, `{"default":1,"legacy":2,"standard":3,"route_only":4}`, persisted.Value)

	delete(groups, "topup_only")
	encoded, err = common.Marshal(groups)
	require.NoError(t, err)
	require.NoError(t, model.UpdateGroupSettingsOptions(map[string]string{"UserGroups": string(encoded), "TopupGroupRatio": `{"default":1,"student":0.8}`}))
	// Stale legacy-only sources no longer silently recreate registry entries.
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"default":1,"student":0.8,"topup_only":1}`))
	effective, err := model.GetUserGroupRegistry()
	require.NoError(t, err)
	assert.NotContains(t, effective, "topup_only")
	persisted = model.Option{}
	require.NoError(t, db.Where(&model.Option{Key: "UserGroups"}).First(&persisted).Error)
	setting.ResetUserGroups()
	require.NoError(t, setting.UpdateUserGroupsByJSONString(persisted.Value))
	effective, err = model.GetUserGroupRegistry()
	require.NoError(t, err)
	assert.NotContains(t, effective, "topup_only")
	assert.Equal(t, "standard", effective["student"])

	// Real API-key authentication keeps selected routes separate from billing identity.
	require.NoError(t, model.UpdateGroupSettingsOptions(map[string]string{"UserUsableGroups": `{"default":"Default","standard":"Standard","route_only":"Route"}`}))
	student := model.User{Username: "student-account", Password: "placeholder", Group: "student", Status: common.UserStatusEnabled, AffCode: "group-student"}
	require.NoError(t, db.Create(&student).Error)
	for _, tc := range []struct {
		name, group, route string
		status             int
	}{
		{"missing selection forbidden", "", "", http.StatusForbidden},
		{"explicit", "route_only", "route_only", http.StatusOK},
		{"account group forbidden", "student", "", http.StatusForbidden},
		{"unknown forbidden", "missing", "", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := model.Token{UserId: student.Id, Key: strings.Repeat("x", 30) + strings.ReplaceAll(tc.name, " ", ""), Group: tc.group, Status: common.TokenStatusEnabled, UnlimitedQuota: true, ExpiredTime: -1}
			require.NoError(t, db.Create(&token).Error)
			router := gin.New()
			router.GET("/v1/models", middleware.TokenAuth(), func(c *gin.Context) {
				assert.Equal(t, "student", common.GetContextKeyString(c, constant.ContextKeyUserGroup))
				assert.Equal(t, tc.route, common.GetContextKeyString(c, constant.ContextKeyUsingGroup))
				resolved, err := getModelListGroups(c)
				require.NoError(t, err)
				assert.Equal(t, []string{tc.route}, resolved.ownerGroups)
				c.Status(http.StatusOK)
			})
			request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			request.Header.Set("Authorization", "Bearer sk-"+token.Key)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			assert.Equal(t, tc.status, recorder.Code, recorder.Body.String())
		})
	}
	t.Run("request snapshot survives a committed route replacement", func(t *testing.T) {
		replacementGroups := maps.Clone(groups)
		replacementGroups["student"] = "replacement"
		encoded, err := common.Marshal(replacementGroups)
		require.NoError(t, err)
		token := model.Token{UserId: student.Id, Key: "snapshottestkey", Group: "standard", Status: common.TokenStatusEnabled, UnlimitedQuota: true, ExpiredTime: -1}
		require.NoError(t, db.Create(&token).Error)
		router := gin.New()
		router.GET("/v1/models", middleware.TokenAuth(), func(c *gin.Context) {
			require.Equal(t, "standard", common.GetContextKeyString(c, constant.ContextKeyUsingGroup))
			require.NoError(t, db.Delete(&token).Error, "release the Key's route reference after authentication")
			require.NoError(t, model.UpdateGroupSettingsOptions(map[string]string{
				"UserGroups": string(encoded), "GroupRatio": `{"default":1,"legacy":2,"replacement":7,"route_only":4}`,
				"UserUsableGroups": `{"default":"Default","replacement":"Replacement","route_only":"Route"}`,
				"GroupGroupRatio":  `{"student":{"replacement":0.75}}`, "TopupGroupRatio": `{"default":1,"student":0.8}`,
				"SpecialPricingRules": `[{"user_group":"student","model":"gpt-image-*","price":0.09,"billing_unit":"image"}]`,
			}))
			live := setting.CaptureGroupSettings()
			assert.Equal(t, "replacement", live.DefaultGroup("student"))
			assert.False(t, live.ContainsRatio("standard"))
			livePrice, matched := live.ResolveSpecialPricing("student", "gpt-image-2")
			require.True(t, matched)
			assert.Equal(t, 0.09, livePrice.Price)
			requestSnapshot := setting.RequestGroupSettings(c)
			requestPrice, matched := requestSnapshot.ResolveSpecialPricing("student", "gpt-image-2")
			require.True(t, matched)
			assert.Zero(t, requestPrice.Price, "an in-flight request retains the price captured at authentication")
			assert.Equal(t, "standard", requestSnapshot.DefaultGroup("student"))
			assert.True(t, requestSnapshot.ContainsRatio("standard"))
			assert.Equal(t, []string{"standard"}, requestSnapshot.FilterAutoGroups("student", []string{"standard"}, 1))
			info := &relaycommon.RelayInfo{UserGroup: "student", UsingGroup: "standard"}
			price := helper.HandleGroupRatio(c, info)
			assert.Equal(t, 0.5, price.GroupRatio)
			assert.True(t, price.HasSpecialRatio)
			resolved, err := getModelListGroups(c)
			require.NoError(t, err)
			assert.Equal(t, []string{"standard"}, resolved.ownerGroups)
			c.Status(http.StatusOK)
		})
		request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		request.Header.Set("Authorization", "Bearer sk-"+token.Key)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	})

	t.Run("management endpoint authorization", func(t *testing.T) {
		credentials := map[string]string{"anonymous": ""}
		for _, role := range []struct {
			name string
			role int
		}{{"user", common.RoleCommonUser}, {"admin", common.RoleAdminUser}, {"root", common.RoleRootUser}} {
			user := model.User{Username: "group-auth-" + role.name, Password: "placeholder", Group: "default", Role: role.role, Status: common.UserStatusEnabled, AuthVersion: 1, AffCode: "group-auth-" + role.name}
			require.NoError(t, db.Create(&user).Error)
			_, credentials[role.name] = createAccessTokenTestSession(t, user.Id, "group-session-"+role.name)
			if role.name == "root" {
				credentials["root read PAT"], _ = createScopedAccessToken(t, user.Id, 0, "group:read")
				credentials["root write PAT"], _ = createScopedAccessToken(t, user.Id, 0, "option:write")
			}
		}
		router := gin.New()
		routes := router.Group("/api/group", middleware.AdminAuth())
		routes.GET("/user", GetAdminUserGroups)
		routes.PUT("/settings", middleware.RootAuth(), UpdateGroupSettings)
		for _, tc := range []struct {
			role, method, path string
			status             int
		}{
			{"anonymous", http.MethodGet, "/api/group/user", http.StatusUnauthorized},
			{"user", http.MethodGet, "/api/group/user", http.StatusForbidden},
			{"admin", http.MethodGet, "/api/group/user", http.StatusOK},
			{"anonymous", http.MethodPut, "/api/group/settings", http.StatusUnauthorized},
			{"user", http.MethodPut, "/api/group/settings", http.StatusForbidden},
			{"admin", http.MethodPut, "/api/group/settings", http.StatusForbidden},
			{"root", http.MethodPut, "/api/group/settings", http.StatusOK},
			{"root read PAT", http.MethodGet, "/api/group/user", http.StatusOK},
			{"root read PAT", http.MethodPut, "/api/group/settings", http.StatusForbidden},
			{"root write PAT", http.MethodGet, "/api/group/user", http.StatusForbidden},
			{"root write PAT", http.MethodPut, "/api/group/settings", http.StatusOK},
		} {
			t.Run(tc.role+" "+tc.method, func(t *testing.T) {
				request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"options":{"TopupGroupRatio":"{\"default\":1,\"student\":0.8}"}}`))
				request.Header.Set("Content-Type", "application/json")
				if credential := credentials[tc.role]; credential != "" {
					request.Header.Set("Authorization", "Bearer "+credential)
				}
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				assert.Equal(t, tc.status, recorder.Code, recorder.Body.String())
				if tc.status == http.StatusOK {
					assert.True(t, decodeAPIResponse(t, recorder).Success, recorder.Body.String())
				}
			})
		}
	})

	t.Run("canonical keys win on reload and alias-only keys populate both views", func(t *testing.T) {
		require.NoError(t, db.Save(&model.Option{Key: "GroupGroupRatio", Value: `{}`}).Error)
		require.NoError(t, db.Save(&model.Option{Key: "group_ratio_setting.group_group_ratio", Value: `{"student":{"replacement":0.4}}`}).Error)
		require.NoError(t, db.Save(&model.Option{Key: "group_ratio_setting.group_ratio", Value: `{"default":1,"legacy":2,"replacement":6,"route_only":4}`}).Error)
		model.InitOptionMap()
		assert.JSONEq(t, `{}`, ratio_setting.GroupGroupRatio2JSONString())
		assert.JSONEq(t, `{}`, common.OptionMap["GroupGroupRatio"])
		assert.JSONEq(t, `{}`, common.OptionMap["group_ratio_setting.group_group_ratio"])
		assert.Equal(t, 7.0, ratio_setting.GetGroupRatio("replacement"))
		assert.JSONEq(t, common.OptionMap["GroupRatio"], common.OptionMap["group_ratio_setting.group_ratio"])
		require.NoError(t, db.Where(&model.Option{Key: "GroupGroupRatio"}).Delete(&model.Option{}).Error)
		require.NoError(t, db.Where(&model.Option{Key: "GroupRatio"}).Delete(&model.Option{}).Error)
		model.InitOptionMap()
		assert.Equal(t, 0.4, service.GetUserGroupRatio("student", "replacement"))
		assert.JSONEq(t, ratio_setting.GroupGroupRatio2JSONString(), common.OptionMap["GroupGroupRatio"])
		assert.JSONEq(t, common.OptionMap["GroupGroupRatio"], common.OptionMap["group_ratio_setting.group_group_ratio"])
		assert.Equal(t, 6.0, ratio_setting.GetGroupRatio("replacement"))
		assert.JSONEq(t, ratio_setting.GroupRatio2JSONString(), common.OptionMap["GroupRatio"])
	})

	t.Run("database read leaves request snapshots available", func(t *testing.T) {
		captured := make(chan *setting.GroupSettingsSnapshot, 1)
		require.NoError(t, db.Callback().Query().Before("gorm:query").Register("group_test_read_snapshot", func(tx *gorm.DB) {
			if tx.Statement.Table != "options" {
				return
			}
			go func() { captured <- setting.CaptureGroupSettings() }()
			select {
			case snapshot := <-captured:
				assert.Equal(t, "replacement", snapshot.DefaultGroup("student"))
			case <-time.After(2 * time.Second):
				tx.AddError(errors.New("database query blocked request configuration capture"))
				t.Error("request snapshots must remain available while options are queried")
			}
		}))
		model.InitOptionMap()
		require.NoError(t, db.Callback().Query().Remove("group_test_read_snapshot"))
	})

	t.Run("legacy async settlement uses account and routing groups", func(t *testing.T) {
		require.NoError(t, db.AutoMigrate(&model.Task{}))
		oldModelRatios := ratio_setting.ModelRatio2JSONString()
		oldBatch, oldMemory := common.BatchUpdateEnabled, common.MemoryCacheEnabled
		common.BatchUpdateEnabled, common.MemoryCacheEnabled = false, false
		t.Cleanup(func() {
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldModelRatios))
			common.BatchUpdateEnabled, common.MemoryCacheEnabled = oldBatch, oldMemory
		})
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"group-legacy-task":2}`))
		userGroups := setting.GetUserGroupsCopy()
		userGroups["settlement_members"] = "replacement"
		encoded, err := common.Marshal(userGroups)
		require.NoError(t, err)
		require.NoError(t, model.UpdateGroupSettingsOptions(map[string]string{"UserGroups": string(encoded)}))
		for _, tc := range []struct {
			name, taskGroup, special string
			want                     int
		}{
			{"mapped base", "", `{}`, 960},
			{"mapped special", "", `{"settlement_members":{"replacement":0.5}}`, 80},
			{"explicit route special", "route_only", `{"settlement_members":{"replacement":0.5,"route_only":0.25}}`, 40},
			{"mapped free special", "", `{"settlement_members":{"replacement":0}}`, 0},
		} {
			t.Run(tc.name, func(t *testing.T) {
				require.NoError(t, model.UpdateGroupSettingsOptions(map[string]string{"GroupGroupRatio": tc.special}))
				suffix := strings.ReplaceAll(tc.name, " ", "")
				user := model.User{Username: "settle-" + suffix, AffCode: "settle-" + suffix, Group: "settlement_members", Status: common.UserStatusEnabled, Quota: 950, UsedQuota: 50, RequestCount: 1}
				require.NoError(t, db.Create(&user).Error)
				channel := model.Channel{Name: "settle-" + suffix, UsedQuota: 50}
				require.NoError(t, db.Create(&channel).Error)
				token := model.Token{UserId: user.Id, Key: "settle" + suffix, RemainQuota: 950, UsedQuota: 50, Status: common.TokenStatusEnabled}
				require.NoError(t, db.Create(&token).Error)
				task := model.Task{TaskID: "settle-" + suffix, UserId: user.Id, ChannelId: channel.Id, Quota: 50, Group: tc.taskGroup, Status: model.TaskStatusSuccess,
					Properties: model.Properties{OriginModelName: "group-legacy-task"}, PrivateData: model.TaskPrivateData{BillingSource: service.BillingSourceWallet, TokenId: token.Id}}
				require.NoError(t, db.Create(&task).Error)
				require.True(t, service.RecalculateTaskQuotaByTokens(context.Background(), &task, 80))
				var persistedTask model.Task
				require.NoError(t, db.First(&persistedTask, task.ID).Error)
				require.NoError(t, db.First(&user, user.Id).Error)
				require.NoError(t, db.First(&token, token.Id).Error)
				require.NoError(t, db.First(&channel, channel.Id).Error)
				assert.Equal(t, tc.want, task.Quota)
				assert.Equal(t, tc.want, persistedTask.Quota)
				assert.Equal(t, 1000-tc.want, user.Quota)
				assert.Equal(t, 1000-tc.want, token.RemainQuota)
				assert.Equal(t, tc.want, token.UsedQuota)
				assert.Equal(t, tc.want, user.UsedQuota)
				assert.Equal(t, int64(tc.want), channel.UsedQuota)
				assert.Equal(t, 1, user.RequestCount)
			})
		}
	})

}
