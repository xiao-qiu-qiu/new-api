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
	UserID        int `gorm:"primaryKey;autoIncrement:false"`
	Participating bool
	Prompted      bool
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
				{UserId: 91001, CreatedAt: now - 10, Type: model.LogTypeConsume, PromptTokens: 100, CompletionTokens: 20, Other: `{"cache_tokens":80}`},
				{UserId: 91001, CreatedAt: now - 10, Type: model.LogTypeConsume, PromptTokens: 10, CompletionTokens: 20, Other: `{"usage_semantic":"anthropic","cache_tokens":80,"cache_creation_tokens":30}`},
				{UserId: 91001, CreatedAt: now - 10, Type: model.LogTypeError, PromptTokens: 1000},
				{UserId: 91001, CreatedAt: now - 8*86400, Type: model.LogTypeConsume, PromptTokens: 1000},
				{UserId: 91002, CreatedAt: now - 10, Type: model.LogTypeConsume, PromptTokens: 9000},
				{UserId: 91003, CreatedAt: now - 10, Type: model.LogTypeConsume, PromptTokens: 8000},
			}
			require.NoError(t, db.Create(&logs).Error)
			// Upgrade the prior release's consent schema, preserving an existing choice.
			require.NoError(t, db.Migrator().DropTable(&model.UserRankingPreference{}))
			require.NoError(t, db.AutoMigrate(&legacyRankingPreference{}))
			require.NoError(t, db.Create(&legacyRankingPreference{UserID: 91002, Prompted: true}).Error)
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.UserRankingPreference{}))
			}
			ctx := context.Background()
			preference, err := model.GetUserRankingPreference(ctx, 91001)
			require.NoError(t, err)
			assert.False(t, preference.Participating)
			assert.False(t, preference.Prompted)
			require.NoError(t, model.SetUserRankingPreference(ctx, 91001, true))
			require.NoError(t, model.SetUserRankingPreference(ctx, 91003, true))
			result, err := GetUserRankings(ctx, "week", 91001)
			require.NoError(t, err)
			require.Len(t, result.Users, 1)
			assert.Equal(t, "a***e", result.Users[0].Name)
			assert.Equal(t, "a***e", result.Users[0].MaskedUsername)
			assert.Equal(t, int64(260), result.TotalTokens)
			assert.True(t, result.Users[0].IsSelf)
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
			result, err = GetUserRankings(ctx, "week", 91002)
			require.NoError(t, err)
			require.Len(t, result.Users, 2)
			assert.Equal(t, int64(9260), result.TotalTokens)
			assert.True(t, result.Users[0].IsSelf)
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
