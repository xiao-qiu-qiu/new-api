package setting

import (
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

// UserGroups maps account groups to their default routing/billing group.
// It deliberately does not define prices or the set of routing groups.
var userGroups = map[string]string{"default": "default"}
var userGroupsMutex sync.RWMutex
var userGroupsConfigured bool

// ResetUserGroups starts a fresh option load. An absent persisted option must
// remain distinguishable from an explicitly saved registry.
func ResetUserGroups() {
	userGroupsMutex.Lock()
	defer userGroupsMutex.Unlock()
	userGroups = map[string]string{"default": "default"}
	userGroupsConfigured = false
}

func UserGroupsConfigured() bool {
	userGroupsMutex.RLock()
	defer userGroupsMutex.RUnlock()
	return userGroupsConfigured
}

func GetUserGroupsCopy() map[string]string {
	userGroupsMutex.RLock()
	defer userGroupsMutex.RUnlock()
	groups := make(map[string]string, len(userGroups))
	for name, route := range userGroups {
		groups[name] = route
	}
	return groups
}

func UserGroups2JSONString() string {
	data, _ := common.Marshal(GetUserGroupsCopy())
	return string(data)
}

func ParseUserGroups(value string) (map[string]string, error) {
	var groups map[string]string
	if err := common.UnmarshalJsonStr(value, &groups); err != nil || groups == nil {
		return nil, common.NewMessage("User groups must map user group names to default ratio groups")
	}
	for name, route := range groups {
		if name == "" || name == "auto" || name != strings.TrimSpace(name) || utf8.RuneCountInString(name) > 64 ||
			route == "" || route == "auto" || route != strings.TrimSpace(route) {
			return nil, common.NewMessage("User groups must map user group names to default ratio groups")
		}
	}
	return groups, nil
}

func UpdateUserGroupsByJSONString(value string) error {
	groups, err := ParseUserGroups(value)
	if err != nil {
		return err
	}
	userGroupsMutex.Lock()
	defer userGroupsMutex.Unlock()
	userGroups = groups
	userGroupsConfigured = true
	return nil
}

// GetUserDefaultRatioGroup preserves legacy same-name routing until explicitly
// overridden. Account-only groups inherit the ordinary default route.
func GetUserDefaultRatioGroup(userGroup string) string {
	return CaptureGroupSettings().DefaultGroup(userGroup)
}
