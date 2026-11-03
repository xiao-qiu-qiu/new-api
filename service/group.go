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
	return snapshot.FilterAutoGroups(userGroup, groups, snapshot.MaxTokenAutoGroups)
}

// GetRequestAutoGroups uses the configuration generation captured at authentication.
func GetRequestAutoGroups(c *gin.Context, userGroup string) []string {
	snapshot := setting.RequestGroupSettings(c)
	value, ok := common.GetContextKey(c, constant.ContextKeyTokenAutoGroups)
	if !ok {
		return snapshot.FilterAutoGroups(userGroup, snapshot.AutoGroups, len(snapshot.AutoGroups))
	}
	groups, ok := value.([]string)
	if !ok {
		return []string{}
	}
	return snapshot.FilterAutoGroups(userGroup, groups, snapshot.MaxTokenAutoGroups)
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
