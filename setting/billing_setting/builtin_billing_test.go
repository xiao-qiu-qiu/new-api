package billing_setting_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/aws"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	"github.com/QuantumNous/new-api/relay/channel/xunfei"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGPT6AstraBuiltinBilling(t *testing.T) {
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	saved := *settings
	savedRatios, savedPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	savedOptions := common.OptionMap
	t.Cleanup(func() {
		*settings, common.OptionMap = saved, savedOptions
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
	})
	common.OptionMap = map[string]string{"billing_setting.billing_mode": `{}`, "billing_setting.billing_expr": `{}`}
	require.NoError(t, config.GlobalConfig.LoadFromDB(common.OptionMap))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	assert.Equal(t, billing_setting.BillingModeTieredExpr, billing_setting.GetBillingMode("gpt-6-astra"))
	expression, ok := billing_setting.GetBillingExpr("gpt-6-astra")
	require.True(t, ok)

	for _, tc := range []struct {
		name                           string
		input, output, cached, written int
		request                        string
		quota                          int
	}{
		{"standard", 1000, 100, 0, 0, `{}`, 7500},
		{"client flex cannot discount standard pricing", 1000, 100, 0, 0, `{"service_tier":"flex"}`, 7500},
		{"cache at context boundary", 272000, 1000, 200000, 20000, `{}`, 510000},
		{"whole request above boundary", 272001, 1000, 200000, 20000, `{}`, 1007510},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := &dto.Usage{
				PromptTokens: tc.input, CompletionTokens: tc.output,
				PromptTokensDetails: dto.InputTokenDetails{CachedTokens: tc.cached, CacheWriteTokens: tc.written},
			}
			params := service.BuildTieredTokenParams(usage, false, billingexpr.UsedVars(expression))
			result, err := billingexpr.ComputeTieredQuotaWithRequest(&billingexpr.BillingSnapshot{
				ExprString: expression, GroupRatio: 1, QuotaPerUnit: 500000,
			}, params, billingexpr.RequestInput{Body: []byte(tc.request)})
			require.NoError(t, err)
			assert.Equal(t, tc.quota, result.ActualQuotaAfterGroup)
		})
	}

	t.Run("admin options expose defaults without persisting them", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		controller.GetOptions(ctx)
		var response struct {
			Success bool
			Data    []model.Option
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		require.True(t, response.Success)
		found := map[string]string{}
		for _, option := range response.Data {
			if _, ok := common.OptionMap[option.Key]; ok {
				assert.NotContains(t, found, option.Key)
				var values map[string]string
				require.NoError(t, common.UnmarshalJsonStr(option.Value, &values))
				found[option.Key] = values["gpt-6-astra"]
				assert.Equal(t, `{}`, common.OptionMap[option.Key])
			}
		}
		assert.Equal(t, map[string]string{"billing_setting.billing_mode": "tiered_expr", "billing_setting.billing_expr": expression}, found)
	})

	for _, tc := range []struct {
		name, mode, expr, ratios, prices, wantMode string
	}{
		{"custom expression overrides legacy price", "tiered_expr", "p * 7", `{"gpt-6-astra":8}`, `{}`, "tiered_expr"},
		{"explicit ratio mode", "ratio", "", `{}`, `{}`, "ratio"},
		{"existing free token price", "", "", `{"gpt-6-astra":0}`, `{}`, "ratio"},
		{"existing per-call price", "", "", `{}`, `{"gpt-6-astra":0.1}`, "ratio"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			*settings = billing_setting.BillingSetting{BillingMode: map[string]string{}, BillingExpr: map[string]string{}}
			if tc.mode != "" {
				settings.BillingMode["gpt-6-astra"] = tc.mode
			}
			if tc.expr != "" {
				settings.BillingExpr["gpt-6-astra"] = tc.expr
			}
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(tc.ratios))
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(tc.prices))
			assert.Equal(t, tc.wantMode, billing_setting.GetBillingMode("gpt-6-astra"))
			actual, ok := billing_setting.GetBillingExpr("gpt-6-astra")
			assert.Equal(t, tc.expr, actual)
			assert.Equal(t, tc.expr != "", ok)
		})
	}
}

func TestGPT6ServiceTierPrices(t *testing.T) {
	for _, tc := range []struct {
		model               string
		standard, ultrafast float64
	}{
		{"gpt-6-astra", 13450, 80700},
		{"gpt-6.1-sol", 2670, 16020},
		{"gpt-6-sol", 2690, 2690},
		{"gpt-6-luna", 134.5, 134.5},
	} {
		t.Run(tc.model, func(t *testing.T) {
			expression, ok := billing_setting.GetBuiltinBillingExpr(tc.model)
			require.True(t, ok)
			require.NoError(t, billing_setting.SmokeTestExpr(expression))
			params := service.BuildTieredTokenParams(&dto.Usage{PromptTokens: 1000, CompletionTokens: 100,
				PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 200, CacheWriteTokens: 100}}, false, billingexpr.UsedVars(expression))
			for _, tier := range []struct {
				name string
				cost float64
			}{
				{"default", tc.standard}, {"priority", tc.standard * 2}, {"ultrafast", tc.ultrafast}, {"flex", tc.standard * 0.5},
			} {
				cost, trace, err := billingexpr.RunExprWithRequest(expression, params, billingexpr.RequestInput{ServiceTier: tier.name})
				require.NoError(t, err)
				assert.InDelta(t, tier.cost, cost, 0.000001)
				matched := 0
				for _, rule := range trace.RequestRules {
					if rule.Matched {
						matched++
					}
				}
				assert.LessOrEqual(t, matched, 1, "service tiers must not stack")
			}
		})
	}
	for _, tier := range []string{"priority", "ultrafast", "flex"} {
		assert.Error(t, billing_setting.SmokeTestExpr(fmt.Sprintf(`service_tier == %q ? tier("bad", -p) : tier("base", p)`, tier)))
	}
	cost, _, err := billingexpr.RunExprWithRequest(`tier("test", p) * (param("service_tier") == "fast" ? 3 : 1) * (service_tier == "priority" ? 2 : 1)`,
		billingexpr.TokenParams{P: 100}, billingexpr.RequestInput{Body: []byte(`{"service_tier":"fast"}`)})
	require.NoError(t, err)
	assert.Equal(t, float64(300), cost, "effective tier must not replace the original param value")
}

type serviceTierReservation struct {
	quota int
	limit int
}

func (s *serviceTierReservation) Settle(int) error         { return nil }
func (s *serviceTierReservation) Refund(*gin.Context)      {}
func (s *serviceTierReservation) NeedsRefund() bool        { return false }
func (s *serviceTierReservation) GetPreConsumedQuota() int { return s.quota }
func (s *serviceTierReservation) Reserve(target int) error {
	if s.limit > 0 && target > s.limit {
		return errors.New("insufficient quota")
	}
	s.quota = max(s.quota, target)
	return nil
}

func TestServiceTierForwardReservationAndSettlement(t *testing.T) {
	expression, ok := billing_setting.GetBuiltinBillingExpr("gpt-6-astra")
	require.True(t, ok)
	for _, tc := range []struct {
		name, requested, observed, mode, sent, effective string
		allow                                            bool
		reserve, charge, limit                           int
	}{
		{"filtered priority", "priority", "default", "responses", "", "default", false, 5000, 7500, 0},
		{"fast alias", "fast", "priority", "chat", "priority", "priority", true, 10000, 15000, 0},
		{"chat SSE final downgrade without object", "priority", "default", "chat-stream", "priority", "default", true, 10000, 7500, 0},
		{"upstream downgrade", "priority", "default", "responses", "priority", "default", true, 10000, 7500, 0},
		{"ultrafast SSE", "ultrafast", "ultrafast", "stream", "ultrafast", "ultrafast", true, 30000, 45000, 0},
		{"SSE downgrade", "priority", "default", "stream", "priority", "default", true, 10000, 7500, 0},
		{"auto is not a downgrade", "priority", "auto", "responses", "priority", "priority", true, 10000, 15000, 0},
		{"unsolicited upgrade", "default", "ultrafast", "responses", "default", "default", true, 5000, 7500, 0},
		{"flex reserves discounted price", "flex", "flex", "responses", "flex", "flex", true, 2500, 3750, 3000},
		{"reject expensive tier before upstream", "ultrafast", "ultrafast", "responses", "", "", true, 0, 0, 10000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forwarded := make(chan string, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					ServiceTier string `json:"service_tier"`
				}
				if err := common.DecodeJson(r.Body, &body); err != nil {
					t.Error(err)
				}
				forwarded <- body.ServiceTier
				if tc.mode == "chat-stream" {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, "data: {\"service_tier\":\"priority\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
					_, _ = fmt.Fprintf(w, "data: {\"service_tier\":%q,\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":100,\"total_tokens\":1100}}\n\ndata: [DONE]\n\n", tc.observed)
					return
				}
				if tc.mode == "chat" {
					_, _ = fmt.Fprintf(w, `{"object":"chat.completion","model":"gpt-6-astra","service_tier":%q,"choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100}}`, tc.observed)
					return
				}
				response := fmt.Sprintf(`{"object":"response","model":"gpt-6-astra","service_tier":%q,"status":"completed","usage":{"input_tokens":1000,"output_tokens":100,"total_tokens":1100}}`, tc.observed)
				if tc.mode == "stream" {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
					return
				}
				_, _ = fmt.Fprint(w, response)
			}))
			defer upstream.Close()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			request := []byte(fmt.Sprintf(`{"model":"gpt-6-astra","service_tier":%q}`, tc.requested))
			request, err := relaycommon.RemoveDisabledFields(request, dto.ChannelOtherSettings{AllowServiceTier: tc.allow}, false)
			require.NoError(t, err)
			reservation := &serviceTierReservation{limit: tc.limit}
			info := &relaycommon.RelayInfo{
				RelayFormat: types.RelayFormatOpenAIResponses, RequestedServiceTier: tc.requested, ReasoningEffort: "high",
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-6-astra"}, OriginModelName: "gpt-6-astra",
				IsStream: strings.Contains(tc.mode, "stream"), DisablePing: true, StartTime: time.Now(),
				Billing: reservation, DeferServiceTierReservation: true, ForcePreConsume: true,
				PriceData: hosttypes.PriceData{GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1}},
				TieredBillingSnapshot: &billingexpr.BillingSnapshot{BillingMode: "tiered_expr", ExprString: expression,
					ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, EstimatedPromptTokens: 1000, QuotaPerUnit: 500000},
				BillingRequestInput: &billingexpr.RequestInput{Body: []byte(fmt.Sprintf(`{"service_tier":%q}`, tc.requested))},
			}
			if strings.HasPrefix(tc.mode, "chat") {
				info.RelayFormat = types.RelayFormatOpenAI
			}
			req, err := http.NewRequest(http.MethodPost, upstream.URL, strings.NewReader(string(request)))
			require.NoError(t, err)
			response, err := channel.DoRequest(c, req, info)
			if tc.charge == 0 {
				require.Error(t, err)
				assert.Empty(t, forwarded, "insufficient reservation must block upstream submission")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.sent, <-forwarded)
			assert.Equal(t, tc.reserve, reservation.quota)
			var usage *dto.Usage
			var apiErr *types.NewAPIError
			switch tc.mode {
			case "chat":
				usage, apiErr = openai.OpenaiHandler(c, info, response)
			case "chat-stream":
				usage, apiErr = openai.OaiStreamHandler(c, info, response)
			case "stream":
				usage, apiErr = openai.OaiResponsesStreamHandler(c, info, response)
			default:
				usage, apiErr = openai.OaiResponsesHandler(c, info, response)
			}
			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			applies, quota, result := service.TryTieredSettle(info, service.BuildTieredTokenParams(usage, false, billingexpr.UsedVars(expression)))
			require.True(t, applies)
			assert.Equal(t, tc.charge, quota)
			assert.Equal(t, tc.effective, info.ServiceTier)
			other := service.GenerateTextOtherInfo(c, info, 0, 1, 0, 0, 0, 0, 1)
			service.InjectTieredBillingInfo(other, info, result)
			var logged map[string]any
			require.NoError(t, common.UnmarshalJsonStr(other.JSONString(), &logged))
			assert.Equal(t, tc.effective, logged["service_tier"])
			assert.Equal(t, tc.requested, logged["requested_service_tier"])
			assert.Equal(t, "high", logged["reasoning_effort"])
		})
	}
}

func TestServiceTierChatResponsesConversion(t *testing.T) {
	request, err := relayconvert.ChatCompletionsRequestToResponsesRequest(&dto.GeneralOpenAIRequest{
		Model: "gpt-6-astra", ServiceTier: common.RawMessage(`"ultrafast"`),
		Messages: []dto.Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "ultrafast", request.ServiceTier)
	chat, err := relayconvert.ResponsesRequestToChatCompletionsRequest(request)
	require.NoError(t, err)
	assert.JSONEq(t, `"ultrafast"`, string(chat.ServiceTier))
	response, _, err := relayconvert.ChatCompletionsResponseToResponsesResponse(&dto.OpenAITextResponse{ServiceTier: "priority", Model: "gpt-6-astra"}, "resp-test")
	require.NoError(t, err)
	assert.Equal(t, "priority", response.ServiceTier)
	chatResponse, _, err := relayconvert.ResponsesResponseToChatCompletionsResponse(response, "chat-test")
	require.NoError(t, err)
	assert.Equal(t, "priority", chatResponse.ServiceTier)
	assert.Equal(t, "priority", (&dto.ChatCompletionsStreamResponse{ServiceTier: "priority"}).Copy().ServiceTier)
}

func TestServiceTierPreconsumeDefersUntilFinalRequest(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-6-astra","service_tier":"flex"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAIResponses, OriginModelName: "gpt-6-astra", UsingGroup: "default",
		Request: &dto.OpenAIResponsesRequest{Model: "gpt-6-astra", ServiceTier: "flex"}}
	require.Nil(t, relay.PrepareRequestBilling(c, info))
	assert.Nil(t, info.Billing)
	assert.True(t, info.DeferServiceTierReservation)
	assert.True(t, info.ForcePreConsume)
	require.Nil(t, service.PrepareTieredBillingForSelectedGroup(c, info))
	assert.Nil(t, info.Billing, "group retry preparation must also wait for the final tier")
}

func TestServiceTierTerminalPrecedence(t *testing.T) {
	info := &relaycommon.RelayInfo{OutboundServiceTier: "priority", ServiceTier: "priority"}
	info.ObserveResponseServiceTier([]byte(`{"type":"response.created","response":{"service_tier":"flex"}}`))
	assert.Equal(t, "flex", info.ServiceTier)
	info.ObserveResponseServiceTier([]byte(`{"type":"response.in_progress","response":{"service_tier":"default"}}`))
	assert.Equal(t, "priority", info.ServiceTier, "conflicting early observations retain the outgoing tier")
	info.ObserveResponseServiceTier([]byte(`{"type":"response.completed","response":{"service_tier":"default"}}`))
	assert.Equal(t, "default", info.ServiceTier)
	info.ObserveResponseServiceTier([]byte(`{"type":"response.in_progress","response":{"service_tier":"flex"}}`))
	assert.Equal(t, "default", info.ServiceTier, "terminal observation stays authoritative")
}

func TestServiceTierObservedPriceCap(t *testing.T) {
	sol, ok := billing_setting.GetBuiltinBillingExpr("gpt-6-sol")
	require.True(t, ok)
	for _, tc := range []struct {
		name, expression, outbound, observed string
		quota                                int
	}{
		{"unpriced ultrafast to priority", sol, "ultrafast", "priority", 1500},
		{"administrator non-monotonic pricing", `tier("base", p * (service_tier == "flex" ? 100 : 10))`, "priority", "flex", 5000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{OutboundServiceTier: tc.outbound, ServiceTier: tc.outbound,
				TieredBillingSnapshot: &billingexpr.BillingSnapshot{BillingMode: "tiered_expr", ExprString: tc.expression,
					ExprHash: billingexpr.ExprHashString(tc.expression), GroupRatio: 1, QuotaPerUnit: 500000}}
			info.ObserveResponseServiceTier([]byte(fmt.Sprintf(`{"service_tier":%q}`, tc.observed)))
			applies, quota, result := service.TryTieredSettle(info, billingexpr.TokenParams{P: 1000, C: 100, Len: 1000})
			require.True(t, applies)
			require.NotNil(t, result)
			assert.Equal(t, tc.quota, quota)
			assert.Equal(t, tc.outbound, info.ServiceTier)
			assert.Equal(t, tc.observed, info.ObservedServiceTier)
		})
	}
}

func TestServiceTierSDKDispatchRequiresReservation(t *testing.T) {
	expression, ok := billing_setting.GetBuiltinBillingExpr("gpt-6-astra")
	require.True(t, ok)
	for _, adaptor := range []channel.Adaptor{&aws.Adaptor{ClientMode: aws.ClientModeAKSK}, &xunfei.Adaptor{}} {
		t.Run(adaptor.GetChannelName(), func(t *testing.T) {
			info := &relaycommon.RelayInfo{DeferServiceTierReservation: true, Billing: &serviceTierReservation{limit: 1},
				PriceData: hosttypes.PriceData{GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1}},
				TieredBillingSnapshot: &billingexpr.BillingSnapshot{BillingMode: "tiered_expr", ExprString: expression,
					ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000, EstimatedPromptTokens: 1000}}
			_, err := adaptor.DoRequest(nil, info, nil)
			require.Error(t, err, "SDK dispatch must stop before any network call when reservation fails")
		})
	}
}

func TestImageModelBuiltinPricesAndOverrides(t *testing.T) {
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	saved := *settings
	savedRatios, savedPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		*settings = saved
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
	})
	for _, name := range []string{"gpt-image-2", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare"} {
		t.Run(name, func(t *testing.T) {
			*settings = billing_setting.BillingSetting{BillingMode: map[string]string{}, BillingExpr: map[string]string{}}
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
			expression, ok := billing_setting.GetBillingExpr(name)
			require.True(t, ok)
			usage := &dto.Usage{PromptTokens: 1000, CompletionTokens: 100, PromptTokensDetails: dto.InputTokenDetails{
				CachedTokens: 300, ImageTokens: 600, CachedTokensDetails: &dto.CachedTokenDetails{ImageTokens: common.GetPointer(200)},
			}}
			result, err := billingexpr.ComputeTieredQuota(&billingexpr.BillingSnapshot{ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000},
				service.BuildTieredTokenParams(usage, false, billingexpr.UsedVars(expression)))
			require.NoError(t, err)
			assert.Equal(t, 4113, result.ActualQuotaAfterGroup)
			encoded, err := common.Marshal(map[string]float64{name: 0})
			require.NoError(t, err)
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(encoded)))
			assert.Equal(t, "ratio", billing_setting.GetBillingMode(name))
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(encoded)))
			assert.Equal(t, "ratio", billing_setting.GetBillingMode(name))
			settings.BillingMode[name] = "tiered_expr"
			settings.BillingExpr[name] = `tier("custom", p * 7)`
			actual, ok := billing_setting.GetBillingExpr(name)
			require.True(t, ok)
			assert.Equal(t, settings.BillingExpr[name], actual)
		})
	}
}
