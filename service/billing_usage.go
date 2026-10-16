package service

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

const (
	usageBillingPathLocal              = "local"
	usageBillingPathUpstream           = "upstream"
	usageBillingPathOpenAI             = "billing-usage-openai"
	usageBillingPathOpenAIEstimated    = "billing-usage-openai-estimated"
	usageBillingPathAnthropic          = "billing-usage-anthropic"
	usageBillingPathAnthropicEstimated = "billing-usage-anthropic-estimated"
	usageBillingPathGemini             = "billing-usage-gemini"
	usageBillingPathGeminiEstimated    = "billing-usage-gemini-estimated"
)

func effectiveBillingUsage(usage *dto.Usage) *dto.Usage {
	if billingUsage, ok := usageFromBillingUsage(usage); ok {
		// Responses may report only output usage. Preserve the native snapshot
		// for cache completeness, while settling the locally estimated prompt.
		if usage.BillingUsage.Source == dto.BillingUsageSourceOAIResponses && usage.BillingUsage.Estimated && billingUsage.PromptTokens == 0 && usage.PromptTokens > 0 {
			billingUsage.PromptTokens = usage.PromptTokens
			billingUsage.InputTokens = usage.PromptTokens
			billingUsage.TotalTokens = billingUsage.PromptTokens + billingUsage.CompletionTokens
		}
		return billingUsage
	}
	return usage
}

func usageBillingPathForLog(isLocalCountTokens bool, usage *dto.Usage) string {
	effectiveUsage, ok := usageFromBillingUsage(usage)
	if !ok {
		if isLocalCountTokens || (usage != nil && usage.BillingUsage != nil && usage.BillingUsage.Estimated) {
			return usageBillingPathLocal
		}
		return usageBillingPathUpstream
	}

	switch effectiveUsage.UsageSemantic {
	case dto.BillingUsageSemanticOpenAI:
		if usage.BillingUsage.Estimated {
			return usageBillingPathOpenAIEstimated
		}
		return usageBillingPathOpenAI
	case dto.BillingUsageSemanticAnthropic:
		if usage.BillingUsage.Estimated {
			return usageBillingPathAnthropicEstimated
		}
		return usageBillingPathAnthropic
	case dto.BillingUsageSemanticGemini:
		if usage.BillingUsage.Estimated {
			return usageBillingPathGeminiEstimated
		}
		return usageBillingPathGemini
	}

	return usageBillingPathUpstream
}

func appendUsageBillingPathForLog(other *model.LogOther, isLocalCountTokens bool, usage *dto.Usage) {
	if other == nil {
		return
	}
	other.SetAdmin("usage_billing_path", usageBillingPathForLog(isLocalCountTokens, usage))
	if usage == nil || usage.BillingUsage == nil || usage.BillingUsage.Source != dto.BillingUsageSourceOAIResponses {
		return
	}
	billing := usage.BillingUsage
	upstream := billing.OpenAIUsage
	cacheKnown := upstream != nil && (upstream.InputTokens > 0 || upstream.PromptTokens > 0) &&
		(upstream.InputTokensDetails != nil || upstream.PromptTokensDetails.CachedTokens > 0 || upstream.PromptCacheHitTokens > 0)
	// Public completeness facts survive ordinary self/token log projections.
	// A numeric cache_tokens=0 is only a measured zero when this flag is true;
	// an absent flag retains historical behavior for older logs and formats.
	other.SetPublic("usage_estimated", billing.Estimated)
	other.SetPublic("cache_usage_known", cacheKnown)
}

func usageFromBillingUsage(usage *dto.Usage) (*dto.Usage, bool) {
	if usage == nil || usage.BillingUsage == nil {
		return nil, false
	}
	return usage.BillingUsage.CanonicalUsage()
}
