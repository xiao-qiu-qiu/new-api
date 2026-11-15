package controller

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

type adminTokenResponse struct {
	ID              int      `json:"id"`
	UserID          int      `json:"user_id"`
	Key             string   `json:"key"`
	Name            string   `json:"name"`
	Status          int      `json:"status"`
	CreatedTime     int64    `json:"created_time"`
	AccessedTime    int64    `json:"accessed_time"`
	ExpiredTime     int64    `json:"expired_time"`
	Group           string   `json:"group"`
	AutoGroups      []string `json:"auto_groups"`
	CrossGroupRetry bool     `json:"cross_group_retry"`
	Username        string   `json:"username"`
	DisplayName     string   `json:"display_name"`
	UserGroup       string   `json:"user_group"`
	Editable        bool     `json:"editable"`
}

func GetAdminTokens(c *gin.Context) {
	userID := 0
	if raw := c.Query("user_id"); raw != "" {
		var err error
		userID, err = strconv.Atoi(raw)
		if err != nil || userID <= 0 {
			common.ApiErrorT(c, "Invalid parameters")
			return
		}
	}
	page := common.GetPageQuery(c)
	tokens, total, err := model.SearchAdminTokens(strings.TrimSpace(c.Query("keyword")), userID, c.Query("group"), page.GetStartIdx(), page.GetPageSize())
	if err != nil {
		common.ApiErrorT(c, "Failed to search tokens")
		return
	}
	items := make([]adminTokenResponse, 0, len(tokens))
	for _, token := range tokens {
		groups, _ := token.GetAutoGroups()
		items = append(items, adminTokenResponse{
			ID: token.Id, UserID: token.UserId, Key: token.GetMaskedKey(), Name: token.Name,
			Status: token.Status, CreatedTime: token.CreatedTime, AccessedTime: token.AccessedTime, ExpiredTime: token.ExpiredTime,
			Group: token.Group, AutoGroups: groups, CrossGroupRetry: token.CrossGroupRetry,
			Username: token.Username, DisplayName: token.DisplayName, UserGroup: token.UserGroup,
			Editable: canManageTargetRole(c.GetInt("role"), token.UserRole),
		})
	}
	page.SetTotal(int(total))
	page.SetItems(items)
	common.ApiSuccess(c, page)
}

// normalizeAdminTokenIDs bounds both query parameters and batch bodies before
// looking up owners. Duplicate IDs refer to one token and count only once.
func normalizeAdminTokenIDs(ids []int) ([]int, error) {
	if len(ids) == 0 {
		return nil, common.NewMessage("Token IDs cannot be empty")
	}
	if len(ids) > 100 {
		return nil, common.NewMessage("Too many items in batch request, maximum is {{max}}", map[string]any{"max": 100})
	}
	ids = slices.Clone(ids)
	slices.Sort(ids)
	if ids[0] <= 0 {
		return nil, common.NewMessage("Invalid parameters")
	}
	return slices.Compact(ids), nil
}

func GetAdminTokenGroups(c *gin.Context) {
	ids := make([]int, 0)
	for raw := range strings.SplitSeq(c.Query("ids"), ",") {
		id, err := strconv.Atoi(raw)
		if err != nil {
			common.ApiErrorT(c, "Invalid parameters")
			return
		}
		ids = append(ids, id)
	}
	ids, err := normalizeAdminTokenIDs(ids)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	owners, err := model.GetAdminTokenOwners(ids)
	if err != nil {
		var message *common.Message
		if errors.As(err, &message) {
			common.ApiError(c, message)
		} else {
			common.ApiErrorT(c, "Failed to search tokens")
		}
		return
	}
	snapshot := setting.CaptureGroupSettings()
	groups := make([]string, 0)
	for _, owner := range owners {
		if !canManageTargetRole(c.GetInt("role"), owner.UserRole) {
			common.ApiErrorT(c, "No permission to update users of same or higher permission level")
			return
		}
	}
	for group := range snapshot.Usable(owners[0].UserGroup) {
		allowed := true
		for _, owner := range owners {
			if !snapshot.Selectable(owner.UserGroup, group) {
				allowed = false
				break
			}
		}
		if allowed {
			groups = append(groups, group)
		}
	}
	slices.Sort(groups)
	common.ApiSuccess(c, gin.H{"groups": groups, "max_count": snapshot.MaxTokenAutoGroups})
}

func UpdateAdminTokenGroup(c *gin.Context) {
	var request struct {
		IDs             []int    `json:"ids"`
		Group           string   `json:"group"`
		AutoGroups      []string `json:"auto_groups"`
		CrossGroupRetry bool     `json:"cross_group_retry"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	params := tokenBatchAuditParams(c, request.IDs)
	ids, err := normalizeAdminTokenIDs(request.IDs)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if request.Group == "" {
		common.ApiErrorT(c, "Select at least one group for this token")
		return
	}
	token := model.Token{Group: request.Group, CrossGroupRetry: request.CrossGroupRetry}
	if token.Group != "auto" {
		request.AutoGroups = nil
		token.CrossGroupRetry = false
	}
	if err := token.SetAutoGroups(request.AutoGroups); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	snapshot := setting.CaptureGroupSettings()
	err = model.UpdateAdminTokenGroups(ids, token.Group, token.AutoGroups, token.CrossGroupRetry, func(userGroup string, role int) error {
		if !canManageTargetRole(c.GetInt("role"), role) {
			return common.NewMessage("No permission to update users of same or higher permission level")
		}
		if token.Group == "auto" {
			return service.ValidateTokenAutoGroups(snapshot, userGroup, request.AutoGroups)
		}
		if !snapshot.Selectable(userGroup, token.Group) {
			return common.NewMessage("Group {{group}} is unavailable or unauthorized", map[string]any{"group": token.Group})
		}
		return nil
	})
	if err != nil {
		var message *common.Message
		if errors.As(err, &message) {
			common.ApiError(c, message)
		} else {
			common.ApiErrorT(c, "Failed to update API key groups")
		}
		return
	}
	params["count"] = len(ids)
	params["changed_fields"] = []string{"group", "auto_groups", "cross_group_retry"}
	common.SetContextKey(c, constant.ContextKeyTokenAuditSucceeded, true)
	common.ApiSuccess(c, gin.H{"count": len(ids)})
}
