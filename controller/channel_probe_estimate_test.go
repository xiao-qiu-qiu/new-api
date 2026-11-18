package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func channelProbePriceTestSettings(t *testing.T) *setting.GroupSettingsSnapshot {
	t.Helper()
	previousConfig := config.GlobalConfig.ExportAllConfigs()
	previousRatios, previousPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	previousCompletion, previousCache := ratio_setting.CompletionRatio2JSONString(), ratio_setting.CacheRatio2JSONString()
	previousQuota := common.QuotaPerUnit
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(previousConfig))
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousPrices))
		require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(previousCompletion))
		require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(previousCache))
		common.QuotaPerUnit = previousQuota
	})
	common.QuotaPerUnit = 500000
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"probe-legacy":2,"probe-free":0}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"probe-request":0.012}`))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"probe-legacy":3}`))
	require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(`{"probe-legacy":0}`))
	expressions := map[string]string{
		"probe-token":                         `tier("internal-private-route", p * 2 + c * 10 + cr * 0.2)`,
		"probe-heavy-cache":                   `tier("private-cache-tier", p * 1000 + cr * 0)`,
		"probe-fixed":                         `tier("private-fixed-tier", fixed(0.007))`,
		"probe-context-fixed":                 `len > 10000 ? tier("long", p * 2 + c * 3) : tier("short", fixed(0.007))`,
		"probe-time":                          `hour("UTC") >= 0 && minute("UTC") >= 0 && weekday("UTC") >= 0 && month("UTC") >= 1 && day("UTC") >= 1 ? tier("current", p * 2) : tier("other", p)`,
		"gpt-6.1-sol@effort:high@thinking:on": `tier("private-reasoning", p * 7 + c * 21 + cr * 0.7)`,
		"deepseek-v4-pro":                     `tier("deepseek", p * 3 + c * 9 + cr * 0.3)`,
	}
	modes := make(map[string]string, len(expressions))
	for name := range expressions {
		modes[name] = "tiered_expr"
	}
	encodedModes, err := common.Marshal(modes)
	require.NoError(t, err)
	encodedExpressions, err := common.Marshal(expressions)
	require.NoError(t, err)
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":         string(encodedModes),
		"billing_setting.billing_expr":         string(encodedExpressions),
		"billing_setting.ignore_group_ratio":   `{"gpt-6.1-sol":true,"gpt-6.1-sol-high":false}`,
		"quota_setting.pre_consume_multiplier": "1",
	}))
	return &setting.GroupSettingsSnapshot{
		Ratios:        map[string]float64{"public-group": 2, "free-group": 0},
		UsableGroups:  map[string]string{"public-group": "Public", "free-group": "Free"},
		SpecialRatios: map[string]map[string]float64{"discounted": {"public-group": 0.25}, "free-user": {"public-group": 0}},
	}
}

func TestChannelProbePriceScenarios(t *testing.T) {
	settings := channelProbePriceTestSettings(t)
	previousDB := model.DB
	model.DB = nil
	t.Cleanup(func() { model.DB = previousDB })
	for _, test := range []struct {
		name, modelName, userGroup, group, tier string
		input, output, cache                    int
		want                                    float64
		unit                                    string
	}{
		{"normal group", "probe-token", "ordinary", "public-group", "default", 10000, 1000, 9000, 0.0276, "token"},
		{"special group ratio", "probe-token", "discounted", "public-group", "default", 10000, 1000, 9000, 0.00345, "token"},
		{"ordinary zero ratio", "probe-token", "ordinary", "free-group", "default", 10000, 1000, 9000, 0, "token"},
		{"dedicated zero ratio", "probe-token", "free-user", "public-group", "default", 10000, 1000, 9000, 0, "token"},
		{"legacy explicit zero cache", "probe-legacy", "ordinary", "public-group", "default", 10000, 1000, 9000, 0.032, "token"},
		{"legacy free model", "probe-free", "ordinary", "public-group", "default", 10000, 1000, 9000, 0, "token"},
		{"legacy per request", "probe-request", "ordinary", "public-group", "default", 10000, 1000, 9000, 0.024, "request"},
		{"fixed request zero tokens", "probe-fixed", "ordinary", "public-group", "default", 0, 0, 0, 0.014, "request"},
		{"actual context switches unit", "probe-context-fixed", "ordinary", "public-group", "default", 20000, 1000, 19000, 0.086, "token"},
		{"current time dependent price", "probe-time", "ordinary", "public-group", "default", 10000, 1000, 0, 0.04, "token"},
		{"ignore ratio fallback", "gpt-6.1-sol@temperature:0.2", "ordinary", "free-group", "default", 10000, 1000, 9000, 0.0129, "token"},
		{"legacy reasoning explicit false", "gpt-6.1-sol-high", "ordinary", "public-group", "default", 10000, 1000, 9000, 0.0686, "token"},
		{"canonical reasoning alias", "gpt-6.1-sol@thinking:on@effort:high@temperature:0.3", "ordinary", "public-group", "default", 10000, 1000, 9000, 0.0686, "token"},
		{"deepseek thinking depth", "deepseek-v4-pro-low", "ordinary", "public-group", "default", 10000, 1000, 9000, 0.0294, "token"},
		{"standard", "gpt-6.1-sol", "ordinary", "public-group", "default", 10000, 1000, 9000, 0.0129, "token"},
		{"priority", "gpt-6.1-sol", "ordinary", "public-group", "priority", 10000, 1000, 9000, 0.0258, "token"},
		{"fast alias", "gpt-6.1-sol", "ordinary", "public-group", "fast", 10000, 1000, 9000, 0.0258, "token"},
		{"flex", "gpt-6.1-sol", "ordinary", "public-group", "flex", 10000, 1000, 9000, 0.00645, "token"},
		{"ultrafast", "gpt-6.1-sol", "ordinary", "public-group", "ultrafast", 10000, 1000, 9000, 0.0774, "token"},
		{"long context includes cached tokens", "gpt-6.1-sol", "ordinary", "public-group", "default", 300000, 1000, 290000, 0.113, "token"},
		{"cached actual fits while uncached preconsume overflows", "probe-heavy-cache", "ordinary", "public-group", "default", 10000000, 0, 10000000, 0, "token"},
		{"cached actual positive charge avoids preconsume overflow", "probe-heavy-cache", "ordinary", "public-group", "default", 10000000, 0, 9999999, 0.002, "token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			*setting.PinGroupSettings(ctx) = *settings
			ctx.Set("auto_group", "private-unselectable")
			estimate, err := estimateChannelProbePrice(ctx, test.modelName, test.userGroup, test.group, service.ChannelProbePriceInput{
				InputTokens: test.input, OutputTokens: test.output, CacheReadTokens: test.cache, ServiceTier: test.tier,
			})
			require.NoError(t, err)
			assert.InDelta(t, test.want, estimate.USD, 1e-12)
			assert.Equal(t, test.unit, estimate.BillingUnit)
			encoded, err := common.Marshal(estimate)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "matched_tier")
			assert.NotContains(t, string(encoded), "private")
		})
	}
}

func TestChannelProbePriceDependencies(t *testing.T) {
	settings := channelProbePriceTestSettings(t)
	for _, test := range []struct{ dependency, expression string }{
		{"param", `param("size") == "large" ? tier("large", p * 2) : tier("base", p)`},
		{"header", `header("private-discount") == "secret" ? tier("discount", p) : tier("base", p * 2)`},
		{"u", `u("seconds") * 2`},
		{"image_count", `tier("image", fixed(0.1)) * image_count`},
	} {
		t.Run(test.dependency, func(t *testing.T) {
			_, err := billingexpr.CompileFromCache(test.expression)
			require.NoError(t, err)
			assert.True(t, billingexpr.UsedVars(test.expression)[test.dependency], "callee must be present in UsedVars")
			encoded, err := common.Marshal(map[string]string{"probe-dependent": test.expression})
			require.NoError(t, err)
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
				"billing_setting.billing_mode": `{"probe-dependent":"tiered_expr"}`,
				"billing_setting.billing_expr": string(encoded),
			}))
			assert.False(t, service.ChannelProbeSupportsPriceEstimate("probe-dependent", "ordinary", settings))
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			*setting.PinGroupSettings(ctx) = *settings
			_, err = estimateChannelProbePrice(ctx, "probe-dependent", "ordinary", "public-group", service.ChannelProbePriceInput{InputTokens: 1000})
			require.Error(t, err)
			assert.NotContains(t, err.Error(), test.expression)
		})
	}
}

func TestChannelProbePriceEndpointAndMetadata(t *testing.T) {
	settings := channelProbePriceTestSettings(t)
	db := channelProbeTestDB(t)
	previousLogDB := model.LOG_DB
	model.LOG_DB = nil
	t.Cleanup(func() { model.LOG_DB = previousLogDB })
	probe := channelProbeFixture()
	probe.Scope, probe.ChannelID, probe.GroupName, probe.GuestVisible = "group", 0, "public-group", true
	probe.Model, probe.Models = "probe-token", []string{"probe-token", "probe-fixed"}
	require.NoError(t, model.SaveChannelProbe(&probe, true))
	view, err := service.BuildPublicChannelProbeModelWithSettings(probe, "probe-token", time.Now(), 1, "ordinary", settings)
	require.NoError(t, err)
	assert.True(t, view.CanEstimatePrice, "a valid price does not require historic samples")
	assert.Nil(t, view.EstimatedPricePer100MTokens)
	for _, unit := range []string{"request", "image"} {
		for _, price := range []float64{0, 0.003} {
			settings.SpecialPricingRules = []setting.SpecialPricingRule{{UserGroup: "ordinary", Model: "probe-token", Price: price, BillingUnit: unit}}
			view, err := service.BuildPublicChannelProbeModelWithSettings(probe, "probe-token", time.Now(), 1, "ordinary", settings)
			require.NoError(t, err)
			require.NotNil(t, view.ConfiguredPrice)
			assert.Equal(t, price, *view.ConfiguredPrice)
			assert.Equal(t, unit, view.ConfiguredPriceUnit)
			assert.Nil(t, view.EstimatedPricePer100MTokens)
			assert.False(t, view.CanEstimatePrice, "the exact configured unit price needs no scenario button")
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			*setting.PinGroupSettings(ctx) = *settings
			estimate, err := estimateChannelProbePrice(ctx, "probe-token", "ordinary", "public-group", service.ChannelProbePriceInput{InputTokens: 10000, OutputTokens: 1000})
			require.NoError(t, err)
			assert.Equal(t, price, estimate.USD)
			assert.Equal(t, unit, estimate.BillingUnit)
		}
	}
	settings.SpecialPricingRules = nil
	dependency, err := common.Marshal(map[string]string{"probe-token": `header("private-price") == "secret" ? tier("a", p) : tier("b", p * 2)`})
	require.NoError(t, err)
	previousExpressions := config.GlobalConfig.ExportAllConfigs()["billing_setting.billing_expr"]
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_expr": string(dependency)}))
	unsupported, err := service.BuildPublicChannelProbeModelWithSettings(probe, "probe-token", time.Now(), 1, "ordinary", settings)
	require.NoError(t, err)
	assert.False(t, unsupported.CanEstimatePrice)
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_expr": previousExpressions}))
	for _, test := range []struct {
		name, extra, userGroup                     string
		userID                                     int
		removeGroup, hidden, disabled, unpublished bool
		want                                       int
	}{
		{name: "guest without history", want: http.StatusOK},
		{name: "signed in", userID: 42, userGroup: "ordinary", want: http.StatusOK},
		{name: "unselectable anonymous group", removeGroup: true, want: http.StatusNotFound},
		{name: "unselectable signed in group", removeGroup: true, userID: 42, userGroup: "ordinary", want: http.StatusNotFound},
		{name: "guest hidden", hidden: true, want: http.StatusNotFound},
		{name: "disabled", disabled: true, want: http.StatusNotFound},
		{name: "unpublished", unpublished: true, want: http.StatusNotFound},
		{name: "arbitrary group rejected", extra: "&group=private", want: http.StatusBadRequest},
		{name: "repeated count", extra: "&input_tokens=10", want: http.StatusBadRequest},
		{name: "unknown tier", extra: "&service_tier=secret", want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, db.Model(&model.ChannelProbe{}).Where("id = ?", probe.ID).Updates(map[string]any{
				"enabled": !test.disabled, "public": !test.unpublished, "guest_visible": !test.hidden,
			}).Error)
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Set("id", test.userID)
			ctx.Set("group", test.userGroup)
			snapshot := setting.PinGroupSettings(ctx)
			*snapshot = *settings
			if test.removeGroup {
				snapshot.UsableGroups = map[string]string{}
			}
			ctx.Request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/channel/probes/estimate?probe_id=%d&model=probe-token&input_tokens=10000&output_tokens=1000&cache_read_tokens=9000%s", probe.ID, test.extra), nil)
			GetChannelProbePriceEstimate(ctx)
			assert.Equal(t, test.want, recorder.Code, recorder.Body.String())
			assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
			assert.NotContains(t, recorder.Body.String(), "private")
			assert.NotContains(t, recorder.Body.String(), "matched_tier")
		})
	}
	for _, values := range []url.Values{
		{"model": {"not-published"}, "input_tokens": {"1"}, "output_tokens": {"0"}, "cache_read_tokens": {"0"}},
		{"model": {"probe-token"}, "input_tokens": {"10000001"}, "output_tokens": {"0"}, "cache_read_tokens": {"0"}},
		{"model": {"probe-token"}, "input_tokens": {"1"}, "output_tokens": {"-1"}, "cache_read_tokens": {"0"}},
		{"model": {"probe-token"}, "input_tokens": {"1"}, "output_tokens": {"0"}, "cache_read_tokens": {"2"}},
		{"model": {"probe-token"}, "input_tokens": {"1.5"}, "output_tokens": {"0"}, "cache_read_tokens": {"0"}},
	} {
		values.Set("probe_id", fmt.Sprint(probe.ID))
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel/probes/estimate?"+values.Encode(), nil)
		*setting.PinGroupSettings(ctx) = *settings
		GetChannelProbePriceEstimate(ctx)
		assert.False(t, recorder.Code == http.StatusOK, recorder.Body.String())
	}
}
