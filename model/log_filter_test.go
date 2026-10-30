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
	return db
}

func TestLogTextFilterDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres", "clickhouse"} {
		t.Run(dialect, func(t *testing.T) {
			db := openLogFilterTestDatabase(t, dialect)
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
					stat, err := SumUsedQuota(filter.Type, filter.StartTimestamp, filter.EndTimestamp, filter.ModelName, filter.Username, filter.TokenName, filter.Channel, filter.Group)
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
						_, err = SumUsedQuota(0, 0, 0, filter.ModelName, filter.Username, filter.TokenName, 0, "")
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
