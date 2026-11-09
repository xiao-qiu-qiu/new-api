package model

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/clickhouse"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// The external DSNs must point at isolated databases without a logs table.
// Only the table created by this fixture is removed during cleanup.
func openLogFilterTestDatabase(t *testing.T, dialect string) *gorm.DB {
	t.Helper()
	var driver gorm.Dialector
	switch dialect {
	case "sqlite":
		driver = sqlite.Open(filepath.Join(t.TempDir(), "logs.db"))
	case "mysql":
		dsn := os.Getenv("TEST_MYSQL_DSN")
		if dsn == "" {
			t.Skip("TEST_MYSQL_DSN is not configured")
		}
		driver = mysql.Open(dsn)
	case "postgres":
		dsn := os.Getenv("TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("TEST_POSTGRES_DSN is not configured")
		}
		driver = postgres.Open(dsn)
	case "clickhouse":
		dsn := os.Getenv("TEST_CLICKHOUSE_DSN")
		if dsn == "" {
			t.Skip("TEST_CLICKHOUSE_DSN is not configured")
		}
		driver = clickhouse.Open(dsn)
	}
	db, err := gorm.Open(driver, &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { assert.NoError(t, sqlDB.Close()) })
	require.False(t, db.Migrator().HasTable(&Log{}), "use an isolated test database without a logs table")
	if dialect == "clickhouse" {
		require.NoError(t, db.Exec(clickHouseLogCreateTableSQL(0)).Error)
	} else {
		require.NoError(t, db.AutoMigrate(&Log{}))
	}
	t.Cleanup(func() { assert.NoError(t, db.Migrator().DropTable(&Log{})) })
	versionSQL := "SELECT version()"
	if dialect == "sqlite" {
		versionSQL = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, db.Raw(versionSQL).Scan(&version).Error)
	t.Logf("log database: %s %s", dialect, version)
	previousDB, previousType, previousGroupCol := LOG_DB, common.LogDatabaseType(), logGroupCol
	LOG_DB = db
	common.SetLogDatabaseType(common.DatabaseType(dialect))
	logGroupCol = "`group`"
	if dialect == "postgres" {
		logGroupCol = `"group"`
	}
	t.Cleanup(func() {
		LOG_DB, logGroupCol = previousDB, previousGroupCol
		common.SetLogDatabaseType(previousType)
	})
	return db
}

func TestLogTextFilterDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres", "clickhouse"} {
		t.Run(dialect, func(t *testing.T) {
			db := openLogFilterTestDatabase(t, dialect)
			now := time.Now().Unix()
			entries := []Log{
				{UserId: 1, Username: "alice", ModelName: `pre-gpt_4\mini!-post`, TokenName: `prefix-key_!\main-suffix`, Quota: 11, PromptTokens: 2, CompletionTokens: 3, RequestId: "own", CreatedAt: now - 1},
				{UserId: 2, Username: "malice", ModelName: `pre-gpt_4\mini!-post`, TokenName: `prefix-key_!\main-suffix`, Quota: 13, PromptTokens: 7, CompletionTokens: 11, RequestId: "similar-user", CreatedAt: now - 2},
				{UserId: 1, Username: "former-name", ModelName: `pre-gpt_4\mini!-post`, TokenName: `prefix-key_!\main-suffix`, Quota: 17, PromptTokens: 13, CompletionTokens: 17, RequestId: "renamed-owner", CreatedAt: now - 120},
				{UserId: 1, Username: "alice", ModelName: `pre-gptX4\mini!-post`, TokenName: `prefix-key_!\main-suffix`, Quota: 19, PromptTokens: 19, CompletionTokens: 23, RequestId: "different-model", CreatedAt: now - 3},
				{UserId: 1, Username: "alice", ModelName: `pre-gpt_4\mini!-post`, TokenName: `prefix-keyX!\main-suffix`, Quota: 23, PromptTokens: 29, CompletionTokens: 31, RequestId: "different-token", CreatedAt: now - 4},
				{UserId: 3, Username: "other", ModelName: `pre-gpt_4\mini!-post`, TokenName: `prefix-key_!\main-suffix`, Quota: 29, PromptTokens: 37, CompletionTokens: 41, RequestId: "different-user", CreatedAt: now - 5},
				{UserId: 1, Username: "alice", ModelName: `pre-gpt_4\mini!-post`, TokenName: `prefix-key_!\main-suffix`, Quota: 1000, RequestId: "error", CreatedAt: now - 6, Type: LogTypeError},
				{UserId: 1, Username: "alice", ModelName: `pre-gpt_4\mini!-post`, TokenName: `prefix-key_!\main-suffix`, Quota: 31, RequestId: "outside-period", CreatedAt: now - 7200},
			}
			for i := range entries {
				entries[i].Id = i + 1
				entries[i].Group = "team"
				if entries[i].Type == LogTypeUnknown {
					entries[i].Type = LogTypeConsume
				}
			}
			require.NoError(t, db.Create(&entries).Error)

			for _, tc := range []struct {
				name             string
				model, token     string
				username         string
				allIDs, ownIDs   []string
				allStat, ownStat Stat
			}{
				{
					name: "fragments with literal underscore escape and backslash", model: `gpt_4\mini!`, token: `key_!\main`, username: "lic",
					allIDs: []string{"own", "similar-user"}, ownIDs: []string{"own", "renamed-owner"},
					allStat: Stat{Quota: 24, Rpm: 2, Tpm: 23}, ownStat: Stat{Quota: 28, Rpm: 1, Tpm: 5},
				},
				{
					name: "single character fragments", model: "4", token: "!", username: "a",
					allIDs: []string{"own", "similar-user", "renamed-owner", "different-model", "different-token"}, ownIDs: []string{"own", "renamed-owner", "different-model", "different-token"},
					allStat: Stat{Quota: 83, Rpm: 4, Tpm: 125}, ownStat: Stat{Quota: 70, Rpm: 3, Tpm: 107},
				},
				{
					name: "legacy explicit percent patterns", model: "pre-gpt%post", token: "prefix-key%", username: "%lic%",
					allIDs: []string{"own", "similar-user", "different-model", "different-token"}, ownIDs: []string{"own", "renamed-owner", "different-model", "different-token"},
					allStat: Stat{Quota: 66, Rpm: 4, Tpm: 125}, ownStat: Stat{Quota: 70, Rpm: 3, Tpm: 107},
				},
				{
					name:   "empty names preserve other filters",
					allIDs: []string{"own", "similar-user", "renamed-owner", "different-model", "different-token", "different-user"}, ownIDs: []string{"own", "renamed-owner", "different-model", "different-token"},
					allStat: Stat{Quota: 112, Rpm: 5, Tpm: 203}, ownStat: Stat{Quota: 70, Rpm: 3, Tpm: 107},
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					filter := LogFilter{Type: LogTypeConsume, StartTimestamp: now - 3600, EndTimestamp: now + 60, ModelName: tc.model, TokenName: tc.token, Username: tc.username, Group: "team"}
					logs, total, err := GetAllLogs(filter, 0, 20)
					require.NoError(t, err)
					assert.EqualValues(t, len(tc.allIDs), total)
					allIDs := make([]string, 0, len(logs))
					for _, log := range logs {
						allIDs = append(allIDs, log.RequestId)
					}
					assert.ElementsMatch(t, tc.allIDs, allIDs)
					var exported []string
					require.NoError(t, StreamAllLogs(context.Background(), filter, common.RoleAdminUser, func(log *Log) error {
						exported = append(exported, log.RequestId)
						return nil
					}))
					assert.ElementsMatch(t, tc.allIDs, exported)
					stat, err := SumUsedQuota(filter)
					require.NoError(t, err)
					assert.Equal(t, tc.allStat, stat)

					// Self endpoints discard the administrator username filter.
					filter.Username = ""
					logs, total, err = GetUserLogs(1, filter, 0, 20)
					require.NoError(t, err)
					assert.EqualValues(t, len(tc.ownIDs), total)
					ownIDs := make([]string, 0, len(logs))
					for _, log := range logs {
						assert.Equal(t, 1, log.UserId)
						ownIDs = append(ownIDs, log.RequestId)
					}
					assert.ElementsMatch(t, tc.ownIDs, ownIDs)
					exported = nil
					require.NoError(t, StreamUserLogs(context.Background(), 1, filter, func(log *Log) error {
						exported = append(exported, log.RequestId)
						return nil
					}))
					assert.ElementsMatch(t, tc.ownIDs, exported)
					stat, err = SumUserUsedQuota(1, filter)
					require.NoError(t, err)
					assert.Equal(t, tc.ownStat, stat, "self statistics must include renamed owners and exclude similar usernames")
				})
			}
			t.Run("invalid explicit patterns retain validation", func(t *testing.T) {
				for _, pattern := range []string{"%", "gpt%%", "%g%p%t"} {
					for _, filter := range []LogFilter{{ModelName: pattern}, {Username: pattern}, {TokenName: pattern}} {
						_, _, err := GetAllLogs(filter, 0, 20)
						require.Error(t, err)
						require.Error(t, StreamAllLogs(context.Background(), filter, common.RoleAdminUser, func(*Log) error {
							t.Error("invalid searches must not export rows")
							return nil
						}))
						_, err = SumUsedQuota(filter)
						require.Error(t, err)
					}
				}
			})
			t.Run("missing user id does not widen self statistics", func(t *testing.T) {
				stat, err := SumUserUsedQuota(0, LogFilter{})
				require.NoError(t, err)
				assert.Equal(t, Stat{}, stat)
			})
		})
	}
}

func TestLogErrorFilterDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres", "clickhouse"} {
		t.Run(dialect, func(t *testing.T) {
			db := openLogFilterTestDatabase(t, dialect)
			now := time.Now().Unix()
			entries := []Log{
				{TokenName: "success", Other: `{}`},
				{TokenName: "empty"},
				{TokenName: "null", Other: `null`},
				{TokenName: "non-object", Other: `[]`},
				{TokenName: "completed", Other: ` { "stream_status" : { "status" : "ok", "response_status" : "completed", "end_reason" : "done", "errors" : [ ] } } `},
				{TokenName: "policy-content", Type: LogTypeError, Content: "Blocked By Policy"},
				{TokenName: "code-nonstream", Type: LogTypeError, Other: `{"error_code":"cyber_policy","error_type":"invalid_request_error","status_code":403}`},
				{TokenName: "code-other-owner", UserId: 2, Type: LogTypeError, Other: `{"error_code":"cyber_policy"}`},
				{TokenName: "code-nested", Type: LogTypeError, Other: `{"error":{"code":"cyber_policy","message":"public failure wording"}}`},
				{TokenName: "legacy-terminal", Other: `{"stream_status":{"status":"error","end_error":"{\"type\":\"error\",\"error\":{\"code\":\"cyber_policy\",\"message\":\"blocked by policy\"}}"}}`},
				{TokenName: "root-terminal", Other: `{"stream_status":{"status":"error","end_error":"{\n \"code\" : \"cyber_policy\"\n}"}}`},
				{TokenName: "response-terminal", Other: `{"stream_status":{"status":"ok","response_status":"failed","end_error":"{\"response\":{\"error\":{\"code\":\"cyber_policy\"}}}"}}`},
				{TokenName: "structured-stream", Other: `{"stream_status":{"status":"error","error_code":"cyber_policy"}}`},
				{TokenName: "incomplete", Other: `{"stream_status":{"status":"ok","response_status":"incomplete"}}`},
				{TokenName: "cancelled", Other: `{"stream_status":{"response_status":"cancelled"}}`},
				{TokenName: "canceled", Other: `{"stream_status":{"response_status":"canceled"}}`},
				{TokenName: "timeout", Other: `{"stream_status":{"status":"ok","response_status":"completed","end_reason":"timeout"}}`},
				{TokenName: "error-count", Other: `{"stream_status":{"status":"ok","error_count":2}}`},
				{TokenName: "error-array", Other: `{"stream_status":{"status":"ok","errors":["buffer aborted"]}}`},
				{TokenName: "client-gone", Other: `{"stream_status":{"status":"ok","response_status":"completed","client_gone":true}}`},
				{TokenName: "plain-terminal", Other: `{"stream_status":{"end_error":"upstream closed"}}`},
				{TokenName: "code-prefix", Type: LogTypeError, Other: `{"error_code":"cyber_policy_extra"}`},
				{TokenName: "code-case", Type: LogTypeError, Other: `{"error_code":"CYBER_POLICY"}`},
				{TokenName: "code-mention", Other: `{"stream_status":{"end_error":"{\"message\":\"cyber_policy\"}"}}`},
				{TokenName: "quoted-code-mention", Other: `{"stream_status":{"end_error":"{\"message\":\"quoted \\\"code\\\":\\\"cyber_policy\\\"\"}"}}`},
				{TokenName: "literal-content", Type: LogTypeError, Content: `prefix literal%_!\path suffix`},
				{TokenName: "literal-array", Other: `{"stream_status":{"status":"error","errors":["prefix LITERAL%_!\\path suffix"]}}`},
				{TokenName: "literal-decoy", Type: LogTypeError, Content: "literalXX!path"},
				{TokenName: "hidden", Other: `{"admin_info":{"error_code":"cyber_policy","message":"secret-only"},"root_info":{"stream_status":{"status":"error"},"error_code":"cyber_policy","message":"secret-only"},"audit_info":{"message":"secret-only"}}`},
				{TokenName: "legacy-hidden", Other: `{"reject_reason":"secret-only","channel_name":"secret-only"}`},
				{TokenName: "json-null-error", Other: `{"error_code":null,"stream_status":{"end_error":null,"error_count":null,"errors":null}}`},
				{TokenName: "retry-failed", RequestId: "retry", Type: LogTypeError, Content: "blocked by policy"},
				{TokenName: "retry-success", RequestId: "retry", Other: `{}`},
			}
			for i := range entries {
				entries[i].Id = i + 1
				entries[i].CreatedAt = now - 5
				entries[i].Group = "team"
				if entries[i].UserId == 0 {
					entries[i].UserId = 1
				}
				if entries[i].RequestId == "" {
					entries[i].RequestId = entries[i].TokenName
				}
				if entries[i].Type == LogTypeUnknown {
					entries[i].Type = LogTypeConsume
					entries[i].Quota = 3
					entries[i].PromptTokens, entries[i].CompletionTokens = 2, 1
				}
			}
			require.NoError(t, db.Create(&entries).Error)
			if dialect != "clickhouse" {
				// ClickHouse's existing logs schema uses a non-nullable String.
				require.NoError(t, db.Model(&Log{}).Where("token_name = ?", "empty").UpdateColumn("other", nil).Error)
				// Keep both the SQL NULL and historical empty-string contracts.
				require.NoError(t, db.Model(&Log{}).Where("token_name = ?", "success").UpdateColumn("other", "").Error)
			}
			streamErrors := []string{"legacy-terminal", "root-terminal", "response-terminal", "structured-stream", "incomplete", "cancelled", "canceled", "timeout", "error-count", "error-array", "client-gone", "plain-terminal", "code-mention", "quoted-code-mention", "literal-array"}
			errorLogs := []string{"policy-content", "code-nonstream", "code-other-owner", "code-nested", "code-prefix", "code-case", "literal-content", "literal-decoy", "retry-failed"}
			failed := append(append([]string{}, streamErrors...), errorLogs...)
			for _, tc := range []struct {
				name   string
				filter LogFilter
				ids    []string
			}{
				{name: "case insensitive content and public terminal", filter: LogFilter{ErrorKeyword: "BLOCKED BY POLICY"}, ids: []string{"policy-content", "legacy-terminal", "retry-failed"}},
				{name: "public error message", filter: LogFilter{ErrorKeyword: "public failure wording"}, ids: []string{"code-nested"}},
				{name: "decoded error arrays", filter: LogFilter{ErrorKeyword: "BUFFER ABORTED"}, ids: []string{"error-array"}},
				{name: "literal wildcard escape and backslash", filter: LogFilter{ErrorKeyword: `literal%_!\path`}, ids: []string{"literal-content", "literal-array"}},
				{name: "single literal percent", filter: LogFilter{ErrorKeyword: "%"}, ids: []string{"literal-content", "literal-array"}},
				{name: "single literal escape", filter: LogFilter{ErrorKeyword: "!"}, ids: []string{"literal-content", "literal-array", "literal-decoy"}},
				{name: "hidden scopes never participate", filter: LogFilter{ErrorKeyword: "secret-only"}},
				{name: "exact code includes historical JSON string terminals", filter: LogFilter{ErrorCode: "cyber_policy"}, ids: []string{"code-nonstream", "code-other-owner", "code-nested", "legacy-terminal", "root-terminal", "response-terminal", "structured-stream"}},
				{name: "code comparison is case sensitive", filter: LogFilter{ErrorCode: "CYBER_POLICY"}, ids: []string{"code-case"}},
				{name: "no code prefix matching", filter: LogFilter{ErrorCode: "cyber"}},
				{name: "JSON null is not an error code", filter: LogFilter{ErrorCode: "null"}},
				{name: "stream failures", filter: LogFilter{RequestResult: "stream_error"}, ids: streamErrors},
				{name: "all failures", filter: LogFilter{RequestResult: "failed"}, ids: failed},
				{name: "successful historical and completed consume logs", filter: LogFilter{RequestResult: "success"}, ids: []string{"success", "empty", "null", "non-object", "completed", "hidden", "legacy-hidden", "json-null-error", "retry-success"}},
				{name: "composed error filters", filter: LogFilter{ErrorKeyword: "blocked by policy", ErrorCode: "cyber_policy", RequestResult: "stream_error"}, ids: []string{"legacy-terminal"}},
				{name: "type intersects result", filter: LogFilter{Type: LogTypeError, RequestResult: "stream_error"}},
				{name: "retry failed row", filter: LogFilter{RequestId: "retry", RequestResult: "failed"}, ids: []string{"retry-failed"}},
				{name: "retry successful row", filter: LogFilter{RequestId: "retry", RequestResult: "success"}, ids: []string{"retry-success"}},
				{name: "retry rows remain separate", filter: LogFilter{RequestId: "retry"}, ids: []string{"retry-failed", "retry-success"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					filter := tc.filter
					filter.StartTimestamp, filter.EndTimestamp, filter.Group = now-3600, now+60, "team"
					for _, self := range []bool{false, true} {
						var expected []string
						var expectedStat Stat
						for _, id := range tc.ids {
							for _, entry := range entries {
								if entry.TokenName != id || (self && entry.UserId != 1) {
									continue
								}
								expected = append(expected, id)
								if entry.Type == LogTypeConsume {
									expectedStat.Quota += entry.Quota
									expectedStat.Rpm++
									expectedStat.Tpm += entry.PromptTokens + entry.CompletionTokens
								}
							}
						}
						var logs []*Log
						var total int64
						var err error
						if self {
							logs, total, err = GetUserLogs(1, filter, 0, 2)
						} else {
							logs, total, err = GetAllLogs(filter, 0, 2)
						}
						require.NoError(t, err)
						assert.EqualValues(t, len(expected), total)
						assert.Len(t, logs, min(2, len(expected)), "filter before applying pagination")
						for _, log := range logs {
							assert.Contains(t, expected, log.TokenName)
						}
						var exported []string
						consume := func(log *Log) error {
							exported = append(exported, log.TokenName)
							if self {
								assert.Equal(t, 1, log.UserId)
								assert.NotContains(t, log.Other, "secret-only")
							}
							return nil
						}
						var stat Stat
						if self {
							require.NoError(t, StreamUserLogs(context.Background(), 1, filter, consume))
							stat, err = SumUserUsedQuota(1, filter)
						} else {
							require.NoError(t, StreamAllLogs(context.Background(), filter, common.RoleAdminUser, consume))
							stat, err = SumUsedQuota(filter)
						}
						assert.ElementsMatch(t, expected, exported)
						require.NoError(t, err)
						assert.Equal(t, expectedStat, stat, "same search and ownership boundary for consumption statistics")
					}
				})
			}
			t.Run("unknown result is rejected by every query", func(t *testing.T) {
				filter := LogFilter{RequestResult: "unknown"}
				_, _, err := GetAllLogs(filter, 0, 2)
				require.Error(t, err)
				_, _, err = GetUserLogs(1, filter, 0, 2)
				require.Error(t, err)
				_, err = SumUsedQuota(filter)
				require.Error(t, err)
				_, err = SumUserUsedQuota(1, filter)
				require.Error(t, err)
				require.Error(t, StreamAllLogs(context.Background(), filter, common.RoleAdminUser, func(*Log) error { t.Error("invalid filter must not export rows"); return nil }))
				require.Error(t, StreamUserLogs(context.Background(), 1, filter, func(*Log) error { t.Error("invalid filter must not export rows"); return nil }))
			})
		})
	}
}
