package controller

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type channelAffinityManagementRequest struct {
	Action          string                              `json:"action"`
	Bindings        []service.ChannelAffinityBindingRef `json:"bindings"`
	TargetChannelID *int                                `json:"target_channel_id"`
}

func ListChannelAffinityBindings(c *gin.Context) {
	filter := service.ChannelAffinityBindingFilter{Page: 1, PageSize: 20}
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid channel affinity binding filter"))
		return
	}
	for name, values := range query {
		if len(values) != 1 {
			common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid channel affinity binding filter"))
			return
		}
		value := values[0]
		var number *int
		switch name {
		case "channel_id":
			number = &filter.ChannelID
		case "user_id":
			number = &filter.UserID
		case "token_id":
			number = &filter.TokenID
		case "page":
			number = &filter.Page
		case "page_size":
			number = &filter.PageSize
		case "model", "binding_id", "rule_name", "using_group", "key_fp":
			if value == "" || value != strings.TrimSpace(value) || len(value) > 256 || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
				common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid channel affinity binding filter"))
				return
			}
			switch name {
			case "model":
				filter.Model = value
			case "binding_id":
				if !validChannelAffinityOpaqueValue(value, 64) {
					common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid channel affinity binding reference"))
					return
				}
				filter.BindingID = value
			case "rule_name":
				filter.RuleName = value
			case "using_group":
				filter.UsingGroup = value
			case "key_fp":
				filter.KeyFingerprint = value
			}
		default:
			common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid channel affinity binding filter"))
			return
		}
		if number != nil {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 0 || strconv.Itoa(parsed) != value {
				common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid channel affinity binding filter"))
				return
			}
			*number = parsed
		}
	}
	if filter.PageSize > 200 || filter.Page > (int(^uint(0)>>1)/filter.PageSize) {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid channel affinity binding filter"))
		return
	}
	result, err := service.ListChannelAffinityBindings(filter)
	if err != nil {
		channelAffinityManagementError(c, err)
		return
	}
	common.ApiSuccess(c, result)
}

func GetChannelAffinityTargets(c *gin.Context) {
	_, refs, err := readChannelAffinityManagementRequest(c, false)
	if err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, err)
		return
	}
	items, err := service.GetChannelAffinityTargets(refs)
	if err != nil {
		channelAffinityManagementError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"items": items})
}

func MutateChannelAffinityBindings(c *gin.Context) {
	request, refs, err := readChannelAffinityManagementRequest(c, true)
	if err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, err)
		return
	}
	targetID := 0
	if request.TargetChannelID != nil {
		targetID = *request.TargetChannelID
	}
	result, err := service.MutateChannelAffinityBindings(request.Action, refs, targetID)
	status := http.StatusOK
	if err != nil {
		channelAffinityManagementError(c, err)
		status = c.Writer.Status()
	} else {
		common.ApiSuccess(c, result)
	}
	sourceIDs := make([]int, 0, len(refs))
	for _, ref := range refs {
		sourceIDs = append(sourceIDs, ref.ChannelID)
	}
	slices.Sort(sourceIDs)
	params := map[string]any{
		"action": request.Action, "source_channel_ids": slices.Compact(sourceIDs), "target_channel_id": targetID,
		"requested": len(refs), "succeeded": result.Succeeded, "expired": result.Expired,
		"conflicted": result.Conflicted, "failed": result.Failed,
	}
	if err != nil {
		params["failed"] = len(refs)
	}
	auditAction := "channel.affinity_" + request.Action
	model.RecordOperationAuditLog(c.GetInt("id"), c.GetInt("role"), auditContentEN(auditAction, params), c.ClientIP(), auditAction, params, auditOperatorInfo(c), &model.AuditRequestInfo{
		Method: c.Request.Method, Route: c.FullPath(), Path: c.FullPath(), Status: status,
		Success: err == nil && result.Failed == 0 && result.Conflicted == 0 && result.Expired == 0,
	}, c)
	markAuditLogged(c)
}

// Binding identifiers are opaque handles. Short display fingerprints and raw cache
// keys must never be accepted as mutation references; the service resolves handles.
func validChannelAffinityOpaqueValue(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, ch := range value {
		if ch >= 'a' && ch <= 'f' || ch >= '0' && ch <= '9' {
			continue
		}
		return false
	}
	return true
}

func readChannelAffinityManagementRequest(c *gin.Context, mutation bool) (channelAffinityManagementRequest, []service.ChannelAffinityBindingRef, error) {
	var request channelAffinityManagementRequest
	invalid := common.NewMessage("Invalid channel affinity request")
	if c.ContentType() != "application/json" {
		return request, nil, invalid
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10))
	if err != nil {
		return request, nil, invalid
	}
	var fields map[string]common.RawMessage
	if common.Unmarshal(body, &fields) != nil || fields == nil {
		return request, nil, invalid
	}
	for key := range fields {
		if key != "bindings" && (!mutation || key != "action" && key != "target_channel_id") {
			return request, nil, invalid
		}
	}
	if common.Unmarshal(body, &request) != nil {
		return request, nil, invalid
	}
	if mutation && request.Action != "reset" && request.Action != "migrate" {
		return request, nil, common.NewMessage("Unknown channel affinity action")
	}
	if mutation && (request.Action == "migrate" && (request.TargetChannelID == nil || *request.TargetChannelID <= 0) || request.Action == "reset" && fields["target_channel_id"] != nil) {
		return request, nil, common.NewMessage("Invalid channel affinity target")
	}
	if len(request.Bindings) == 0 || len(request.Bindings) > 200 {
		return request, nil, common.NewMessage("Select between 1 and 200 channel affinity bindings")
	}
	var rawRefs []map[string]common.RawMessage
	if common.Unmarshal(fields["bindings"], &rawRefs) != nil {
		return request, nil, invalid
	}
	seen := make(map[string]bool, len(request.Bindings))
	for i, ref := range request.Bindings {
		for key := range rawRefs[i] {
			if key != "id" && key != "version" && key != "channel_id" {
				return request, nil, invalid
			}
		}
		if !validChannelAffinityOpaqueValue(ref.ID, 64) || !validChannelAffinityOpaqueValue(ref.Version, 32) || ref.ChannelID <= 0 {
			return request, nil, common.NewMessage("Invalid channel affinity binding reference")
		}
		if seen[ref.ID] {
			return request, nil, common.NewMessage("Duplicate channel affinity binding reference")
		}
		seen[ref.ID] = true
	}
	return request, request.Bindings, nil
}

func channelAffinityManagementError(c *gin.Context, err error) {
	var message *common.Message
	if errors.As(err, &message) {
		common.ApiErrorStatus(c, http.StatusBadRequest, message)
		return
	}
	// Cache errors may contain raw Redis keys; return only the stable UI message.
	common.ApiErrorStatus(c, http.StatusInternalServerError, common.NewMessage("Channel affinity operation failed"))
}
