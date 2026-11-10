package controller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/synctest"
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
	"gorm.io/driver/clickhouse"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
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
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	previousClient := common.RDB
	common.RedisEnabled, common.MemoryCacheEnabled, common.RDB = true, false, client
	t.Cleanup(func() {
		assert.NoError(t, client.Close())
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled, common.MemoryCacheEnabled, common.RDB = previousRedis, previousMemory, previousClient
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
			testDSN := os.Getenv("TEST_" + strings.ToUpper(tc.kind) + "_DSN")
			if tc.env != "" && dsn == "" && testDSN == "" {
				t.Skip(tc.env + " is not configured")
			}
			var db *gorm.DB
			var isolatedDSN string
			if testDSN != "" && tc.kind != "sqlite" {
				// TEST_* databases are already isolated and need no CREATE
				// DATABASE privilege. Do not invoke unrelated audit migrations.
				var driver gorm.Dialector
				switch tc.kind {
				case "mysql":
					driver = mysql.Open(testDSN)
				case "postgres":
					driver = postgres.Open(testDSN)
				case "clickhouse":
					driver = clickhouse.Open(testDSN)
				}
				var err error
				db, err = gorm.Open(driver, &gorm.Config{})
				require.NoError(t, err)
				require.False(t, db.Migrator().HasTable(&model.Log{}), "TEST_* DSN must point at an isolated log database")
				if tc.kind != "clickhouse" {
					require.False(t, db.Migrator().HasTable(&model.Channel{}))
				}
				sqlDB, err := db.DB()
				require.NoError(t, err)
				t.Cleanup(func() { assert.NoError(t, sqlDB.Close()) })
				t.Cleanup(func() {
					assert.NoError(t, db.Migrator().DropTable(&model.Log{}))
					if tc.kind != "clickhouse" {
						assert.NoError(t, db.Migrator().DropTable(&model.Channel{}))
					}
				})
			} else {
				db, isolatedDSN = newAuditTestDatabase(t, tc.kind, dsn)
			}
			mainDB, mainType := db, tc.typ
			if tc.kind == "clickhouse" {
				mainDB, _ = newAuditTestDatabase(t, "sqlite", "")
				mainType = common.DatabaseTypeSQLite
			}
			useLogExportDatabases(t, mainDB, db, mainType, tc.typ)
			require.NoError(t, mainDB.AutoMigrate(&model.Channel{}))
			if tc.kind == "clickhouse" {
				if testDSN != "" {
					require.NoError(t, db.Set("gorm:table_options", "ENGINE=MergeTree() ORDER BY (created_at, request_id)").AutoMigrate(&model.Log{}))
				} else {
					t.Setenv("LOG_SQL_DSN", isolatedDSN)
					require.NoError(t, model.InitLogDB())
				}
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

func TestLogQueryProtection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := newAuditTestDatabase(t, "sqlite", "")
	useLogExportDatabases(t, db, db, common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	for _, handler := range []gin.HandlerFunc{GetUserLogs, GetLogsSelfStat, ExportUserLogs} {
		for _, query := range []string{"error_keyword=403", "error_code=cyber_policy", "request_result=failed"} {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/log/self?"+query, nil)
			ctx.Set("id", 301)
			ctx.Set("role", common.RoleCommonUser)
			handler(ctx)
			assert.Equal(t, http.StatusForbidden, recorder.Code)
			assert.Contains(t, recorder.Body.String(), "Error log searches are only available to administrators")
		}
	}
	now := time.Now().Unix()
	for _, handler := range []gin.HandlerFunc{GetUserLogs, GetLogsSelfStat, ExportUserLogs, GetAllLogs, GetLogsStat, ExportAllLogs} {
		for _, query := range []string{
			fmt.Sprintf("error_keyword=403&start_timestamp=%d&end_timestamp=%d", now-30*24*3600, now),
			"start_timestamp=-1", "start_timestamp=oops", "end_timestamp=999999999999999999999",
			"start_timestamp=4&end_timestamp=3", "channel=-1", "type=500", "type=1&type=2",
			"error_keyword=" + strings.Repeat("a", 257), "error_code=" + strings.Repeat("b", 129),
			"request_result=unknown",
		} {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/log?"+query, nil)
			ctx.Set("id", 302)
			ctx.Set("role", common.RoleAdminUser)
			handler(ctx)
			assert.Equal(t, http.StatusBadRequest, recorder.Code, query)
		}
	}
	for _, query := range []string{"page_size=-1", "ps=-1", "size=-1", "p=-1", "p=0", "p=9223372036854775807&page_size=100", "page_size=oops", "p=1&p=2", "error_keyword=403&p=2"} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/api/log?"+query, nil)
		ctx.Set("id", 303)
		ctx.Set("role", common.RoleAdminUser)
		GetAllLogs(ctx)
		assert.Equal(t, http.StatusBadRequest, recorder.Code, query)
	}
	t.Run("missing bounds search only the last day", func(t *testing.T) {
		require.NoError(t, db.Create(&[]model.Log{
			{UserId: 304, Type: model.LogTypeError, Content: "403", CreatedAt: now - 10, RequestId: "recent"},
			{UserId: 304, Type: model.LogTypeError, Content: "403", CreatedAt: now - 25*3600, RequestId: "old"},
		}).Error)
		response := callLogHandler(t, GetUserLogs, 304, common.RoleAdminUser, "error_keyword=403")[0]
		require.Equal(t, true, response["success"])
		data := response["data"].(map[string]any)
		assert.Equal(t, false, data["total_exact"])
		assert.EqualValues(t, 1, data["total"])
		items := data["items"].([]any)
		require.Len(t, items, 1)
		assert.Equal(t, "recent", items[0].(map[string]any)["request_id"])
	})
	t.Run("ordinary page size remains capped", func(t *testing.T) {
		response := callLogHandler(t, GetUserLogs, 305, common.RoleCommonUser, "page_size=101")[0]
		require.Equal(t, true, response["success"])
		assert.EqualValues(t, 100, response["data"].(map[string]any)["page_size"])
	})
	t.Run("common pagination clamps negatives and prevents overflow", func(t *testing.T) {
		for _, query := range []string{"page_size=-1&p=-2", "ps=-1", "size=-1", "p=9223372036854775807&page_size=100"} {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodGet, "/?"+query, nil)
			page := common.GetPageQuery(ctx)
			assert.GreaterOrEqual(t, page.Page, 1)
			assert.GreaterOrEqual(t, page.PageSize, 1)
			assert.LessOrEqual(t, page.PageSize, 100)
			assert.GreaterOrEqual(t, page.GetStartIdx(), 0)
			assert.GreaterOrEqual(t, page.GetEndIdx(), page.GetStartIdx())
		}
	})
	t.Run("canceled client does not reach the database", func(t *testing.T) {
		for _, handler := range []gin.HandlerFunc{GetUserLogs, GetLogsSelfStat} {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			requestContext, cancel := context.WithCancel(context.Background())
			cancel()
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/log/self", nil).WithContext(requestContext)
			ctx.Set("id", 306)
			ctx.Set("role", common.RoleCommonUser)
			handler(ctx)
			assert.Equal(t, http.StatusRequestTimeout, recorder.Code)
		}
	})
	t.Run("ordinary user scans share the heavy budget without losing permission", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), model.LogQueryTimeout)
		defer cancel()
		release, retry, err := service.AcquireLogQuery(ctx, 320, true, false)
		require.NoError(t, err)
		require.Zero(t, retry)
		defer release()
		for _, handler := range []gin.HandlerFunc{GetUserLogs, GetLogsSelfStat} {
			for _, query := range []string{
				fmt.Sprintf("model_name=gpt&start_timestamp=%d&end_timestamp=%d", now-60, now),
				fmt.Sprintf("token_name=key&start_timestamp=%d&end_timestamp=%d", now-60, now),
				fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-30*24*3600, now),
			} {
				recorder := httptest.NewRecorder()
				request, _ := gin.CreateTestContext(recorder)
				request.Request = httptest.NewRequest(http.MethodGet, "/api/log/self?"+query, nil)
				request.Set("id", 321)
				request.Set("role", common.RoleCommonUser)
				handler(request)
				assert.Equal(t, http.StatusTooManyRequests, recorder.Code, query)
			}
			response := callLogHandler(t, handler, 321, common.RoleCommonUser, fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-60, now))[0]
			assert.Equal(t, true, response["success"], "recent unfiltered reads do not compete for the scan slot")
		}
	})
	t.Run("query deadline reaches database and returns 504", func(t *testing.T) {
		require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:log-deadline", func(tx *gorm.DB) {
			deadline, ok := tx.Statement.Context.Deadline()
			assert.True(t, ok)
			assert.LessOrEqual(t, time.Until(deadline), model.LogQueryTimeout)
			tx.AddError(context.DeadlineExceeded)
		}))
		t.Cleanup(func() { db.Callback().Query().Remove("test:log-deadline") })
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/api/log/self", nil)
		ctx.Set("id", 307)
		ctx.Set("role", common.RoleCommonUser)
		GetUserLogs(ctx)
		assert.Equal(t, http.StatusGatewayTimeout, recorder.Code)
		assert.Contains(t, recorder.Body.String(), "Log query timed out")
	})
}

func TestLogQueryConcurrencyAndRate(t *testing.T) {
	for _, useRedis := range []bool{false, true} {
		t.Run(fmt.Sprintf("redis=%v", useRedis), func(t *testing.T) {
			previousRedis, previousClient := common.RedisEnabled, common.RDB
			common.RedisEnabled = useRedis
			var server *miniredis.Miniredis
			if useRedis {
				server = miniredis.RunT(t)
				client := redis.NewClient(&redis.Options{Addr: server.Addr()})
				common.RDB = client
				t.Cleanup(func() { assert.NoError(t, client.Close()) })
			}
			t.Cleanup(func() { common.RedisEnabled, common.RDB = previousRedis, previousClient })
			ctx, cancel := context.WithTimeout(context.Background(), model.LogQueryTimeout)
			defer cancel()
			first, retry, err := service.AcquireLogQuery(ctx, 401, false, false)
			require.NoError(t, err)
			require.Zero(t, retry)
			t.Cleanup(first)
			second, retry, err := service.AcquireLogQuery(ctx, 401, false, false)
			require.NoError(t, err)
			require.Zero(t, retry, "list and stat may execute together")
			t.Cleanup(second)
			_, retry, err = service.AcquireLogQuery(ctx, 401, false, false)
			require.NoError(t, err)
			assert.Positive(t, retry, "user concurrency is two")
			third, retry, err := service.AcquireLogQuery(ctx, 402, true, false)
			require.NoError(t, err)
			require.Zero(t, retry)
			t.Cleanup(third)
			_, retry, err = service.AcquireLogQuery(ctx, 403, true, false)
			require.NoError(t, err)
			assert.Positive(t, retry, "heavy global concurrency is one")
			_, retry, err = service.AcquireLogQuery(ctx, 403, false, true)
			require.NoError(t, err)
			assert.Positive(t, retry, "exports share the heavy slot")
			fourth, retry, err := service.AcquireLogQuery(ctx, 403, false, false)
			require.NoError(t, err)
			require.Zero(t, retry)
			t.Cleanup(fourth)
			_, retry, err = service.AcquireLogQuery(ctx, 404, false, false)
			require.NoError(t, err)
			assert.Positive(t, retry, "global concurrency is four")
			first()
			first() // Idempotent release cannot remove another reservation.
			second()
			third()
			fourth()
			for range 10 {
				release, retry, err := service.AcquireLogQuery(ctx, 405, true, false)
				require.NoError(t, err)
				require.Zero(t, retry)
				release()
			}
			_, retry, err = service.AcquireLogQuery(ctx, 405, true, false)
			require.NoError(t, err)
			assert.Positive(t, retry, "completed requests still spend the rate budget")

			// The controller uses 429 for administrators too, with Retry-After.
			release, retry, err := service.AcquireLogQuery(ctx, 406, true, false)
			require.NoError(t, err)
			require.Zero(t, retry)
			t.Cleanup(release)
			recorder := httptest.NewRecorder()
			request, _ := gin.CreateTestContext(recorder)
			request.Request = httptest.NewRequest(http.MethodGet, "/api/log?"+url.Values{"error_keyword": {"403"}}.Encode(), nil)
			request.Set("id", 407)
			request.Set("role", common.RoleAdminUser)
			GetAllLogs(request)
			assert.Equal(t, http.StatusTooManyRequests, recorder.Code)
			assert.NotEmpty(t, recorder.Header().Get("Retry-After"))
			release()
			if useRedis {
				longCtx, longCancel := context.WithTimeout(context.Background(), model.LogExportQueryTimeout)
				defer longCancel()
				longRelease, retry, err := service.AcquireLogQuery(longCtx, 410, false, true)
				require.NoError(t, err)
				require.Zero(t, retry)
				t.Cleanup(longRelease)
				shortRelease, retry, err := service.AcquireLogQuery(ctx, 411, false, false)
				require.NoError(t, err)
				require.Zero(t, retry)
				t.Cleanup(shortRelease)
				server.FastForward(11 * time.Second)
				assert.True(t, server.Exists("{log-query}:global"), "short query must not shorten the export's global lease")
				assert.True(t, server.Exists("{log-query}:heavy-global"))
				_, retry, err = service.AcquireLogQuery(ctx, 412, true, false)
				require.NoError(t, err)
				assert.Positive(t, retry)
			}
		})
	}
}

func TestLogQueryCanceledLease(t *testing.T) {
	for _, tc := range []struct{ useRedis, expired bool }{{false, false}, {false, true}, {true, false}, {true, true}} {
		useRedis := tc.useRedis
		t.Run(fmt.Sprintf("redis=%v/expired=%v", useRedis, tc.expired), func(t *testing.T) {
			check := func(t *testing.T) {
				previousRedis, previousClient := common.RedisEnabled, common.RDB
				common.RedisEnabled = useRedis
				t.Cleanup(func() { common.RedisEnabled, common.RDB = previousRedis, previousClient })
				var server *miniredis.Miniredis
				if useRedis {
					server = miniredis.RunT(t)
					client := redis.NewClient(&redis.Options{Addr: server.Addr()})
					common.RDB = client
					t.Cleanup(func() { assert.NoError(t, client.Close()) })
				}
				budget := model.LogQueryTimeout
				if tc.expired {
					budget = 100 * time.Millisecond
				}
				ctx, cancel := context.WithTimeout(context.Background(), budget)
				defer cancel()
				release, retry, err := service.AcquireLogQuery(ctx, 701, true, false)
				require.NoError(t, err)
				require.Zero(t, retry)
				if tc.expired {
					<-ctx.Done()
				} else {
					cancel()
				}
				release()
				next, nextCancel := context.WithTimeout(context.Background(), model.LogQueryTimeout)
				defer nextCancel()
				unexpectedRelease, retry, err := service.AcquireLogQuery(next, 702, true, false)
				require.NoError(t, err)
				if unexpectedRelease != nil {
					unexpectedRelease()
				}
				assert.Positive(t, retry, "cancellation must retain the global scan lease")
				if useRedis {
					server.FastForward(11 * time.Second)
				} else {
					// The synctest clock advances without wall-clock waiting.
					<-time.After(11 * time.Second)
				}
				recovered, recoveredCancel := context.WithTimeout(context.Background(), model.LogQueryTimeout)
				defer recoveredCancel()
				release, retry, err = service.AcquireLogQuery(recovered, 702, true, false)
				require.NoError(t, err)
				require.Zero(t, retry, "the retained lease must expire")
				release()
			}
			if useRedis {
				check(t)
			} else {
				synctest.Test(t, check)
			}
		})
	}
}

type failingLogExportWriter struct {
	gin.ResponseWriter
	failed bool
	cancel context.CancelFunc
}

func (w *failingLogExportWriter) Write(data []byte) (int, error) {
	if !w.failed {
		w.failed = true
		if w.cancel != nil {
			w.cancel()
		}
		return 0, io.ErrClosedPipe
	}
	return w.ResponseWriter.Write(data)
}

func TestLogExportFailureRetainsQueryLease(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, outcome := range []string{"sql_error", "write_error", "client_cancel"} {
		t.Run(outcome, func(t *testing.T) {
			db, _ := newAuditTestDatabase(t, "sqlite", "")
			useLogExportDatabases(t, db, db, common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
			require.NoError(t, db.AutoMigrate(&model.Log{}))
			require.NoError(t, db.Create(&model.Log{UserId: 705, Type: model.LogTypeConsume, CreatedAt: time.Now().Unix() - 10}).Error)
			if outcome == "sql_error" {
				require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:export-connection-error", func(tx *gorm.DB) {
					tx.AddError(io.ErrUnexpectedEOF)
				}))
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			requestCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.Request = httptest.NewRequest(http.MethodGet, "/api/log/self/export", nil).WithContext(requestCtx)
			c.Set("id", 705)
			c.Set("role", common.RoleAdminUser)
			if outcome != "sql_error" {
				writer := &failingLogExportWriter{ResponseWriter: c.Writer}
				if outcome == "client_cancel" {
					writer.cancel = cancel
				}
				c.Writer = writer
			}
			ExportUserLogs(c)
			assert.True(t, c.GetBool("log_query_failed"), "stream failure must be recorded despite HTTP 200")
			assert.Equal(t, http.StatusOK, c.Writer.Status())
			next, nextCancel := context.WithTimeout(context.Background(), model.LogQueryTimeout)
			defer nextCancel()
			release, retry, err := service.AcquireLogQuery(next, 706, true, false)
			require.NoError(t, err)
			if release != nil {
				release()
			}
			assert.Positive(t, retry, "stream failures must not release the database budget")
		})
	}
}

func TestLogQueryFinishOutcome(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, outcome := range []string{"success", "server_error", "stream_error"} {
		t.Run(outcome, func(t *testing.T) {
			previousRedis, previousClient := common.RedisEnabled, common.RDB
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			common.RedisEnabled, common.RDB = true, client
			t.Cleanup(func() {
				common.RedisEnabled, common.RDB = previousRedis, previousClient
				assert.NoError(t, client.Close())
			})
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/api/log/export", nil)
			c.Set("id", 703)
			ctx, finish, ok := beginLogQuery(c, true, true)
			require.True(t, ok)
			if outcome == "server_error" {
				logQueryError(c, fmt.Errorf("SQL connection lost"))
			} else if outcome == "stream_error" {
				c.Set("log_query_failed", true)
			}
			finish()
			assert.ErrorIs(t, ctx.Err(), context.Canceled)
			next, cancel := context.WithTimeout(context.Background(), model.LogQueryTimeout)
			defer cancel()
			release, retry, err := service.AcquireLogQuery(next, 704, true, false)
			require.NoError(t, err)
			if release != nil {
				release()
			}
			if outcome == "success" {
				assert.Zero(t, retry, "successful finish releases before canceling its context")
			} else {
				assert.Positive(t, retry, "uncertain SQL/stream outcomes must retain the lease")
			}
		})
	}
}
