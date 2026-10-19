package common

import (
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
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
