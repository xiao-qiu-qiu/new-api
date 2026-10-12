package model

import (
	"math"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func setupChannelStatusTest(t *testing.T) {
	t.Helper()
	truncateTables(t)
	require.NoError(t, DB.Exec("DELETE FROM abilities").Error)
	require.NoError(t, DB.Exec("DELETE FROM channels").Error)

	memoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		common.MemoryCacheEnabled = memoryCacheEnabled
	})
}

func TestChannelProbeUsageEstimates(t *testing.T) {
	const normal = `{"model_ratio":1,"model_price":-1,"cache_tokens":50}`
	for _, tc := range []struct {
		name       string
		other      string
		prompt     int
		quota      int
		rate       *float64
		price      *float64
		cacheCount int64
		priceCount int64
	}{
		{name: "normal", other: normal, prompt: 100, quota: 100, rate: common.GetPointer(50.0), price: common.GetPointer(100.0), cacheCount: 10, priceCount: 10},
		{name: "zero is valid", other: `{"model_ratio":0,"model_price":-1,"cache_tokens":0}`, prompt: 100, rate: common.GetPointer(0.0), price: common.GetPointer(0.0), cacheCount: 10, priceCount: 10},
		{name: "anthropic cache added once", other: `{"billing_mode":"tiered_expr","billing_unit":"token","usage_semantic":"anthropic","cache_tokens":60,"cache_write_tokens":30,"cache_creation_tokens":30,"cache_creation_tokens_5m":10,"cache_creation_tokens_1h":20}`, prompt: 10, quota: 100, rate: common.GetPointer(60.0), price: common.GetPointer(100.0), cacheCount: 10, priceCount: 10},
		{name: "modern openai overrides claude request flag", other: `{"model_ratio":1,"model_price":-1,"usage_semantic":"openai","claude":true,"cache_tokens":50,"cache_write_tokens":20}`, prompt: 100, quota: 100, rate: common.GetPointer(50.0), price: common.GetPointer(100.0), cacheCount: 10, priceCount: 10},
		{name: "subscription actual usage", other: `{"model_ratio":1,"model_price":-1,"cache_tokens":50,"billing_source":"subscription","subscription_consumed":200}`, prompt: 100, quota: 100, rate: common.GetPointer(50.0), price: common.GetPointer(200.0), cacheCount: 10, priceCount: 10},
		{name: "unknown cache retains price only", other: `{"model_ratio":1,"model_price":-1}`, prompt: 100, quota: 100, price: common.GetPointer(100.0), priceCount: 10},
		{name: "anthropic unknown cache read", other: `{"model_ratio":1,"model_price":-1,"usage_semantic":"anthropic","cache_creation_tokens":0}`, prompt: 100, quota: 100},
		{name: "anthropic unknown cache write", other: `{"model_ratio":1,"model_price":-1,"usage_semantic":"anthropic","cache_tokens":50}`, prompt: 100, quota: 100},
		{name: "anthropic explicit total", other: `{"model_ratio":1,"model_price":-1,"usage_semantic":"anthropic","input_tokens_total":100}`, prompt: 10, quota: 100, price: common.GetPointer(100.0), priceCount: 10},
		{name: "negative quota", other: normal, prompt: 100, quota: -1},
		{name: "negative input", other: normal, prompt: -1, quota: 100},
		{name: "invalid cache count", other: `{"model_ratio":1,"cache_tokens":101}`, prompt: 100, quota: 100},
		{name: "fractional token count", other: `{"model_ratio":1,"cache_tokens":0.5}`, prompt: 100, quota: 100},
		{name: "invalid metadata", other: "{", prompt: 100, quota: 100},
		{name: "fixed request", other: `{"model_ratio":1,"model_price":0,"cache_tokens":50}`, prompt: 100, quota: 100},
		{name: "tiered request", other: `{"billing_mode":"tiered_expr","billing_unit":"request","cache_tokens":50}`, prompt: 100, quota: 100},
		{name: "media tokens", other: `{"model_ratio":1,"cache_tokens":50,"billing_tokens":{"img":1}}`, prompt: 100, quota: 100},
		{name: "task", other: `{"model_ratio":1,"cache_tokens":50,"is_task":true}`, prompt: 100, quota: 100},
		{name: "audio", other: `{"model_ratio":1,"cache_tokens":50,"audio_input":1}`, prompt: 100, quota: 100},
		{name: "abnormal stream", other: `{"model_ratio":1,"cache_tokens":50,"stream_status":{"status":"error"}}`, prompt: 100, quota: 100},
		{name: "normal completed stream", other: `{"model_ratio":1,"cache_tokens":50,"stream_status":{"status":"ok","response_status":"completed","end_reason":"done"}}`, prompt: 100, quota: 100, rate: common.GetPointer(50.0), price: common.GetPointer(100.0), cacheCount: 10, priceCount: 10},
		{name: "rejected", other: `{"model_ratio":1,"cache_tokens":50,"admin_info":{"reject_reason":"rejected"}}`, prompt: 100, quota: 100},
		{name: "saturated", other: `{"model_ratio":1,"cache_tokens":50,"admin_info":{"quota_saturation":{"kind":"overflow"}}}`, prompt: 100, quota: 100},
		{name: "estimated usage", other: `{"model_ratio":1,"cache_tokens":50,"admin_info":{"usage_billing_path":"billing-usage-openai-estimated"}}`, prompt: 100, quota: 100},
		{name: "tool charge keeps cache only", other: `{"model_ratio":1,"cache_tokens":50,"tool_surcharges":[{"count":1,"price":1}]}`, prompt: 100, quota: 100, rate: common.GetPointer(50.0), cacheCount: 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := make([]Log, 10)
			for i := range logs {
				logs[i] = Log{PromptTokens: tc.prompt, CompletionTokens: 100, Quota: tc.quota, Other: tc.other}
			}
			stats := aggregateChannelProbeUsage(logs, 500000)
			assert.Equal(t, tc.cacheCount, stats.CacheSampleCount)
			assert.Equal(t, tc.priceCount, stats.PriceSampleCount)
			assert.Equal(t, tc.rate, stats.AverageCacheHitRate)
			assert.Equal(t, tc.price, stats.EstimatedPricePer100MTokens)
		})
	}
	logs := make([]Log, 10)
	for i := range logs {
		logs[i] = Log{PromptTokens: 100, CompletionTokens: 100, Quota: 100, Other: normal}
	}
	insufficient := aggregateChannelProbeUsage(logs[:9], 500000)
	assert.EqualValues(t, 9, insufficient.CacheSampleCount)
	assert.EqualValues(t, 9, insufficient.PriceSampleCount)
	assert.Nil(t, insufficient.AverageCacheHitRate)
	assert.Nil(t, insufficient.EstimatedPricePer100MTokens)
	assert.Nil(t, aggregateChannelProbeUsage(logs, math.SmallestNonzeroFloat64).EstimatedPricePer100MTokens)
	logs[0].PromptTokens = 1000
	logs[0].Other = `{"model_ratio":1,"cache_tokens":900}`
	weighted := aggregateChannelProbeUsage(logs, 500000)
	require.NotNil(t, weighted.AverageCacheHitRate)
	require.NotNil(t, weighted.EstimatedPricePer100MTokens)
	assert.InDelta(t, 1350.0/1900*100, *weighted.AverageCacheHitRate, 1e-9)
	assert.InDelta(t, 1000.0/2900*200, *weighted.EstimatedPricePer100MTokens, 1e-9)
}

func TestChannelProbeUsageDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(":memory:")
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
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "probe_usage_test_"}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			previousDB, previousType, previousEnabled := LOG_DB, common.LogDatabaseType(), common.LogConsumeEnabled
			LOG_DB = db
			common.SetLogDatabaseType(common.DatabaseType(dialect))
			common.LogConsumeEnabled = true
			t.Cleanup(func() {
				LOG_DB = previousDB
				common.SetLogDatabaseType(previousType)
				common.LogConsumeEnabled = previousEnabled
				require.NoError(t, db.Migrator().DropTable(&Log{}))
				require.NoError(t, sqlDB.Close())
			})
			require.NoError(t, db.AutoMigrate(&Log{}))
			var version string
			query := "SELECT version()"
			if dialect == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("%s version: %s", dialect, version)
			end := time.Now().Unix()
			var logs []Log
			for _, age := range []int64{60, 2 * 3600, 8 * 3600, 16 * 3600} {
				for range 10 {
					logs = append(logs, Log{CreatedAt: end - age, Type: LogTypeConsume, ModelName: "model%_test", ChannelId: 7, Group: "group'\"test", PromptTokens: 100, CompletionTokens: 100, Quota: 100, Other: `{"model_ratio":1,"cache_tokens":50}`})
				}
			}
			for _, wrong := range []Log{
				{CreatedAt: end - 60, Type: LogTypeConsume, ModelName: "model%_test", ChannelId: 8, Group: "other"},
				{CreatedAt: end - 60, Type: LogTypeError, ModelName: "model%_test", ChannelId: 7, Group: "group'\"test"},
				{CreatedAt: end - 60, Type: LogTypeConsume, ModelName: "modelX_test", ChannelId: 7, Group: "group'\"test"},
				{CreatedAt: end, Type: LogTypeConsume, ModelName: "model%_test", ChannelId: 7, Group: "group'\"test"},
				{CreatedAt: end - 24*3600 - 1, Type: LogTypeConsume, ModelName: "model%_test", ChannelId: 7, Group: "group'\"test"},
			} {
				wrong.PromptTokens, wrong.CompletionTokens, wrong.Quota, wrong.Other = 100, 100, 9999, `{"model_ratio":1,"cache_tokens":100}`
				logs = append(logs, wrong)
			}
			require.NoError(t, db.Create(&logs).Error)
			for i, hours := range []int{1, 6, 12, 24} {
				for _, probe := range []ChannelProbe{{Scope: "group", GroupName: "group'\"test"}, {Scope: "upstream", ChannelID: 7}} {
					stats, err := GetChannelProbeUsageStats(probe, "model%_test", end-int64(hours)*3600, end)
					require.NoError(t, err)
					assert.EqualValues(t, (i+1)*10, stats.CacheSampleCount)
					assert.EqualValues(t, (i+1)*10, stats.PriceSampleCount)
					require.NotNil(t, stats.AverageCacheHitRate)
					require.NotNil(t, stats.EstimatedPricePer100MTokens)
					assert.Equal(t, 50.0, *stats.AverageCacheHitRate)
					assert.Equal(t, 5e7/common.QuotaPerUnit, *stats.EstimatedPricePer100MTokens)
				}
			}
			latest := make([]Log, 1001)
			for i := range latest {
				latest[i] = Log{CreatedAt: end - 30, Type: LogTypeConsume, ModelName: "bounded", ChannelId: 7, PromptTokens: 100, CompletionTokens: 100, Quota: 100, Other: `{"model_ratio":1,"cache_tokens":50}`}
			}
			latest[0].Quota = 999999
			require.NoError(t, db.CreateInBatches(&latest, 100).Error)
			probe := ChannelProbe{Scope: "upstream", ChannelID: 7}
			stats, err := GetChannelProbeUsageStats(probe, "bounded", end-3600, end)
			require.NoError(t, err)
			assert.EqualValues(t, 1000, stats.PriceSampleCount)
			require.NotNil(t, stats.EstimatedPricePer100MTokens)
			assert.InDelta(t, 5e7/common.QuotaPerUnit, *stats.EstimatedPricePer100MTokens, 1e-9)
			common.LogConsumeEnabled = false
			disabled, err := GetChannelProbeUsageStats(probe, "bounded", end-3600, end)
			require.NoError(t, err)
			assert.Equal(t, ChannelProbeUsageStats{}, disabled)
			common.LogConsumeEnabled = true
			require.NoError(t, db.Migrator().DropTable(&Log{}))
			_, err = GetChannelProbeUsageStats(probe, "unavailable", end-3600, end)
			require.Error(t, err)
		})
	}
}

func TestUpdateChannelStatusPersistsMultiKeyState(t *testing.T) {
	setupChannelStatusTest(t)

	channel := Channel{
		Name:   "multi-key-status",
		Key:    "key-a\nkey-b",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: ChannelInfo{
			IsMultiKey:           true,
			MultiKeySize:         2,
			MultiKeyMode:         constant.MultiKeyModePolling,
			MultiKeyPollingIndex: 1,
		},
	}
	require.NoError(t, DB.Create(&channel).Error)

	changed := UpdateChannelStatus(channel.Id, "key-a", common.ChannelStatusAutoDisabled, "provider rejected key")
	require.True(t, changed)

	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.ChannelInfo.MultiKeyStatusList[0])
	assert.Equal(t, "provider rejected key", stored.ChannelInfo.MultiKeyDisabledReason[0])
	assert.NotZero(t, stored.ChannelInfo.MultiKeyDisabledTime[0])
	assert.Equal(t, 1, stored.ChannelInfo.MultiKeyPollingIndex)
}

func TestSaveStatusStateFromSingleKeySnapshotPreservesUnownedColumns(t *testing.T) {
	setupChannelStatusTest(t)

	channel := Channel{
		Name:        "single-key-status",
		Key:         "original-key",
		Status:      common.ChannelStatusEnabled,
		Models:      "original-model",
		Group:       "default",
		UsedQuota:   100,
		ChannelInfo: ChannelInfo{},
	}
	require.NoError(t, DB.Create(&channel).Error)

	stale, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)

	concurrentChannelInfo := ChannelInfo{
		IsMultiKey:           true,
		MultiKeySize:         2,
		MultiKeyMode:         constant.MultiKeyModePolling,
		MultiKeyPollingIndex: 1,
	}
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
		"key":          "rotated-key",
		"used_quota":   gorm.Expr("used_quota + ?", 250),
		"models":       "concurrent-model",
		"channel_info": concurrentChannelInfo,
	}).Error)

	stale.Status = common.ChannelStatusManuallyDisabled
	stale.SetOtherInfo(map[string]any{
		"status_reason": "manual operation",
		"status_time":   int64(1234),
	})
	require.NoError(t, stale.saveStatusState())

	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
	assert.Equal(t, "rotated-key", stored.Key)
	assert.Equal(t, int64(350), stored.UsedQuota)
	assert.Equal(t, "concurrent-model", stored.Models)
	assert.Equal(t, concurrentChannelInfo, stored.ChannelInfo)

	otherInfo := stored.GetOtherInfo()
	assert.Equal(t, "manual operation", otherInfo["status_reason"])
	assert.Equal(t, float64(1234), otherInfo["status_time"])
}
