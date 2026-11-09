package setting

import (
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

// GroupSettingsPublicationMutex protects whole configuration publications and
// snapshot captures, never individual getters. Consumers release it before
// using their immutable snapshot, so nested business calls cannot deadlock.
var GroupSettingsPublicationMutex sync.RWMutex

type GroupSettingsSnapshot struct {
	UserGroups          map[string]string
	Configured          bool
	Ratios              map[string]float64
	SpecialRatios       map[string]map[string]float64
	SpecialPricingRules []SpecialPricingRule
	UsableGroups        map[string]string
	SpecialUsableGroups map[string]map[string]string
	AutoGroups          []string
	MaxTokenAutoGroups  int
	TopupRatios         map[string]float64
}

func CaptureGroupSettings() *GroupSettingsSnapshot {
	GroupSettingsPublicationMutex.RLock()
	defer GroupSettingsPublicationMutex.RUnlock()
	return captureGroupSettings()
}

// CaptureGroupSettingsAndOptions is for the settings page, which also needs
// option strings from this same publication. Relay requests do not copy them.
func CaptureGroupSettingsAndOptions() (*GroupSettingsSnapshot, map[string]string) {
	GroupSettingsPublicationMutex.RLock()
	defer GroupSettingsPublicationMutex.RUnlock()
	snapshot := captureGroupSettings()
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return snapshot, maps.Clone(common.OptionMap)
}

// The caller holds GroupSettingsPublicationMutex for the entire capture.
func captureGroupSettings() *GroupSettingsSnapshot {
	snapshot := &GroupSettingsSnapshot{
		UserGroups: GetUserGroupsCopy(), Configured: UserGroupsConfigured(),
		Ratios:              ratio_setting.GetGroupRatioCopy(),
		SpecialRatios:       ratio_setting.GetGroupRatioSetting().GroupGroupRatio.ReadAll(),
		SpecialPricingRules: GetSpecialPricingRulesCopy(),
		UsableGroups:        GetUserUsableGroupsCopy(),
		SpecialUsableGroups: ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.ReadAll(),
		AutoGroups:          slices.Clone(GetAutoGroups()), MaxTokenAutoGroups: GetMaxTokenAutoGroups(),
	}
	_ = common.UnmarshalJsonStr(common.TopupGroupRatio2JSONString(), &snapshot.TopupRatios)
	for name, ratios := range snapshot.SpecialRatios {
		snapshot.SpecialRatios[name] = maps.Clone(ratios)
	}
	for name, groups := range snapshot.SpecialUsableGroups {
		snapshot.SpecialUsableGroups[name] = maps.Clone(groups)
	}
	return snapshot
}

const groupSettingsContextKey = "group_settings_snapshot"

// PinGroupSettings makes authentication, Auto selection and pricing use the
// same generation for the entire request, without holding a streaming lock.
func PinGroupSettings(c *gin.Context) *GroupSettingsSnapshot {
	snapshot := RequestGroupSettings(c)
	c.Set(groupSettingsContextKey, snapshot)
	return snapshot
}

func RequestGroupSettings(c *gin.Context) *GroupSettingsSnapshot {
	if c != nil {
		if value, exists := c.Get(groupSettingsContextKey); exists {
			if snapshot, ok := value.(*GroupSettingsSnapshot); ok {
				return snapshot
			}
		}
	}
	return CaptureGroupSettings()
}

func (s *GroupSettingsSnapshot) DefaultGroup(userGroup string) string {
	if group := s.UserGroups[userGroup]; group != "" {
		return group
	}
	if userGroup != "" && userGroup != "auto" && s.ContainsRatio(userGroup) {
		return userGroup
	}
	return "default"
}

func (s *GroupSettingsSnapshot) ContainsRatio(group string) bool {
	_, exists := s.Ratios[group]
	return exists
}

func (s *GroupSettingsSnapshot) Usable(userGroup string) map[string]string {
	groups := maps.Clone(s.UsableGroups)
	if groups == nil {
		groups = make(map[string]string)
	}
	if userGroup == "" {
		return groups
	}
	for target, description := range s.SpecialUsableGroups[userGroup] {
		if group, remove := strings.CutPrefix(target, "-:"); remove {
			delete(groups, group)
		} else {
			groups[strings.TrimPrefix(target, "+:")] = description
		}
	}
	return groups
}

func (s *GroupSettingsSnapshot) Selectable(userGroup, group string) bool {
	_, allowed := s.Usable(userGroup)[group]
	return group != "" && group != "auto" && allowed && s.ContainsRatio(group)
}

func (s *GroupSettingsSnapshot) FilterAutoGroups(userGroup string, groups []string, limit int) []string {
	filtered := make([]string, 0, min(len(groups), limit))
	seen := make(map[string]bool)
	usable := s.Usable(userGroup)
	for _, group := range groups {
		if _, allowed := usable[group]; !allowed || group == "" || group == "auto" || !s.ContainsRatio(group) || seen[group] {
			continue
		}
		seen[group] = true
		filtered = append(filtered, group)
		if len(filtered) == limit {
			break
		}
	}
	return filtered
}

func (s *GroupSettingsSnapshot) GroupRatio(userGroup, group string) float64 {
	if ratio, exists := s.SpecialRatios[userGroup][group]; exists {
		return ratio
	}
	if ratio, exists := s.Ratios[group]; exists {
		return ratio
	}
	return 1
}

// Go decodes null into scalar zero values. Reject null anywhere before typed
// decoding so explicit numeric zero remains distinguishable from missing data.
func ValidateGroupSettingsJSON(key, value string) error {
	switch key {
	case "SpecialPricingRules":
		_, err := ParseSpecialPricingRules(value)
		return err
	case "UserGroups", "GroupRatio", "GroupGroupRatio", "TopupGroupRatio", "UserUsableGroups",
		"group_ratio_setting.group_special_usable_group", "group_ratio_setting.group_ratio", "group_ratio_setting.group_group_ratio", "AutoGroups":
	default:
		return nil
	}
	var decoded any
	if err := common.UnmarshalJsonStr(value, &decoded); err != nil {
		return common.NewMessage("Invalid parameters")
	}
	pending := []any{decoded}
	for len(pending) > 0 {
		last := len(pending) - 1
		value := pending[last]
		pending = pending[:last]
		switch value := value.(type) {
		case nil:
			return common.NewMessage("Invalid parameters")
		case map[string]any:
			for _, child := range value {
				pending = append(pending, child)
			}
		case []any:
			pending = append(pending, value...)
		}
	}
	var err error
	switch key {
	case "GroupRatio", "TopupGroupRatio", "group_ratio_setting.group_ratio":
		var values map[string]float64
		err = common.UnmarshalJsonStr(value, &values)
	case "GroupGroupRatio", "group_ratio_setting.group_group_ratio":
		var values map[string]map[string]float64
		err = common.UnmarshalJsonStr(value, &values)
	case "group_ratio_setting.group_special_usable_group":
		var values map[string]map[string]string
		err = common.UnmarshalJsonStr(value, &values)
	case "UserUsableGroups":
		var values map[string]string
		err = common.UnmarshalJsonStr(value, &values)
	case "UserGroups":
		_, err = ParseUserGroups(value)
	case "AutoGroups":
		var values []string
		err = common.UnmarshalJsonStr(value, &values)
	}
	if err != nil {
		return common.NewMessage("Invalid parameters")
	}
	return nil
}
