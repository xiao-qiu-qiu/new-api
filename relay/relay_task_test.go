package relay

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	kittypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupRelayChannelDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	previousCache := common.MemoryCacheEnabled
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&model.Channel{}))
	model.DB = database
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.MemoryCacheEnabled = previousCache
		require.NoError(t, sqlDB.Close())
	})
	return database
}

func TestRealtimeFetchAppliesChannelHeaderOverrides(t *testing.T) {
	database := setupRelayChannelDB(t)
	service.InitHttpClient()
	requests := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		_, _ = w.Write([]byte(`{"name":"operations/task-1","done":false}`))
	}))
	t.Cleanup(server.Close)
	headerOverride := `{"X-API-Key":"{api_key}"}`
	channel := &model.Channel{
		Name: "realtime-header-override", Type: constant.ChannelTypeGemini,
		Key: "channel-key", BaseURL: &server.URL, HeaderOverride: &headerOverride,
	}
	require.NoError(t, database.Create(channel).Error)
	task := &model.Task{
		TaskID: "public-task", ChannelId: channel.Id,
		Status: model.TaskStatusInProgress, Progress: "50%",
		PrivateData: model.TaskPrivateData{
			Key: "saved-key", UpstreamTaskID: base64.RawURLEncoding.EncodeToString([]byte("operations/task-1")),
		},
	}

	tryRealtimeFetch(task, true)

	require.Len(t, requests, 1)
	request := <-requests
	assert.Equal(t, "/v1beta/operations/task-1", request.URL.Path)
	assert.Equal(t, "saved-key", request.Header.Get("X-API-Key"))
	assert.Equal(t, "saved-key", request.Header.Get("X-Goog-Api-Key"))
}

func TestRealtimeFetchRefundsTaskItFindsFailed(t *testing.T) {
	database := setupRelayChannelDB(t)
	require.NoError(t, database.AutoMigrate(&model.Task{}, &model.User{}, &model.Log{}))
	previousLogDB := model.LOG_DB
	previousFactory := service.GetTaskAdaptorFunc
	previousRedis := common.RedisEnabled
	model.LOG_DB = database
	common.RedisEnabled = false
	service.GetTaskAdaptorFunc = func(platform constant.TaskPlatform) service.TaskPollingAdaptor {
		if adaptor := GetTaskAdaptor(platform); adaptor != nil {
			return adaptor
		}
		return nil
	}
	t.Cleanup(func() {
		model.LOG_DB = previousLogDB
		service.GetTaskAdaptorFunc = previousFactory
		common.RedisEnabled = previousRedis
	})
	service.InitHttpClient()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"operations/task-1","done":true,"error":{"code":3,"message":"blocked by safety"}}`))
	}))
	t.Cleanup(server.Close)
	channel := &model.Channel{Name: "realtime-refund", Type: constant.ChannelTypeGemini, Key: "channel-key", BaseURL: &server.URL}
	require.NoError(t, database.Create(channel).Error)
	require.NoError(t, database.Create(&model.User{Id: 7, Username: "realtime-owner", Quota: 100}).Error)
	task := &model.Task{
		TaskID: "public-task", Platform: constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeGemini)),
		UserId: 7, ChannelId: channel.Id, Quota: 1000,
		Status: model.TaskStatusInProgress, Progress: "50%",
		PrivateData: model.TaskPrivateData{
			Key: "channel-key", UpstreamTaskID: base64.RawURLEncoding.EncodeToString([]byte("operations/task-1")),
		},
	}
	require.NoError(t, database.Create(task).Error)

	tryRealtimeFetch(task, true)

	var stored model.Task
	require.NoError(t, database.First(&stored, task.ID).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusFailure), stored.Status)
	assert.Equal(t, "blocked by safety", stored.FailReason)
	assert.Zero(t, stored.Quota, "the failed task is refunded")
	var user model.User
	require.NoError(t, database.First(&user, 7).Error)
	assert.Equal(t, 1100, user.Quota)
}

func TestApplyChannelPinPreservesOriginTasksAndRetryMode(t *testing.T) {
	database := setupRelayChannelDB(t)
	channel := &model.Channel{Name: "origin-channel", Key: "sk-test", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeDoubaoVideo}
	require.NoError(t, database.Create(channel).Error)
	originTask := &model.Task{
		TaskID: "task-lock", ChannelId: channel.Id, Action: "text_to_video", Status: model.TaskStatusSuccess,
		PrivateData: model.TaskPrivateData{UpstreamTaskID: "upstream-task-lock"},
		Data:        []byte(`{"id":"upstream-task-lock"}`),
	}

	for _, tc := range []struct {
		name     string
		tokenPin bool
		apply    func(*gin.Context, *relaycommon.RelayInfo) *dto.TaskError
	}{
		{name: "origin affinity", apply: ApplyOriginTaskAffinity},
		{name: "same channel retry", apply: ApplyChannelPin},
		{name: "token pin suppresses channel lock", tokenPin: true, apply: ApplyChannelPin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			common.SetContextKey(c, constant.ContextKeyOriginTasks, []*model.Task{originTask})
			constraints := service.GetChannelConstraints(c)
			constraints.AddPin(dto.ChannelPin{ChannelId: channel.Id, Source: dto.PinSourceOriginTask, Rank: dto.PinRankOriginTask, RetryMode: dto.PinRetrySameChannel})
			if tc.tokenPin {
				constraints.AddPin(dto.ChannelPin{ChannelId: channel.Id, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt})
			}
			info := &relaycommon.RelayInfo{TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
			require.Nil(t, tc.apply(c, info))
			if tc.tokenPin {
				assert.Nil(t, info.LockedChannel)
			} else {
				locked, ok := info.LockedChannel.(*model.Channel)
				require.True(t, ok)
				require.NotNil(t, locked)
				assert.Equal(t, channel.Id, locked.Id)
			}
			require.Len(t, info.OriginTasks, 1)
			assert.Equal(t, "task-lock", info.OriginTasks[0].TaskID)
			assert.Equal(t, "upstream-task-lock", info.OriginTasks[0].UpstreamTaskID)
			assert.Equal(t, "text_to_video", info.OriginTasks[0].Action)
			assert.Equal(t, string(model.TaskStatusSuccess), info.OriginTasks[0].Status)
			assert.Equal(t, []byte(originTask.Data), info.OriginTasks[0].Data)
		})
	}
}

func TestTaskModel2DtoNormalizesLegacyAction(t *testing.T) {
	task := &model.Task{Action: "firstTailGenerate"}

	dtoTask := TaskModel2Dto(task)

	assert.Equal(t, constant.TaskActionFirstTailToVideo, dtoTask.Action)
	assert.Equal(t, "firstTailGenerate", task.Action)
}

const mappingOrderSubmitPlugin = `
export const meta = {apiVersion:1,key:"maporder",name:"Map Order",version:"1.0.0",author:{name:"Test"},models:["declared-model"],fetchMode:"per_task"};
export function buildSubmitRequest(ctx) {
  return {url: ctx.baseUrl+"/submit", method:"POST", body:{upstreamModel: ctx.upstreamModel, model: ctx.model}, action:"text_to_video"};
}
export function parseSubmitResponse(){return {taskId:"1"};}
export function buildQueryRequest(){return {url:"https://provider.example"};}
export function parseTaskResult(){return {status:"SUCCESS"};}
`

const mappingOrderRewritePlugin = `
export const meta = {apiVersion:1,key:"maporder-rw",name:"Map Order RW",version:"1.0.0",author:{name:"Test"},models:["declared-model"],fetchMode:"per_task"};
export function buildSubmitRequest(ctx) {
  return {url: ctx.baseUrl+"/submit", method:"POST", body:{upstreamModel: ctx.upstreamModel}, rewriteModel:"rewritten"};
}
export function parseSubmitResponse(){return {taskId:"1"};}
export function buildQueryRequest(){return {url:"https://provider.example"};}
export function parseTaskResult(){return {status:"SUCCESS"};}
`

func pinMappingOrderPlugin(t *testing.T, c *gin.Context, source string) {
	t.Helper()
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	c.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{Plugin: plugin})
}

func newTaskSubmitContext(t *testing.T, originalModel, mapping string) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	require.NoError(t, i18n.Init())
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	common.SetContextKey(c, constant.ContextKeyOriginalModel, originalModel)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, "https://provider.example")
	if mapping != "" {
		c.Set("model_mapping", mapping)
	}
	c.Set("task_request", map[string]any{"prompt": "p"})
	return c, &relaycommon.RelayInfo{TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
}

func TestRelayTaskSubmitMapsBeforeValidateWhenOriginSet(t *testing.T) {
	const mapping = `{"alias-model":"mid-model","mid-model":"declared-model"}`

	c, info := newTaskSubmitContext(t, "alias-model", mapping)
	pinMappingOrderPlugin(t, c, mappingOrderSubmitPlugin)
	info.OriginModelName = "alias-model"

	_, taskErr := RelayTaskSubmit(c, info)
	require.NotNil(t, taskErr)
	assert.Equal(t, "model_price_error", taskErr.Code)
	assert.Equal(t, "alias-model", info.OriginModelName)
	assert.Equal(t, "declared-model", info.UpstreamModelName)
	assert.True(t, info.IsModelMapped)
}

func TestRelayTaskSubmitDeclaredNameWithoutMappingIsUnchanged(t *testing.T) {
	c, info := newTaskSubmitContext(t, "declared-model", "")
	pinMappingOrderPlugin(t, c, mappingOrderSubmitPlugin)
	info.OriginModelName = "declared-model"

	_, taskErr := RelayTaskSubmit(c, info)
	require.NotNil(t, taskErr)
	assert.Equal(t, "model_price_error", taskErr.Code)
	assert.Equal(t, "declared-model", info.OriginModelName)
	assert.Equal(t, "declared-model", info.UpstreamModelName)
	assert.False(t, info.IsModelMapped)
}

func TestRelayTaskSubmitDoesNotApplyMappingTwice(t *testing.T) {
	c, info := newTaskSubmitContext(t, "alias-model", `{"alias-model":"declared-model"}`)
	pinMappingOrderPlugin(t, c, mappingOrderRewritePlugin)
	info.OriginModelName = "alias-model"

	_, taskErr := RelayTaskSubmit(c, info)
	require.NotNil(t, taskErr)
	assert.Equal(t, "model_price_error", taskErr.Code)
	assert.Equal(t, "rewritten", info.UpstreamModelName, "late mapping would overwrite rewriteModel with the chain tail")
	assert.Equal(t, "alias-model", info.OriginModelName)
}

func TestRelayTaskSubmitEmptyOriginKeepsLateMapping(t *testing.T) {
	plugin, err := pluginruntime.NewRegistry().Register(mappingOrderSubmitPlugin, pluginruntime.Options{})
	require.NoError(t, err)
	synthesized := service.CoverTaskActionToModelName(constant.TaskPlatform(plugin.Meta.Key), "text_to_video")
	c, info := newTaskSubmitContext(t, "pre-validate-upstream",
		`{"pre-validate-upstream":"should-not-apply-early","`+synthesized+`":"legacy-tail"}`)
	c.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{Plugin: plugin})
	info.OriginModelName = ""

	_, taskErr := RelayTaskSubmit(c, info)
	require.NotNil(t, taskErr)
	assert.Equal(t, "model_price_error", taskErr.Code)
	assert.Equal(t, synthesized, info.OriginModelName)
	assert.Equal(t, "legacy-tail", info.UpstreamModelName)
	assert.True(t, info.IsModelMapped)
}

const billingFallbackPlugin = `
export const meta = {apiVersion:1,key:"bill-fallback",name:"Bill Fallback",version:"1.0.0",author:{name:"Test"},models:["declared-model"],fetchMode:"per_task"};
export function buildSubmitRequest(ctx) {
  return {url: ctx.baseUrl+"/submit", method:"POST", body:{upstreamModel: ctx.upstreamModel, model: ctx.model}, action:"text_to_video"};
}
export function parseSubmitResponse(){return {taskId:"1"};}
export function buildQueryRequest(){return {url:"https://provider.example"};}
export function parseTaskResult(){return {status:"SUCCESS"};}
`

func saveBillingConfig(t *testing.T) {
	t.Helper()
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})
}

func TestRelayTaskSubmitAliasBillingIdentityAndExprFallback(t *testing.T) {
	const mapping = `{"alias-model":"declared-model"}`
	const aliasExpr = `tier("alias", 2)`
	const tailExpr = `tier("tail", 3)`
	const legacyExpr = `tier("legacy", u("old_units") * 2)`

	tests := []struct {
		name       string
		modes      map[string]string
		exprs      map[string]string
		wantTiered bool
		wantExpr   string
		source     string
	}{
		{
			name:       "alias own tiered wins",
			modes:      map[string]string{"alias-model": "tiered_expr", "declared-model": "tiered_expr"},
			exprs:      map[string]string{"alias-model": aliasExpr, "declared-model": tailExpr},
			wantTiered: true,
			wantExpr:   aliasExpr,
		},
		{
			name:       "fallback uses tail expr",
			modes:      map[string]string{"declared-model": "tiered_expr"},
			exprs:      map[string]string{"declared-model": tailExpr},
			wantTiered: true,
			wantExpr:   tailExpr,
		},
		{
			name:       "stored expression keeps running after schema narrows",
			modes:      map[string]string{"declared-model": "tiered_expr"},
			exprs:      map[string]string{"declared-model": legacyExpr},
			wantTiered: true,
			wantExpr:   legacyExpr,
			source: strings.Replace(billingFallbackPlugin, `fetchMode:"per_task"`, `fetchMode:"per_task", usageSchema:{old_units:{type:"number",unit:"count"}}, usageProfiles:[{models:["declared-model"],schema:{seconds:{type:"number",unit:"second"}}}]`, 1) + `
export function extractUsage(){return {old_units:2};}
`,
		},
		{
			name:       "neither tiered uses ordinary pricing",
			wantTiered: false,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			saveBillingConfig(t)
			if len(testCase.modes) > 0 {
				modeJSON, marshalErr := common.Marshal(testCase.modes)
				require.NoError(t, marshalErr)
				exprJSON, marshalErr := common.Marshal(testCase.exprs)
				require.NoError(t, marshalErr)
				require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
					"billing_setting.billing_mode": string(modeJSON),
					"billing_setting.billing_expr": string(exprJSON),
				}))
				if testCase.wantExpr == aliasExpr {
					require.Equal(t, billing_setting.BillingModeTieredExpr, billing_setting.GetBillingMode("alias-model"))
				} else {
					require.Equal(t, billing_setting.BillingModeRatio, billing_setting.GetBillingMode("alias-model"))
					require.Equal(t, billing_setting.BillingModeTieredExpr, billing_setting.GetBillingMode("declared-model"))
				}
			}

			c, info := newTaskSubmitContext(t, "alias-model", mapping)
			c.Set("group", "default")
			info.UserGroup = "default"
			info.UsingGroup = "default"
			source := testCase.source
			if source == "" {
				source = billingFallbackPlugin
			}
			pinMappingOrderPlugin(t, c, source)
			info.OriginModelName = "alias-model"

			_, taskErr := RelayTaskSubmit(c, info)
			require.NotNil(t, taskErr)
			assert.Equal(t, "alias-model", info.OriginModelName)
			assert.Equal(t, "declared-model", info.UpstreamModelName)
			assert.True(t, info.IsModelMapped)

			task := model.InitTask(constant.TaskPlatform("bill-fallback"), info)
			assert.Equal(t, "alias-model", task.Properties.OriginModelName)
			assert.Equal(t, "declared-model", task.Properties.UpstreamModelName)

			if testCase.wantTiered {
				require.NotNil(t, info.TieredBillingSnapshot, "submission error: %+v", taskErr)
				billingModel := "declared-model"
				if testCase.wantExpr == aliasExpr {
					billingModel = "alias-model"
				}
				assert.Equal(t, billingModel, info.GetBillingModelName())
				assert.Equal(t, billingModel, info.TieredBillingSnapshot.ModelName)
				assert.Equal(t, testCase.wantExpr, info.TieredBillingSnapshot.ExprString)
				assert.Equal(t, billingexpr.ExprHashString(testCase.wantExpr), info.TieredBillingSnapshot.ExprHash)
				assert.NotEqual(t, "model_price_error", taskErr.Code)
				if testCase.wantExpr == legacyExpr {
					assert.Equal(t, 4*common.QuotaPerUnit, info.TieredBillingSnapshot.EstimatedQuotaBeforeGroup)
				}
			} else {
				assert.Nil(t, info.TieredBillingSnapshot)
				assert.Equal(t, "model_price_error", taskErr.Code)
			}
		})
	}
}

func TestSharedTaskBillingExpressionSelectionAndFrozenSettlement(t *testing.T) {
	const baseExpr = `tier("base", u("seconds") * 2)`
	const alphaExpr = `tier("alpha", u("seconds") * 3)`
	const betaExpr = `tier("beta", u("credits") * 5)`
	const aliasExpr = `tier("alias", u("credits") * 7)`
	for _, tc := range []struct {
		name, plugin, model, mapping, modelExpr, mode, wantExpr string
		policy                                                  string
		variants                                                map[string]string
		wantPriceError, profiled                                bool
	}{
		{name: "executing plugin override", plugin: "billing-beta", model: "declared-model", modelExpr: baseExpr, mode: "tiered_expr", variants: map[string]string{"billing-alpha::declared-model": alphaExpr, "billing-beta::declared-model": betaExpr}, wantExpr: betaExpr},
		{name: "override ignores model mode", plugin: "billing-beta", model: "declared-model", modelExpr: baseExpr, mode: "ratio", variants: map[string]string{"billing-beta::declared-model": betaExpr}, wantExpr: betaExpr},
		{name: "model expression fallback", plugin: "billing-alpha", model: "declared-model", modelExpr: baseExpr, mode: "tiered_expr", wantExpr: baseExpr},
		{name: "alias override precedes mapped override", plugin: "billing-beta", model: "alias-model", mapping: `{"alias-model":"declared-model"}`, modelExpr: baseExpr, mode: "tiered_expr", variants: map[string]string{"billing-beta::declared-model": betaExpr, "billing-beta::alias-model": aliasExpr}, wantExpr: aliasExpr},
		{name: "mapped override precedes model fallback", plugin: "billing-beta", model: "alias-model", mapping: `{"alias-model":"declared-model"}`, modelExpr: baseExpr, mode: "tiered_expr", variants: map[string]string{"billing-beta::declared-model": betaExpr}, wantExpr: betaExpr},
		{name: "alias policy true precedes mapped false", plugin: "billing-beta", model: "alias-model", mapping: `{"alias-model":"declared-model"}`, variants: map[string]string{"billing-beta::declared-model": betaExpr}, wantExpr: betaExpr, policy: `{"alias-model":true,"declared-model":false}`},
		{name: "missing alias policy uses mapped policy", plugin: "billing-beta", model: "alias-model", mapping: `{"alias-model":"declared-model"}`, variants: map[string]string{"billing-beta::declared-model": betaExpr}, wantExpr: betaExpr, policy: `{"declared-model":true}`},
		{name: "unconfigured plugin cannot use another schema", plugin: "billing-beta", model: "declared-model", modelExpr: baseExpr, mode: "tiered_expr", wantPriceError: true},
		{name: "missing usage in skipped branch remains incompatible", plugin: "billing-beta", model: "declared-model", modelExpr: `true ? tier("free", 0) : tier("missing", u("seconds"))`, mode: "tiered_expr", wantPriceError: true},
		{name: "fixed pricing is still rejected", plugin: "billing-beta", model: "declared-model", variants: map[string]string{"billing-beta::declared-model": `tier("fixed", fixed(1))`}, wantPriceError: true},
		{name: "endpoint mapping keeps the declared profile", plugin: "billing-beta", model: "declared-model", mapping: `{"declared-model":"ep-endpoint"}`, modelExpr: baseExpr, mode: "tiered_expr", variants: map[string]string{"billing-beta::declared-model": betaExpr}, wantExpr: betaExpr, profiled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saveBillingConfig(t)
			registry := pluginruntime.NewRegistry()
			for _, spec := range []struct{ key, field, unit string }{{"billing-alpha", "seconds", "second"}, {"billing-beta", "credits", "credit"}} {
				source := strings.ReplaceAll(billingFallbackPlugin, "bill-fallback", spec.key)
				schema := `usageSchema:{` + spec.field + `:{type:"number",unit:"` + spec.unit + `"}}`
				if tc.profiled && spec.key == "billing-beta" {
					// The endpoint ID is undeclared, so only the declared model's profile carries this schema.
					schema = `usageSchema:{seconds:{type:"number",unit:"second"}},usageProfiles:[{models:["declared-model"],schema:{` + spec.field + `:{type:"number",unit:"` + spec.unit + `"}}}]`
				}
				source = strings.Replace(source, `fetchMode:"per_task"`, `fetchMode:"per_task",`+schema, 1)
				source += `export function extractUsage(){return {` + spec.field + `:2};}`
				_, err := registry.Register(source, pluginruntime.Options{})
				require.NoError(t, err)
			}
			variants := tc.variants
			if variants == nil {
				variants = map[string]string{}
			}
			rawVariants, err := common.Marshal(variants)
			require.NoError(t, err)
			modes, err := common.Marshal(map[string]string{"declared-model": tc.mode})
			require.NoError(t, err)
			expressions, err := common.Marshal(map[string]string{"declared-model": tc.modelExpr})
			require.NoError(t, err)
			policy := tc.policy
			if policy == "" {
				policy = `{"declared-model":true,"alias-model":false}`
			}
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
				billing_setting.PluginBillingExprOption: string(rawVariants), "billing_setting.billing_mode": string(modes), "billing_setting.billing_expr": string(expressions),
				"billing_setting.ignore_group_ratio":    policy,
				"group_ratio_setting.group_ratio":       `{"default":3}`,
				"group_ratio_setting.group_group_ratio": `{"default":{"default":0.25}}`,
			}))
			c, info := newTaskSubmitContext(t, tc.model, tc.mapping)
			c.Set("group", "default")
			c.Set("task_plugin_key", tc.plugin)
			info.UserGroup = "default"
			info.UsingGroup = "default"
			info.OriginModelName = tc.model
			plugin, ok := registry.Generation().Get(tc.plugin)
			require.True(t, ok)
			c.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{Generation: registry.Generation(), Plugin: plugin})
			_, taskErr := RelayTaskSubmit(c, info)
			require.NotNil(t, taskErr) // This fixture stops at reservation, before upstream submission.
			if tc.wantPriceError {
				assert.Equal(t, "model_price_error", taskErr.Code)
				assert.Nil(t, info.TieredBillingSnapshot)
				return
			}
			require.NotNil(t, info.TieredBillingSnapshot, "submission error: %+v", taskErr)
			assert.Equal(t, tc.wantExpr, info.TieredBillingSnapshot.ExprString)
			wantGroupRatio := 1.0
			if tc.model == "alias-model" && tc.policy == "" {
				wantGroupRatio = 0.25
			}
			assert.Equal(t, wantGroupRatio, info.PriceData.GroupRatioInfo.GroupRatio)
			assert.Equal(t, wantGroupRatio, info.TieredBillingSnapshot.GroupRatio)
			assert.Equal(t, info.GetBillingModelName(), info.TieredBillingSnapshot.ModelName)
			assert.NotEqual(t, "model_price_error", taskErr.Code)
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
				billing_setting.PluginBillingExprOption: `{}`, "billing_setting.billing_expr": `{}`,
				"billing_setting.ignore_group_ratio": `{"declared-model":false,"alias-model":true}`,
				"group_ratio_setting.group_ratio":    `{"default":9}`,
			}))
			field := "seconds"
			if tc.plugin == "billing-beta" {
				field = "credits"
			}
			result, usage, err := service.EvaluateTaskCompletionUsage(info.TieredBillingSnapshot, map[string]any{field: float64(4)})
			require.NoError(t, err)
			assert.Equal(t, float64(4), usage[field])
			assert.Equal(t, float64(2), info.TieredBillingSnapshot.UsageFacts[field])
			assert.Equal(t, 2*info.TieredBillingSnapshot.EstimatedQuotaAfterGroup, result.ActualQuotaAfterGroup)
			assert.Equal(t, tc.wantExpr, info.TieredBillingSnapshot.ExprString)
		})
	}
}

func TestSharedEndpointBillingKeepsOrdinaryAndPluginPricesSeparate(t *testing.T) {
	require.NoError(t, i18n.Init())
	const taskExpr = `tier("images", u("image_count") * 0.05)`
	const ordinaryExpr = `tier("images", fixed(0.04)) * image_count`
	savedPrices := ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices)) })
	for _, tc := range []struct {
		name, expression, override          string
		ordinaryQuota, taskQuota, taskFinal int
		wantError                           string
		alias, shared, legacy               bool
	}{
		{name: "task usage price is rejected by ordinary relay", expression: taskExpr, taskQuota: 50000, taskFinal: 75000, wantError: "task usage expression"},
		{name: "enum fallback cannot silently bill zero", expression: `u("output_image_type") == "2k" ? tier("2k", u("image_count") * 0.08) : tier("1k", 0.04)`, taskQuota: 20000, taskFinal: 20000, wantError: "task usage expression"},
		{name: "unselected usage branch is rejected", expression: `true ? tier("flat", 0.04) : tier("images", u("image_count"))`, taskQuota: 20000, taskFinal: 20000, wantError: "task usage expression"},
		{name: "dynamic usage key is rejected", expression: `tier("images", u(u("unit")) * 0.05)`, taskQuota: 50000, taskFinal: 75000, wantError: "task usage expression"},
		{name: "plain task USD price is ambiguous", expression: `tier("flat", 0.04)`, taskQuota: 20000, taskFinal: 20000, wantError: "shares its billing expression"},
		{name: "alias retains task USD price", expression: `tier("flat", 0.04)`, taskQuota: 20000, taskFinal: 20000, wantError: "shares its billing expression", alias: true},
		{name: "per image relay and plugin overrides", expression: ordinaryExpr, override: taskExpr, ordinaryQuota: 40000, taskQuota: 50000, taskFinal: 75000},
		{name: "mapped plugin override", expression: ordinaryExpr, override: taskExpr, ordinaryQuota: 40000, taskQuota: 50000, taskFinal: 75000, alias: true},
		{name: "token relay and plugin overrides", expression: `tier("tokens", p * 2)`, override: taskExpr, ordinaryQuota: 1000, taskQuota: 50000, taskFinal: 75000},
		{name: "ordinary constant has ordinary units with plugin override", expression: `tier("flat", 80000)`, override: taskExpr, ordinaryQuota: 40000, taskQuota: 50000, taskFinal: 75000},
		{name: "another plugin still inherits task USD units", expression: `tier("flat", 0.04)`, override: taskExpr, taskQuota: 50000, taskFinal: 75000, wantError: "shares its billing expression", shared: true},
		{name: "legacy per image pricing stays shared", ordinaryQuota: 40000, taskQuota: 40000, legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saveBillingConfig(t)
			name, mapping, mappedModel := "declared-model", "", ""
			if tc.alias {
				name, mapping, mappedModel = "alias-model", `{"alias-model":"declared-model"}`, "declared-model"
			}
			mode := billing_setting.BillingModeTieredExpr
			if tc.legacy {
				mode = billing_setting.BillingModeRatio
			}
			variants := map[string]string{}
			if tc.override != "" {
				variants["bill-fallback::declared-model"] = tc.override
			}
			modes, err := common.Marshal(map[string]string{name: mode})
			require.NoError(t, err)
			expressions, err := common.Marshal(map[string]string{name: tc.expression})
			require.NoError(t, err)
			overrides, err := common.Marshal(variants)
			require.NoError(t, err)
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
				"billing_setting.billing_mode": string(modes), "billing_setting.billing_expr": string(expressions),
				billing_setting.PluginBillingExprOption: string(overrides), "group_ratio_setting.group_ratio": `{"default":1}`,
			}))
			prices, err := common.Marshal(map[string]float64{name: 0.04})
			require.NoError(t, err)
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(prices)))
			registry := pluginruntime.NewRegistry()
			source := strings.Replace(billingFallbackPlugin, `fetchMode:"per_task"`, `fetchMode:"per_task",usageSchema:{image_count:{type:"number",unit:"count",description:"Image generation unit price"},output_image_type:{enum:["1k","2k"]},unit:{enum:["image_count"]}}`, 1)
			source += `export function extractUsage(){return {image_count:2,output_image_type:"1k",unit:"image_count"};}`
			plugin, err := registry.Register(source, pluginruntime.Options{})
			require.NoError(t, err)
			candidates := []pluginruntime.ProtocolBinding{{Plugin: plugin}}
			if tc.shared {
				other, err := registry.Register(strings.ReplaceAll(source, "bill-fallback", "other-images"), pluginruntime.Options{})
				require.NoError(t, err)
				candidates = append(candidates, pluginruntime.ProtocolBinding{Plugin: other})
			}
			ordinaryContext, _ := gin.CreateTestContext(httptest.NewRecorder())
			ordinaryContext.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			ordinaryContext.Request.Header.Set("Accept-Language", "en")
			ordinaryContext.Set(pluginruntime.ContextKeyRelayEndpoint, pluginruntime.PinnedEndpoint{Generation: registry.Generation(), Model: name, MappedModel: mappedModel, Candidates: candidates})
			ordinary := &relaycommon.RelayInfo{
				OriginModelName: name, UserGroup: "default", UsingGroup: "default", RelayFormat: kittypes.RelayFormatOpenAIImage,
				Request: &kitdto.ImageRequest{Model: name, N: common.GetPointer(uint(2))}, BillingRequestInput: &billingexpr.RequestInput{},
			}
			ordinaryPrice, err := helper.ModelPriceHelper(ordinaryContext, ordinary, 1000, &kittypes.TokenCountMeta{})
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				assert.Nil(t, ordinary.TieredBillingSnapshot)
				assert.Nil(t, ordinary.Billing, "pricing must fail before reservation")
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.ordinaryQuota, ordinaryPrice.QuotaToPreConsume)
			}

			pluginContext, task := newTaskSubmitContext(t, name, mapping)
			pluginContext.Set("group", "default")
			pluginContext.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{Generation: registry.Generation(), Plugin: plugin})
			task.OriginModelName, task.UserGroup, task.UsingGroup = name, "default", "default"
			_, taskErr := RelayTaskSubmit(pluginContext, task)
			require.NotNil(t, taskErr) // This fixture stops at reservation, before upstream submission.
			assert.NotEqual(t, "model_price_error", taskErr.Code)
			assert.Equal(t, tc.taskQuota, task.PriceData.Quota)
			if tc.legacy {
				assert.Nil(t, task.TieredBillingSnapshot)
				assert.Nil(t, ordinary.TieredBillingSnapshot)
				return
			}
			require.NotNil(t, task.TieredBillingSnapshot)
			assert.True(t, task.TieredBillingSnapshot.TaskUsageBilling)
			// Updating live prices must not change either in-flight meter.
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
				"billing_setting.billing_expr": `{}`, billing_setting.PluginBillingExprOption: `{}`,
			}))
			settlement, _, err := service.EvaluateTaskCompletionUsage(task.TieredBillingSnapshot, map[string]any{"image_count": float64(3)})
			require.NoError(t, err)
			assert.Equal(t, tc.taskFinal, settlement.ActualQuotaAfterGroup)
			if tc.wantError != "" {
				return
			}
			require.NotNil(t, ordinary.TieredBillingSnapshot)
			assert.False(t, ordinary.TieredBillingSnapshot.TaskUsageBilling)
			settlement, err = billingexpr.ComputeTieredQuotaWithRequest(ordinary.TieredBillingSnapshot, billingexpr.TokenParams{P: 1000, Len: 1000}, *ordinary.BillingRequestInput)
			require.NoError(t, err)
			assert.Equal(t, tc.ordinaryQuota, settlement.ActualQuotaAfterGroup)
		})
	}
}

// Issue #7478: task APIs answer 201 Created or 202 Accepted on submission.
// Every 2xx must reach parseSubmitResponse; only other statuses are upstream
// failures that keep the upstream status and body.
func TestRelayTaskSubmitAcceptsAnySuccessfulUpstreamStatus(t *testing.T) {
	service.InitHttpClient()
	const source = `
export const meta = {apiVersion:1,key:"status-echo",name:"Status Echo",version:"1.0.0",author:{name:"Test"},models:["declared-model"],fetchMode:"per_task"};
export function buildSubmitRequest(ctx) { return {url: ctx.baseUrl+"/submit", method:"POST", body:{model: ctx.model}, action:"text_to_video"}; }
export function parseSubmitResponse(ctx, response) { return {taskId: response.body.id, taskData: {status: response.statusCode}}; }
export function buildQueryRequest(ctx) { return {url: ctx.baseUrl+"/query"}; }
export function parseTaskResult() { return {status:"SUCCESS"}; }
`
	for _, tc := range []struct {
		status   int
		wantCode string
	}{
		{status: http.StatusOK},
		{status: http.StatusCreated},
		{status: http.StatusAccepted},
		{status: http.StatusBadRequest, wantCode: "fail_to_fetch_task"},
		{status: http.StatusBadGateway, wantCode: "fail_to_fetch_task"},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			saveBillingConfig(t)
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
				"billing_setting.billing_mode": `{"declared-model":"tiered_expr"}`,
				"billing_setting.billing_expr": `{"declared-model":"tier(\"flat\", 3)"}`,
			}))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"id":"job-42","message":"upstream body"}`))
			}))
			defer server.Close()

			c, info := newTaskSubmitContext(t, "declared-model", "")
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, server.URL)
			c.Set("group", "default")
			info.UserGroup, info.UsingGroup = "default", "default"
			info.OriginModelName = "declared-model"
			// An existing reservation stands in for the wallet so the fixture reaches the upstream call.
			info.Billing = &imageReservation{limit: 1 << 30}
			pinMappingOrderPlugin(t, c, source)

			result, taskErr := RelayTaskSubmit(c, info)
			if tc.wantCode != "" {
				require.NotNil(t, taskErr)
				assert.Equal(t, tc.wantCode, taskErr.Code)
				assert.Equal(t, tc.status, taskErr.StatusCode)
				assert.Contains(t, taskErr.Message, "upstream body")
				assert.Nil(t, result)
				return
			}
			require.Nil(t, taskErr, "submission error: %+v", taskErr)
			require.NotNil(t, result)
			assert.Equal(t, "job-42", result.UpstreamTaskID)
			assert.JSONEq(t, `{"status":`+strconv.Itoa(tc.status)+`}`, string(result.TaskData))
		})
	}
}

// A retry prices the plugin it lands on from scratch and holds that price
// before the upstream sees the request. Plugin A fails upstream on the first
// attempt; the retry runs on plugin B, pinned the way
// SetupContextForSelectedChannel pins another candidate plugin's channel.
func TestRelayTaskSubmitRetryRepricesAndReservesBeforeUpstream(t *testing.T) {
	service.InitHttpClient()
	const source = `
export const meta = {apiVersion:1,key:"KEY",name:"KEY",version:"1.0.0",author:{name:"Test"},models:["declared-model"],fetchMode:"per_task",usageSchema:{seconds:{type:"number",unit:"second",description:"Video generation unit price"}}};
export function buildSubmitRequest(ctx) { return {url: ctx.baseUrl+"/submit", method:"POST", body:{model: ctx.model}, action:"text_to_video"}; }
export function parseSubmitResponse(ctx, response) { return {taskId: response.body.id}; }
export function buildQueryRequest(ctx) { return {url: ctx.baseUrl+"/query"}; }
export function parseTaskResult() { return {status:"SUCCESS"}; }
export function extractUsage() { return {seconds:2}; }
`
	pricier := map[string]string{"retry-a::declared-model": `tier("a", u("seconds") * 0.05)`, "retry-b::declared-model": `tier("b", u("seconds") * 0.4)`}
	for _, tc := range []struct {
		name, perCallPrice, wantCode string
		expressions                  map[string]string
		wallet, wantQuota            int
		wantSubmitted                []string
	}{
		{name: "per-call plugin drops the previous expression", expressions: map[string]string{"retry-a::declared-model": `tier("a", u("seconds") * 0.02)`}, perCallPrice: `{"declared-model":0.04}`, wallet: 1 << 30, wantQuota: 40000, wantSubmitted: []string{"a", "b"}},
		{name: "pricier plugin is held before submission", expressions: pricier, perCallPrice: `{}`, wallet: 1 << 30, wantQuota: 400000, wantSubmitted: []string{"a", "b"}},
		{name: "unaffordable plugin is never submitted", expressions: pricier, perCallPrice: `{}`, wallet: 100000, wantCode: "insufficient_user_quota", wantSubmitted: []string{"a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saveBillingConfig(t)
			savedPrices := ratio_setting.ModelPrice2JSONString()
			t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices)) })
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(tc.perCallPrice))
			expressions, err := common.Marshal(tc.expressions)
			require.NoError(t, err)
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
				billing_setting.PluginBillingExprOption: string(expressions),
				"billing_setting.billing_mode":          `{}`,
				"billing_setting.billing_expr":          `{}`,
				"group_ratio_setting.group_ratio":       `{"default":1}`,
			}))
			registry := pluginruntime.NewRegistry()
			for _, key := range []string{"retry-a", "retry-b"} {
				_, err := registry.Register(strings.ReplaceAll(source, "KEY", key), pluginruntime.Options{})
				require.NoError(t, err)
			}
			// The fake stands in for the session the first attempt created; its
			// limit is the most the wallet can hold.
			billing := &imageReservation{limit: tc.wallet}
			var submitted []string
			var heldAtSubmit []int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempt, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
				submitted = append(submitted, attempt)
				heldAtSubmit = append(heldAtSubmit, billing.held)
				w.Header().Set("Content-Type", "application/json")
				if attempt == "a" {
					w.WriteHeader(http.StatusInternalServerError)
				}
				_, _ = w.Write([]byte(`{"id":"job-` + attempt + `"}`))
			}))
			defer server.Close()

			c, info := newTaskSubmitContext(t, "declared-model", "")
			c.Set("group", "default")
			info.UserGroup, info.UsingGroup, info.OriginModelName = "default", "default", "declared-model"
			info.Billing = billing
			var result *TaskSubmitResult
			var taskErr *dto.TaskError
			for _, attempt := range []string{"a", "b"} {
				plugin, ok := registry.Generation().Get("retry-" + attempt)
				require.True(t, ok)
				c.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{Generation: registry.Generation(), Plugin: plugin})
				c.Set("task_plugin_key", plugin.Meta.Key)
				common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, server.URL+"/"+attempt)
				result, taskErr = RelayTaskSubmit(c, info)
				if attempt == "a" {
					require.NotNil(t, taskErr)
					require.Equal(t, http.StatusInternalServerError, taskErr.StatusCode, "the first attempt fails upstream and is retried")
				}
			}

			require.Equal(t, tc.wantSubmitted, submitted)
			if tc.wantCode != "" {
				require.NotNil(t, taskErr)
				assert.Equal(t, tc.wantCode, taskErr.Code)
				assert.Equal(t, http.StatusForbidden, taskErr.StatusCode)
				return
			}
			require.Nil(t, taskErr, "submission error: %+v", taskErr)
			assert.Equal(t, tc.wantQuota, result.Quota)
			assert.Equal(t, tc.wantQuota, heldAtSubmit[1], "plugin B must not see the request before its price is held")
			if tc.perCallPrice != `{}` {
				assert.Nil(t, info.TieredBillingSnapshot, "a per-call plugin must not settle with the previous plugin's expression")
				return
			}
			require.NotNil(t, info.TieredBillingSnapshot)
			assert.Equal(t, tc.expressions["retry-b::declared-model"], info.TieredBillingSnapshot.ExprString)
		})
	}
}

func TestRelayTaskSpecialPricingReservationAndFrozenCompletion(t *testing.T) {
	service.InitHttpClient()
	const source = `
export const meta = {apiVersion:1,key:"special-images",name:"Special Images",version:"1.0.0",author:{name:"Test"},models:["gpt-image-2"],fetchMode:"per_task",usageSchema:{image_count:{type:"number",unit:"count",description:"Image generation unit price"}}};
export function buildSubmitRequest(ctx) { return {url:ctx.baseUrl+"/submit",method:"POST",body:{model:ctx.model},action:"text_to_image"}; }
export function parseSubmitResponse(ctx, response) { return {taskId:response.body.id,taskData:response.body,immediate:response.body.immediate ? {status:"SUCCESS"} : undefined}; }
export function buildQueryRequest(ctx) { return {url:ctx.baseUrl+"/query"}; }
export function parseTaskResult() { return {status:"SUCCESS"}; }
export function extractUsage() { return {image_count:2,seconds:10,quality:4}; }
export function extractUsageOnSubmit() { return {image_count:99,quality:9}; }
export function extractUsageOnComplete(ctx, result, data) { return {image_count:data.count}; }
`
	for _, tc := range []struct {
		name, userGroup, group, unit, pin  string
		price                              float64
		actual, wantReserved, wantFinal    int
		immediate, legacy, alias           bool
		zeroBalance, preConsume, noSpecial bool
		wantCode                           string
	}{
		{name: "VIP remains free", userGroup: "vip", group: "premium", unit: "image", pin: "plugin", actual: 3, immediate: true},
		{name: "zero balance VIP image skips reservation", userGroup: "vip", group: "premium", unit: "image", pin: "plugin", actual: 3, immediate: true, zeroBalance: true},
		{name: "zero balance VIP request skips reservation", userGroup: "vip", group: "premium", unit: "request", pin: "plugin", actual: 3, immediate: true, zeroBalance: true},
		{name: "zero balance still fails when free pre-consume enabled", userGroup: "vip", group: "premium", unit: "image", pin: "plugin", actual: 3, immediate: true, zeroBalance: true, preConsume: true, wantCode: "insufficient_user_quota"},
		{name: "ordinary zero-price task keeps reservation semantics", userGroup: "vip", group: "premium", unit: "request", pin: "plugin", actual: 3, immediate: true, zeroBalance: true, noSpecial: true, wantCode: "insufficient_user_quota"},
		{name: "ordinary images use final count", userGroup: "ordinary", group: "premium", unit: "image", pin: "endpoint", price: 0.03, actual: 3, wantReserved: 30000, wantFinal: 45000, immediate: true},
		{name: "free routing group still uses special price", userGroup: "ordinary", group: "free", unit: "image", pin: "route", price: 0.03, actual: 1, wantReserved: 30000, wantFinal: 15000, immediate: true},
		{name: "async completion freezes original rule", userGroup: "ordinary", group: "default", unit: "image", pin: "plugin", price: 0.03, actual: 4, wantReserved: 30000, wantFinal: 60000},
		{name: "channel alias matches original model", userGroup: "ordinary", group: "premium", unit: "image", pin: "plugin", price: 0.03, actual: 3, wantReserved: 30000, wantFinal: 45000, alias: true},
		{name: "legacy request price needs no usage hook", userGroup: "ordinary", group: "premium", unit: "request", pin: "plugin", price: 0.03, actual: 4, wantReserved: 15000, wantFinal: 15000, legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saveBillingConfig(t)
			savedRules := setting.SpecialPricingRules2JSONString()
			t.Cleanup(func() { require.NoError(t, setting.UpdateSpecialPricingRulesByJSONString(savedRules)) })
			rules, err := common.Marshal([]setting.SpecialPricingRule{
				{UserGroup: "vip", Model: "gpt-image-*", Price: 0, BillingUnit: tc.unit},
				{UserGroup: "*", Model: "gpt-image-*", Price: tc.price, BillingUnit: tc.unit},
			})
			require.NoError(t, err)
			require.NoError(t, setting.UpdateSpecialPricingRulesByJSONString(string(rules)))
			if tc.noSpecial {
				require.NoError(t, setting.UpdateSpecialPricingRulesByJSONString(`[]`))
			}
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
				"group_ratio_setting.group_ratio":             `{"default":4,"premium":8,"free":0}`,
				"billing_setting.billing_mode":                `{"gpt-image-2":"tiered_expr"}`,
				"billing_setting.billing_expr":                `{"gpt-image-2":"tier(\"wrong-model-price\", 5)"}`,
				billing_setting.PluginBillingExprOption:       `{"special-images::gpt-image-2":"tier(\"wrong-provider-price\", 9)"}`,
				"quota_setting.enable_free_model_pre_consume": strconv.FormatBool(tc.preConsume),
			}))
			if tc.noSpecial {
				require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
					billing_setting.PluginBillingExprOption: `{"special-images::gpt-image-2":"tier(\"ordinary-free\", 0)"}`,
				}))
			}
			billing := &imageReservation{limit: 1 << 30}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				body, marshalErr := common.Marshal(map[string]any{"id": "special-job", "count": tc.actual, "immediate": tc.immediate})
				if marshalErr != nil {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				_, _ = w.Write(body)
			}))
			defer server.Close()
			modelName, mapping := "gpt-image-2", ""
			if tc.alias {
				modelName, mapping = "gpt-image-alias", `{"gpt-image-alias":"gpt-image-2"}`
			}
			c, info := newTaskSubmitContext(t, modelName, mapping)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, server.URL)
			c.Set("auto_group", tc.group)
			info.OriginModelName, info.UserGroup, info.UsingGroup = modelName, tc.userGroup, "default"
			info.Billing = billing
			var zeroBalanceDB *gorm.DB
			if tc.zeroBalance {
				zeroBalanceDB = setupRelayChannelDB(t)
				require.NoError(t, zeroBalanceDB.AutoMigrate(&model.User{}))
				require.NoError(t, zeroBalanceDB.Create(&model.User{Id: 1, Username: "zero-balance-vip", Quota: 0}).Error)
				previousRedis := common.RedisEnabled
				common.RedisEnabled = false
				t.Cleanup(func() { common.RedisEnabled = previousRedis })
				info.UserId, info.UserQuota, info.Billing = 1, 0, nil
				info.UserSetting.BillingPreference = "wallet_only"
			}
			setting.PinGroupSettings(c)
			pluginSource := source
			if tc.legacy {
				pluginSource = strings.ReplaceAll(billingFallbackPlugin, "declared-model", "gpt-image-2")
			}
			registry := pluginruntime.NewRegistry()
			plugin, err := registry.Register(pluginSource, pluginruntime.Options{})
			require.NoError(t, err)
			switch tc.pin {
			case "endpoint":
				c.Set(pluginruntime.ContextKeyPinnedEndpoint, pluginruntime.PinnedEndpoint{Generation: registry.Generation(), Plugin: plugin})
			case "route":
				c.Set(pluginruntime.ContextKeyPinnedRoute, pluginruntime.PinnedRoute{Generation: registry.Generation(), Plugin: plugin})
			default:
				c.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{Generation: registry.Generation(), Plugin: plugin})
			}
			result, taskErr := RelayTaskSubmit(c, info)
			if tc.wantCode != "" {
				require.NotNil(t, taskErr)
				assert.Equal(t, tc.wantCode, taskErr.Code)
				assert.Equal(t, http.StatusForbidden, taskErr.StatusCode)
				assert.False(t, info.PriceData.FreeModel)
				assert.Nil(t, result)
				return
			}
			require.Nil(t, taskErr, "submission error: %+v", taskErr)
			require.NotNil(t, result)
			if tc.zeroBalance {
				assert.True(t, info.PriceData.FreeModel)
				assert.False(t, info.ForcePreConsume)
				assert.Nil(t, info.Billing, "zero-balance VIP must reach success without opening a billing session")
				require.NotNil(t, result.Immediate)
				assert.Equal(t, model.TaskStatusSuccess, result.Immediate.Status)
				require.NoError(t, service.SettleBilling(c, info, result.Quota), "successful free completion must settle without funding")
				var user model.User
				require.NoError(t, zeroBalanceDB.First(&user, info.UserId).Error)
				assert.Zero(t, user.Quota)
			}
			assert.Equal(t, tc.wantReserved, billing.held)
			assert.Equal(t, tc.group, info.UsingGroup)
			assert.Equal(t, 1.0, info.PriceData.GroupRatioInfo.GroupRatio)
			assert.True(t, info.PriceData.GroupRatioInfo.IgnoreGroupRatio)
			assert.Empty(t, info.PriceData.OtherRatios(), "dimensions and legacy submission ratios must not multiply the final price")
			snap := info.TieredBillingSnapshot
			require.NotNil(t, snap)
			assert.True(t, snap.TaskUsageBilling)
			if tc.unit == "image" {
				require.NotNil(t, snap.EstimatedImageCount)
				assert.Equal(t, 2, *snap.EstimatedImageCount)
			} else {
				assert.Nil(t, snap.EstimatedImageCount)
			}
			assert.Equal(t, modelName, snap.ModelName)
			assert.Equal(t, "special", snap.EstimatedTier)
			wantExpr := `tier("special", ` + strconv.FormatFloat(tc.price, 'g', -1, 64) + `)`
			if tc.unit == "image" {
				wantExpr = `tier("special", u("image_count") * ` + strconv.FormatFloat(tc.price, 'g', -1, 64) + `)`
			}
			assert.Equal(t, wantExpr, snap.ExprString)
			if tc.immediate {
				assert.Equal(t, tc.wantFinal, result.Quota)
			} else {
				assert.Equal(t, tc.wantReserved, result.Quota)
			}

			// Exercise the persisted snapshot format used by background polling.
			encoded, err := common.Marshal(model.TaskBillingContext{TieredSnapshot: snap})
			require.NoError(t, err)
			var persisted model.TaskBillingContext
			require.NoError(t, common.Unmarshal(encoded, &persisted))
			require.NoError(t, setting.UpdateSpecialPricingRulesByJSONString(`[{"user_group":"*","model":"gpt-image-*","price":9,"billing_unit":"request"}]`))
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"group_ratio_setting.group_ratio": `{"default":99,"premium":99,"free":99}`}))
			settlement, _, err := service.EvaluateTaskCompletionUsage(persisted.TieredSnapshot, map[string]any{"image_count": float64(tc.actual)})
			require.NoError(t, err)
			assert.Equal(t, tc.wantFinal, settlement.ActualQuotaAfterGroup)
			assert.Equal(t, wantExpr, persisted.TieredSnapshot.ExprString)
			assert.Equal(t, 1.0, persisted.TieredSnapshot.GroupRatio)
			if tc.unit == "image" {
				for _, invalid := range []float64{0, 0.5, 129} {
					retained, _, err := service.EvaluateTaskCompletionUsage(persisted.TieredSnapshot, map[string]any{"image_count": invalid})
					require.NoError(t, err)
					assert.Equal(t, result.Quota, retained.ActualQuotaAfterGroup, "invalid count %v must retain the frozen image quantity", invalid)
				}
			}
		})
	}
}

func TestTaskImageCompletionFallbackKeepsOtherTaskMeterSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		actual      float64
	}{
		{name: "ordinary task image_count may be zero", field: "image_count"},
		{name: "ordinary task meter keeps fractional usage", field: "image_count", actual: 0.5},
		{name: "seconds may settle to zero", field: "seconds"},
		{name: "tokens may settle to zero", field: "tokens"},
		{name: "credits may settle to zero", field: "credits"},
		{name: "other counts may settle to zero", field: "clips"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expression := `tier("ordinary", u("` + tc.field + `"))`
			snap := &billingexpr.BillingSnapshot{
				ExprString: expression, ExprHash: billingexpr.ExprHashString(expression),
				TaskUsageBilling: true, GroupRatio: 1, QuotaPerUnit: 2, ExprVersion: 1,
				UsageFacts: map[string]any{tc.field: float64(2)},
			}
			result, facts, err := service.EvaluateTaskCompletionUsage(snap, map[string]any{tc.field: tc.actual})
			require.NoError(t, err)
			assert.Equal(t, tc.actual, facts[tc.field])
			assert.Equal(t, common.QuotaRound(2*tc.actual), result.ActualQuotaAfterGroup)
			assert.Equal(t, float64(2), snap.UsageFacts[tc.field], "completion must not mutate frozen submission facts")
		})
	}
}

func TestRelayTaskSpecialImagePricingRequiresSelectedModelMeter(t *testing.T) {
	for _, tc := range []struct{ name, schema, usage, wantCode string }{
		{name: "missing image meter", schema: `{seconds:{type:"number",unit:"second"}}`, usage: `{seconds:2}`, wantCode: "model_price_error"},
		{name: "seconds are not images", schema: `{image_count:{type:"number",unit:"second"}}`, usage: `{image_count:2}`, wantCode: "model_price_error"},
		{name: "tokens are not images", schema: `{image_count:{type:"number",unit:"token"}}`, usage: `{image_count:2}`, wantCode: "model_price_error"},
		{name: "selected profile replaces default meter", schema: `{image_count:{type:"number",unit:"count"}},usageProfiles:[{models:["declared-model"],schema:{seconds:{type:"number",unit:"second"}}}]`, usage: `{image_count:2}`, wantCode: "model_price_error"},
		{name: "no usage hook", schema: `{image_count:{type:"number",unit:"count"}}`, wantCode: "plugin_usage_invalid"},
		{name: "missing count fact", schema: `{image_count:{type:"number",unit:"count"}}`, usage: `{seconds:2}`, wantCode: "plugin_usage_invalid"},
		{name: "fractional count", schema: `{image_count:{type:"number",unit:"count"}}`, usage: `{image_count:1.5}`, wantCode: "plugin_usage_invalid"},
		{name: "zero count", schema: `{image_count:{type:"number",unit:"count"}}`, usage: `{image_count:0}`, wantCode: "plugin_usage_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			savedRules := setting.SpecialPricingRules2JSONString()
			t.Cleanup(func() { require.NoError(t, setting.UpdateSpecialPricingRulesByJSONString(savedRules)) })
			require.NoError(t, setting.UpdateSpecialPricingRulesByJSONString(`[{"user_group":"*","model":"declared-model","price":0,"billing_unit":"image"}]`))
			source := strings.Replace(billingFallbackPlugin, `fetchMode:"per_task"`, `fetchMode:"per_task",usageSchema:`+tc.schema, 1)
			if strings.Contains(tc.schema, `unit:"token"`) {
				source = strings.Replace(source, `usageSchema:`, `usageExamples:[{label:"image token fixture",facts:{image_count:1}}],usageSchema:`, 1)
			}
			if tc.usage != "" {
				source += `export function extractUsage(){return ` + tc.usage + `;}`
			}
			c, info := newTaskSubmitContext(t, "declared-model", "")
			info.OriginModelName, info.UserGroup, info.UsingGroup = "declared-model", "vip", "default"
			pinMappingOrderPlugin(t, c, source)
			result, taskErr := RelayTaskSubmit(c, info)
			require.NotNil(t, taskErr)
			assert.Equal(t, tc.wantCode, taskErr.Code)
			assert.Equal(t, http.StatusBadRequest, taskErr.StatusCode)
			assert.Contains(t, taskErr.Message, "image_count")
			assert.Nil(t, result)
			assert.Nil(t, info.Billing, "an invalid meter must fail before reservation and upstream submission, even for VIP zero prices")
			assert.Nil(t, info.TieredBillingSnapshot)
		})
	}
}
