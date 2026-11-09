package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

func GetUserUsableGroups(userGroup string) map[string]string {
	return setting.CaptureGroupSettings().Usable(userGroup)
}

func GroupInUserUsableGroups(userGroup, groupName string) bool {
	_, ok := setting.CaptureGroupSettings().Usable(userGroup)[groupName]
	return ok
}

func IsUserSelectableGroup(userGroup, groupName string) bool {
	return setting.CaptureGroupSettings().Selectable(userGroup, groupName)
}

func GetUserAutoGroup(userGroup string) []string {
	snapshot := setting.CaptureGroupSettings()
	return snapshot.FilterAutoGroups(userGroup, snapshot.AutoGroups, len(snapshot.AutoGroups))
}

func FilterUserTokenAutoGroups(userGroup string, groups []string) []string {
	snapshot := setting.CaptureGroupSettings()
	if ValidateTokenAutoGroups(snapshot, userGroup, groups) != nil {
		return []string{}
	}
	return groups
}

// ValidateTokenAutoGroups rejects an invalid selection as a whole; filtering it
// would silently change the token's routing scope or priority order.
func ValidateTokenAutoGroups(snapshot *setting.GroupSettingsSnapshot, userGroup string, groups []string) error {
	if len(groups) == 0 {
		return common.NewMessage("Select at least one group for this token")
	}
	if len(groups) > snapshot.MaxTokenAutoGroups {
		return common.NewMessage("A token can select at most {{max}} groups", map[string]any{"max": snapshot.MaxTokenAutoGroups})
	}
	seen := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		if _, exists := seen[group]; exists {
			return common.NewMessage("Group {{group}} is duplicated", map[string]any{"group": group})
		}
		seen[group] = struct{}{}
		if !snapshot.Selectable(userGroup, group) {
			return common.NewMessage("Group {{group}} is unavailable or unauthorized", map[string]any{"group": group})
		}
	}
	return nil
}

// GetRequestAutoGroups uses the configuration generation captured at authentication.
func GetRequestAutoGroups(c *gin.Context, userGroup string) []string {
	snapshot := setting.RequestGroupSettings(c)
	value, ok := common.GetContextKey(c, constant.ContextKeyTokenAutoGroups)
	if !ok {
		return []string{}
	}
	groups, ok := value.([]string)
	if !ok {
		return []string{}
	}
	// The session playground binds its selected global snapshot explicitly;
	// the per-key selection limit does not apply to that independent mode.
	if common.GetContextKeyBool(c, constant.ContextKeyIsPlayground) {
		return snapshot.FilterAutoGroups(userGroup, groups, len(groups))
	}
	if ValidateTokenAutoGroups(snapshot, userGroup, groups) != nil {
		return []string{}
	}
	return groups
}

// GetGroupsEnabledModels 按 groups 顺序获取各分组启用的模型并去重
func GetGroupsEnabledModels(groups []string) []string {
	seen := make(map[string]struct{})
	models := make([]string, 0)
	for _, group := range groups {
		for _, modelName := range model.GetGroupEnabledModels(group) {
			if _, ok := seen[modelName]; !ok {
				seen[modelName] = struct{}{}
				models = append(models, modelName)
			}
		}
	}
	return models
}

// GetUserGroupRatio 获取用户使用某个分组的倍率
// userGroup 用户分组
// group 需要获取倍率的分组
func GetUserGroupRatio(userGroup, group string) float64 {
	return setting.CaptureGroupSettings().GroupRatio(userGroup, group)
}
