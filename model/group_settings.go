package model

import (
	"math"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
)

var groupSettingsMutex sync.Mutex

func applyGroupSettingOption(key, value string) error {
	if err := setting.ValidateGroupSettingsJSON(key, value); err != nil {
		return err
	}
	if err := updateOptionMap(key, value); err != nil {
		return err
	}
	alias := ""
	switch key {
	case "GroupRatio":
		alias = "group_ratio_setting.group_ratio"
	case "GroupGroupRatio":
		alias = "group_ratio_setting.group_group_ratio"
	}
	if alias != "" {
		common.OptionMapRWMutex.Lock()
		common.OptionMap[alias] = value
		common.OptionMapRWMutex.Unlock()
	}
	return nil
}

func IsGroupSettingsOption(key string) bool {
	switch key {
	case "UserGroups", "GroupRatio", "GroupGroupRatio", "TopupGroupRatio", "UserUsableGroups", "SpecialPricingRules",
		"group_ratio_setting.group_special_usable_group", "group_ratio_setting.group_ratio",
		"group_ratio_setting.group_group_ratio", "AutoGroups", "DefaultUseAutoGroup", "MaxTokenAutoGroups":
		return true
	}
	return false
}

// UpdateGroupSettingsOptions validates the final snapshot, then persists all
// supplied keys together. Single-key option updates use this same path.
func UpdateGroupSettingsOptions(values map[string]string) error {
	groupSettingsMutex.Lock()
	defer groupSettingsMutex.Unlock()
	for key := range values {
		if !IsGroupSettingsOption(key) {
			return common.NewMessage("Invalid parameters")
		}
	}
	// Canonicalize registered aliases so an old duplicate cannot overwrite the
	// canonical option on the next database refresh.
	updates := make(map[string]string, len(values))
	for key, value := range values {
		switch key {
		case "group_ratio_setting.group_ratio":
			key = "GroupRatio"
		case "group_ratio_setting.group_group_ratio":
			key = "GroupGroupRatio"
		}
		if old, exists := updates[key]; exists && old != value {
			return common.NewMessage("Invalid parameters")
		}
		updates[key] = value
	}
	for key, value := range updates {
		if err := setting.ValidateGroupSettingsJSON(key, value); err != nil {
			return err
		}
	}
	groups, err := GetUserGroupRegistry()
	if err != nil {
		return err
	}
	oldGroups := groups
	if value, ok := updates["UserGroups"]; ok {
		groups, err = setting.ParseUserGroups(value)
		if err != nil {
			return err
		}
	}
	specialPricing := setting.GetSpecialPricingRulesCopy()
	if value, supplied := updates["SpecialPricingRules"]; supplied {
		specialPricing, err = setting.ParseSpecialPricingRules(value)
		if err != nil {
			return err
		}
	}
	for _, rule := range specialPricing {
		if rule.UserGroup == "*" {
			continue
		}
		if _, exists := groups[rule.UserGroup]; !exists {
			if _, replacing := updates["UserGroups"]; replacing {
				return common.NewMessage("User group is still referenced")
			}
			return common.NewMessage("User group does not exist")
		}
	}
	ratios := ratio_setting.GetGroupRatioCopy()
	oldRatios := ratios
	if value, ok := updates["GroupRatio"]; ok {
		ratios = nil
		if err := common.UnmarshalJsonStr(value, &ratios); err != nil || ratios == nil {
			return common.NewMessage("Invalid parameters")
		}
		for name, ratio := range ratios {
			if name == "" || name == "auto" || name != strings.TrimSpace(name) || ratio < 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
				return common.NewMessage("Invalid parameters")
			}
		}
	}
	for name, route := range groups {
		if _, exists := ratios[route]; !exists {
			_, previouslyAvailable := oldRatios[route]
			if previouslyAvailable || oldGroups[name] != route {
				return common.NewMessage("Default ratio group does not exist")
			}
		}
	}
	removedRatios := make(map[string]bool)
	for name := range oldRatios {
		if _, exists := ratios[name]; !exists {
			removedRatios[name] = true
		}
	}
	for key, value := range updates {
		switch key {
		case "TopupGroupRatio":
			var topup map[string]float64
			if err := common.UnmarshalJsonStr(value, &topup); err != nil || topup == nil {
				return common.NewMessage("Invalid parameters")
			}
			for source, ratio := range topup {
				if _, exists := groups[source]; !exists {
					return common.NewMessage("User group does not exist")
				}
				if ratio < 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
					return common.NewMessage("Invalid parameters")
				}
			}
		case "GroupGroupRatio":
			var special map[string]map[string]float64
			if err := common.UnmarshalJsonStr(value, &special); err != nil || special == nil {
				return common.NewMessage("Invalid parameters")
			}
			old := ratio_setting.GetGroupRatioSetting().GroupGroupRatio.ReadAll()
			for source, targets := range special {
				if _, exists := groups[source]; !exists {
					return common.NewMessage("User group does not exist")
				}
				for target, ratio := range targets {
					if ratio < 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
						return common.NewMessage("Invalid parameters")
					}
					if _, exists := ratios[target]; !exists {
						previous, unchanged := old[source][target]
						if !unchanged || previous != ratio {
							return common.NewMessage("Default ratio group does not exist")
						}
					}
				}
			}
		case "UserUsableGroups":
			var usable map[string]string
			if err := common.UnmarshalJsonStr(value, &usable); err != nil || usable == nil {
				return common.NewMessage("Invalid parameters")
			}
			for target := range usable {
				if _, exists := ratios[target]; !exists && target != "auto" {
					if _, unchanged := setting.GetUserUsableGroupsCopy()[target]; !unchanged {
						return common.NewMessage("Default ratio group does not exist")
					}
				}
			}
		case "group_ratio_setting.group_special_usable_group":
			var special map[string]map[string]string
			if err := common.UnmarshalJsonStr(value, &special); err != nil || special == nil {
				return common.NewMessage("Invalid parameters")
			}
			old := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.ReadAll()
			for source, targets := range special {
				if _, exists := groups[source]; !exists {
					return common.NewMessage("User group does not exist")
				}
				for target, desc := range targets {
					name := strings.TrimPrefix(strings.TrimPrefix(target, "+:"), "-:")
					if _, exists := ratios[name]; !exists && name != "auto" {
						previous, unchanged := old[source][target]
						if !unchanged || previous != desc {
							return common.NewMessage("Default ratio group does not exist")
						}
					}
				}
			}
		case "AutoGroups":
			var auto []string
			if err := common.UnmarshalJsonStr(value, &auto); err != nil || auto == nil {
				return common.NewMessage("Invalid parameters")
			}
			for _, target := range auto {
				if _, exists := ratios[target]; !exists || target == "auto" {
					return common.NewMessage("Default ratio group does not exist")
				}
			}
		case "DefaultUseAutoGroup":
			if value != "true" && value != "false" {
				return common.NewMessage("Invalid parameters")
			}
		case "MaxTokenAutoGroups":
			if err := setting.ValidateMaxTokenAutoGroups(value); err != nil {
				return err
			}
		}
	}
	if _, replacing := updates["UserGroups"]; replacing {
		// Config references must be removed in the same request as their source
		// group. Existing account and subscription references cannot be removed.
		for _, key := range []string{"TopupGroupRatio", "GroupGroupRatio", "group_ratio_setting.group_special_usable_group"} {
			value, supplied := updates[key]
			if !supplied {
				switch key {
				case "TopupGroupRatio":
					value = common.TopupGroupRatio2JSONString()
				case "GroupGroupRatio":
					value = ratio_setting.GroupGroupRatio2JSONString()
				default:
					value = ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.MarshalJSONString()
				}
			}
			var sources map[string]any
			if err := common.UnmarshalJsonStr(value, &sources); err != nil {
				return err
			}
			for source := range sources {
				if _, exists := groups[source]; !exists {
					return common.NewMessage("User group is still referenced")
				}
			}
		}
	}
	if len(removedRatios) > 0 {
		snapshot := map[string]string{
			"UserUsableGroups": setting.UserUsableGroups2JSONString(),
			"GroupGroupRatio":  ratio_setting.GroupGroupRatio2JSONString(),
			"group_ratio_setting.group_special_usable_group": ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.MarshalJSONString(),
			"AutoGroups": setting.AutoGroups2JsonString(),
		}
		for key, value := range snapshot {
			if updated, supplied := updates[key]; supplied {
				value = updated
			}
			var targets []string
			switch key {
			case "AutoGroups":
				if err := common.UnmarshalJsonStr(value, &targets); err != nil {
					return err
				}
			case "UserUsableGroups":
				var usable map[string]string
				if err := common.UnmarshalJsonStr(value, &usable); err != nil {
					return err
				}
				for target := range usable {
					targets = append(targets, target)
				}
			default:
				var special map[string]map[string]any
				if err := common.UnmarshalJsonStr(value, &special); err != nil {
					return err
				}
				for _, groupTargets := range special {
					for target := range groupTargets {
						if key == "group_ratio_setting.group_special_usable_group" {
							target = strings.TrimPrefix(strings.TrimPrefix(target, "+:"), "-:")
						}
						targets = append(targets, target)
					}
				}
			}
			for _, target := range targets {
				if removedRatios[target] {
					return common.NewMessage("Ratio group is still referenced")
				}
			}
		}
	}
	err = DB.Transaction(func(tx *gorm.DB) error {
		if _, replacing := updates["UserGroups"]; replacing {
			references, err := referencedUserGroups(tx)
			if err != nil {
				return err
			}
			for _, name := range references {
				if name != "" && name != "auto" {
					if _, exists := groups[name]; !exists {
						return common.NewMessage("User group is still referenced")
					}
				}
			}
		}
		if len(removedRatios) > 0 {
			var channelGroups, tokenGroups []string
			if err := tx.Model(&Channel{}).Distinct("group").Pluck("group", &channelGroups).Error; err != nil {
				return err
			}
			if err := tx.Model(&Token{}).Distinct("group").Pluck("group", &tokenGroups).Error; err != nil {
				return err
			}
			for _, channelGroup := range channelGroups {
				for group := range strings.SplitSeq(channelGroup, ",") {
					if removedRatios[group] {
						return common.NewMessage("Ratio group is still referenced")
					}
				}
			}
			for _, group := range tokenGroups {
				if removedRatios[group] {
					return common.NewMessage("Ratio group is still referenced")
				}
			}
			var autoTokens []Token
			if err := tx.Select("auto_groups").Where(&Token{Group: "auto"}).Find(&autoTokens).Error; err != nil {
				return err
			}
			for _, token := range autoTokens {
				groups, err := token.GetAutoGroups()
				if err != nil {
					return err
				}
				for _, group := range groups {
					if removedRatios[group] {
						return common.NewMessage("Ratio group is still referenced")
					}
				}
			}
		}
		for key, value := range updates {
			option := Option{Key: key}
			if err := tx.FirstOrCreate(&option, Option{Key: key}).Error; err != nil {
				return err
			}
			option.Value = value
			if err := tx.Save(&option).Error; err != nil {
				return err
			}
		}
		for alias, key := range map[string]string{"group_ratio_setting.group_ratio": "GroupRatio", "group_ratio_setting.group_group_ratio": "GroupGroupRatio"} {
			if _, updated := updates[key]; updated {
				if err := tx.Where(&Option{Key: alias}).Delete(&Option{}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	setting.GroupSettingsPublicationMutex.Lock()
	defer setting.GroupSettingsPublicationMutex.Unlock()
	for key, value := range updates {
		if err := applyGroupSettingOption(key, value); err != nil {
			return err
		}
	}
	return nil
}
