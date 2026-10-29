package common

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	appcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// NormalizeServiceTier accepts OpenAI's documented tiers and the common Fast
// alias. Unknown values cannot become billing multipliers.
func NormalizeServiceTier(tier string) string {
	return dto.NormalizeServiceTier(tier)
}

func requestedServiceTier(request dto.Request) string {
	switch req := request.(type) {
	case *dto.GeneralOpenAIRequest:
		if value := gjson.ParseBytes(req.ServiceTier); value.Type == gjson.String {
			return strings.TrimSpace(value.String())
		}
	case *dto.OpenAIResponsesRequest:
		return strings.TrimSpace(req.ServiceTier)
	case *dto.OpenAIResponsesCompactionRequest:
		return strings.TrimSpace(req.ServiceTier)
	}
	return ""
}

// ApplyOutboundServiceTierPolicy runs after conversion, filtering and channel
// overrides, before the final tier is reserved. It never changes the original
// request, RequestedServiceTier or the frozen param() billing input.
func (info *RelayInfo) ApplyOutboundServiceTierPolicy(c *gin.Context, payload []byte) ([]byte, string, error) {
	var metadata struct {
		Model       string               `json:"model"`
		ServiceTier appcommon.RawMessage `json:"service_tier"`
	}
	if err := appcommon.Unmarshal(payload, &metadata); err != nil {
		return nil, "", err
	}
	model := metadata.Model
	if model == "" && info.ChannelMeta != nil {
		model = info.UpstreamModelName
	}
	// The actual outgoing model wins, including passthrough and model overrides.
	gptText := dto.IsGPTTextModel(model)
	textRequest := info.RelayMode == relayconstant.RelayModeChatCompletions || info.RelayMode == relayconstant.RelayModeResponses || info.RelayMode == relayconstant.RelayModeResponsesCompact
	var locked string
	if c != nil && gptText {
		switch info.RelayMode {
		case relayconstant.RelayModeChatCompletions, relayconstant.RelayModeResponses, relayconstant.RelayModeResponsesCompact:
			switch policy := appcommon.GetContextKeyString(c, constant.ContextKeyTokenOpenAITierPolicy); policy {
			case "priority", "ultrafast":
				locked = policy
			}
		}
	}
	tier := locked
	if locked == "" && len(metadata.ServiceTier) > 0 {
		if err := appcommon.Unmarshal(metadata.ServiceTier, &tier); err != nil {
			return nil, "", err
		}
	}
	explicitCapabilities := gptText && textRequest && info.ChannelMeta != nil && len(info.ChannelOtherSettings.SupportedServiceTiers) > 0
	if explicitCapabilities {
		if locked == "" && len(metadata.ServiceTier) == 0 && !info.ChannelOtherSettings.AllowServiceTier {
			requestedTier := NormalizeServiceTier(info.RequestedServiceTier)
			if requestedTier != "" && requestedTier != "default" {
				// A conditional model override can turn a non-GPT candidate
				// into GPT after the legacy field filter already ran. Reject
				// that configuration conflict instead of silently downgrading.
				return nil, "", types.NewErrorWithStatusCode(
					fmt.Errorf("the selected channel removed requested service_tier %q; review model mapping and overrides", requestedTier),
					types.ErrorCode("service_tier"), http.StatusBadRequest, types.ErrOptionWithSkipRetry(),
				)
			}
		}
		canonicalTier := NormalizeServiceTier(tier)
		if tier == "" {
			canonicalTier = "default"
		}
		if canonicalTier != "" && !slices.Contains(info.ChannelOtherSettings.SupportedServiceTiers, canonicalTier) {
			return nil, "", types.NewErrorWithStatusCode(
				fmt.Errorf("the selected channel does not support service_tier %q", canonicalTier),
				types.ErrorCode("service_tier"), http.StatusBadRequest, types.ErrOptionWithSkipRetry(),
			)
		}
	}
	fastAlias := strings.EqualFold(strings.TrimSpace(tier), "fast")
	if locked == "" && !fastAlias && (!explicitCapabilities || len(metadata.ServiceTier) == 0) {
		return payload, tier, nil
	}
	if locked == "" && fastAlias {
		tier = "priority"
	}
	// Re-encode the top-level object when rewriting so duplicate/escaped tier
	// keys cannot leave a second client-controlled value for upstream decoders.
	var fields map[string]appcommon.RawMessage
	if err := appcommon.Unmarshal(payload, &fields); err != nil {
		return nil, "", err
	}
	if fields == nil {
		return nil, "", fmt.Errorf("outbound request must be a JSON object")
	}
	if locked != "" || fastAlias {
		encodedTier, err := appcommon.Marshal(tier)
		if err != nil {
			return nil, "", err
		}
		fields["service_tier"] = encodedTier
	}
	updated, err := appcommon.Marshal(fields)
	return updated, tier, err
}

// ObserveResponseServiceTier accepts an upstream downgrade, never an unsolicited
// upgrade. A terminal declaration overrides early observations. Conflicting
// early declarations retain the outbound tier until a terminal resolves them.
func (info *RelayInfo) ObserveResponseServiceTier(payload []byte) {
	info.observeServiceTier(payload, false)
}

// ObserveFinalResponseServiceTier marks the last Chat frame after the stream
// reader reaches its protocol terminal. Compatible chunks may omit object.
func (info *RelayInfo) ObserveFinalResponseServiceTier(payload []byte) {
	info.observeServiceTier(payload, true)
}

func (info *RelayInfo) observeServiceTier(payload []byte, finalFrame bool) {
	if info == nil || info.serviceTierFinal {
		return
	}
	value := gjson.GetBytes(payload, "response.service_tier")
	if !value.Exists() {
		value = gjson.GetBytes(payload, "service_tier")
	}
	if value.Type != gjson.String {
		return
	}
	switch strings.ToLower(strings.TrimSpace(value.String())) {
	case "auto", "scale":
		// These are routing choices, not proof of the actual executed tier.
		return
	}
	tier := NormalizeServiceTier(value.String())
	if tier == "" {
		return
	}
	eventType := gjson.GetBytes(payload, "type").String()
	terminal := finalFrame || (!info.IsStream && eventType == "" && gjson.GetBytes(payload, "object").String() != "chat.completion.chunk")
	switch eventType {
	case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		terminal = true
	}
	if terminal {
		info.serviceTierFinal = true
		info.serviceTierAmbiguous = false
	} else if info.ObservedServiceTier != "" && info.ObservedServiceTier != tier {
		info.serviceTierAmbiguous = true
	}
	info.ObservedServiceTier = tier
	outbound := info.OutboundServiceTier
	if outbound == "" {
		outbound = "default"
	}
	info.ServiceTier = outbound
	if !info.serviceTierAmbiguous && serviceTierRank(tier) <= serviceTierRank(outbound) {
		info.ServiceTier = tier
	}
}

func serviceTierRank(tier string) int {
	switch tier {
	case "flex":
		return 0
	case "priority":
		return 2
	case "ultrafast":
		return 3
	default:
		return 1
	}
}
