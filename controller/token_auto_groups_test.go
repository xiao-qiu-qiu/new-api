package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func configureTokenAutoGroupsTest(t *testing.T, maxCount string, autoGroups string) {
	t.Helper()
	originalMax := setting.GetMaxTokenAutoGroups()
	originalAutoGroups := setting.AutoGroups2JsonString()
	originalUsableGroups := setting.UserUsableGroups2JSONString()
	originalRatios := ratio_setting.GroupRatio2JSONString()
	specialUsable := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup
	originalSpecialUsable := specialUsable.MarshalJSONString()
	require.NoError(t, setting.UpdateMaxTokenAutoGroups(maxCount))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(autoGroups))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":1,"blocked":1}`))
	require.NoError(t, specialUsable.UnmarshalJSON([]byte(`{}`)))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateMaxTokenAutoGroups(stringInt(originalMax)))
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalRatios))
		require.NoError(t, specialUsable.UnmarshalJSON([]byte(originalSpecialUsable)))
	})
}

func stringInt(value int) string {
	return fmt.Sprintf("%d", value)
}

func setupTokenAutoGroupsControllerTest(t *testing.T) *model.User {
	t.Helper()
	originalDB, originalLogDB := model.DB, model.LOG_DB
	originalRedis := common.RedisEnabled
	originalMainType, originalLogType := common.MainDatabaseType(), common.LogDatabaseType()
	originalMaster := common.IsMasterNode
	t.Cleanup(func() {
		model.DB, model.LOG_DB, common.RedisEnabled = originalDB, originalLogDB, originalRedis
		common.SetDatabaseTypes(originalMainType, originalLogType)
		common.IsMasterNode = originalMaster
	})
	db := setupTokenControllerTestDB(t)
	t.Setenv("LOG_SQL_DSN", "")
	common.IsMasterNode = false
	require.NoError(t, model.InitLogDB())
	common.IsMasterNode = originalMaster
	require.NoError(t, db.AutoMigrate(&model.User{}))
	user := &model.User{
		Id:       101,
		Username: "token-auto-user",
		Password: "password",
		Group:    "default",
		Status:   common.UserStatusEnabled,
	}
	require.NoError(t, db.Create(user).Error)
	return user
}

func baseAutoTokenRequest(name string) map[string]any {
	return map[string]any{
		"name":              name,
		"expired_time":      -1,
		"remain_quota":      0,
		"unlimited_quota":   true,
		"group":             "auto",
		"cross_group_retry": true,
	}
}

func newTokenAutoGroupsAuthenticatedContext(t *testing.T, method string, target string, body any, userID int) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	ctx, recorder := newAuthenticatedContext(t, method, target, body, userID)
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	return ctx, recorder
}

func TestAddTokenRejectsEmptyAutoGroupsWithoutGlobalInheritance(t *testing.T) {
	tests := []struct {
		name         string
		includeField bool
		value        any
	}{
		{name: "omitted"},
		{name: "null", includeField: true, value: nil},
		{name: "empty array", includeField: true, value: []string{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configureTokenAutoGroupsTest(t, "5", `["default","vip"]`)
			user := setupTokenAutoGroupsControllerTest(t)
			request := baseAutoTokenRequest("create-" + test.name)
			if test.includeField {
				request["auto_groups"] = test.value
			}

			ctx, recorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodPost, "/api/token/", request, user.Id)
			AddToken(ctx)

			response := decodeAPIResponse(t, recorder)
			assert.False(t, response.Success)
			assert.Equal(t, "Select at least one group for this token", response.Message)
			var count int64
			require.NoError(t, model.DB.Model(&model.Token{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestAddTokenPersistsOrderedAutoGroupsSnapshot(t *testing.T) {
	configureTokenAutoGroupsTest(t, "5", `[]`)
	user := setupTokenAutoGroupsControllerTest(t)
	request := baseAutoTokenRequest("ordered-snapshot")
	request["auto_groups"] = []string{"vip", "default"}

	ctx, recorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodPost, "/api/token/", request, user.Id)
	AddToken(ctx)
	require.True(t, decodeAPIResponse(t, recorder).Success)

	var token model.Token
	require.NoError(t, model.DB.Where("name = ?", "ordered-snapshot").First(&token).Error)
	assert.JSONEq(t, `["vip","default"]`, token.AutoGroups)
	assert.True(t, token.CrossGroupRetry)

	getCtx, getRecorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodGet, "/api/token/"+stringInt(token.Id), nil, user.Id)
	getCtx.Params = append(getCtx.Params, gin.Param{Key: "id", Value: stringInt(token.Id)})
	GetToken(getCtx)
	getResponse := decodeAPIResponse(t, getRecorder)
	require.True(t, getResponse.Success)
	var data struct {
		AutoGroups []string `json:"auto_groups"`
	}
	require.NoError(t, common.Unmarshal(getResponse.Data, &data))
	assert.Equal(t, []string{"vip", "default"}, data.AutoGroups)
}

func TestUpdateTokenAutoGroupsTriStateAndNonAutoCleanup(t *testing.T) {
	tests := []struct {
		name               string
		includeField       bool
		value              any
		group              string
		expectedAutoGroups string
		expectedRetry      bool
		rejected           bool
	}{
		{name: "omitted preserves", group: "auto", expectedAutoGroups: `["vip","default"]`, expectedRetry: true},
		{name: "explicit reordered selection", includeField: true, value: []string{"default", "vip"}, group: "auto", expectedAutoGroups: `["default","vip"]`, expectedRetry: true},
		{name: "null rejected", includeField: true, value: nil, group: "auto", rejected: true},
		{name: "empty rejected", includeField: true, value: []string{}, group: "auto", rejected: true},
		{name: "unknown rejected", includeField: true, value: []string{"default", "missing"}, group: "auto", rejected: true},
		{name: "non auto clears and disables retry", includeField: true, value: []string{"vip"}, group: "default"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configureTokenAutoGroupsTest(t, "5", `["default","vip"]`)
			user := setupTokenAutoGroupsControllerTest(t)
			token := seedToken(t, model.DB, user.Id, "update-auto", "update-auto-key")
			token.Group = "auto"
			token.CrossGroupRetry = true
			require.NoError(t, token.SetAutoGroups([]string{"vip", "default"}))
			require.NoError(t, model.DB.Save(token).Error)

			request := baseAutoTokenRequest("updated-auto")
			request["id"] = token.Id
			request["status"] = common.TokenStatusEnabled
			request["group"] = test.group
			if test.includeField {
				request["auto_groups"] = test.value
			}
			ctx, recorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodPut, "/api/token/", request, user.Id)
			UpdateToken(ctx)
			response := decodeAPIResponse(t, recorder)
			assert.Equal(t, !test.rejected, response.Success, response.Message)

			var updated model.Token
			require.NoError(t, model.DB.First(&updated, token.Id).Error)
			if test.rejected {
				assert.Equal(t, token.Group, updated.Group)
				assert.Equal(t, token.AutoGroups, updated.AutoGroups)
				assert.Equal(t, token.Name, updated.Name)
				return
			}
			if test.expectedAutoGroups == "" {
				assert.Empty(t, updated.AutoGroups)
			} else {
				assert.JSONEq(t, test.expectedAutoGroups, updated.AutoGroups)
			}
			assert.Equal(t, test.expectedRetry, updated.CrossGroupRetry)
		})
	}
}

func TestAddTokenRejectsInvalidAutoGroups(t *testing.T) {
	tests := []struct {
		name     string
		maxCount string
		groups   []string
	}{
		{name: "over limit", maxCount: "1", groups: []string{"default", "vip"}},
		{name: "duplicate", maxCount: "5", groups: []string{"default", "default"}},
		{name: "auto pseudo group", maxCount: "5", groups: []string{"auto"}},
		{name: "unavailable", maxCount: "5", groups: []string{"missing"}},
		{name: "unauthorized", maxCount: "5", groups: []string{"default", "blocked"}},
		{name: "empty group", maxCount: "5", groups: []string{"default", ""}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configureTokenAutoGroupsTest(t, test.maxCount, `["default","vip"]`)
			user := setupTokenAutoGroupsControllerTest(t)
			request := baseAutoTokenRequest("invalid-" + test.name)
			request["auto_groups"] = test.groups

			ctx, recorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodPost, "/api/token/", request, user.Id)
			AddToken(ctx)

			response := decodeAPIResponse(t, recorder)
			assert.False(t, response.Success)
			var count int64
			require.NoError(t, model.DB.Model(&model.Token{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestGetTokenAutoGroupsReturnsAllSelectableGroupsWithoutGlobalAuto(t *testing.T) {
	configureTokenAutoGroupsTest(t, "1", `[]`)
	user := setupTokenAutoGroupsControllerTest(t)

	ctx, recorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodGet, "/api/token/auto-groups", nil, user.Id)
	GetTokenAutoGroups(ctx)

	response := decodeAPIResponse(t, recorder)
	require.True(t, response.Success, response.Message)
	var data struct {
		Groups   []string `json:"groups"`
		MaxCount int      `json:"max_count"`
	}
	require.NoError(t, common.Unmarshal(response.Data, &data))
	assert.Equal(t, []string{"default", "vip"}, data.Groups)
	assert.Equal(t, 1, data.MaxCount)
}

func TestTokenRequiresExplicitAuthorizedSingleGroup(t *testing.T) {
	for _, operation := range []string{"create", "update"} {
		for _, group := range []string{"", " ", "missing", "blocked", "vip"} {
			t.Run(operation+"/group="+group, func(t *testing.T) {
				configureTokenAutoGroupsTest(t, "5", `["default"]`)
				user := setupTokenAutoGroupsControllerTest(t)
				request := baseAutoTokenRequest("single-group")
				request["group"] = group
				request["auto_groups"] = []string{"default", "vip"}
				var tokenID int
				if operation == "update" {
					tokenID = seedToken(t, model.DB, user.Id, "original-single", "singlekey").Id
					request["id"] = tokenID
				}
				ctx, recorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodPost, "/api/token/", request, user.Id)
				if operation == "create" {
					AddToken(ctx)
				} else {
					UpdateToken(ctx)
				}
				response := decodeAPIResponse(t, recorder)
				assert.Equal(t, group == "vip", response.Success, response.Message)
				var tokens []model.Token
				require.NoError(t, model.DB.Find(&tokens).Error)
				if group == "vip" {
					require.Len(t, tokens, 1)
					assert.Equal(t, "vip", tokens[0].Group)
					assert.Empty(t, tokens[0].AutoGroups)
					assert.False(t, tokens[0].CrossGroupRetry)
				} else if operation == "create" {
					assert.Empty(t, tokens)
				} else {
					require.Len(t, tokens, 1)
					assert.Equal(t, "default", tokens[0].Group)
					assert.Equal(t, "original-single", tokens[0].Name)
				}
			})
		}
	}
}

func TestTokenStatusOnlyUpdatePreservesGroupSelection(t *testing.T) {
	for _, group := range []string{"", "vip", "auto"} {
		t.Run("group="+group, func(t *testing.T) {
			configureTokenAutoGroupsTest(t, "5", `[]`)
			user := setupTokenAutoGroupsControllerTest(t)
			token := seedToken(t, model.DB, user.Id, "status-only", "statuskey")
			token.Group = group
			if group == "auto" {
				token.CrossGroupRetry = true
				require.NoError(t, token.SetAutoGroups([]string{"vip", "default"}))
			}
			require.NoError(t, model.DB.Save(token).Error)
			ctx, recorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodPut, "/api/token/?status_only=true", map[string]any{
				"id": token.Id, "status": common.TokenStatusDisabled,
			}, user.Id)
			UpdateToken(ctx)
			require.True(t, decodeAPIResponse(t, recorder).Success)
			var updated model.Token
			require.NoError(t, model.DB.First(&updated, token.Id).Error)
			assert.Equal(t, common.TokenStatusDisabled, updated.Status)
			assert.Equal(t, token.Group, updated.Group)
			assert.Equal(t, token.AutoGroups, updated.AutoGroups)
			assert.Equal(t, token.CrossGroupRetry, updated.CrossGroupRetry)
		})
	}
}

func tokenGroupsRuntimeRequest(t *testing.T, token *model.Token) *httptest.ResponseRecorder {
	t.Helper()
	require.NoError(t, i18n.Init())
	router := gin.New()
	router.Use(middleware.TokenAuth())
	router.GET("/v1/models", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"group":  common.GetContextKeyString(c, constant.ContextKeyUsingGroup),
			"groups": service.GetRequestAutoGroups(c, common.GetContextKeyString(c, constant.ContextKeyUserGroup)),
		})
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer sk-"+token.Key)
	request.Header.Set("Accept-Language", "en")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	assert.NotContains(t, recorder.Body.String(), token.Key)
	return recorder
}

func TestTokenAuthRequiresExplicitValidGroupsWithoutFallback(t *testing.T) {
	for _, test := range []struct {
		name, group, groups string
		allowed             bool
	}{
		{name: "legacy empty group"},
		{name: "existing single group", group: "vip", allowed: true},
		{name: "single group removed", group: "missing"},
		{name: "single group unauthorized", group: "blocked"},
		{name: "ordered groups without auto permission", group: "auto", groups: `["vip","default"]`, allowed: true},
		{name: "missing auto selection", group: "auto"},
		{name: "empty array", group: "auto", groups: `[]`},
		{name: "null selection", group: "auto", groups: `null`},
		{name: "malformed selection", group: "auto", groups: `not-json`},
		{name: "duplicate group", group: "auto", groups: `["vip","vip"]`},
		{name: "empty group", group: "auto", groups: `["default",""]`},
		{name: "pseudo group", group: "auto", groups: `["auto"]`},
		{name: "partially unauthorized", group: "auto", groups: `["default","blocked"]`},
		{name: "all groups invalid", group: "auto", groups: `["missing","blocked"]`},
		{name: "current limit exceeded", group: "auto", groups: `["vip","default","blocked"]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			configureTokenAutoGroupsTest(t, "2", `["default"]`)
			user := setupTokenAutoGroupsControllerTest(t)
			token := seedToken(t, model.DB, user.Id, "runtime-selection", "runtimekey")
			token.Group, token.AutoGroups = test.group, test.groups
			require.NoError(t, model.DB.Save(token).Error)
			recorder := tokenGroupsRuntimeRequest(t, token)
			if !test.allowed {
				assert.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
				if test.group == "" {
					assert.Contains(t, recorder.Body.String(), "select its groups")
				}
				return
			}
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			var result struct {
				Group  string   `json:"group"`
				Groups []string `json:"groups"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
			assert.Equal(t, test.group, result.Group)
			if test.group == "auto" {
				assert.Equal(t, []string{"vip", "default"}, result.Groups)
			}
		})
	}
}

func TestTokenGroupsRevalidatePermissionsOnEachRequestAndUpdate(t *testing.T) {
	for _, group := range []string{"vip", "auto"} {
		for _, revoke := range []string{"one permission", "all permissions", "ratio removed", "identity changed"} {
			t.Run(group+"/"+revoke, func(t *testing.T) {
				configureTokenAutoGroupsTest(t, "5", `["default"]`)
				user := setupTokenAutoGroupsControllerTest(t)
				token := seedToken(t, model.DB, user.Id, "revoke-selection", "revokekey")
				token.Group = group
				if group == "auto" {
					require.NoError(t, token.SetAutoGroups([]string{"vip", "default"}))
				}
				require.NoError(t, model.DB.Save(token).Error)
				require.Equal(t, http.StatusOK, tokenGroupsRuntimeRequest(t, token).Code)
				switch revoke {
				case "one permission":
					require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default"}`))
				case "all permissions":
					require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"blocked":"Blocked"}`))
					require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"blocked":1}`))
				case "ratio removed":
					require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
				case "identity changed":
					require.NoError(t, ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.UnmarshalJSON([]byte(`{"limited":{"-:vip":""}}`)))
					require.NoError(t, model.DB.Model(user).Update("group", "limited").Error)
				}
				assert.Equal(t, http.StatusForbidden, tokenGroupsRuntimeRequest(t, token).Code)
				request := baseAutoTokenRequest("revoked-update")
				request["id"], request["group"] = token.Id, group
				ctx, recorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodPut, "/api/token/", request, user.Id)
				if revoke == "identity changed" {
					common.SetContextKey(ctx, constant.ContextKeyUserGroup, "limited")
				}
				UpdateToken(ctx)
				assert.False(t, decodeAPIResponse(t, recorder).Success, "omitting auto_groups must still revalidate the preserved selection")
				var updated model.Token
				require.NoError(t, model.DB.First(&updated, token.Id).Error)
				assert.Equal(t, token.Group, updated.Group)
				assert.Equal(t, token.AutoGroups, updated.AutoGroups)
			})
		}
	}
}

func TestTokenOrderedGroupsSkipUnsupportedModelBeforeRetry(t *testing.T) {
	for _, firstMatches := range []bool{false, true} {
		t.Run(fmt.Sprintf("first_matches=%t", firstMatches), func(t *testing.T) {
			configureTokenAutoGroupsTest(t, "5", `[]`)
			user := setupTokenAutoGroupsControllerTest(t)
			require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Ability{}))
			previousMemoryCache, previousRetries := common.MemoryCacheEnabled, common.RetryTimes
			common.MemoryCacheEnabled, common.RetryTimes = false, 0
			t.Cleanup(func() { common.MemoryCacheEnabled, common.RetryTimes = previousMemoryCache, previousRetries })
			const requestedModel = "ordered-model"
			for i, group := range []string{"vip", "default"} {
				modelName := requestedModel
				if i == 0 && !firstMatches {
					modelName = "other-model"
				}
				priority, weight := int64(0), uint(100)
				require.NoError(t, model.DB.Create(&model.Channel{
					Id: i + 1, Type: constant.ChannelTypeOpenAI, Name: group, Key: "fixture", Status: common.ChannelStatusEnabled,
					Group: group, Models: modelName, Priority: &priority, Weight: &weight,
				}).Error)
				require.NoError(t, model.DB.Create(&model.Ability{
					Group: group, Model: modelName, ChannelId: i + 1, Enabled: true, Priority: &priority, Weight: weight,
				}).Error)
			}
			token := seedToken(t, model.DB, user.Id, "model-routing", "modelroutingkey")
			token.Group, token.CrossGroupRetry = "auto", false
			require.NoError(t, token.SetAutoGroups([]string{"vip", "default"}))
			require.NoError(t, model.DB.Save(token).Error)
			require.NoError(t, i18n.Init())
			router := gin.New()
			router.GET("/v1/models", middleware.TokenAuth(), func(c *gin.Context) {
				defer service.ReleaseChannelConcurrency(c)
				channel, selectedGroup, err := service.CacheGetRandomSatisfiedChannel(&service.RetryParam{
					Ctx: c, TokenGroup: "auto", ModelName: requestedModel, Retry: common.GetPointer(0),
				})
				require.NoError(t, err)
				require.NotNil(t, channel)
				if firstMatches {
					assert.Equal(t, "vip", selectedGroup)
				} else {
					assert.Equal(t, "default", selectedGroup, "model matching skips the unsupported first group with cross-group retry disabled")
				}
				c.Status(http.StatusOK)
			})
			request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			request.Header.Set("Authorization", "Bearer sk-"+token.Key)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		})
	}
}

func TestPlaygroundAutoBindsExplicitSessionSnapshot(t *testing.T) {
	configureTokenAutoGroupsTest(t, "1", `["vip","default"]`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/pg/models?group=auto", nil)
	ctx.Set("id", 101)
	ctx.Set("session_id", "fixture-session")
	ctx.Set("auth_version", int64(1))
	ctx.Set("session_version", int64(1))
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP","auto":"Auto"}`))
	middleware.PreparePlaygroundRelay()(ctx)
	require.False(t, ctx.IsAborted(), recorder.Body.String())
	assert.Equal(t, []string{"vip", "default"}, service.GetRequestAutoGroups(ctx, "default"))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`[]`))
	assert.Equal(t, []string{"vip", "default"}, service.GetRequestAutoGroups(ctx, "default"), "the session keeps the captured generation without the per-key limit")
}
