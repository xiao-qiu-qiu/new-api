package setting

import (
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

// UserGroups registers account identities. Values may be empty; non-empty
// values preserve legacy defaults for historical tasks and non-Key consumers.
// This registry does not grant access to routing groups or define their prices.
var userGroups = map[string]string{"default": ""}
var userGroupsMutex sync.RWMutex
var userGroupsConfigured bool

// ResetUserGroups starts a fresh option load. An absent persisted option must
// remain distinguishable from an explicitly saved registry.
func ResetUserGroups() {
	userGroupsMutex.Lock()
	defer userGroupsMutex.Unlock()
	userGroups = map[string]string{"default": ""}
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
	var decoded map[string]*string
	if err := common.UnmarshalJsonStr(value, &decoded); err != nil || decoded == nil {
		return nil, common.NewMessage("Invalid parameters")
	}
	groups := make(map[string]string, len(decoded))
	for name, route := range decoded {
		if name == "" || name == "auto" || name == "*" || name != strings.TrimSpace(name) || utf8.RuneCountInString(name) > 64 ||
			route == nil || *route == "auto" || *route != strings.TrimSpace(*route) {
			return nil, common.NewMessage("Invalid parameters")
		}
		groups[name] = *route
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

// GetUserDefaultRatioGroup retains historical defaults for non-Key consumers.
// Key selection uses only explicitly available routing groups.
func GetUserDefaultRatioGroup(userGroup string) string {
	return CaptureGroupSettings().DefaultGroup(userGroup)
}
