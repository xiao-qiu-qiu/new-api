package common

import (
	"fmt"
	"strings"

	appcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// NormalizeServiceTier accepts OpenAI's documented tiers and the common Fast
// alias. Unknown values cannot become billing multipliers.
func NormalizeServiceTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "fast", "priority":
		return "priority"
	case "ultrafast":
		return "ultrafast"
	case "flex":
		return "flex"
	case "default", "standard", "auto", "scale":
		return "default"
	default:
		return ""
	}
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
	// Only the last namespace component is matched. Keep this rule aligned with
	// the API-key editor: gpt- followed by a digit, excluding non-text families.
	model = strings.ToLower(strings.TrimSpace(model))
	model = model[strings.LastIndex(model, "/")+1:]
	gptText := len(model) > 4 && strings.HasPrefix(model, "gpt-") && model[4] >= '0' && model[4] <= '9'
	for _, excluded := range []string{"image", "audio", "tts", "embed", "transcrib", "realtime"} {
		if strings.Contains(model, excluded) {
			gptText = false
			break
		}
	}
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
	if locked == "" && !strings.EqualFold(strings.TrimSpace(tier), "fast") {
		return payload, tier, nil
	}
	if locked == "" {
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
	encodedTier, err := appcommon.Marshal(tier)
	if err != nil {
		return nil, "", err
	}
	fields["service_tier"] = encodedTier
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
