package common

import (
	"net/http/httptest"
	"testing"

	appcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMixedModelKeyUltrafastPolicy(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	appcommon.SetContextKey(c, constant.ContextKeyTokenOpenAITierPolicy, "ultrafast")

	// Both requests use the same key policy; only the GPT request may gain a tier.
	for _, model := range []string{"gpt-6-astra", "deepseek-v4-flash"} {
		t.Run(model, func(t *testing.T) {
			request := &dto.GeneralOpenAIRequest{Model: model}
			payload, err := appcommon.Marshal(request)
			require.NoError(t, err)
			info := &RelayInfo{
				OriginModelName: model,
				RelayMode:       relayconstant.RelayModeChatCompletions,
				Request:         request,
				ChannelMeta: &ChannelMeta{
					UpstreamModelName: model,
					ChannelOtherSettings: dto.ChannelOtherSettings{
						SupportedServiceTiers: []string{"default", "ultrafast"},
					},
				},
			}
			outbound, tier, err := info.ApplyOutboundServiceTierPolicy(c, payload)
			require.NoError(t, err)
			var sent map[string]any
			require.NoError(t, appcommon.Unmarshal(outbound, &sent))
			if model == "gpt-6-astra" {
				assert.Equal(t, "ultrafast", tier)
				assert.Equal(t, "ultrafast", sent["service_tier"])
			} else {
				assert.Empty(t, tier)
				assert.NotContains(t, sent, "service_tier")
				assert.Equal(t, payload, outbound)
			}
			assert.Empty(t, request.ServiceTier, "key policy must not mutate the original request")
		})
	}
}

func TestUltrafastPolicyUsesFinalModelAndPreservesProviderTier(t *testing.T) {
	for _, tc := range []struct {
		name, original, upstream, body, wantTier string
		override                                 map[string]any
	}{
		{
			name: "GPT mapped to DeepSeek", original: "gpt-6-astra", upstream: "deepseek-v4-flash",
			body: `{"model":"deepseek-v4-flash","messages":[]}`,
		},
		{
			name: "channel model override away from GPT", original: "gpt-6-astra", upstream: "gpt-6-astra",
			body: `{"model":"gpt-6-astra","messages":[]}`, override: map[string]any{"model": "deepseek-v4-flash"},
		},
		{
			name: "channel operation override away from GPT", original: "gpt-6-astra", upstream: "gpt-6-astra",
			body:     `{"model":"gpt-6-astra","messages":[]}`,
			override: map[string]any{"operations": []any{map[string]any{"path": "model", "mode": "set", "value": "deepseek-v4-flash"}}},
		},
		{
			name: "DeepSeek mapped to GPT", original: "deepseek-v4-flash", upstream: "openai/gpt-6-astra",
			body: `{"model":"openai/gpt-6-astra","messages":[]}`, wantTier: "ultrafast",
		},
		{
			name: "missing body model uses mapped DeepSeek", original: "gpt-6-astra", upstream: "deepseek-v4-flash",
			body: `{"messages":[]}`,
		},
		{
			name: "opaque alias uses mapped GPT", original: "custom-alias", upstream: "openai/gpt-6-astra",
			body: `{"messages":[]}`, wantTier: "ultrafast",
		},
		{
			name: "Claude native tier survives key policy", original: "claude-opus", upstream: "claude-opus",
			body: `{"model":"claude-opus","service_tier":"standard_only","messages":[]}`, wantTier: "standard_only",
		},
		{
			name: "channel override preserves Claude tier", original: "gpt-6-astra", upstream: "gpt-6-astra",
			body: `{"model":"gpt-6-astra","messages":[]}`, wantTier: "auto",
			override: map[string]any{"model": "claude-opus", "service_tier": "auto"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			appcommon.SetContextKey(c, constant.ContextKeyTokenOpenAITierPolicy, "ultrafast")
			info := &RelayInfo{
				OriginModelName: tc.original,
				RelayMode:       relayconstant.RelayModeChatCompletions,
				ChannelMeta: &ChannelMeta{
					UpstreamModelName: tc.upstream,
					ParamOverride:     tc.override,
				},
			}
			payload, err := ApplyParamOverrideWithRelayInfo([]byte(tc.body), info)
			require.NoError(t, err)
			outbound, tier, err := info.ApplyOutboundServiceTierPolicy(c, payload)
			require.NoError(t, err)
			assert.Equal(t, tc.wantTier, tier)
			var sent map[string]any
			require.NoError(t, appcommon.Unmarshal(outbound, &sent))
			if tc.wantTier == "" {
				assert.NotContains(t, sent, "service_tier")
			} else {
				assert.Equal(t, tc.wantTier, sent["service_tier"])
			}
			if tc.wantTier != "ultrafast" {
				assert.Equal(t, payload, outbound, "the GPT key policy must preserve non-GPT payloads")
			}
		})
	}
}
