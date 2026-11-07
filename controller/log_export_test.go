package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/active_requests"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// callLogHandler runs a log handler as the given user and returns the JSON
// lines of the response.
func callLogHandler(t *testing.T, handler gin.HandlerFunc, userId int, role int, query string) []map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/log?"+query, nil)
	ctx.Set("id", userId)
	ctx.Set("role", role)
	handler(ctx)
	var lines []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(recorder.Body.String()), "\n") {
		var value map[string]any
		require.NoError(t, common.UnmarshalJsonStr(line, &value))
		lines = append(lines, value)
	}
	return lines
}

func exportedRequestIds(lines []map[string]any) []any {
	ids := make([]any, 0, len(lines))
	for _, line := range lines[:len(lines)-1] {
		ids = append(ids, line["request_id"])
	}
	return ids
}

// seedExportLogs stores logs of two users: three in the last hour and one
// from 400 days ago.
func seedExportLogs(t *testing.T, mainDB *gorm.DB, logDB *gorm.DB, now int64) {
	t.Helper()
	require.NoError(t, mainDB.Create(&model.Channel{Id: 9, Name: "primary"}).Error)
	logs := []model.Log{
		{UserId: 1, Username: "alice", CreatedAt: now - 3600, Type: model.LogTypeConsume, ModelName: "gpt-a", ChannelId: 9, Quota: 10, RequestId: "a-1", Other: `{"admin_info":{"use_channel":["9"]}}`},
		{UserId: 2, Username: "bob", CreatedAt: now - 1800, Type: model.LogTypeConsume, ModelName: "gpt-b", ChannelId: 9, Quota: 20, RequestId: "b-1"},
		{UserId: 1, Username: "alice", CreatedAt: now - 60, Type: model.LogTypeError, ModelName: "gpt-a", RequestId: "a-2"},
		{UserId: 1, Username: "alice", CreatedAt: now - 400*24*3600, Type: model.LogTypeConsume, ModelName: "gpt-a", RequestId: "a-old"},
	}
	for i := range logs {
		require.NoError(t, logDB.Create(&logs[i]).Error)
	}
}

func useLogExportDatabases(t *testing.T, mainDB *gorm.DB, logDB *gorm.DB, mainType common.DatabaseType, logType common.DatabaseType) {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis, previousMemory := common.RedisEnabled, common.MemoryCacheEnabled
	model.DB, model.LOG_DB = mainDB, logDB
	common.SetDatabaseTypes(mainType, logType)
	common.RedisEnabled, common.MemoryCacheEnabled = false, false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled, common.MemoryCacheEnabled = previousRedis, previousMemory
	})
}

func TestLogExportDatabaseMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousMaster := common.IsMasterNode
	common.IsMasterNode = true
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	for _, tc := range []struct {
		kind, env string
		typ       common.DatabaseType
	}{
		{"sqlite", "", common.DatabaseTypeSQLite},
		{"mysql", "AUDIT_MYSQL_DSN", common.DatabaseTypeMySQL},
		{"postgres", "AUDIT_POSTGRES_DSN", common.DatabaseTypePostgreSQL},
		{"clickhouse", "AUDIT_CLICKHOUSE_DSN", common.DatabaseTypeClickHouse},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if tc.env != "" && dsn == "" {
				t.Skip(tc.env + " is not configured")
			}
			db, isolatedDSN := newAuditTestDatabase(t, tc.kind, dsn)
			mainDB, mainType := db, tc.typ
			if tc.kind == "clickhouse" {
				mainDB, _ = newAuditTestDatabase(t, "sqlite", "")
				mainType = common.DatabaseTypeSQLite
			}
			useLogExportDatabases(t, mainDB, db, mainType, tc.typ)
			require.NoError(t, mainDB.AutoMigrate(&model.Channel{}))
			if tc.kind == "clickhouse" {
				t.Setenv("LOG_SQL_DSN", isolatedDSN)
				require.NoError(t, model.InitLogDB())
			} else {
				require.NoError(t, db.AutoMigrate(&model.Log{}))
			}
			versionSQL := "SELECT version()"
			if tc.kind == "sqlite" {
				versionSQL = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, model.LOG_DB.Raw(versionSQL).Scan(&version).Error)
			t.Logf("log database: %s %s", tc.kind, version)
			now := time.Now().Unix()
			seedExportLogs(t, mainDB, model.LOG_DB, now)
			query := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-2*24*3600, now+3600)

			listed := callLogHandler(t, GetUserLogs, 1, common.RoleCommonUser, query)[0]
			require.Equal(t, true, listed["success"])
			assert.EqualValues(t, 2, listed["data"].(map[string]any)["total"])
			listed = callLogHandler(t, GetAllLogs, 3, common.RoleAdminUser, query+"&username=alice")[0]
			require.Equal(t, true, listed["success"])
			assert.EqualValues(t, 2, listed["data"].(map[string]any)["total"])

			own := callLogHandler(t, ExportUserLogs, 1, common.RoleCommonUser, query)
			require.Len(t, own, 3)
			assert.Equal(t, []any{"a-2", "a-1"}, exportedRequestIds(own))
			assert.Equal(t, map[string]any{"success": true, "message": "", "data": map[string]any{"count": float64(2)}}, own[2])
			assert.Equal(t, float64(1), own[0]["id"], "user exports carry list positions, not row ids")
			assert.Empty(t, own[1]["channel_name"])
			assert.NotContains(t, own[1]["other"], "admin_info")

			all := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, query+"&model_name=gpt-%25")
			require.Len(t, all, 4)
			assert.Equal(t, []any{"a-2", "b-1", "a-1"}, exportedRequestIds(all))
			assert.Equal(t, "primary", all[2]["channel_name"])
			assert.Contains(t, all[2]["other"], "admin_info")
			assert.Equal(t, true, all[3]["success"])
		})
	}
}

func TestLogExportRules(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := newAuditTestDatabase(t, "sqlite", "")
	useLogExportDatabases(t, db, db, common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Log{}, &model.QuotaData{}))
	now := time.Now().Unix()
	seedExportLogs(t, db, db, now)
	logSetting := operation_setting.GetLogSetting()
	previousSetting := *logSetting
	t.Cleanup(func() { *logSetting = previousSetting })
	recent := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-2*24*3600, now)
	tooLong := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-366*24*3600, now)

	t.Run("time range", func(t *testing.T) {
		rejected := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, tooLong)
		require.Len(t, rejected, 1)
		assert.Equal(t, false, rejected[0]["success"])
		assert.Equal(t, "The time range cannot exceed {{days}} days", rejected[0]["message_key"])
		assert.Equal(t, map[string]any{"days": float64(365)}, rejected[0]["message_params"])

		assert.Equal(t, false, callLogHandler(t, GetUserLogs, 1, common.RoleCommonUser, tooLong)[0]["success"])
		assert.Equal(t, false, callLogHandler(t, GetLogsSelfStat, 1, common.RoleCommonUser, tooLong)[0]["success"])
		assert.Equal(t, true, callLogHandler(t, GetUserLogs, 3, common.RoleAdminUser, tooLong)[0]["success"], "administrators search their own logs without a limit")

		// The dashboard follows the same range instead of its old one-month limit.
		fortyDays := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-40*24*3600, now)
		for _, handler := range []gin.HandlerFunc{GetUserQuotaDates, GetUserFlowQuotaDates} {
			assert.Equal(t, "The time range cannot exceed {{days}} days", callLogHandler(t, handler, 1, common.RoleCommonUser, tooLong)[0]["message_key"])
			assert.Equal(t, true, callLogHandler(t, handler, 1, common.RoleCommonUser, fortyDays)[0]["success"])
		}

		// Without a start, a user's search reaches back the whole range.
		listed := callLogHandler(t, GetUserLogs, 1, common.RoleCommonUser, "")[0]
		assert.Equal(t, true, listed["success"])
		assert.EqualValues(t, 2, listed["data"].(map[string]any)["total"])

		logSetting.MaxRangeDays = 400
		assert.Equal(t, true, callLogHandler(t, GetUserLogs, 1, common.RoleCommonUser, tooLong)[0]["success"])
		logSetting.MaxRangeDays = previousSetting.MaxRangeDays
	})

	t.Run("user export switch", func(t *testing.T) {
		logSetting.UserExportEnabled = false
		t.Cleanup(func() { logSetting.UserExportEnabled = previousSetting.UserExportEnabled })
		disabled := callLogHandler(t, ExportUserLogs, 1, common.RoleCommonUser, recent)
		require.Len(t, disabled, 1)
		assert.Equal(t, "Log export is disabled", disabled[0]["message_key"])
		assert.Equal(t, true, callLogHandler(t, ExportUserLogs, 3, common.RoleAdminUser, recent)[0]["success"], "administrators export their own logs even when users cannot")
	})

	t.Run("one export per user", func(t *testing.T) {
		release, ok, err := service.AcquireLogExportLock(1)
		require.NoError(t, err)
		require.True(t, ok)
		busy := callLogHandler(t, ExportUserLogs, 1, common.RoleCommonUser, recent)
		require.Len(t, busy, 1)
		assert.Equal(t, "Another log export is running. Try again after it finishes.", busy[0]["message_key"])
		other := callLogHandler(t, ExportUserLogs, 2, common.RoleCommonUser, recent)
		assert.Equal(t, true, other[len(other)-1]["success"], "other users are not blocked")
		release()
		assert.Len(t, callLogHandler(t, ExportUserLogs, 1, common.RoleCommonUser, recent), 3)
	})
}

func TestAcquireLogExportLock(t *testing.T) {
	for _, useRedis := range []bool{false, true} {
		t.Run(fmt.Sprintf("redis=%v", useRedis), func(t *testing.T) {
			previousRedis, previousClient := common.RedisEnabled, common.RDB
			common.RedisEnabled = useRedis
			if useRedis {
				server := miniredis.RunT(t)
				common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
				require.NoError(t, common.RDB.Ping(context.Background()).Err())
			}
			t.Cleanup(func() { common.RedisEnabled, common.RDB = previousRedis, previousClient })

			release, ok, err := service.AcquireLogExportLock(1)
			require.NoError(t, err)
			require.True(t, ok)
			_, ok, err = service.AcquireLogExportLock(1)
			require.NoError(t, err)
			assert.False(t, ok, "a second export of the same user must wait")
			releaseOther, ok, err := service.AcquireLogExportLock(2)
			require.NoError(t, err)
			assert.True(t, ok)
			releaseOther()
			release()
			releaseAgain, ok, err := service.AcquireLogExportLock(1)
			require.NoError(t, err)
			assert.True(t, ok, "a finished export frees the user")
			releaseAgain()
		})
	}
}

func TestActiveRequestsVisibilityAndFilters(t *testing.T) {
	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedis })
	now := time.Now().UnixMilli()
	for _, record := range []active_requests.Record{
		{RequestID: "active-alice", UserID: 1, Username: "alice", TokenID: 11, TokenName: "work", ModelName: "gpt-test", Group: "premium", Channel: 7, ChannelName: "private-upstream", StartedAt: now},
		{RequestID: "active-bob", UserID: 2, Username: "bob", TokenName: "other", ModelName: "claude", Group: "default", Channel: 8, StartedAt: now},
	} {
		handle := active_requests.Start(record)
		require.NotNil(t, handle)
		t.Cleanup(handle.Finish)
	}
	query := "username=bob&channel=8&user_id=2"
	response := callLogHandler(t, GetUserActiveRequests, 1, common.RoleCommonUser, query)[0]
	require.Equal(t, true, response["success"])
	data := response["data"].(map[string]any)
	assert.EqualValues(t, 1, data["total"])
	assert.Positive(t, data["server_time"])
	items := data["items"].([]any)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	assert.Equal(t, "active-alice", item["request_id"])
	assert.Empty(t, item["username"])
	assert.Empty(t, item["channel_name"])
	assert.EqualValues(t, 0, item["channel"])
	assert.Equal(t, "work", item["token_name"])
	assert.EqualValues(t, 11, item["token_id"])
	assert.Equal(t, "premium", item["group"])

	response = callLogHandler(t, GetActiveRequests, 3, common.RoleAdminUser, "username=ali&channel=7&model_name=gpt-%25&token_name=work&group=premium&type=2")[0]
	require.Equal(t, true, response["success"])
	data = response["data"].(map[string]any)
	require.EqualValues(t, 1, data["total"])
	item = data["items"].([]any)[0].(map[string]any)
	assert.Equal(t, "alice", item["username"])
	assert.Equal(t, "private-upstream", item["channel_name"])
	assert.EqualValues(t, 7, item["channel"])
	for _, query := range []string{"type=5", "upstream_request_id=unknown", "request_id=active-bob"} {
		response = callLogHandler(t, GetUserActiveRequests, 1, common.RoleCommonUser, query)[0]
		require.Equal(t, true, response["success"])
		data = response["data"].(map[string]any)
		assert.EqualValues(t, 0, data["total"])
		assert.Empty(t, data["items"])
	}
	response = callLogHandler(t, GetUserActiveRequests, 0, common.RoleCommonUser, "")[0]
	assert.EqualValues(t, 0, response["data"].(map[string]any)["total"], "missing identity cannot list all users")
	tooLong := fmt.Sprintf("start_timestamp=%d", time.Now().Unix()-366*24*3600)
	response = callLogHandler(t, GetUserActiveRequests, 1, common.RoleCommonUser, tooLong)[0]
	assert.Equal(t, false, response["success"])
}

func TestActiveRequestsAuthorization(t *testing.T) {
	admin, _ := setupAccessTokenAudit(t)
	user := &model.User{Username: "active-user", AffCode: "active-user", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, model.DB.Create(user).Error)
	_, adminToken := createAccessTokenTestSession(t, admin.Id, "active-admin-session")
	_, userToken := createAccessTokenTestSession(t, user.Id, "active-user-session")
	adminLog, _ := createScopedAccessToken(t, admin.Id, 0, "log:read")
	adminUsage, _ := createScopedAccessToken(t, admin.Id, 0, "usage:read")
	userUsage, _ := createScopedAccessToken(t, user.Id, 0, "usage:read")
	userProfile, _ := createScopedAccessToken(t, user.Id, 0, "profile:read")
	expiredLog, _ := createScopedAccessToken(t, admin.Id, time.Now().Unix()-1, "log:read")
	revokedLog, revoked := createScopedAccessToken(t, admin.Id, 0, "log:read")
	_, err := model.DeleteUserAccessToken(admin.Id, revoked.Id)
	require.NoError(t, err)
	for _, userID := range []int{admin.Id, user.Id} {
		handle := active_requests.Start(active_requests.Record{RequestID: fmt.Sprintf("pat-active-%d", userID), UserID: userID, Username: "private-user", Channel: 7, ChannelName: "private-upstream"})
		t.Cleanup(handle.Finish)
	}
	engine := gin.New()
	engine.GET("/api/log/active", middleware.AdminAuth(), GetActiveRequests)
	engine.GET("/api/log/self/active", middleware.UserAuth(), GetUserActiveRequests)
	for _, tc := range []struct {
		name, path, token string
		status            int
	}{
		{"anonymous admin", "/api/log/active", "", http.StatusUnauthorized},
		{"anonymous self", "/api/log/self/active", "", http.StatusUnauthorized},
		{"ordinary user admin", "/api/log/active", userToken, http.StatusForbidden},
		{"ordinary user self", "/api/log/self/active", userToken, http.StatusOK},
		{"administrator", "/api/log/active", adminToken, http.StatusOK},
		{"log PAT administrator", "/api/log/active", adminLog, http.StatusOK},
		{"usage PAT cannot read global", "/api/log/active", adminUsage, http.StatusForbidden},
		{"usage PAT cannot bypass role", "/api/log/active", userUsage, http.StatusForbidden},
		{"usage PAT self", "/api/log/self/active?user_id=1&username=private-user&channel=7", userUsage, http.StatusOK},
		{"administrator usage PAT self", "/api/log/self/active", adminUsage, http.StatusOK},
		{"log PAT lacks own usage scope", "/api/log/self/active", adminLog, http.StatusForbidden},
		{"profile PAT lacks usage scope", "/api/log/self/active", userProfile, http.StatusForbidden},
		{"expired log PAT", "/api/log/active", expiredLog, http.StatusUnauthorized},
		{"revoked log PAT", "/api/log/active", revokedLog, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := accessTokenRequest(engine, http.MethodGet, tc.path, tc.token, "", "")
			assert.Equal(t, tc.status, response.Code, response.Body.String())
			if tc.status == http.StatusOK {
				assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
				var result struct {
					Success bool                   `json:"success"`
					Data    active_requests.Result `json:"data"`
				}
				require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
				require.True(t, result.Success)
				if strings.Contains(tc.path, "/self/") {
					require.Equal(t, 1, result.Data.Total)
					require.Len(t, result.Data.Items, 1)
					item := result.Data.Items[0]
					if tc.token == userToken || tc.token == userUsage {
						assert.Equal(t, user.Id, item.UserID)
					} else {
						assert.Equal(t, admin.Id, item.UserID)
					}
					assert.Empty(t, item.Username)
					assert.Zero(t, item.Channel)
					assert.Empty(t, item.ChannelName)
				}
			}
		})
	}
}
