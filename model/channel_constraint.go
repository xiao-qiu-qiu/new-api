package model

import (
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/reasoning"
)

var filterEvalOrder = []dto.ChannelFilterKind{
	dto.FilterRequestPath,
	dto.FilterTaskPluginIdentity,
	dto.FilterResponsesWebSocket,
	dto.FilterServiceTier,
}

// ChannelSatisfiesFilters reports whether ch passes every filter.
// On false, it returns the kind of the first violated filter in evaluation order.
func ChannelSatisfiesFilters(ch *Channel, modelName string, filters []dto.ChannelFilter) (bool, dto.ChannelFilterKind) {
	if ch == nil {
		return false, ""
	}
	for _, kind := range filterEvalOrder {
		for _, filter := range filters {
			if filter.Kind != kind {
				continue
			}
			if !channelMatchesFilter(ch, modelName, filter) {
				return false, kind
			}
		}
	}
	return true, ""
}

// filterCandidateIDs applies filters to a cached candidate id list.
// Caller must hold channelSyncLock (read lock). The input slice is never mutated.
// A missing id in channelsIDM is kept for request_path (downstream consistency
// error) and dropped for task_plugin_identity, matching the previous filters.
func filterCandidateIDs(ids []int, modelName string, filters []dto.ChannelFilter) (kept []int, emptiedBy dto.ChannelFilterKind) {
	if len(ids) == 0 {
		return ids, ""
	}
	kept = ids
	for _, kind := range filterEvalOrder {
		kindFilters := filtersByKind(filters, kind)
		if len(kindFilters) == 0 {
			continue
		}
		next := make([]int, 0, len(kept))
		for _, id := range kept {
			channel, exists := channelsIDM[id]
			if candidatePassesKindFilters(channel, exists, modelName, kind, kindFilters) {
				next = append(next, id)
			}
		}
		if len(kept) > 0 && len(next) == 0 {
			return next, kind
		}
		kept = next
	}
	return kept, ""
}

func filtersByKind(filters []dto.ChannelFilter, kind dto.ChannelFilterKind) []dto.ChannelFilter {
	var matched []dto.ChannelFilter
	for _, filter := range filters {
		if filter.Kind == kind {
			matched = append(matched, filter)
		}
	}
	return matched
}

func candidatePassesKindFilters(ch *Channel, exists bool, modelName string, kind dto.ChannelFilterKind, filters []dto.ChannelFilter) bool {
	if kind == dto.FilterRequestPath && !exists {
		return true
	}
	if !exists || ch == nil {
		return false
	}
	for _, filter := range filters {
		if !channelMatchesFilter(ch, modelName, filter) {
			return false
		}
	}
	return true
}

func channelMatchesFilter(ch *Channel, modelName string, filter dto.ChannelFilter) bool {
	switch filter.Kind {
	case dto.FilterServiceTier:
		if filter.ModelName != "" {
			modelName = filter.ModelName
		}
		return ChannelSupportsServiceTier(ch, modelName, filter.ServiceTier, filter.OpenAITierPolicy)
	case dto.FilterRequestPath:
		if filter.RequestPath == "" {
			return true
		}
		if !constant.IsAdvancedCustomChannel(ch.Type) {
			return true
		}
		config := ch.GetOtherSettings().AdvancedCustom
		return config != nil && config.SupportsPathForModel(filter.RequestPath, modelName)
	case dto.FilterTaskPluginIdentity:
		if filter.TaskPluginKey == "" {
			return ch.Type != constant.ChannelTypeTaskPlugin
		}
		if ch.Type == constant.ChannelTypeTaskPlugin || ch.Type == constant.ChannelTypeNewAPI {
			// A New API channel serves every plugin it is extended with; the
			// pinned plugin or any shared-model candidate may execute there.
			setting := ch.GetSetting()
			return setting.BindsTaskPlugin(filter.TaskPluginKey) || slices.ContainsFunc(filter.TaskPluginKeys, setting.BindsTaskPlugin)
		}
		return slices.Contains(filter.TaskPluginChannelTypes, ch.Type)
	case dto.FilterResponsesWebSocket:
		if !ch.GetSetting().ResponsesWebSocketEnabled {
			return false
		}
		switch ch.Type {
		case constant.ChannelTypeOpenAI, constant.ChannelTypeCodex, constant.ChannelTypeSub2API, constant.ChannelTypeNewAPI:
			return true
		case constant.ChannelTypeAdvancedCustom:
			// The session forwards native Responses events without protocol
			// conversion, so only a converter-free /v1/responses route qualifies.
			route, ok := ch.GetOtherSettings().AdvancedCustom.MatchPathForModel("/v1/responses", modelName)
			return ok && route.IsNative()
		default:
			return false
		}
	default:
		return true
	}
}

// IsGPTTextModel matches the final upstream model's last namespace component.
// Keep both routing and outbound key policy decisions on this shared rule.
func IsGPTTextModel(modelName string) bool {
	return kitdto.IsGPTTextModel(modelName)
}

// ResolveServiceTierModel predicts the final model for candidate filtering.
// Conditional parameter operations need the full request and are checked again
// against the actual outgoing payload by the relay boundary.
func (ch *Channel) ResolveServiceTierModel(modelName string) string {
	if ch == nil {
		return modelName
	}
	if mappingJSON := ch.GetModelMapping(); mappingJSON != "" && mappingJSON != "{}" {
		var mapping map[string]string
		if err := common.UnmarshalJsonStr(mappingJSON, &mapping); err == nil {
			visited := map[string]bool{modelName: true}
			for {
				mapped := mapping[modelName]
				if mapped == "" {
					mapped = mapping[reasoning.BaseModelName(modelName)]
				}
				if mapped == "" || visited[mapped] {
					break
				}
				visited[mapped] = true
				modelName = mapped
			}
		}
	}
	if ch.ParamOverride != nil && *ch.ParamOverride != "" {
		var override struct {
			Model string `json:"model"`
		}
		if err := common.UnmarshalJsonStr(*ch.ParamOverride, &override); err == nil && override.Model != "" {
			modelName = override.Model
		}
	}
	return modelName
}

// ChannelSupportsServiceTier applies explicit GPT capabilities before priority
// or weight selection. Empty declarations retain unrestricted legacy routing.
func ChannelSupportsServiceTier(ch *Channel, modelName, serviceTier, keyPolicy string) bool {
	if ch == nil {
		return false
	}
	if !IsGPTTextModel(ch.ResolveServiceTierModel(modelName)) {
		return true
	}
	tier := kitdto.NormalizeServiceTier(serviceTier)
	if keyPolicy == OpenAITierPolicyPriority || keyPolicy == OpenAITierPolicyUltrafast {
		tier = keyPolicy
	}
	if tier == "" {
		return true
	}
	supported := ch.GetOtherSettings().SupportedServiceTiers
	return len(supported) == 0 || slices.Contains(supported, tier)
}
