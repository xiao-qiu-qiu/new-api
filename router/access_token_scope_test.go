package router

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// accessTokenExemptRoutes are /api routes that never reach dashboard
// authentication, so a personal access token has no rule to satisfy there.
var accessTokenExemptRoutes = []string{
	// Anonymous pages and setup.
	"GET /api/setup",
	"POST /api/setup",
	"GET /api/status",
	"GET /api/uptime/status",
	"GET /api/notice",
	"GET /api/user-agreement",
	"GET /api/privacy-policy",
	"GET /api/about",
	"GET /api/home_page_content",
	"GET /api/ratio_config",
	"GET /api/user/groups",

	// Anonymous account entry points.
	"GET /api/verification",
	"GET /api/reset_password",
	"POST /api/user/reset",
	"POST /api/user/register",
	"GET /api/user/login/encryption-key",
	"POST /api/user/login",
	"POST /api/user/login/2fa",
	"POST /api/user/login/verify",
	"POST /api/user/login/passkey/begin",
	"POST /api/user/login/passkey/finish",
	"POST /api/user/passkey/login/begin",
	"POST /api/user/passkey/login/finish",
	"GET /api/oauth/wechat",
	"GET /api/oauth/telegram/login",
	"GET /api/oauth/telegram/bind/:flow_token",

	// Browser session cookie endpoints.
	"POST /api/user/auth/refresh",
	"POST /api/user/auth/logout",

	// Payment callbacks.
	"POST /api/stripe/webhook",
	"POST /api/creem/webhook",
	"POST /api/waffo/webhook",
	"POST /api/waffo-pancake/webhook/:env",
	"GET /api/user/epay/notify",
	"POST /api/user/epay/notify",
	"GET /api/subscription/epay/notify",
	"POST /api/subscription/epay/notify",
	"GET /api/subscription/epay/return",
	"POST /api/subscription/epay/return",

	// API key authentication (TokenAuthReadOnly).
	"GET /api/usage/token/",
	"GET /api/log/token",
}

// accessTokenHelperRoutes are declared through handlePermissionRoute outside
// channelPermissionRoutes.
var accessTokenHelperRoutes = map[string]authz.Permission{
	"GET /api/task_plugin_options": authz.TaskPluginBind,
	"GET /api/audit":               authz.AuditRead,
}

func newAccessTokenScopeTestEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// A permission route that is also in the static table panics here.
	require.NotPanics(t, func() {
		SetApiRouter(engine)
		SetDashboardRouter(engine)
		SetRelayRouter(engine)
		SetTaskPluginProtocolRouter(engine)
		SetVideoRouter(engine)
		SetTaskRouter(engine)
	})
	return engine
}

func TestAccessTokenRouteRulesCoverEveryDashboardRoute(t *testing.T) {
	engine := newAccessTokenScopeTestEngine(t)
	exempt := map[string]bool{}
	for _, key := range accessTokenExemptRoutes {
		exempt[key] = true
	}
	registered := map[string]bool{}
	panel := map[string]bool{}
	for _, route := range engine.Routes() {
		if !strings.HasPrefix(route.Path, "/api/") && !strings.HasPrefix(route.Path, "/pg/") {
			continue
		}
		key := route.Method + " " + route.Path
		registered[key] = true
		if !exempt[key] {
			panel[key] = true
		}
	}
	for _, key := range accessTokenExemptRoutes {
		assert.True(t, registered[key], "exempt route %s is no longer registered", key)
	}

	declared := map[string]int{}
	for _, key := range middleware.AccessTokenRouteRuleKeys() {
		declared[key]++
	}
	for key := range panel {
		assert.Equal(t, 1, declared[key], "panel route %s must be declared exactly once", key)
	}
	for key := range declared {
		assert.True(t, panel[key], "declared route %s is not a registered panel route", key)
	}

	permissionRoutes := map[string]authz.Permission{}
	for _, route := range channelPermissionRoutes {
		permissionRoutes[route.method+" "+joinPaths("/api/channel", route.path)] = route.permission
	}
	for key, permission := range accessTokenHelperRoutes {
		permissionRoutes[key] = permission
	}
	for key, permission := range permissionRoutes {
		rule, ok := middleware.AccessTokenRouteRule(key)
		require.True(t, ok, key)
		assert.Equal(t, "scope", rule.Kind(), key)
		assert.Equal(t, service.AccessTokenScopeOf(permission), rule.Scope(), key)
	}
}

func TestAdminUserRankingsRouteAuthorization(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.UserRankingPreference{}, &model.Log{}, &model.UserAccessToken{}, &model.AuditLog{}))
	previousDB, previousLogs := model.DB, model.LOG_DB
	previousRedis, previousRateLimit := common.RedisEnabled, common.GlobalApiRateLimitEnable
	previousSecret := common.SessionSecret
	model.DB, model.LOG_DB = db, db
	common.RedisEnabled, common.GlobalApiRateLimitEnable = false, false
	common.SessionSecret = "rankings-admin-route-test-secret"
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	previousNavigation, hadNavigation := common.OptionMap["HeaderNavModules"]
	common.OptionMap["HeaderNavModules"] = `{"rankings":false}`
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogs
		common.RedisEnabled, common.GlobalApiRateLimitEnable = previousRedis, previousRateLimit
		common.SessionSecret = previousSecret
		common.OptionMapRWMutex.Lock()
		if hadNavigation {
			common.OptionMap["HeaderNavModules"] = previousNavigation
		} else {
			delete(common.OptionMap, "HeaderNavModules")
		}
		common.OptionMapRWMutex.Unlock()
		service.InvalidateUserRankings()
		_ = sqlDB.Close()
	})
	service.InvalidateUserRankings()
	tokens := make(map[string]string)
	for i, fixture := range []struct {
		name, sid string
		role      int
		status    int
	}{
		{"user", "ranking-user-session", common.RoleCommonUser, common.UserStatusEnabled},
		{"admin", "ranking-admin-session", common.RoleAdminUser, common.UserStatusEnabled},
		{"root", "ranking-root-session", common.RoleRootUser, common.UserStatusEnabled},
		{"disabled", "ranking-disabled-session", common.RoleAdminUser, common.UserStatusDisabled},
	} {
		user := model.User{Id: 94000 + i, Username: fixture.name, AffCode: fixture.sid, Role: fixture.role, Status: fixture.status, AuthVersion: 1}
		require.NoError(t, db.Create(&user).Error)
		now := time.Now().Unix()
		session := model.UserSession{
			SID: fixture.sid, UserID: user.Id, Version: 1, UserAuthVersion: 1,
			Status: model.UserSessionStatusActive, RefreshHash: fixture.sid,
			LoginMethod: "password", LastActiveAt: now, ExpiresAt: now + 3600,
		}
		require.NoError(t, db.Create(&session).Error)
		token, _, err := service.IssueAccessToken(service.AuthIdentity{
			UserID: user.Id, SessionID: session.SID, UserAuthVersion: 1, SessionVersion: 1,
		})
		require.NoError(t, err)
		tokens[fixture.name] = token
	}
	engine := gin.New()
	SetApiRouter(engine)
	for _, test := range []struct {
		name, identity, query string
		wantStatus            int
	}{
		{"anonymous", "", "", http.StatusUnauthorized},
		{"ordinary user", "user", "?admin=true", http.StatusForbidden},
		{"disabled administrator", "disabled", "", http.StatusUnauthorized},
		{"administrator with public navigation disabled", "admin", "", http.StatusOK},
		{"root with public navigation disabled", "root", "?period=week&metric=spending", http.StatusOK},
		{"invalid metric", "admin", "?metric=invalid", http.StatusBadRequest},
		{"invalid period", "admin", "?period=invalid", http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/rankings/users/admin"+test.query, nil)
			if test.identity != "" {
				request.Header.Set("Authorization", "Bearer "+tokens[test.identity])
			}
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			require.Equal(t, test.wantStatus, response.Code, response.Body.String())
			assert.Contains(t, response.Header().Get("Cache-Control"), "no-store")
			if test.wantStatus != http.StatusOK {
				return
			}
			var body struct {
				Success bool                         `json:"success"`
				Data    service.UserRankingsResponse `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
			assert.True(t, body.Success)
			assert.Len(t, body.Data.Users, len(tokens))
			require.NotNil(t, body.Data.Self)
			assert.Equal(t, test.identity, body.Data.Self.Username)
		})
	}
	// The public feature switch still applies to the public endpoint.
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/rankings/users?admin=true", nil))
	assert.Equal(t, http.StatusForbidden, response.Code)

	for _, test := range []struct {
		name    string
		changes map[string]any
	}{
		{"expired session", map[string]any{"expires_at": time.Now().Unix() - 1}},
		{"revoked session", map[string]any{"status": model.UserSessionStatusRevoked}},
		{"rotated session rejects the old token", map[string]any{"version": 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, db.Model(&model.UserSession{}).Where("sid = ?", "ranking-admin-session").Updates(test.changes).Error)
			request := httptest.NewRequest(http.MethodGet, "/api/rankings/users/admin", nil)
			request.Header.Set("Authorization", "Bearer "+tokens["admin"])
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			assert.Equal(t, http.StatusUnauthorized, response.Code)
			assert.NotContains(t, response.Body.String(), `"username"`)
			require.NoError(t, db.Model(&model.UserSession{}).Where("sid = ?", "ranking-admin-session").
				Updates(map[string]any{"expires_at": time.Now().Unix() + 3600, "status": model.UserSessionStatusActive, "version": 1}).Error)
		})
	}
	for _, test := range []struct {
		name, scope string
		userID      int
		expired     bool
		wantStatus  int
	}{
		{"administrator with log scope", "log:read", 94001, false, http.StatusOK},
		{"administrator missing log scope", "profile:read", 94001, false, http.StatusForbidden},
		{"ordinary user with log scope", "log:read", 94000, false, http.StatusForbidden},
		{"expired administrator token", "log:read", 94001, true, http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := model.AccessTokenPrefix + strings.ReplaceAll(test.name, " ", "-")
			token := model.UserAccessToken{UserId: test.userID, Name: test.name, TokenHash: model.AccessTokenFingerprint(raw)}
			if test.expired {
				token.ExpiresAt = time.Now().Unix() - 1
			}
			require.NoError(t, token.SetScopes([]string{test.scope}))
			require.NoError(t, db.Create(&token).Error)
			request := httptest.NewRequest(http.MethodGet, "/api/rankings/users/admin", nil)
			request.Header.Set("Authorization", "Bearer "+raw)
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			assert.Equal(t, test.wantStatus, response.Code, response.Body.String())
			assert.Contains(t, response.Header().Get("Cache-Control"), "no-store")
			var audit model.AuditLog
			require.NoError(t, db.Where("token_ref = ?", token.TokenHash).Take(&audit).Error)
			assert.Equal(t, test.wantStatus, audit.Status)
			encoded, err := common.Marshal(audit)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), raw)
		})
	}
}

func TestAccessTokenCatalogCoversEveryPermission(t *testing.T) {
	catalog := service.AccessTokenCatalog(1, common.RoleRootUser)
	scopes := map[string]bool{}
	resources := map[string]int{}
	for _, group := range catalog {
		for _, resource := range group.Resources {
			resources[resource.Resource]++
			for _, action := range resource.Actions {
				scopes[resource.Resource+":"+action.Action] = true
			}
		}
	}
	for _, permission := range authz.AllPermissions() {
		assert.True(t, scopes[service.AccessTokenScopeOf(permission)], "root catalog lacks %s", service.AccessTokenScopeOf(permission))
	}
	for resource, count := range resources {
		assert.Equal(t, 1, count, "resource %s appears in more than one catalog entry", resource)
	}
}

func TestAccessTokenVerificationScopesAreAllListed(t *testing.T) {
	files, err := filepath.Glob("../service/*.go")
	require.NoError(t, err)
	fileSet := token.NewFileSet()
	var scopes []string
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, file, nil, 0)
		require.NoError(t, err)
		for _, declaration := range parsed.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, spec := range general.Specs {
				value := spec.(*ast.ValueSpec)
				for i, name := range value.Names {
					if !strings.HasPrefix(name.Name, "VerificationScope") || i >= len(value.Values) {
						continue
					}
					literal, ok := value.Values[i].(*ast.BasicLit)
					require.True(t, ok, name.Name)
					scope, err := strconv.Unquote(literal.Value)
					require.NoError(t, err)
					scopes = append(scopes, scope)
				}
			}
		}
	}
	require.NotEmpty(t, scopes)
	for _, scope := range scopes {
		_, listed := service.AccessTokenVerificationScope(scope)
		assert.True(t, listed, "verification scope %s has no access token rule", scope)
	}
}
