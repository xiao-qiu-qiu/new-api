package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"gorm.io/gorm"
	"maps"
)

// GetUserGroupRegistry includes legacy account references without treating every
// routing group as an account group. Reading this registry never writes options.
func GetUserGroupRegistry() (map[string]string, error) {
	return GetUserGroupRegistryWithSnapshot(setting.CaptureGroupSettings())
}

func GetUserGroupRegistryWithSnapshot(snapshot *setting.GroupSettingsSnapshot) (map[string]string, error) {
	groups := maps.Clone(snapshot.UserGroups)
	names, err := referencedUserGroups(DB)
	if err != nil {
		return nil, err
	}
	if !snapshot.Configured {
		for name := range snapshot.TopupRatios {
			names = append(names, name)
		}
		for name := range snapshot.SpecialRatios {
			names = append(names, name)
		}
		for name := range snapshot.SpecialUsableGroups {
			names = append(names, name)
		}
	}
	for _, name := range names {
		if name != "" && name != "auto" {
			if _, exists := groups[name]; !exists {
				groups[name] = snapshot.DefaultGroup(name)
			}
		}
	}
	return groups, nil
}

func referencedUserGroups(db *gorm.DB) ([]string, error) {
	names := []string{"default"}
	var accountGroups []string
	if err := db.Model(&User{}).Distinct("group").Pluck("group", &accountGroups).Error; err != nil {
		return nil, err
	}
	names = append(names, accountGroups...)
	var plans []SubscriptionPlan
	if err := db.Select("upgrade_group", "downgrade_group").Find(&plans).Error; err != nil {
		return nil, err
	}
	for _, plan := range plans {
		names = append(names, plan.UpgradeGroup, plan.DowngradeGroup)
	}
	var subscriptions []UserSubscription
	if err := db.Select("upgrade_group", "downgrade_group", "prev_user_group").Where("status = ?", "active").Find(&subscriptions).Error; err != nil {
		return nil, err
	}
	for _, subscription := range subscriptions {
		names = append(names, subscription.UpgradeGroup, subscription.DowngradeGroup, subscription.PrevUserGroup)
	}
	return names, nil
}

func ValidateUserGroup(group string) error {
	if group == "default" {
		return nil
	}
	if _, ok := setting.GetUserGroupsCopy()[group]; ok {
		return nil
	}
	groups, err := GetUserGroupRegistry()
	if err != nil {
		return err
	}
	if _, ok := groups[group]; !ok {
		return common.NewMessage("User group does not exist")
	}
	return nil
}
