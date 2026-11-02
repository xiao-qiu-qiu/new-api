package controller

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelAffinityManagementRejectsInvalidInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ref := `{"id":"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789","version":"0123456789abcdef0123456789abcdef","channel_id":3}`
	tooMany := strings.TrimSuffix(strings.Repeat(ref+",", 201), ",")
	for _, tc := range []struct {
		name, path, body, contentType string
		handler                       gin.HandlerFunc
	}{
		{"empty bindings", "/targets", `{"bindings":[]}`, "application/json", GetChannelAffinityTargets},
		{"too many bindings", "/targets", `{"bindings":[` + tooMany + `]}`, "application/json", GetChannelAffinityTargets},
		{"duplicate binding", "/targets", `{"bindings":[` + ref + `,` + ref + `]}`, "application/json", GetChannelAffinityTargets},
		{"missing version", "/targets", `{"bindings":[{"id":"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789","channel_id":3}]}`, "application/json", GetChannelAffinityTargets},
		{"missing source channel", "/targets", `{"bindings":[{"id":"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789","version":"v1"}]}`, "application/json", GetChannelAffinityTargets},
		{"raw redis key", "/targets", `{"bindings":[{"id":"new-api:channel_affinity:v1:key","version":"v1","channel_id":3}]}`, "application/json", GetChannelAffinityTargets},
		{"display fingerprint", "/targets", `{"bindings":[{"id":"ab12cd34","version":"v1","channel_id":3}]}`, "application/json", GetChannelAffinityTargets},
		{"raw key field", "/targets", `{"bindings":[` + strings.TrimSuffix(ref, "}") + `,"key":"secret"}]}`, "application/json", GetChannelAffinityTargets},
		{"unknown request field", "/targets", `{"bindings":[` + ref + `],"redis_key":"secret"}`, "application/json", GetChannelAffinityTargets},
		{"targets cannot accept action", "/targets", `{"bindings":[` + ref + `],"action":"reset"}`, "application/json", GetChannelAffinityTargets},
		{"unknown action", "/actions", `{"bindings":[` + ref + `],"action":"delete"}`, "application/json", MutateChannelAffinityBindings},
		{"missing migration target", "/actions", `{"bindings":[` + ref + `],"action":"migrate"}`, "application/json", MutateChannelAffinityBindings},
		{"zero migration target", "/actions", `{"bindings":[` + ref + `],"action":"migrate","target_channel_id":0}`, "application/json", MutateChannelAffinityBindings},
		{"reset target forbidden", "/actions", `{"bindings":[` + ref + `],"action":"reset","target_channel_id":3}`, "application/json", MutateChannelAffinityBindings},
		{"null reset target forbidden", "/actions", `{"bindings":[` + ref + `],"action":"reset","target_channel_id":null}`, "application/json", MutateChannelAffinityBindings},
		{"invalid JSON", "/targets", `{"bindings":`, "application/json", GetChannelAffinityTargets},
		{"trailing JSON", "/targets", `{"bindings":[]} {}`, "application/json", GetChannelAffinityTargets},
		{"null body", "/targets", `null`, "application/json", GetChannelAffinityTargets},
		{"simple request content type", "/targets", `{"bindings":[` + ref + `]}`, "text/plain", GetChannelAffinityTargets},
		{"oversized body", "/targets", strings.Repeat(" ", 128<<10) + `{}`, "application/json", GetChannelAffinityTargets},
		{"negative filter", "/bindings?user_id=-1", "", "", ListChannelAffinityBindings},
		{"invalid filter", "/bindings?channel_id=oops", "", "", ListChannelAffinityBindings},
		{"duplicate filter", "/bindings?user_id=1&user_id=2", "", "", ListChannelAffinityBindings},
		{"empty filter", "/bindings?rule_name=", "", "", ListChannelAffinityBindings},
		{"unknown filter", "/bindings?raw_key=secret", "", "", ListChannelAffinityBindings},
		{"zero page", "/bindings?page=0", "", "", ListChannelAffinityBindings},
		{"page size cap", "/bindings?page_size=201", "", "", ListChannelAffinityBindings},
		{"page overflow", "/bindings?page=9223372036854775807&page_size=200", "", "", ListChannelAffinityBindings},
		{"fingerprint as binding filter", "/bindings?binding_id=ab12cd34", "", "", ListChannelAffinityBindings},
		{"malformed query encoding", "/bindings?page=%ZZ", "", "", ListChannelAffinityBindings},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel/affinity"+tc.path, strings.NewReader(tc.body))
			ctx.Request.Header.Set("Content-Type", tc.contentType)
			tc.handler(ctx)
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			var result struct {
				Success    bool
				MessageKey string `json:"message_key"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			assert.False(t, result.Success)
			assert.NotEmpty(t, result.MessageKey)
			assert.NotContains(t, response.Body.String(), "secret")
		})
	}
}

func TestChannelAffinityManagementAuthorizationAndAudit(t *testing.T) {
	admin, _ := setupAccessTokenAudit(t)
	user := &model.User{Username: "affinity-user", AffCode: "affinity-user", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, model.DB.Create(user).Error)
	_, sessionToken := createAccessTokenTestSession(t, admin.Id, "affinity-admin-session")
	credentials := map[string]string{"session": sessionToken}
	for _, grant := range []struct {
		name   string
		userID int
		scope  string
	}{
		{"read", admin.Id, "channel:read"},
		{"operate", admin.Id, "channel:operate"},
		{"write", admin.Id, "channel:write"},
		{"user-read", user.Id, "channel:read"},
		{"user-operate", user.Id, "channel:operate"},
	} {
		raw, _ := createScopedAccessToken(t, grant.userID, 0, grant.scope)
		credentials[grant.name] = raw
	}
	engine := gin.New()
	engine.Use(middleware.RequestId(), middleware.AccessTokenAudit())
	group := engine.Group("/api/channel/affinity", middleware.AdminAuth(), middleware.DisableCache())
	for _, route := range []struct {
		method, path string
		permission   authz.Permission
		handler      gin.HandlerFunc
	}{
		{http.MethodGet, "/bindings", authz.ChannelRead, ListChannelAffinityBindings},
		{http.MethodPost, "/targets", authz.ChannelRead, GetChannelAffinityTargets},
		{http.MethodPost, "/actions", authz.ChannelOperate, MutateChannelAffinityBindings},
	} {
		middleware.DeclareAccessTokenPermissionRoute(route.method, group.BasePath()+route.path, route.permission)
		group.Handle(route.method, route.path, middleware.RequirePermission(route.permission), route.handler)
	}
	for _, tc := range []struct {
		name, credential, method, path string
		status                         int
	}{
		{"anonymous", "", http.MethodPost, "/actions", http.StatusUnauthorized},
		{"ordinary user read", "user-read", http.MethodGet, "/bindings?page=0", http.StatusForbidden},
		{"ordinary user targets", "user-read", http.MethodPost, "/targets", http.StatusForbidden},
		{"ordinary user actions", "user-operate", http.MethodPost, "/actions", http.StatusForbidden},
		{"list read scope", "read", http.MethodGet, "/bindings?page=0", http.StatusBadRequest},
		{"list missing read scope", "operate", http.MethodGet, "/bindings?page=0", http.StatusForbidden},
		{"targets read scope", "read", http.MethodPost, "/targets", http.StatusBadRequest},
		{"targets missing read scope", "operate", http.MethodPost, "/targets", http.StatusForbidden},
		{"actions operate scope", "operate", http.MethodPost, "/actions", http.StatusBadRequest},
		{"actions read scope", "read", http.MethodPost, "/actions", http.StatusForbidden},
		{"write scope does not imply operate", "write", http.MethodPost, "/actions", http.StatusForbidden},
		{"session read", "session", http.MethodGet, "/bindings?page=0", http.StatusBadRequest},
		{"session targets", "session", http.MethodPost, "/targets", http.StatusBadRequest},
		{"session actions", "session", http.MethodPost, "/actions", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := accessTokenRequest(engine, tc.method, group.BasePath()+tc.path, credentials[tc.credential], "", `{}`)
			assert.Equal(t, tc.status, response.Code, response.Body.String())
		})
	}
	var targetOperationCount int64
	require.NoError(t, model.LOG_DB.Model(&model.AuditLog{}).Where("route = ? AND category = ?", group.BasePath()+"/targets", model.AuditCategoryOperation).Count(&targetOperationCount).Error)
	assert.Zero(t, targetOperationCount, "read-only POST target preview must not create operation audits")
	var targetPATCount int64
	require.NoError(t, model.LOG_DB.Model(&model.AuditLog{}).Where("route = ? AND category = ?", group.BasePath()+"/targets", model.AuditCategoryAccessToken).Count(&targetPATCount).Error)
	assert.Positive(t, targetPATCount, "the narrow exclusion must preserve PAT access audits")

	// A refresh cookie cannot authenticate channel writes or target previews.
	for _, path := range []string{"/actions", "/targets"} {
		request := httptest.NewRequest(http.MethodPost, group.BasePath()+path, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: sessionToken})
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		assert.Equal(t, http.StatusUnauthorized, response.Code)
	}

	// Valid but absent handles return per-binding expiry, with one structured audit
	// per operation and no handle, version or bearer secret in the audit metadata.
	refID := strings.Repeat("e", 64)
	refVersion := strings.Repeat("f", 32)
	for _, action := range []string{"reset", "migrate"} {
		body := `{"action":"` + action + `","bindings":[{"id":"` + refID + `","version":"` + refVersion + `","channel_id":3}]`
		targetID := 0
		if action == "migrate" {
			body += `,"target_channel_id":59`
			targetID = 59
		}
		body += `}`
		response := accessTokenRequest(engine, http.MethodPost, group.BasePath()+"/actions", credentials["operate"], "", body)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		assert.Contains(t, response.Body.String(), `"expired":1`)
		var entries []model.AuditLog
		require.NoError(t, model.LOG_DB.Where("action = ?", "channel.affinity_"+action).Find(&entries).Error)
		require.Len(t, entries, 1, "manual audit must suppress the AdminAuth fallback")
		entry := entries[0]
		assert.Equal(t, admin.Id, entry.UserId)
		assert.Equal(t, common.RoleAdminUser, entry.ActorRole)
		assert.False(t, entry.Success)
		require.NotNil(t, entry.Other.AdminInfo)
		assert.Equal(t, admin.Id, entry.Other.AdminInfo.AdminID)
		require.NotNil(t, entry.Other.Op)
		params, err := common.Marshal(entry.Other.Op.Params)
		require.NoError(t, err)
		assert.Contains(t, string(params), `"source_channel_ids":[3]`)
		assert.Contains(t, string(params), `"expired":1`)
		var auditParams struct {
			TargetChannelID int `json:"target_channel_id"`
		}
		require.NoError(t, common.Unmarshal(params, &auditParams))
		assert.Equal(t, targetID, auditParams.TargetChannelID)
		encoded, err := common.Marshal(entries)
		require.NoError(t, err)
		for _, secret := range []string{refID, refVersion, credentials["operate"]} {
			assert.NotContains(t, string(encoded), secret)
		}
	}

	// PAT scopes and the current owner's server-side permissions are both required.
	require.NoError(t, authz.SetUserPermissions(admin.Id, authz.PermissionsMap{authz.ResourceChannel: {authz.ActionRead: false, authz.ActionOperate: false}}))
	for _, tc := range []struct{ method, path, credential string }{
		{http.MethodGet, "/bindings?page=0", "read"},
		{http.MethodPost, "/targets", "read"},
		{http.MethodPost, "/actions", "operate"},
		{http.MethodPost, "/actions", "session"},
	} {
		response := accessTokenRequest(engine, tc.method, group.BasePath()+tc.path, credentials[tc.credential], "", `{}`)
		assert.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
	}
}

func TestChannelAffinityManagementErrorsExcludeCacheDetails(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		key    string
	}{
		{errors.New("redis new-api:channel_affinity:private-secret failed"), http.StatusInternalServerError, "Channel affinity operation failed"},
		{fmt.Errorf("redis private-secret: %w", common.NewMessage("Channel affinity binding has expired")), http.StatusBadRequest, "Channel affinity binding has expired"},
	} {
		response := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(response)
		channelAffinityManagementError(ctx, tc.err)
		assert.Equal(t, tc.status, response.Code)
		var result struct {
			Key string `json:"message_key"`
		}
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
		assert.Equal(t, tc.key, result.Key)
		assert.NotContains(t, response.Body.String(), "private-secret")
	}
}

func TestChannelHasSensitiveChanges(t *testing.T) {
	baseURL := "https://api.example.com"
	headerOverride := `{"Authorization":"Bearer {api_key}"}`
	origin := &model.Channel{
		Type:           1,
		Key:            "old-key",
		BaseURL:        &baseURL,
		HeaderOverride: &headerOverride,
		Models:         "gpt-4o",
		Group:          "default",
	}

	t.Run("non-sensitive routing fields", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		updated.Models = "gpt-4o,gpt-4o-mini"
		updated.Group = "vip"

		assert.False(t, channelHasSensitiveChanges(&updated, origin, map[string]any{
			"models": updated.Models,
			"group":  updated.Group,
		}))
	})

	t.Run("key change", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		updated.Key = "new-key"

		assert.True(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"key": updated.Key}))
	})

	t.Run("base url change", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		newBaseURL := "https://leak.example.com"
		updated.BaseURL = &newBaseURL

		assert.True(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"base_url": newBaseURL}))
	})

	t.Run("header override change", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		newHeaderOverride := `{"X-Key":"{api_key}"}`
		updated.HeaderOverride = &newHeaderOverride

		assert.True(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"header_override": newHeaderOverride}))
	})

	t.Run("omitted sensitive fields do not use zero values", func(t *testing.T) {
		updated := PatchChannel{}
		updated.Id = origin.Id
		updated.Priority = origin.Priority

		assert.False(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"priority": 10}))
	})

	t.Run("unknown field fails closed", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}

		assert.True(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"future_secret_field": "x"}))
	})

	t.Run("status is operational", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		updated.Status = common.ChannelStatusManuallyDisabled

		assert.False(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"status": updated.Status}))
	})

	t.Run("read-only fields are ignored by sensitivity check", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		updated.Balance = 99
		updated.UsedQuota = 100
		updated.ResponseTime = 200

		assert.False(t, channelHasSensitiveChanges(&updated, origin, map[string]any{
			"balance":       updated.Balance,
			"used_quota":    updated.UsedQuota,
			"response_time": updated.ResponseTime,
		}))
	})
}

func TestClearChannelReadOnlyFields(t *testing.T) {
	channel := PatchChannel{Channel: model.Channel{
		CreatedTime:        11,
		TestTime:           22,
		ResponseTime:       33,
		Balance:            44.5,
		BalanceUpdatedTime: 55,
		UsedQuota:          66,
		Models:             "gpt-4o",
		Group:              "default",
	}}

	clearChannelReadOnlyFields(&channel, map[string]any{
		"created_time":         channel.CreatedTime,
		"test_time":            channel.TestTime,
		"response_time":        channel.ResponseTime,
		"balance":              channel.Balance,
		"balance_updated_time": channel.BalanceUpdatedTime,
		"used_quota":           channel.UsedQuota,
		"models":               channel.Models,
		"group":                channel.Group,
	})

	assert.Zero(t, channel.CreatedTime)
	assert.Zero(t, channel.TestTime)
	assert.Zero(t, channel.ResponseTime)
	assert.Zero(t, channel.Balance)
	assert.Zero(t, channel.BalanceUpdatedTime)
	assert.Zero(t, channel.UsedQuota)
	assert.Equal(t, "gpt-4o", channel.Models)
	assert.Equal(t, "default", channel.Group)
}

func TestUpdateChannelRejectsStatusField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodPut,
		"/api/channel/",
		bytes.NewBufferString(`{"id":1,"status":2}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")

	UpdateChannel(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
}

func TestChannelStatusValidation(t *testing.T) {
	assert.True(t, isManageableChannelStatus(common.ChannelStatusEnabled))
	assert.True(t, isManageableChannelStatus(common.ChannelStatusManuallyDisabled))
	assert.False(t, isManageableChannelStatus(common.ChannelStatusAutoDisabled))
	assert.False(t, isManageableChannelStatus(0))
}

// TestChannelFieldsAreClassified guards the fail-closed sensitivity check: every
// JSON field of PatchChannel (including the embedded model.Channel) must be listed
// in channelSensitiveFields, channelNonSensitiveFields, or
// channelOperationalFields. A newly added field that is left unclassified will
// fail this test, forcing a conscious permission decision instead of silently
// defaulting either way.
func TestChannelFieldsAreClassified(t *testing.T) {
	classified := func(name string) bool {
		if _, ok := channelSensitiveFields[name]; ok {
			return true
		}
		if _, ok := channelNonSensitiveFields[name]; ok {
			return true
		}
		if _, ok := channelOperationalFields[name]; ok {
			return true
		}
		_, ok := channelReadOnlyFields[name]
		return ok
	}

	var collect func(rt reflect.Type) []string
	collect = func(rt reflect.Type) []string {
		var names []string
		for i := 0; i < rt.NumField(); i++ {
			field := rt.Field(i)
			if field.Anonymous && field.Type.Kind() == reflect.Struct {
				names = append(names, collect(field.Type)...)
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			names = append(names, name)
		}
		return names
	}

	for _, name := range collect(reflect.TypeFor[PatchChannel]()) {
		assert.Truef(t, classified(name),
			"channel field %q is not classified; add it to channelSensitiveFields, channelNonSensitiveFields, channelOperationalFields, or channelReadOnlyFields in channel_authz.go", name)
	}
}
