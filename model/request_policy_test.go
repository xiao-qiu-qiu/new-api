package model

import (
	"errors"
	"maps"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestRequestPolicyDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(":memory:")
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN not configured")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "policy_test_"}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			previousDB, previousType, previousSnapshot := DB, common.MainDatabaseType(), CurrentRequestPolicy()
			common.OptionMapRWMutex.Lock()
			previousOptions := maps.Clone(common.OptionMap)
			common.OptionMap = maps.Clone(previousSnapshot.Options)
			common.OptionMapRWMutex.Unlock()
			DB = db
			common.SetMainDatabaseType(common.DatabaseType(dialect))
			initCol()
			t.Cleanup(func() {
				require.NoError(t, db.Migrator().DropTable(&Option{}))
				for k, v := range previousSnapshot.Options {
					require.NoError(t, updateOptionMap(k, v))
				}
				requestPolicySnapshot.Store(previousSnapshot)
				common.OptionMapRWMutex.Lock()
				common.OptionMap = previousOptions
				common.OptionMapRWMutex.Unlock()
				DB = previousDB
				common.SetMainDatabaseType(previousType)
				initCol()
				require.NoError(t, sqlDB.Close())
			})
			require.NoError(t, db.AutoMigrate(&Option{}))
			var version string
			query := "SELECT version()"
			if dialect == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("database version: %s", version)
			rules := `[{"name":"session","model_regex":[".*"],"key_sources":[{"type":"request_header","key":"X-Session"}],"ttl_seconds":0,"skip_retry_on_failure":false,"include_using_group":false,"param_override_template":{"temperature":0},"future_field":{"enabled":false}}]`
			require.NoError(t, UpdateRequestPolicyOptions(map[string]string{"RetryTimes": "2", "channel_affinity_setting.rules": rules}))
			assert.Equal(t, 2, CurrentRequestPolicy().RetryTimes)
			assert.Equal(t, 2, common.RetryTimes, "the runtime global follows the same write")
			require.NoError(t, UpdateOption("AutomaticRetryStatusCodes", "429,500-503"))
			assert.Equal(t, "429,500-503", CurrentRequestPolicy().Options["AutomaticRetryStatusCodes"])
			assert.Equal(t, "429,500-503", operation_setting.AutomaticRetryStatusCodesToString())
			assert.Error(t, UpdateRequestPolicyOptions(map[string]string{"request_policy_setting.mode": "off"}), "the removed mode switch is not a policy option")
			assert.False(t, CurrentRequestPolicy().Affinity.Rules[0].SkipRetryOnFailure)
			assert.Equal(t, rules, CurrentRequestPolicy().Options["channel_affinity_setting.rules"])
			loadOptionsFromDatabase()
			loadOptionsFromDatabase()
			assert.Equal(t, rules, CurrentRequestPolicy().Options["channel_affinity_setting.rules"], "legacy rule JSON survives reloads without dropping extension fields")
			require.NoError(t, UpdateRequestPolicyOptions(map[string]string{"channel_affinity_setting.session_mode": "strict"}))
			assert.Empty(t, CurrentRequestPolicy().Failover.FallbackRules, "existing settings default to no model overrides")
			assert.Equal(t, "[]", CurrentRequestPolicy().Options["channel_failover_setting.fallback_rules"])
			backupRules := `[{"model_regex":["^gpt-6-astra$"],"channel_id":39},{"model_regex":["^codex-auto-review$"],"channel_id":76}]`
			failover := map[string]string{
				"channel_failover_setting.enabled":               "true",
				"channel_failover_setting.primary_channel_id":    "101",
				"channel_failover_setting.fallback_channel_id":   "102",
				"channel_failover_setting.model_regex":           `["^gpt-"]`,
				"channel_failover_setting.fallback_rules":        backupRules,
				"channel_failover_setting.poll_interval_seconds": "3",
				"channel_failover_setting.recovery_successes":    "2",
			}
			require.NoError(t, UpdateRequestPolicyOptions(failover))
			loadOptionsFromDatabase()
			loadOptionsFromDatabase()
			assert.Equal(t, operation_setting.ChannelFailoverSetting{
				Enabled: true, PrimaryChannelID: 101, FallbackChannelID: 102,
				FallbackRules: []operation_setting.ChannelFailoverFallbackRule{
					{ModelRegex: []string{"^gpt-6-astra$"}, ChannelID: 39},
					{ModelRegex: []string{"^codex-auto-review$"}, ChannelID: 76},
				},
				ModelRegex: []string{"^gpt-"}, PollIntervalSeconds: 3, RecoverySuccesses: 2,
			}, CurrentRequestPolicy().Failover)
			assert.Equal(t, CurrentRequestPolicy().Failover, *operation_setting.GetChannelFailoverSetting())
			assert.Equal(t, backupRules, CurrentRequestPolicy().Options["channel_failover_setting.fallback_rules"])
			assert.Equal(t, "strict", CurrentRequestPolicy().Affinity.SessionMode)
			assert.Equal(t, rules, CurrentRequestPolicy().Options["channel_affinity_setting.rules"], "a global mode never rewrites rule fields")
			snapshot := CurrentRequestPolicy()
			limitRules, err := common.Marshal(slices.Repeat([]operation_setting.ChannelFailoverFallbackRule{{ModelRegex: slices.Repeat([]string{strings.Repeat("x", 256)}, 32), ChannelID: 39}}, 32))
			require.NoError(t, err)
			limitOptions := maps.Clone(snapshot.Options)
			limitOptions["channel_failover_setting.fallback_rules"] = string(limitRules)
			_, err = BuildRequestPolicy(limitOptions)
			require.NoError(t, err, "32 rules with 32 valid patterns of 256 bytes are allowed")
			tooManyRules, err := common.Marshal(slices.Repeat(snapshot.Failover.FallbackRules[:1], 33))
			require.NoError(t, err)
			tooManyPatterns, err := common.Marshal([]operation_setting.ChannelFailoverFallbackRule{{ModelRegex: slices.Repeat([]string{"x"}, 33), ChannelID: 39}})
			require.NoError(t, err)
			for _, invalid := range []map[string]string{
				{"channel_failover_setting.primary_channel_id": "102"},
				{"channel_failover_setting.fallback_channel_id": "0"},
				{"channel_failover_setting.model_regex": `["["]`},
				{"channel_failover_setting.poll_interval_seconds": "0"},
				{"channel_failover_setting.poll_interval_seconds": "61"},
				{"channel_failover_setting.recovery_successes": "0"},
				{"channel_failover_setting.recovery_successes": "11"},
				{"channel_failover_setting.enabled": "yes"},
				{"channel_failover_setting.unknown": "true"},
				{"channel_failover_setting.fallback_rules": string(tooManyRules)},
				{"channel_failover_setting.fallback_rules": string(tooManyPatterns)},
				{"channel_failover_setting.fallback_rules": `[{"model_regex":["` + strings.Repeat("x", 257) + `"],"channel_id":39}]`},
				{"channel_failover_setting.fallback_rules": `[{"model_regex":[],"channel_id":39}]`},
				{"channel_failover_setting.fallback_rules": `[{"channel_id":39}]`},
				{"channel_failover_setting.fallback_rules": `[{"model_regex":[""],"channel_id":39}]`},
				{"channel_failover_setting.fallback_rules": `[{"model_regex":["["],"channel_id":39}]`},
				{"channel_failover_setting.fallback_rules": `[{"model_regex":["x"],"channel_id":0}]`},
				{"channel_failover_setting.fallback_rules": `[{"model_regex":["x"],"channel_id":-1}]`},
				{"channel_failover_setting.fallback_rules": `[{"model_regex":["x"],"channel_id":101}]`},
				{"channel_failover_setting.fallback_rules": `[{"model_regex":["x"],"channel_id":39.5}]`},
				{"channel_failover_setting.fallback_rules": `{"model_regex":["x"],"channel_id":39}`},
			} {
				assert.Error(t, UpdateRequestPolicyOptions(invalid))
				assert.Same(t, snapshot, CurrentRequestPolicy())
			}
			assert.Error(t, UpdateRequestPolicyOptions(map[string]string{"channel_affinity_setting.session_mode": "unknown"}))
			assert.Same(t, snapshot, CurrentRequestPolicy())
			for _, value := range []string{"-1", "1.5", "bad", strconv.Itoa(math.MaxInt)} {
				assert.Error(t, UpdateRequestPolicyOptions(map[string]string{"RetryTimes": value}))
				assert.Same(t, snapshot, CurrentRequestPolicy())
			}
			assert.Error(t, UpdateRequestPolicyOptions(map[string]string{"channel_affinity_setting.rules": "[", "RetryTimes": "8"}))
			assert.Same(t, snapshot, CurrentRequestPolicy())
			var option Option
			require.NoError(t, db.Where(map[string]any{"key": "RetryTimes"}).First(&option).Error)
			assert.Equal(t, "2", option.Value)
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("policy_fail", func(tx *gorm.DB) {
				if tx.Statement.Table == "policy_test_options" {
					tx.AddError(errors.New("save rejected"))
				}
			}))
			assert.Error(t, UpdateRequestPolicyOptions(map[string]string{"RetryTimes": "8", "channel_affinity_setting.session_mode": "off", "channel_failover_setting.fallback_rules": "[]"}))
			assert.Same(t, snapshot, CurrentRequestPolicy())
			require.NoError(t, db.Callback().Update().Remove("policy_fail"))
			option = Option{}
			require.NoError(t, db.Where(map[string]any{"key": "RetryTimes"}).First(&option).Error)
			assert.Equal(t, "2", option.Value)
			loadOptionsFromDatabase()
			assert.Equal(t, "strict", CurrentRequestPolicy().Affinity.SessionMode, "a failed save keeps the persisted global mode")
			assert.Equal(t, backupRules, CurrentRequestPolicy().Options["channel_failover_setting.fallback_rules"], "invalid writes cannot replace persisted model backup rules")
			require.NoError(t, UpdateRequestPolicyOptions(map[string]string{"channel_failover_setting.fallback_rules": "[]"}))
			loadOptionsFromDatabase()
			assert.Empty(t, CurrentRequestPolicy().Failover.FallbackRules, "clearing rules persists instead of reusing old entries")
		})
	}
}
