package model

import (
	"fmt"
	"math"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// channelsRevision is the id of the newest channels sync task, 0 without one.
func channelsRevision(t *testing.T) int64 {
	t.Helper()
	revisions, err := GetLatestSyncRevisions()
	require.NoError(t, err)
	return revisions[SyncTopicChannels]
}

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
	const normal = `{"model_ratio":1,"model_price":-1,"group_ratio":1,"cache_tokens":50}`
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
		{name: "zero is valid", other: `{"model_ratio":0,"model_price":-1,"group_ratio":1,"cache_tokens":0}`, prompt: 100, rate: common.GetPointer(0.0), price: common.GetPointer(0.0), cacheCount: 10, priceCount: 10},
		{name: "anthropic cache added once", other: `{"billing_mode":"tiered_expr","billing_unit":"token","group_ratio":1,"usage_semantic":"anthropic","cache_tokens":60,"cache_write_tokens":30,"cache_creation_tokens":30,"cache_creation_tokens_5m":10,"cache_creation_tokens_1h":20}`, prompt: 10, quota: 100, rate: common.GetPointer(60.0), price: common.GetPointer(100.0), cacheCount: 10, priceCount: 10},
		{name: "modern openai overrides claude request flag", other: `{"model_ratio":1,"model_price":-1,"group_ratio":1,"usage_semantic":"openai","claude":true,"cache_tokens":50,"cache_write_tokens":20}`, prompt: 100, quota: 100, rate: common.GetPointer(50.0), price: common.GetPointer(100.0), cacheCount: 10, priceCount: 10},
		{name: "subscription actual usage", other: `{"model_ratio":1,"model_price":-1,"group_ratio":1,"cache_tokens":50,"billing_source":"subscription","subscription_consumed":200}`, prompt: 100, quota: 100, rate: common.GetPointer(50.0), price: common.GetPointer(200.0), cacheCount: 10, priceCount: 10},
		{name: "unknown cache retains price only", other: `{"model_ratio":1,"model_price":-1,"group_ratio":1}`, prompt: 100, quota: 100, price: common.GetPointer(100.0), priceCount: 10},
		{name: "anthropic unknown cache read", other: `{"model_ratio":1,"model_price":-1,"group_ratio":1,"usage_semantic":"anthropic","cache_creation_tokens":0}`, prompt: 100, quota: 100},
		{name: "anthropic unknown cache write", other: `{"model_ratio":1,"model_price":-1,"group_ratio":1,"usage_semantic":"anthropic","cache_tokens":50}`, prompt: 100, quota: 100},
		{name: "anthropic explicit total", other: `{"model_ratio":1,"model_price":-1,"group_ratio":1,"usage_semantic":"anthropic","input_tokens_total":100}`, prompt: 10, quota: 100, price: common.GetPointer(100.0), priceCount: 10},
		{name: "negative quota", other: normal, prompt: 100, quota: -1},
		{name: "negative input", other: normal, prompt: -1, quota: 100},
		{name: "invalid cache count", other: `{"model_ratio":1,"group_ratio":1,"cache_tokens":101}`, prompt: 100, quota: 100},
		{name: "fractional token count", other: `{"model_ratio":1,"group_ratio":1,"cache_tokens":0.5}`, prompt: 100, quota: 100},
		{name: "invalid metadata", other: "{", prompt: 100, quota: 100},
		{name: "fixed request", other: `{"model_ratio":1,"model_price":0,"group_ratio":1,"cache_tokens":50}`, prompt: 100, quota: 100},
		{name: "tiered request", other: `{"billing_mode":"tiered_expr","billing_unit":"request","group_ratio":1,"cache_tokens":50}`, prompt: 100, quota: 100},
		{name: "media tokens", other: `{"model_ratio":1,"group_ratio":1,"cache_tokens":50,"billing_tokens":{"img":1}}`, prompt: 100, quota: 100},
		{name: "task", other: `{"model_ratio":1,"group_ratio":1,"cache_tokens":50,"is_task":true}`, prompt: 100, quota: 100},
		{name: "audio", other: `{"model_ratio":1,"group_ratio":1,"cache_tokens":50,"audio_input":1}`, prompt: 100, quota: 100},
		{name: "abnormal stream", other: `{"model_ratio":1,"group_ratio":1,"cache_tokens":50,"stream_status":{"status":"error"}}`, prompt: 100, quota: 100},
		{name: "client gone excludes both metrics", other: `{"model_ratio":1,"group_ratio":1,"cache_tokens":50,"stream_status":{"status":"error","end_reason":"client_gone"}}`, prompt: 100, quota: 100},
		{name: "client gone overrides completed status", other: `{"model_ratio":1,"group_ratio":1,"cache_tokens":50,"stream_status":{"status":"ok","response_status":"completed","end_reason":"client_gone"}}`, prompt: 100, quota: 100},
		{name: "normal completed stream", other: `{"model_ratio":1,"group_ratio":1,"cache_tokens":50,"stream_status":{"status":"ok","response_status":"completed","end_reason":"done"}}`, prompt: 100, quota: 100, rate: common.GetPointer(50.0), price: common.GetPointer(100.0), cacheCount: 10, priceCount: 10},
		{name: "rejected", other: `{"model_ratio":1,"group_ratio":1,"cache_tokens":50,"admin_info":{"reject_reason":"rejected"}}`, prompt: 100, quota: 100},
		{name: "saturated", other: `{"model_ratio":1,"group_ratio":1,"cache_tokens":50,"admin_info":{"quota_saturation":{"kind":"overflow"}}}`, prompt: 100, quota: 100},
		{name: "estimated usage", other: `{"model_ratio":1,"group_ratio":1,"cache_tokens":50,"admin_info":{"usage_billing_path":"billing-usage-openai-estimated"}}`, prompt: 100, quota: 100},
		{name: "tool charge keeps cache only", other: `{"model_ratio":1,"group_ratio":1,"cache_tokens":50,"tool_surcharges":[{"count":1,"price":1}]}`, prompt: 100, quota: 100, rate: common.GetPointer(50.0), cacheCount: 10},
		{name: "discount removed", other: `{"model_ratio":1,"group_ratio":0.5,"cache_tokens":50}`, prompt: 100, quota: 100, rate: common.GetPointer(50.0), price: common.GetPointer(200.0), cacheCount: 10, priceCount: 10},
		{name: "markup removed", other: `{"model_ratio":1,"group_ratio":2,"cache_tokens":50}`, prompt: 100, quota: 100, rate: common.GetPointer(50.0), price: common.GetPointer(50.0), cacheCount: 10, priceCount: 10},
		{name: "special group ratio applied once", other: `{"model_ratio":1,"group_ratio":0.25,"user_group_ratio":0.25,"cache_tokens":50}`, prompt: 100, quota: 100, rate: common.GetPointer(50.0), price: common.GetPointer(400.0), cacheCount: 10, priceCount: 10},
		{name: "tiered discount removed", other: `{"billing_mode":"tiered_expr","billing_unit":"token","group_ratio":0.5,"cache_tokens":50}`, prompt: 100, quota: 100, rate: common.GetPointer(50.0), price: common.GetPointer(200.0), cacheCount: 10, priceCount: 10},
		{name: "subscription discount removed", other: `{"model_ratio":1,"group_ratio":0.5,"cache_tokens":50,"billing_source":"subscription","subscription_consumed":200}`, prompt: 100, quota: 100, rate: common.GetPointer(50.0), price: common.GetPointer(400.0), cacheCount: 10, priceCount: 10},
		{name: "missing group ratio keeps cache only", other: `{"model_ratio":1,"user_group_ratio":0.5,"cache_tokens":50}`, prompt: 100, quota: 100, rate: common.GetPointer(50.0), cacheCount: 10},
		{name: "null group ratio keeps cache only", other: `{"model_ratio":1,"group_ratio":null,"cache_tokens":50}`, prompt: 100, quota: 100, rate: common.GetPointer(50.0), cacheCount: 10},
		{name: "zero group ratio keeps cache only", other: `{"model_ratio":1,"group_ratio":0,"cache_tokens":50}`, prompt: 100, quota: 100, rate: common.GetPointer(50.0), cacheCount: 10},
		{name: "negative group ratio keeps cache only", other: `{"model_ratio":1,"group_ratio":-1,"cache_tokens":50}`, prompt: 100, quota: 100, rate: common.GetPointer(50.0), cacheCount: 10},
		{name: "normalization overflow keeps cache only", other: `{"model_ratio":1,"group_ratio":1e-308,"cache_tokens":50}`, prompt: 100, quota: 100, rate: common.GetPointer(50.0), cacheCount: 10},
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
	logs[0].Other = `{"model_ratio":1,"group_ratio":1,"cache_tokens":900}`
	weighted := aggregateChannelProbeUsage(logs, 500000)
	require.NotNil(t, weighted.AverageCacheHitRate)
	require.NotNil(t, weighted.EstimatedPricePer100MTokens)
	assert.InDelta(t, 1350.0/1900*100, *weighted.AverageCacheHitRate, 1e-9)
	assert.InDelta(t, 1000.0/2900*200, *weighted.EstimatedPricePer100MTokens, 1e-9)
	t.Run("client gone leaves valid sample counts and weighted values unchanged", func(t *testing.T) {
		mixed := append(logs, Log{
			PromptTokens: 100000, CompletionTokens: 1, Quota: 100000,
			Other: `{"model_ratio":1,"group_ratio":1,"cache_tokens":99000,"stream_status":{"status":"error","end_reason":"client_gone"}}`,
		})
		assert.Equal(t, weighted, aggregateChannelProbeUsage(mixed, 500000))
	})
}

func TestChannelProbeUsageGroupRatioNormalization(t *testing.T) {
	logs := make([]Log, 10)
	for i := range logs {
		logs[i] = Log{PromptTokens: 100, CompletionTokens: 100, Quota: 50, Other: `{"model_ratio":1,"group_ratio":0.5,"cache_tokens":50}`}
	}
	logs[0] = Log{PromptTokens: 900, CompletionTokens: 100, Quota: 1000, Other: `{"model_ratio":1,"group_ratio":2,"cache_tokens":450}`}
	stats := aggregateChannelProbeUsage(logs, 500000)
	require.NotNil(t, stats.EstimatedPricePer100MTokens)
	assert.EqualValues(t, 10, stats.PriceSampleCount)
	assert.InDelta(t, (9*100.0+500)/(9*200+1000)*200, *stats.EstimatedPricePer100MTokens, 1e-9)

	logs[0].Other = `{"model_ratio":1,"cache_tokens":450}`
	stats = aggregateChannelProbeUsage(logs, 500000)
	assert.EqualValues(t, 10, stats.CacheSampleCount)
	require.NotNil(t, stats.AverageCacheHitRate)
	assert.Equal(t, 50.0, *stats.AverageCacheHitRate)
	assert.EqualValues(t, 9, stats.PriceSampleCount)
	assert.Nil(t, stats.EstimatedPricePer100MTokens)
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
					logs = append(logs, Log{CreatedAt: end - age, Type: LogTypeConsume, ModelName: "model%_test", ChannelId: 7, Group: "group'\"test", PromptTokens: 100, CompletionTokens: 100, Quota: 100, Other: `{"model_ratio":1,"group_ratio":1,"cache_tokens":50}`})
				}
			}
			for _, wrong := range []Log{
				{CreatedAt: end - 60, Type: LogTypeConsume, ModelName: "model%_test", ChannelId: 8, Group: "other"},
				{CreatedAt: end - 60, Type: LogTypeError, ModelName: "model%_test", ChannelId: 7, Group: "group'\"test"},
				{CreatedAt: end - 60, Type: LogTypeConsume, ModelName: "modelX_test", ChannelId: 7, Group: "group'\"test"},
				{CreatedAt: end, Type: LogTypeConsume, ModelName: "model%_test", ChannelId: 7, Group: "group'\"test"},
				{CreatedAt: end - 24*3600 - 1, Type: LogTypeConsume, ModelName: "model%_test", ChannelId: 7, Group: "group'\"test"},
			} {
				wrong.PromptTokens, wrong.CompletionTokens, wrong.Quota, wrong.Other = 100, 100, 9999, `{"model_ratio":1,"group_ratio":1,"cache_tokens":100}`
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
				latest[i] = Log{CreatedAt: end - 30, Type: LogTypeConsume, ModelName: "bounded", ChannelId: 7, PromptTokens: 100, CompletionTokens: 100, Quota: 100, Other: `{"model_ratio":1,"group_ratio":1,"cache_tokens":50}`}
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

// writeAfterNextChannelRead stands for another node: right after the next
// read of the channel, before the writer under test writes, it stores the
// columns write returns for the channel it reads.
func writeAfterNextChannelRead(t *testing.T, channelID int, write func(stored *Channel) map[string]any) {
	t.Helper()
	var done atomic.Bool
	require.NoError(t, DB.Callback().Query().After("gorm:query").Register("test:other_node_write", func(tx *gorm.DB) {
		if !strings.HasSuffix(tx.Statement.Table, "channels") || !done.CompareAndSwap(false, true) {
			return
		}
		var stored Channel
		require.NoError(t, DB.First(&stored, "id = ?", channelID).Error)
		require.NoError(t, DB.Model(&Channel{}).Where("id = ?", channelID).Updates(write(&stored)).Error)
	}))
	t.Cleanup(func() { _ = DB.Callback().Query().Remove("test:other_node_write") })
}

// A writer of other_info writes back only while no other writer, on any node,
// changed it since the read; otherwise it reads again, so both changes are
// kept. It writes its own columns only, so a stale read never puts back the
// key, counters, models or channel_info another writer stored.
func TestChannelOtherInfoWritesKeepConcurrentChanges(t *testing.T) {
	useScheduledChannelDatabase(t)
	common.MemoryCacheEnabled = false

	t.Run("a status change and a balance query of another node", func(t *testing.T) {
		channel := Channel{Name: "status-race", Key: "original-key", Status: common.ChannelStatusEnabled, Models: "original-model", Group: "default", UsedQuota: 100}
		require.NoError(t, channel.Insert())
		concurrentInfo := ChannelInfo{MultiKeyMode: constant.MultiKeyModePolling, MultiKeyPollingIndex: 1}
		writeAfterNextChannelRead(t, channel.Id, func(stored *Channel) map[string]any {
			info := stored.GetOtherInfo()
			info[channelBalanceDetailKey] = map[string]any{"amount": 7, "currency": "USD"}
			stored.SetOtherInfo(info)
			return map[string]any{
				"other_info":   stored.OtherInfo,
				"key":          "rotated-key",
				"used_quota":   gorm.Expr("used_quota + ?", 250),
				"models":       "concurrent-model",
				"channel_info": concurrentInfo,
			}
		})

		require.True(t, UpdateChannelStatus(channel.Id, "", common.ChannelStatusManuallyDisabled, "manual operation"))

		var stored Channel
		require.NoError(t, DB.First(&stored, channel.Id).Error)
		assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
		assert.Equal(t, "manual operation", stored.GetOtherInfo()["status_reason"])
		detail := stored.GetBalanceDetail()
		require.NotNil(t, detail, stored.OtherInfo)
		require.NotNil(t, detail.Amount)
		assert.Equal(t, 7.0, *detail.Amount)
		assert.Equal(t, "rotated-key", stored.Key)
		assert.Equal(t, int64(350), stored.UsedQuota)
		assert.Equal(t, "concurrent-model", stored.Models)
		assert.Equal(t, concurrentInfo, stored.ChannelInfo)
	})

	t.Run("a balance query and a status change of another node", func(t *testing.T) {
		channel := Channel{Name: "balance-race", Key: "key", Status: common.ChannelStatusEnabled, Models: "balance-model", Group: "default"}
		require.NoError(t, channel.Insert())
		// A channel stored before other_info was written has it NULL.
		require.NoError(t, DB.Model(&Channel{}).Where("id = ?", channel.Id).Update("other_info", gorm.Expr("NULL")).Error)
		writeAfterNextChannelRead(t, channel.Id, func(stored *Channel) map[string]any {
			info := stored.GetOtherInfo()
			info["status_reason"] = "other node"
			stored.SetOtherInfo(info)
			return map[string]any{"other_info": stored.OtherInfo, "status": common.ChannelStatusManuallyDisabled}
		})
		amount := 3.5
		detail, err := UpdateChannelBalanceDetail(channel.Id, func(*ChannelBalanceDetail) (*ChannelBalanceDetail, *float64) {
			return &ChannelBalanceDetail{BalanceResult: jsplugin.BalanceResult{Amount: &amount, Currency: "USD"}}, &amount
		})
		require.NoError(t, err)
		require.NotNil(t, detail)

		var stored Channel
		require.NoError(t, DB.First(&stored, channel.Id).Error)
		assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
		assert.Equal(t, "other node", stored.GetOtherInfo()["status_reason"])
		assert.Equal(t, 3.5, stored.Balance)
		assert.NotZero(t, stored.BalanceUpdatedTime)
		require.NotNil(t, stored.GetBalanceDetail())
		assert.Equal(t, 3.5, *stored.GetBalanceDetail().Amount)

		// Storing the same failure twice writes the same values again, which
		// MySQL reports as no changed row.
		for range 2 {
			_, err = UpdateChannelBalanceDetail(channel.Id, func(stored *ChannelBalanceDetail) (*ChannelBalanceDetail, *float64) {
				stored.Error = "upstream failed"
				return stored, nil
			})
			require.NoError(t, err)
		}
		require.NoError(t, DB.First(&stored, channel.Id).Error)
		assert.Equal(t, "upstream failed", stored.GetBalanceDetail().Error)
		assert.Equal(t, "other node", stored.GetOtherInfo()["status_reason"])
	})
}

func TestAutoDisableKeepsManualDisable(t *testing.T) {
	for _, cacheEnabled := range []bool{false, true} {
		for _, tc := range []struct {
			name          string
			multiKey      bool
			initialStatus int
			wantChanged   bool
			wantStatus    int
		}{
			{name: "single key manual disable is kept", initialStatus: common.ChannelStatusManuallyDisabled, wantStatus: common.ChannelStatusManuallyDisabled},
			{name: "multi key manual disable is kept", multiKey: true, initialStatus: common.ChannelStatusManuallyDisabled, wantStatus: common.ChannelStatusManuallyDisabled},
			{name: "single key enabled is auto disabled", initialStatus: common.ChannelStatusEnabled, wantChanged: true, wantStatus: common.ChannelStatusAutoDisabled},
			{name: "multi key enabled is auto disabled", multiKey: true, initialStatus: common.ChannelStatusEnabled, wantChanged: true, wantStatus: common.ChannelStatusAutoDisabled},
		} {
			t.Run(fmt.Sprintf("cache=%t/%s", cacheEnabled, tc.name), func(t *testing.T) {
				setupChannelStatusTest(t)
				common.MemoryCacheEnabled = cacheEnabled

				channel := Channel{Name: t.Name(), Key: "key-a", Status: tc.initialStatus, Models: "test-model", Group: "default"}
				usingKey := "key-a"
				if tc.multiKey {
					// key-b already failed, so a failure on key-a disables every key.
					channel.Key = "key-a\nkey-b"
					channel.ChannelInfo = ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyStatusList: map[int]int{1: common.ChannelStatusAutoDisabled}}
				}
				channel.SetOtherInfo(map[string]any{"status_reason": "manual operation", "status_time": int64(1234)})
				require.NoError(t, channel.Insert())
				InitChannelCache()

				changed := UpdateChannelStatus(channel.Id, usingKey, common.ChannelStatusAutoDisabled, "provider rejected key")
				assert.Equal(t, tc.wantChanged, changed)

				stored, err := GetChannelById(channel.Id, true)
				require.NoError(t, err)
				assert.Equal(t, tc.wantStatus, stored.Status)
				if !tc.wantChanged {
					assert.Equal(t, "manual operation", stored.GetOtherInfo()["status_reason"])
					assert.Equal(t, float64(1234), stored.GetOtherInfo()["status_time"])
					assert.Equal(t, channel.ChannelInfo.MultiKeyStatusList, stored.ChannelInfo.MultiKeyStatusList)
				}
				var abilities []Ability
				require.NoError(t, DB.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
				require.NotEmpty(t, abilities)
				for _, ability := range abilities {
					assert.False(t, ability.Enabled)
				}
				if cacheEnabled {
					cached, err := CacheGetChannel(channel.Id)
					require.NoError(t, err)
					assert.Equal(t, tc.wantStatus, cached.Status)
					assert.Equal(t, stored.ChannelInfo.MultiKeyStatusList, cached.ChannelInfo.MultiKeyStatusList)
				}
			})
		}
	}
}

// A status set by hand reaches every node at once through the channels topic.
// An automatic change, made for the key a request or a channel test used, is
// left to the periodic channel sync and bumps nothing.
func TestUpdateChannelStatusBumpsChannelsOnlyForChangesByHand(t *testing.T) {
	for _, cacheEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("cache=%t", cacheEnabled), func(t *testing.T) {
			setupChannelStatusTest(t)
			common.MemoryCacheEnabled = cacheEnabled
			revision := func() int64 { return channelsRevision(t) }
			single := Channel{Name: "status-single", Key: "key-a", Status: common.ChannelStatusEnabled, Models: "status-model", Group: "default"}
			multi := Channel{Name: "status-multi", Key: "key-a\nkey-b", Status: common.ChannelStatusEnabled, Models: "status-model", Group: "default",
				ChannelInfo: ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyMode: constant.MultiKeyModePolling}}
			require.NoError(t, single.Insert())
			require.NoError(t, multi.Insert())
			InitChannelCache()

			for _, step := range []struct {
				name     string
				id       int
				usingKey string
				status   int
				bump     bool
			}{
				{name: "a request disables a key", id: multi.Id, usingKey: "key-a", status: common.ChannelStatusAutoDisabled},
				{name: "a request disables a single-key channel", id: single.Id, usingKey: "key-a", status: common.ChannelStatusAutoDisabled},
				{name: "a test re-enables the channel", id: single.Id, usingKey: "key-a", status: common.ChannelStatusEnabled},
				{name: "an admin disables a channel", id: single.Id, status: common.ChannelStatusManuallyDisabled, bump: true},
				{name: "an admin disables it again", id: single.Id, status: common.ChannelStatusManuallyDisabled},
				{name: "an admin enables a channel", id: single.Id, status: common.ChannelStatusEnabled, bump: true},
				{name: "an admin disables a multi-key channel", id: multi.Id, status: common.ChannelStatusManuallyDisabled, bump: true},
			} {
				before := revision()
				changed := UpdateChannelStatus(step.id, step.usingKey, step.status, step.name)
				assert.Equal(t, step.bump || step.usingKey != "", changed, step.name)
				assert.Equal(t, step.bump, before != revision(), step.name)
			}
		})
	}
}

// The master runs a plugin's scheduled hook when a key is disabled
// automatically, so a follower's automatic disable of such a key bumps the
// channels topic; the master's own, or one for a plugin without the hook,
// does not.
func TestAutoDisableOfAScheduledPluginKeyBumpsChannelsOnFollowers(t *testing.T) {
	setupChannelStatusTest(t)
	master := common.IsMasterNode
	t.Cleanup(func() { common.IsMasterNode = master })
	for key, hooks := range map[string]string{
		"status-scheduled": `export function scheduled() { return {next: 3600}; }`,
		"status-plain":     "",
	} {
		_, err := jsplugin.DefaultRegistry.Register(`export const meta = {apiVersion: 1, key: "`+key+`", name: "Status", version: "1.0.0", author: {name: "Test"}, relay: [{incoming_path: "/v1/chat/completions", upstream_path: "/chat"}]};`+hooks, jsplugin.Options{})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, jsplugin.DefaultRegistry.Unregister(key)) })
	}
	revision := func() int64 { return channelsRevision(t) }

	for _, tc := range []struct {
		plugin string
		master bool
		bump   bool
	}{
		{plugin: "status-scheduled", bump: true},
		{plugin: "status-scheduled", master: true},
		{plugin: "status-plain"},
	} {
		t.Run(fmt.Sprintf("%s/master=%t", tc.plugin, tc.master), func(t *testing.T) {
			common.IsMasterNode = tc.master
			channel := Channel{Type: constant.ChannelTypeTaskPlugin, Name: t.Name(), Key: "key-a", Status: common.ChannelStatusEnabled, Models: "status-model", Group: "default"}
			channel.SetSetting(dto.ChannelSettings{TaskPluginKey: tc.plugin})
			require.NoError(t, channel.Insert())

			before := revision()
			require.True(t, UpdateChannelStatus(channel.Id, "key-a", common.ChannelStatusAutoDisabled, "quota"))
			assert.Equal(t, tc.bump, before != revision())
		})
	}
}
