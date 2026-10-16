package service

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type legacyRankingPreference struct {
	UserID            int `gorm:"primaryKey;autoIncrement:false"`
	Participating     bool
	Prompted          bool
	Nickname          string `gorm:"size:96"`
	NicknameChangedAt int64
}

func (legacyRankingPreference) TableName() string { return "user_ranking_preferences" }

func TestUserRankingTokenSemantics(t *testing.T) {
	for _, test := range []struct {
		name, other          string
		prompt, output, want int64
	}{
		{"openai cache is already included", `{"cache_tokens":80}`, 100, 20, 120},
		{"anthropic read and split write", `{"usage_semantic":"anthropic","cache_tokens":80,"cache_creation_tokens":30,"cache_creation_tokens_5m":10,"cache_creation_tokens_1h":20}`, 100, 20, 230},
		{"legacy claude", `{"claude":true,"cache_tokens":80,"cache_write_tokens":30}`, 100, 20, 230},
		{"explicit total wins", `{"input_tokens_total":150,"claude":true,"cache_tokens":80}`, 100, 20, 170},
		{"explicit openai prevents legacy addition", `{"usage_semantic":"openai","claude":true,"cache_tokens":80}`, 100, 20, 120},
		{"missing metadata", "", 100, 20, 120},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, model.RankingTokenTotal(test.prompt, test.output, test.other))
		})
	}
	assert.Equal(t, "张**四", model.MaskRankingName("张三李四"))
	assert.Equal(t, "a***e", model.MaskRankingName("alice"))
	assert.Equal(t, "**", model.MaskRankingName("李四"))
}

func TestUserRankingsConsentAndTotals(t *testing.T) {
	for _, engine := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			var dialector gorm.Dialector
			switch engine {
			case "mysql":
				dsn := os.Getenv("NEWAPI_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("NEWAPI_TEST_MYSQL_DSN is not configured")
				}
				dialector = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("NEWAPI_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("NEWAPI_TEST_POSTGRES_DSN is not configured")
				}
				dialector = postgres.Open(dsn)
			default:
				dialector = sqlite.Open(":memory:")
			}
			db, err := gorm.Open(dialector, &gorm.Config{})
			require.NoError(t, err)
			versionQuery := "SELECT version()"
			if engine == "sqlite" {
				versionQuery = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
			t.Logf("Database version: %s", version)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = sqlDB.Close() })
			previousDB, previousLogs := model.DB, model.LOG_DB
			model.DB, model.LOG_DB = db, db
			t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogs; InvalidateUserRankings() })
			InvalidateUserRankings()
			// Existing users/logs precede the additive migration; running it twice
			// must preserve their data and the unique consent record.
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Log{}))
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.UserRankingPreference{}))
			}
			for _, table := range []any{&model.User{}, &model.Log{}, &model.UserRankingPreference{}} {
				if db.Migrator().HasTable(table) {
					require.NoError(t, db.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(table).Error)
				}
			}
			users := []model.User{
				{Id: 91001, Username: "alice", AffCode: "rank-alice", DisplayName: "张三李四", Status: common.UserStatusEnabled},
				{Id: 91002, Username: "never-joined", AffCode: "rank-viewer", Status: common.UserStatusEnabled},
				{Id: 91003, Username: "disabled", AffCode: "rank-disabled", Status: common.UserStatusDisabled},
			}
			require.NoError(t, db.Create(&users).Error)
			now := time.Now().Unix()
			logs := []model.Log{
				{UserId: 91001, CreatedAt: now - 10, Type: model.LogTypeConsume, Quota: 100, PromptTokens: 100, CompletionTokens: 20, Other: `{"cache_tokens":80}`},
				{UserId: 91001, CreatedAt: now - 10, Type: model.LogTypeConsume, ModelName: "beta", Quota: 900, PromptTokens: 10, CompletionTokens: 20, Other: `{"usage_semantic":"anthropic","cache_tokens":80,"cache_creation_tokens":30}`},
				{UserId: 91001, CreatedAt: now - 10, Type: model.LogTypeError, ModelName: "not-consumed", Quota: 99999, PromptTokens: 1000},
				{UserId: 91001, CreatedAt: now - 8*86400, Type: model.LogTypeConsume, ModelName: "expired", Quota: 99999, PromptTokens: 1000},
				{UserId: 91002, CreatedAt: now - 10, Type: model.LogTypeConsume, ModelName: "gamma", Quota: 500, PromptTokens: 9000},
				{UserId: 91003, CreatedAt: now - 10, Type: model.LogTypeConsume, ModelName: "disabled", Quota: 99999, PromptTokens: 8000},
				{UserId: 91001, CreatedAt: now - 10, Type: model.LogTypeConsume},
			}
			require.NoError(t, db.Create(&logs).Error)
			// Older consume logs can have NULL quota and unknown model names.
			require.NoError(t, db.Model(&model.Log{}).Where("id = ?", logs[len(logs)-1].Id).
				Updates(map[string]any{"quota": nil, "model_name": nil}).Error)
			// Upgrade the prior release's consent schema, preserving an existing choice.
			require.NoError(t, db.Migrator().DropTable(&model.UserRankingPreference{}))
			require.NoError(t, db.AutoMigrate(&legacyRankingPreference{}))
			require.NoError(t, db.Create(&legacyRankingPreference{UserID: 91002, Prompted: true}).Error)
			require.NoError(t, db.Create(&legacyRankingPreference{UserID: 91001, Participating: true, Prompted: true}).Error)
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.UserRankingPreference{}))
			}
			ctx := context.Background()
			preference, err := model.GetUserRankingPreference(ctx, 91001)
			require.NoError(t, err)
			assert.True(t, preference.Participating)
			assert.True(t, preference.Prompted)
			assert.False(t, preference.SpendingParticipating)
			assert.False(t, preference.TokenUsagePublic)
			assert.False(t, preference.SpendingUsagePublic)
			require.NoError(t, model.SetUserRankingPreference(ctx, 91001, true))
			require.NoError(t, model.SetUserRankingPreference(ctx, 91003, true))
			result, err := GetUserRankings(ctx, "week", 91001)
			require.NoError(t, err)
			require.Len(t, result.Users, 1)
			assert.Equal(t, "a***e", result.Users[0].Name)
			assert.Equal(t, "a***e", result.Users[0].MaskedUsername)
			assert.Nil(t, result.Users[0].TotalTokens)
			assert.Nil(t, result.Users[0].TotalQuota)
			assert.Empty(t, result.Users[0].Models)
			assert.False(t, result.Users[0].UsagePublic)
			assert.Zero(t, result.TotalTokens)
			assert.True(t, result.Users[0].IsSelf)
			// Explicit disclosure is a new choice, independent of legacy opt-in.
			public := true
			require.NoError(t, model.UpdateUserRankingPreference(ctx, 91001, model.UserRankingPreferenceUpdate{TokenUsagePublic: &public}))
			result, err = GetUserRankings(ctx, "week", 91001)
			require.NoError(t, err)
			assert.Equal(t, int64(260), result.TotalTokens)
			t.Run("nickname_is_public_and_cooldown_survives_consent_changes", func(t *testing.T) {
				at := time.Unix(now, 0)
				require.NoError(t, model.SetRankingNickname(ctx, 91001, "  星河旅人  ", at))
				updated, err := GetUserRankings(ctx, "today", 91001)
				require.NoError(t, err)
				require.Len(t, updated.Users, 1)
				assert.Equal(t, "星河旅人", updated.Users[0].Name)
				assert.Equal(t, "星河旅人", updated.Users[0].Nickname)
				assert.Equal(t, "a***e", updated.Users[0].MaskedUsername)
				assert.Equal(t, int64(86400), updated.PeriodEnd-updated.PeriodStart)
				require.NoError(t, model.SetRankingNickname(ctx, 91001, "星河旅人", at.Add(time.Hour)))
				require.ErrorIs(t, model.SetRankingNickname(ctx, 91001, "新昵称", at.Add(model.RankingNicknameCooldown-time.Second)), model.ErrRankingNicknameCooldown)
				require.ErrorIs(t, model.SetRankingNickname(ctx, 91001, "", at.Add(time.Hour)), model.ErrRankingNicknameCooldown)
				require.NoError(t, model.SetUserRankingPreference(ctx, 91001, false))
				require.NoError(t, model.SetUserRankingPreference(ctx, 91001, true))
				pref, err := model.GetUserRankingPreference(ctx, 91001)
				require.NoError(t, err)
				assert.Equal(t, "星河旅人", pref.Nickname)
				assert.Equal(t, now+int64(model.RankingNicknameCooldown/time.Second), pref.NextNicknameChangeAt)
				require.ErrorIs(t, model.SetRankingNickname(ctx, 91001, strings.Repeat("字", 25), at), model.ErrInvalidRankingNickname)
				require.ErrorIs(t, model.SetRankingNickname(ctx, 91001, "坏\n名字", at), model.ErrInvalidRankingNickname)
				require.NoError(t, model.SetRankingNickname(ctx, 91001, "", at.Add(model.RankingNicknameCooldown)))
				updated, err = GetUserRankings(ctx, "today", 91001)
				require.NoError(t, err)
				assert.Equal(t, "a***e", updated.Users[0].Name)
				assert.Empty(t, updated.Users[0].Nickname)
				// Setting a nickname before choosing participation must not opt the user in.
				require.NoError(t, model.SetRankingNickname(ctx, 91002, "旁观者", at))
				pref, err = model.GetUserRankingPreference(ctx, 91002)
				require.NoError(t, err)
				assert.False(t, pref.Participating)
				assert.True(t, pref.Prompted)
			})
			// Joining on another instance must also work without invalidation.
			require.NoError(t, model.SetUserRankingPreference(ctx, 91002, true))
			require.NoError(t, model.UpdateUserRankingPreference(ctx, 91002, model.UserRankingPreferenceUpdate{TokenUsagePublic: &public}))
			result, err = GetUserRankings(ctx, "week", 91002)
			require.NoError(t, err)
			require.Len(t, result.Users, 2)
			assert.Equal(t, int64(9260), result.TotalTokens)
			assert.True(t, result.Users[0].IsSelf)
			t.Run("independent_boards_and_live_disclosure", func(t *testing.T) {
				private := false
				// Spending disclosure alone grants neither participation nor access
				// to prices on the token board. The warm cache is not invalidated.
				require.NoError(t, model.UpdateUserRankingPreference(ctx, 91001, model.UserRankingPreferenceUpdate{SpendingUsagePublic: &public}))
				tokens, err := GetUserRankings(ctx, "week", 91001)
				require.NoError(t, err)
				require.NotNil(t, tokens.Self)
				assert.Nil(t, tokens.Self.TotalQuota)
				require.Len(t, tokens.Self.Models, 2)
				assert.Equal(t, "beta", tokens.Self.Models[0].ModelName)
				assert.Equal(t, int64(140), *tokens.Self.Models[0].TotalTokens)
				assert.Nil(t, tokens.Self.Models[0].TotalQuota)
				assert.Empty(t, tokens.Self.Models[1].ModelName)
				assert.Equal(t, int64(120), *tokens.Self.Models[1].TotalTokens)
				assert.Equal(t, *tokens.Self.TotalTokens, *tokens.Self.Models[0].TotalTokens+*tokens.Self.Models[1].TotalTokens)
				spending, err := GetUserRankings(ctx, "week", 91001, "spending")
				require.NoError(t, err)
				assert.Empty(t, spending.Users)
				require.NoError(t, model.UpdateUserRankingPreference(ctx, 91001, model.UserRankingPreferenceUpdate{SpendingParticipating: &public}))
				require.NoError(t, model.SetUserRankingPreference(ctx, 91002, true, true, false, false))
				spending, err = GetUserRankings(ctx, "week", 91002, "spending")
				require.NoError(t, err)
				require.Len(t, spending.Users, 2)
				assert.Equal(t, "spending", spending.Metric)
				assert.Equal(t, 1, spending.Users[0].Rank)
				assert.Equal(t, int64(1000), *spending.Users[0].TotalQuota)
				assert.Equal(t, int64(260), *spending.Users[0].TotalTokens)
				require.Len(t, spending.Users[0].Models, 2)
				assert.Equal(t, "beta", spending.Users[0].Models[0].ModelName)
				assert.Equal(t, int64(900), *spending.Users[0].Models[0].TotalQuota)
				assert.Empty(t, spending.Users[0].Models[1].ModelName)
				assert.Equal(t, int64(100), *spending.Users[0].Models[1].TotalQuota)
				assert.Equal(t, *spending.Users[0].TotalQuota, *spending.Users[0].Models[0].TotalQuota+*spending.Users[0].Models[1].TotalQuota)
				assert.Equal(t, int64(1000), spending.TotalQuota)
				assert.Equal(t, int64(260), spending.TotalTokens)
				require.NotNil(t, spending.Self)
				assert.Equal(t, 2, spending.Self.Rank)
				assert.False(t, spending.Self.UsagePublic)
				// Check actual serialized output, including self, for hidden fields.
				encoded, err := common.Marshal(spending.Self)
				require.NoError(t, err)
				var payload map[string]any
				require.NoError(t, common.Unmarshal(encoded, &payload))
				assert.NotContains(t, payload, "total_tokens")
				assert.NotContains(t, payload, "total_quota")
				assert.NotContains(t, payload, "models")
				// Revoking token disclosure hides token counts even inside a
				// public spending tooltip, and hides all token-board details.
				require.NoError(t, model.UpdateUserRankingPreference(ctx, 91001, model.UserRankingPreferenceUpdate{TokenUsagePublic: &private}))
				spending, err = GetUserRankings(ctx, "week", 91001, "spending")
				require.NoError(t, err)
				require.NotNil(t, spending.Self)
				assert.Nil(t, spending.Self.TotalTokens)
				assert.NotNil(t, spending.Self.TotalQuota)
				assert.Nil(t, spending.Self.Models[0].TotalTokens)
				assert.Zero(t, spending.TotalTokens)
				tokens, err = GetUserRankings(ctx, "week", 91001)
				require.NoError(t, err)
				assert.False(t, tokens.Self.UsagePublic)
				assert.Nil(t, tokens.Self.TotalQuota)
				assert.Empty(t, tokens.Self.Models)
				assert.Zero(t, tokens.TotalTokens)
				assert.Zero(t, tokens.TotalQuota)
				// A partial update keeps the other three choices; legacy setters
				// also keep disclosure preferences while participation gates them.
				pref, err := model.GetUserRankingPreference(ctx, 91001)
				require.NoError(t, err)
				assert.True(t, pref.Participating)
				assert.True(t, pref.SpendingParticipating)
				assert.True(t, pref.SpendingUsagePublic)
				require.NoError(t, model.UpdateUserRankingPreference(ctx, 91001, model.UserRankingPreferenceUpdate{TokenUsagePublic: &public}))
				require.NoError(t, model.UpdateUserRankingPreference(ctx, 91001, model.UserRankingPreferenceUpdate{SpendingParticipating: &private}))
				tokens, err = GetUserRankings(ctx, "week", 91001)
				require.NoError(t, err)
				assert.Nil(t, tokens.Self.TotalQuota)
				assert.Nil(t, tokens.Self.Models[0].TotalQuota)
				spending, err = GetUserRankings(ctx, "week", 91001, "spending")
				require.NoError(t, err)
				require.Len(t, spending.Users, 1)
				assert.Nil(t, spending.Self)
				assert.Zero(t, spending.TotalQuota)
				assert.Zero(t, spending.TotalTokens)
			})
			require.NoError(t, model.SetUserRankingPreference(ctx, 91002, false))
			require.NoError(t, model.SetUserRankingPreference(ctx, 91001, false))
			// Do not invalidate the cache: another instance must respect opt-out.
			result, err = GetUserRankings(ctx, "week", 0)
			require.NoError(t, err)
			assert.Empty(t, result.Users)
			assert.Zero(t, result.TotalTokens)
			preference, err = model.GetUserRankingPreference(ctx, 91001)
			require.NoError(t, err)
			assert.True(t, preference.Prompted)
			assert.False(t, preference.Participating)
			var count int64
			require.NoError(t, db.Model(&model.UserRankingPreference{}).Where("user_id = ?", 91001).Count(&count).Error)
			assert.Equal(t, int64(1), count)
			require.NoError(t, db.AutoMigrate(&model.UserRankingPreference{}))
			require.NoError(t, db.Model(&model.Log{}).Count(&count).Error)
			assert.Equal(t, int64(len(logs)), count)
		})
	}
}

func TestUserRankingPeriodBoundaries(t *testing.T) {
	// Calendar-day spending follows the supplied server location; token periods
	// retain their existing rolling windows, including across local midnight.
	now := time.Date(2026, 10, 16, 0, 5, 0, 0, time.FixedZone("server", 8*3600))
	for _, period := range []string{"today", "week", "month", "year"} {
		config, err := rankingConfig(period)
		require.NoError(t, err)
		start, end := userRankingTimeRange(config, "tokens", now)
		assert.Equal(t, now.Add(-config.duration).Unix(), start)
		assert.Equal(t, now.Unix(), end)
		spendingStart, spendingEnd := userRankingTimeRange(config, "spending", now)
		if period == "today" {
			assert.Equal(t, time.Date(2026, 10, 16, 0, 0, 0, 0, now.Location()).Unix(), spendingStart)
		} else {
			assert.Equal(t, start, spendingStart)
		}
		assert.Equal(t, end, spendingEnd)
	}
	_, err := GetUserRankings(t.Context(), "week", 0, "invalid")
	require.ErrorIs(t, err, model.ErrInvalidRankingMetric)
}
