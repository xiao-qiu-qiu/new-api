package controller

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Model metadata may declare endpoints either as a type array or as a path map.
// Conversion must understand both; reading only the map form made every model
// with array-form metadata look like a broken routing configuration.
func TestModelPricingConversionAcceptsDeclaredEndpointArray(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")
	previousQuota := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() { common.QuotaPerUnit = previousQuota })

	for _, tc := range []struct {
		name        string
		endpoints   string
		draft       model.PricingValues
		wantImageCt bool
	}{
		{"array-endpoints-image", `["image-generation","openai"]`,
			model.PricingValues{"ModelRatio": 1.7875, "CompletionRatio": 6, "ImageRatio": 1.6}, false},
		{"array-endpoints-image-fixed", `["image-generation","openai"]`,
			model.PricingValues{"ModelPrice": 1}, true},
		{"array-endpoints-gemini", `["gemini","openai"]`,
			model.PricingValues{"ModelRatio": 0.10725, "CompletionRatio": 100}, false},
		{"array-endpoints-gemini-cache", `["gemini","openai"]`,
			model.PricingValues{"ModelRatio": 0.715, "CompletionRatio": 60, "CacheRatio": 0.1}, false},
		{"array-endpoints-openai-response", `["openai","openai-response","openai-response-compact","gemini"]`,
			model.PricingValues{"ModelRatio": 0.17875, "CompletionRatio": 120}, false},
		{"map-endpoints-image", `{"image-generation": {"path": "/v1/images/generations", "method": "POST"}}`,
			model.PricingValues{"ModelPrice": 1}, true},
		// Endpoint names must be trimmed before the switch matches them: a
		// padded name would otherwise match nothing and silently downgrade a
		// fixed-price image model to per-request billing.
		{"array-endpoints-image-padded", `[" image-generation ","openai"]`,
			model.PricingValues{"ModelPrice": 1}, true},
		{"map-endpoints-image-padded", `{"image-generation ": {"path": "/v1/images/generations", "method": "POST"}}`,
			model.PricingValues{"ModelPrice": 1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, db.Create(&model.Model{
				ModelName: tc.name, Endpoints: tc.endpoints, Status: 1, NameRule: model.NameRuleExact,
			}).Error)
			var response struct {
				Success bool
				Data    model.ModelPricingConversion
			}
			modelManagementRequest(t, PreviewModelPricingConversion, http.MethodPost,
				"/api/option/model_pricing/convert",
				map[string]any{"model_name": tc.name, "pricing": tc.draft}, &response)
			require.True(t, response.Success)
			t.Logf("PROBE %-34s expression=%q unsupported=%q image_count=%v",
				tc.name, response.Data.Expression, response.Data.UnsupportedReason,
				response.Data.BillingDetails.ImageCount)
			require.Empty(t, response.Data.UnsupportedReason)
			require.NotEmpty(t, response.Data.Expression)
			// Declared endpoint types must actually reach the endpoint switch;
			// an array of names read as array indices would silently skip it.
			assert.Equal(t, tc.wantImageCt, response.Data.BillingDetails.ImageCount,
				"declared image-generation endpoint must drive image-count billing")
		})
	}
}

func TestModelPricingIgnoreGroupRatioDatabaseMatrix(t *testing.T) {
	const option = billing_setting.IgnoreGroupRatioOption
	const name = "ignore-group-ratio-model"
	const exact = "gpt-4-gizmo-explicit"
	const inherited = "gpt-4-gizmo-inherited"
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			t.Cleanup(model.InvalidatePricingCache)
			channel := model.Channel{Type: 1, Key: "fixture", Name: "ignore-ratio-fixture", Status: common.ChannelStatusEnabled,
				Models: name + "," + exact + "," + inherited, Group: "default"}
			require.NoError(t, channel.Insert())
			before, err := model.GetModelPricingSnapshot([]string{name})
			require.NoError(t, err)
			assert.Equal(t, false, before.Entries[0].Effective[option])
			assert.False(t, billing_setting.IgnoreGroupRatio(name))
			var count int64
			require.NoError(t, db.Model(&model.Option{}).Where(&model.Option{Key: option}).Count(&count).Error)
			assert.Zero(t, count, "reading legacy options must not create a new row")

			change := model.ModelPricingChange{ModelName: name, ExpectedVersion: before.Entries[0].Version,
				Pricing: model.PricingValues{"ModelPrice": float64(1), option: true}}
			var saved struct{ Success bool }
			recorder := modelManagementRequest(t, UpdateModelPricingConfig, http.MethodPatch, "/api/option/model_pricing",
				map[string]any{"changes": []model.ModelPricingChange{change}}, &saved)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.True(t, saved.Success)
			loaded, err := model.GetModelPricingSnapshot([]string{name})
			require.NoError(t, err)
			assert.Equal(t, true, loaded.Entries[0].Configured[option])
			assert.True(t, billing_setting.IgnoreGroupRatio(name))
			assert.NotEqual(t, before.Entries[0].Version, loaded.Entries[0].Version)
			recorder = modelManagementRequest(t, UpdateModelPricingConfig, http.MethodPatch, "/api/option/model_pricing",
				map[string]any{"changes": []model.ModelPricingChange{change}}, nil)
			assert.Equal(t, http.StatusConflict, recorder.Code)

			for _, ignored := range []bool{true, false} {
				previousVersion := loaded.Entries[0].Version
				change.ExpectedVersion = loaded.Entries[0].Version
				change.Pricing[option] = ignored
				require.NoError(t, model.UpdateModelPricing([]model.ModelPricingChange{change}))
				loaded, err = model.GetModelPricingSnapshot([]string{name})
				require.NoError(t, err)
				assert.Equal(t, ignored, loaded.Entries[0].Configured[option])
				assert.Equal(t, ignored, loaded.Entries[0].Effective[option])
				if !ignored {
					assert.NotEqual(t, previousVersion, loaded.Entries[0].Version, "switch changes participate in optimistic locking")
				}
				var row model.Option
				require.NoError(t, db.Where(&model.Option{Key: option}).Take(&row).Error)
				var stored map[string]any
				require.NoError(t, common.UnmarshalJsonStr(row.Value, &stored))
				assert.Equal(t, ignored, stored[name])
				for range 2 {
					require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{option: row.Value}))
					assert.Equal(t, ignored, billing_setting.IgnoreGroupRatio(name))
				}
				var preview struct {
					Success bool
					Data    model.ModelPricingConversion
				}
				modelManagementRequest(t, PreviewModelPricingConversion, http.MethodPost, "/api/option/model_pricing/convert",
					map[string]any{"model_name": name, "pricing": change.Pricing}, &preview)
				require.True(t, preview.Success)
				require.NotEmpty(t, preview.Data.Expression)
				assert.Equal(t, ignored, preview.Data.Effective[option], "conversion must retain the group policy")
				converted := maps.Clone(preview.Data.Effective)
				converted["billing_setting.billing_mode"] = billing_setting.BillingModeTieredExpr
				converted["billing_setting.billing_expr"] = preview.Data.Expression
				require.NoError(t, model.UpdateModelPricing([]model.ModelPricingChange{{ModelName: name,
					ExpectedVersion: loaded.Entries[0].Version, Pricing: converted}}))
				loaded, err = model.GetModelPricingSnapshot([]string{name})
				require.NoError(t, err)
				assert.Equal(t, ignored, loaded.Entries[0].Configured[option])
				assert.Equal(t, ignored, billing_setting.IgnoreGroupRatio(name))
				var public struct {
					Success bool
					Data    []map[string]any
				}
				modelManagementRequest(t, GetPricing, http.MethodGet, "/api/pricing", nil, &public)
				require.True(t, public.Success)
				var found bool
				for _, price := range public.Data {
					if price["model_name"] == name {
						found = true
						assert.Equal(t, ignored, price["ignore_group_ratio"], "false must also be present in JSON")
					}
				}
				require.True(t, found)
			}

			for _, invalid := range []any{"true", float64(1), nil, []any{true}, map[string]any{"enabled": true}} {
				bad := change
				bad.ExpectedVersion = loaded.Entries[0].Version
				bad.Pricing = maps.Clone(change.Pricing)
				bad.Pricing[option] = invalid
				other := model.ModelPricingChange{ModelName: "ignore-ratio-other", ExpectedVersion: before.EmptyVersion, Pricing: model.PricingValues{option: true}}
				recorder = modelManagementRequest(t, UpdateModelPricingConfig, http.MethodPatch, "/api/option/model_pricing",
					map[string]any{"changes": []model.ModelPricingChange{other, bad}}, nil)
				assert.Equal(t, http.StatusBadRequest, recorder.Code)
				after, err := model.GetModelPricingSnapshot([]string{name})
				require.NoError(t, err)
				assert.Equal(t, loaded.Entries, after.Entries)
				assert.False(t, billing_setting.IgnoreGroupRatio("ignore-ratio-other"))
				encoded, err := common.Marshal(map[string]any{name: invalid})
				require.NoError(t, err)
				assert.Error(t, model.UpdateOption(option, string(encoded)), "legacy option updates must validate booleans too")
			}

			// Exercise normalization, including an exact false overriding a true wildcard.
			require.NoError(t, model.UpdateOption(option, `{"gpt-4-gizmo-*":true,"gpt-4-gizmo-explicit":false,"gpt-4o-gizmo-*":false}`))
			for _, tc := range []struct {
				model string
				want  bool
			}{{exact, false}, {inherited, true}, {"gpt-4o-gizmo-inherited", false}, {"unconfigured-model", false}} {
				assert.Equal(t, tc.want, billing_setting.IgnoreGroupRatio(tc.model))
				snapshot, err := model.GetModelPricingSnapshot([]string{tc.model})
				require.NoError(t, err)
				assert.Equal(t, tc.want, snapshot.Entries[0].Effective[option])
			}
			assert.False(t, billing_setting.IgnoreGroupRatio("unconfigured-model", exact, inherited))
			assert.True(t, billing_setting.IgnoreGroupRatio("unconfigured-model", inherited, exact))
			assert.False(t, billing_setting.IgnoreGroupRatio("gpt-4o-gizmo-inherited", inherited), "a wildcard false must stop fallback")
			var preview struct {
				Success bool
				Data    model.ModelPricingDescription
			}
			modelManagementRequest(t, PreviewModelPricing, http.MethodPost, "/api/option/model_pricing/preview",
				map[string]any{"model_name": exact, "pricing": model.PricingValues{"ModelPrice": float64(1)}}, &preview)
			require.True(t, preview.Success)
			assert.Equal(t, true, preview.Data.Effective[option], "an omitted draft override must use the saved wildcard")
			assert.False(t, billing_setting.IgnoreGroupRatio(exact), "preview must not publish its draft")
			current, err := model.GetModelPricingSnapshot([]string{exact})
			require.NoError(t, err)
			require.NoError(t, model.UpdateModelPricing([]model.ModelPricingChange{{ModelName: exact, ExpectedVersion: current.Entries[0].Version, Reset: true}}))
			assert.True(t, billing_setting.IgnoreGroupRatio(exact), "reset removes the exact override and restores wildcard fallback")
			current, err = model.GetModelPricingSnapshot([]string{name})
			require.NoError(t, err)
			require.NoError(t, model.UpdateModelPricing([]model.ModelPricingChange{{ModelName: name,
				ExpectedVersion: current.Entries[0].Version, Pricing: model.PricingValues{option: true}}}))
			current, err = model.GetModelPricingSnapshot([]string{name})
			require.NoError(t, err)
			require.NoError(t, model.UpdateModelPricing([]model.ModelPricingChange{{ModelName: name, ExpectedVersion: current.Entries[0].Version, Reset: true}}))
			reset, err := model.GetModelPricingSnapshot([]string{name})
			require.NoError(t, err)
			assert.NotContains(t, reset.Entries[0].Configured, option)
			assert.Equal(t, false, reset.Entries[0].Effective[option])

			t.Run("task_alias_uses_the_executing_billing_model", func(t *testing.T) {
				const pluginKey = "ignore-ratio-task"
				const declared = "ignore-ratio-task-declared"
				const alias = "ignore-ratio-task-alias"
				_, err := jsplugin.DefaultRegistry.Register(`
export const meta = {apiVersion:1,key:"ignore-ratio-task",name:"Task ratio fixture",version:"1.0.0",author:{name:"Test"},models:["ignore-ratio-task-declared"],fetchMode:"per_task",usageSchema:{seconds:{type:"number",unit:"second"}}};
export function buildSubmitRequest(){return {};}
export function parseSubmitResponse(){return {};}
export function buildQueryRequest(){return {};}
export function parseTaskResult(){return {};}
`, jsplugin.Options{})
				require.NoError(t, err)
				t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(pluginKey) })
				require.NoError(t, (&model.Channel{Type: constant.ChannelTypeTaskPlugin, Key: "fixture", Name: "Task alias fixture",
					Status: common.ChannelStatusEnabled, Group: "default", Models: declared + "," + alias,
					ModelMapping: common.GetPointer(`{"ignore-ratio-task-alias":"ignore-ratio-task-declared"}`)}).Insert())
				model.InitChannelCache()
				for _, tc := range []struct {
					name           string
					ownExpression  bool
					mappedOverride bool
					ignored        *bool
					declaredIgnore bool
					want           bool
				}{
					{"explicit false overrides inherited expression", false, false, common.GetPointer(false), true, false},
					{"explicit true overrides declared false", false, false, common.GetPointer(true), false, true},
					{"unset inherits declared expression", false, false, nil, true, true},
					{"own expression keeps unset default", true, false, nil, true, false},
					{"explicit false overrides mapped provider", true, true, common.GetPointer(false), true, false},
					{"unset inherits mapped provider", true, true, nil, true, true},
					{"explicit true with own expression", true, false, common.GetPointer(true), false, true},
				} {
					t.Run(tc.name, func(t *testing.T) {
						base := model.PricingValues{option: tc.declaredIgnore, "billing_setting.billing_mode": "tiered_expr", "billing_setting.billing_expr": `tier("base", u("seconds") * 0.2)`}
						if tc.mappedOverride {
							base[billing_setting.PluginBillingExprOption] = map[string]any{pluginKey: `tier("provider", u("seconds") * 0.3)`}
						}
						aliasPrice := model.PricingValues{"ModelPrice": float64(1)}
						if tc.ignored != nil {
							aliasPrice[option] = *tc.ignored
						}
						if tc.ownExpression {
							aliasPrice["billing_setting.billing_mode"] = "tiered_expr"
							aliasPrice["billing_setting.billing_expr"] = `tier("alias", u("seconds") * 0.1)`
						}
						for _, draft := range []struct {
							name    string
							pricing model.PricingValues
						}{{declared, base}, {alias, aliasPrice}} {
							before, err := model.GetModelPricingSnapshot([]string{draft.name})
							require.NoError(t, err)
							require.NoError(t, model.UpdateModelPricing([]model.ModelPricingChange{{ModelName: draft.name, ExpectedVersion: before.Entries[0].Version, Pricing: draft.pricing}}))
						}
						var found bool
						for _, price := range model.GetPricing() {
							if price.ModelName == alias {
								found = true
								assert.Equal(t, tc.want, price.IgnoreGroupRatio)
							}
						}
						require.True(t, found)
						admin, err := model.GetModelPricingSnapshot([]string{alias})
						require.NoError(t, err)
						assert.Equal(t, tc.want, admin.Entries[0].Effective[option], "admin must show the same group policy as public pricing")
						preview, err := model.PreviewModelPricing(alias, aliasPrice)
						require.NoError(t, err)
						assert.Equal(t, tc.want, preview[option])
						billingModel := declared
						if tc.ownExpression && !tc.mappedOverride {
							billingModel = alias
						}
						ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
						actual := helper.HandleGroupRatio(ctx, &relaycommon.RelayInfo{OriginModelName: alias,
							BillingModelName: billingModel, UserGroup: "default", UsingGroup: "default"})
						assert.Equal(t, tc.want, actual.IgnoreGroupRatio, "billing must honor the same policy shown by admin and public pricing")
					})
				}
			})
		})
	}
}
