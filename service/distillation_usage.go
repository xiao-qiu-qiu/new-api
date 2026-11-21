package service

import (
	"math"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

func ObserveCompletedTextGeneration(c *gin.Context, info *relaycommon.RelayInfo) {
	settings := operation_setting.GetDistillationGuard()
	if !settings.Enabled || info.UserId <= 0 ||
		!relayconstant.IsTextGenerationRequest(c.Request.Method, c.Request.URL.Path) || IsMediaGenerationRequest(c) || info.NativeCompactionV2 ||
		!c.Writer.Written() || c.Writer.Status() < http.StatusOK || c.Writer.Status() >= http.StatusMultipleChoices ||
		info.PerformanceBusinessRejection || info.DistillationUsage.NotCompletedText || c.Request.Context().Err() != nil ||
		(info.BillingImageCount != nil && *info.BillingImageCount > 0) ||
		!generationStreamCompleted(info.StreamStatus, info.DistillationUsage.CompletedProtocol) {
		return
	}
	usage := info.DistillationUsage
	hit := !info.ClientIsStream && usage.Known && usage.OutputTokens >= settings.MinOutputTokens && usage.CachedTokens <= settings.MaxCachedTokens
	ObserveNonStreamGeneration(c.Request.Context(), info.UserId, hit, settings)
}

func generationStreamCompleted(status *relaycommon.StreamStatus, completedProtocol bool) bool {
	if status == nil {
		return true
	}
	outcome := status.OutcomeSnapshot()
	if outcome.ClientGone || outcome.HasErrors {
		return false
	}
	completed := outcome.Response == relaycommon.ResponseOutcomeCompleted ||
		outcome.Response == relaycommon.ResponseOutcomeUnknown && completedProtocol
	switch outcome.EndReason {
	case relaycommon.StreamEndReasonDone:
		return completed || outcome.Response == relaycommon.ResponseOutcomeUnknown && !outcome.ExpectsTerminal
	case relaycommon.StreamEndReasonNone, relaycommon.StreamEndReasonEOF, relaycommon.StreamEndReasonHandlerStop:
		return completed
	default:
		return false
	}
}

func captureDistillationUsage(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage) {
	if info.ClientIsStream || c.Request == nil || !operation_setting.GetDistillationGuard().Enabled ||
		!relayconstant.IsTextGenerationRequest(c.Request.Method, c.Request.URL.Path) || IsMediaGenerationRequest(c) {
		return
	}
	output, cached, known := measuredGenerationUsage(usage, info.DistillationUsage.NativeZeroCache, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
	info.DistillationUsage.OutputTokens = output
	info.DistillationUsage.CachedTokens = cached
	info.DistillationUsage.Known = known
}

func measuredGenerationUsage(usage *dto.Usage, nativeZeroCache, estimated bool) (output, cached int, known bool) {
	if usage == nil || estimated {
		return 0, 0, false
	}
	if native := usage.BillingUsage; native != nil {
		if native.Estimated {
			return 0, 0, false
		}
		switch native.Source {
		case dto.BillingUsageSourceClaudeMessages:
			if reported := native.ClaudeUsage; reported != nil && reported.OutputTokens > 0 && reported.CacheReadInputTokens >= 0 &&
				(reported.CacheReadInputTokens > 0 || nativeZeroCache) {
				return reported.OutputTokens, reported.CacheReadInputTokens, true
			}
			return 0, 0, false
		case dto.BillingUsageSourceGeminiChat:
			if reported := native.GeminiUsageMetadata; reported != nil && reported.CandidatesTokenCount >= 0 && reported.ThoughtsTokenCount >= 0 &&
				reported.CandidatesTokenCount <= math.MaxInt-reported.ThoughtsTokenCount && reported.CachedContentTokenCount >= 0 &&
				(reported.CachedContentTokenCount > 0 || nativeZeroCache) {
				output := reported.CandidatesTokenCount + reported.ThoughtsTokenCount
				return output, reported.CachedContentTokenCount, output > 0
			}
			return 0, 0, false
		case dto.BillingUsageSourceOAIChat, dto.BillingUsageSourceOAIResponses:
			usage = native.OpenAIUsage
		default:
			return 0, 0, false
		}
	}
	if usage == nil || (usage.UsageSemantic != "" && usage.UsageSemantic != dto.BillingUsageSemanticOpenAI) {
		return 0, 0, false
	}
	output = max(usage.CompletionTokens, usage.OutputTokens)
	if output <= 0 || usage.CompletionTokens < 0 || usage.OutputTokens < 0 || usage.PromptCacheHitTokens < 0 {
		return 0, 0, false
	}
	cached = max(usage.PromptTokensDetails.CachedTokens, usage.PromptCacheHitTokens)
	known = usage.PromptTokensDetails.HasReportedCachedTokens() || usage.PromptCacheHitTokens > 0
	if details := usage.InputTokensDetails; details != nil && details.HasReportedCachedTokens() {
		if details.CachedTokens < 0 {
			return 0, 0, false
		}
		cached, known = max(cached, details.CachedTokens), true
	}
	input := max(usage.PromptTokens, usage.InputTokens)
	return output, cached, known && input > 0 && usage.PromptTokensDetails.CachedTokens >= 0 && cached <= input
}

func MarkResponsesGenerationStatus(info *relaycommon.RelayInfo, rawStatus []byte) {
	var status string
	if len(rawStatus) > 0 && (common.Unmarshal(rawStatus, &status) != nil || status != "" && status != "completed") {
		info.DistillationUsage.NotCompletedText = true
	}
}
