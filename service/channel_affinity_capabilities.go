package service

import (
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/model_setting"
)

// Model declarations alone do not make an adaptor implement an endpoint. Keep
// this management gate aligned with the converters in relay/channel; normal
// request filters still enforce per-model custom paths, WebSocket and tiers.
func channelAffinityTargetSupportsPath(channel *model.Channel, route channelAffinityRoute) bool {
	apiType, _ := common.ChannelType2APIType(channel.Type)
	path := route.Path
	advanced := constant.IsAdvancedCustomChannel(channel.Type)
	if advanced {
		config := channel.GetOtherSettings().AdvancedCustom
		if config == nil || !config.SupportsPathForModel(path, route.Model) {
			return false
		}
	}
	switch path {
	case "/v1/alpha/search":
		return slices.Contains([]int{constant.ChannelTypeSub2API, constant.ChannelTypeNewAPI, constant.ChannelTypeCodex, constant.ChannelTypeAdvancedCustom}, channel.Type)
	case "/v1/tools/search", "/v1/tools/fetch":
		return slices.Contains([]int{constant.ChannelTypeDeepSeek, constant.ChannelTypeNewAPI, constant.ChannelTypeSub2API, constant.ChannelTypeAdvancedCustom}, channel.Type)
	case "/v1/responses/compact":
		return common.SupportsResponsesCompact(channel.Type, apiType)
	}
	if advanced {
		return true
	}
	if apiType < 0 || apiType == constant.APITypeAIProxyLibrary {
		return false
	}
	// These channel types use the same adaptor implementations.
	if apiType == constant.APITypeOpenRouter || apiType == constant.APITypeXinference {
		apiType = constant.APITypeOpenAI
	} else if apiType == constant.APITypeSub2API {
		apiType = constant.APITypeNewAPI
	}
	if (path == "/v1/chat/completions" || path == "/v1/messages") &&
		!model_setting.GetGlobalSettings().PassThroughRequestEnabled && !channel.GetSetting().PassThroughBodyEnabled &&
		ShouldChatCompletionsUseResponsesGlobal(channel.Id, channel.Type, route.Model) {
		path = "/v1/responses"
	}
	if apiType == constant.APITypeCodex {
		return path == "/v1/responses"
	}

	switch path {
	case "/v1/chat/completions", "/v1/completions":
		return apiType != constant.APITypeReplicate && apiType != constant.APITypeJina
	case "/v1/messages":
		return slices.Contains([]int{
			constant.APITypeOpenAI, constant.APITypeAnthropic, constant.APITypeAli,
			constant.APITypeGemini, constant.APITypeZhipuV4, constant.APITypeOllama,
			constant.APITypePerplexity, constant.APITypeAws, constant.APITypeSiliconFlow,
			constant.APITypeVertexAi, constant.APITypeDeepSeek, constant.APITypeVolcEngine,
			constant.APITypeBaiduV2, constant.APITypeMoonshot, constant.APITypeMiniMax,
			constant.APITypeNewAPI,
		}, apiType)
	case "/v1/responses":
		return slices.Contains([]int{
			constant.APITypeOpenAI, constant.APITypeAnthropic, constant.APITypeAli,
			constant.APITypeGemini, constant.APITypeZhipuV4, constant.APITypeOllama,
			constant.APITypePerplexity, constant.APITypeCloudflare, constant.APITypeDeepSeek,
			constant.APITypeVolcEngine, constant.APITypeXai, constant.APITypeMoonshot,
			constant.APITypeNewAPI,
		}, apiType)
	case "/v1/embeddings":
		return slices.Contains([]int{
			constant.APITypeOpenAI, constant.APITypeBaidu, constant.APITypeAli,
			constant.APITypeGemini, constant.APITypeZhipuV4, constant.APITypeOllama,
			constant.APITypeJina, constant.APITypeCloudflare, constant.APITypeSiliconFlow,
			constant.APITypeMokaAI, constant.APITypeVolcEngine, constant.APITypeMoonshot,
			constant.APITypeMiniMax, constant.APITypeNewAPI,
		}, apiType)
	case "/v1/audio/speech":
		return slices.Contains([]int{constant.APITypeOpenAI, constant.APITypeSiliconFlow, constant.APITypeVolcEngine, constant.APITypeMiniMax}, apiType)
	case "/v1/audio/transcriptions", "/v1/audio/translations":
		return slices.Contains([]int{constant.APITypeOpenAI, constant.APITypeSiliconFlow, constant.APITypeCloudflare}, apiType)
	case "/v1/rerank":
		return slices.Contains([]int{constant.APITypeOpenAI, constant.APITypeAli, constant.APITypeCohere, constant.APITypeJina, constant.APITypeSiliconFlow, constant.APITypeMoonshot}, apiType)
	case "/v1/images/generations":
		if apiType == constant.APITypeGemini || apiType == constant.APITypeVertexAi {
			return strings.HasPrefix(channel.ResolveServiceTierModel(route.Model), "imagen")
		}
		return slices.Contains([]int{
			constant.APITypeOpenAI, constant.APITypeZhipuV4, constant.APITypeSiliconFlow,
			constant.APITypeVolcEngine, constant.APITypeXai, constant.APITypeJimeng,
			constant.APITypeMoonshot, constant.APITypeMiniMax, constant.APITypeReplicate,
			constant.APITypeNewAPI,
		}, apiType)
	case "/v1/images/edits", "/v1/edits":
		return slices.Contains([]int{constant.APITypeOpenAI, constant.APITypeSiliconFlow, constant.APITypeVolcEngine, constant.APITypeXai, constant.APITypeMoonshot, constant.APITypeReplicate, constant.APITypeNewAPI}, apiType)
	case "/v1/moderations", "/v1/realtime":
		return apiType == constant.APITypeOpenAI
	}
	if strings.HasSuffix(path, ":generateContent") || strings.HasSuffix(path, ":streamGenerateContent") {
		return slices.Contains([]int{constant.APITypeOpenAI, constant.APITypeAnthropic, constant.APITypeGemini, constant.APITypeZhipuV4, constant.APITypeVertexAi, constant.APITypeNewAPI}, apiType)
	}
	return false
}
