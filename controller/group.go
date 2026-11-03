package controller

import (
	"net/http"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
)

func GetGroups(c *gin.Context) {
	groupNames := make([]string, 0)
	for groupName := range setting.RequestGroupSettings(c).Ratios {
		groupNames = append(groupNames, groupName)
	}
	slices.Sort(groupNames)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    groupNames,
	})
}

func GetAdminUserGroups(c *gin.Context) {
	groups, err := model.GetUserGroupRegistry()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	slices.Sort(names)
	common.ApiSuccess(c, names)
}

func UpdateGroupSettings(c *gin.Context) {
	var request struct {
		Options map[string]string `json:"options"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || len(request.Options) == 0 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	if err := model.UpdateGroupSettingsOptions(request.Options); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

func GetUserGroups(c *gin.Context) {
	usableGroups := make(map[string]map[string]any)
	userGroup := ""
	userId := c.GetInt("id")
	userGroup, _ = model.GetUserGroup(userId, false)
	groupSettings := setting.RequestGroupSettings(c)
	userUsableGroups := groupSettings.Usable(userGroup)
	for groupName := range groupSettings.Ratios {
		// UserUsableGroups contains the groups that the user can use
		if desc, ok := userUsableGroups[groupName]; ok {
			usableGroups[groupName] = map[string]any{
				"ratio": groupSettings.GroupRatio(userGroup, groupName),
				"desc":  desc,
			}
		}
	}
	if _, ok := userUsableGroups["auto"]; ok {
		usableGroups["auto"] = map[string]any{
			"ratio": "auto",
			"desc":  userUsableGroups["auto"],
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    usableGroups,
	})
}
