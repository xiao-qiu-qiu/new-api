package service

import (
	"context"
	"fmt"
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
			assert.False(t, preference.ModelDetailsPublic)
			require.NoError(t, model.SetUserRankingPreference(ctx, 91001, true))
			require.NoError(t, model.SetUserRankingPreference(ctx, 91003, true))
			result, err := GetUserRankings(ctx, "week", 91001)
			require.NoError(t, err)
			require.Len(t, result.Users, 1)
			assert.Equal(t, "a***e", result.Users[0].Name)
			assert.Equal(t, "a***e", result.Users[0].MaskedUsername)
			assert.Equal(t, int64(260), *result.Users[0].TotalTokens)
			assert.Nil(t, result.Users[0].TotalQuota)
			assert.Empty(t, result.Users[0].Models)
			assert.False(t, result.Users[0].UsagePublic)
			assert.Equal(t, int64(260), result.TotalTokens)
			assert.True(t, result.Users[0].IsSelf)
			public, private := true, false
			t.Run("nickname_is_public_and_cooldown_survives_consent_changes", func(t *testing.T) {
				at := time.Unix(now, 0)
				require.NoError(t, model.SetRankingNickname(ctx, 91001, "  星河旅人  ", at))
				updated, err := GetUserRankings(ctx, "24h", 91001)
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
				updated, err = GetUserRankings(ctx, "24h", 91001)
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
			t.Run("nickname_before_confirmation_keeps_prompt_unanswered", func(t *testing.T) {
				pref, err := model.GetUserRankingPreference(ctx, 91004)
				require.NoError(t, err)
				assert.False(t, pref.Prompted)
				require.NoError(t, model.SetRankingNickname(ctx, 91004, "初次昵称", time.Unix(now, 0)))
				pref, err = model.GetUserRankingPreference(ctx, 91004)
				require.NoError(t, err)
				assert.False(t, pref.Prompted)
				assert.False(t, pref.Participating)
				assert.False(t, pref.SpendingParticipating)
				assert.False(t, pref.ModelDetailsPublic)
				require.NoError(t, model.UpdateUserRankingPreference(ctx, 91004, model.UserRankingPreferenceUpdate{
					Participating: &public, SpendingParticipating: &public, ModelDetailsPublic: &public,
				}))
				pref, err = model.GetUserRankingPreference(ctx, 91004)
				require.NoError(t, err)
				assert.True(t, pref.Prompted)
				assert.True(t, pref.Participating)
				assert.True(t, pref.SpendingParticipating)
				assert.True(t, pref.ModelDetailsPublic)
				assert.Equal(t, "初次昵称", pref.Nickname)
				assert.Equal(t, now, pref.NicknameChangedAt)
			})
			t.Run("shared_disclosure_and_legacy_partial_updates", func(t *testing.T) {
				for _, test := range []struct {
					name                        string
					storedToken, storedSpending bool
					update                      model.UserRankingPreferenceUpdate
					wantToken, wantSpending     bool
				}{
					{"omitted preserves public", true, true, model.UserRankingPreferenceUpdate{Participating: &public}, true, true},
					{"omitted preserves unequal legacy values", true, false, model.UserRankingPreferenceUpdate{Participating: &public}, true, false},
					{"shared false", true, true, model.UserRankingPreferenceUpdate{ModelDetailsPublic: &private}, false, false},
					{"shared true", false, false, model.UserRankingPreferenceUpdate{ModelDetailsPublic: &public}, true, true},
					{"single legacy token true cannot reveal", false, true, model.UserRankingPreferenceUpdate{TokenUsagePublic: &public}, false, false},
					{"single legacy spending true cannot reveal", true, false, model.UserRankingPreferenceUpdate{SpendingUsagePublic: &public}, false, false},
					{"single legacy false hides both", true, true, model.UserRankingPreferenceUpdate{TokenUsagePublic: &private}, false, false},
					{"both legacy true reveal", false, false, model.UserRankingPreferenceUpdate{TokenUsagePublic: &public, SpendingUsagePublic: &public}, true, true},
					{"mixed legacy values hide both", true, true, model.UserRankingPreferenceUpdate{TokenUsagePublic: &public, SpendingUsagePublic: &private}, false, false},
					{"shared false takes precedence", true, true, model.UserRankingPreferenceUpdate{ModelDetailsPublic: &private, TokenUsagePublic: &public, SpendingUsagePublic: &public}, false, false},
					{"shared true takes precedence", false, false, model.UserRankingPreferenceUpdate{ModelDetailsPublic: &public, TokenUsagePublic: &private, SpendingUsagePublic: &private}, true, true},
				} {
					t.Run(test.name, func(t *testing.T) {
						require.NoError(t, db.Model(&model.UserRankingPreference{}).Where("user_id = ?", 91001).
							Updates(map[string]any{"token_usage_public": test.storedToken, "spending_usage_public": test.storedSpending}).Error)
						require.NoError(t, model.UpdateUserRankingPreference(ctx, 91001, test.update))
						pref, err := model.GetUserRankingPreference(ctx, 91001)
						require.NoError(t, err)
						assert.Equal(t, test.wantToken, pref.TokenUsagePublic)
						assert.Equal(t, test.wantSpending, pref.SpendingUsagePublic)
						assert.Equal(t, test.wantToken && test.wantSpending, pref.ModelDetailsPublic)
						assert.True(t, pref.Participating)
						assert.False(t, pref.SpendingParticipating)
					})
				}
			})
			t.Run("independent_boards_and_live_disclosure", func(t *testing.T) {
				// Never invalidate the warm totals cache: every response must use
				// live participation and shared model-detail choices.
				for _, test := range []struct {
					name                      string
					tokens, spending, details bool
				}{
					{"token totals only", true, false, false},
					{"token details", true, false, true},
					{"spending totals only", false, true, false},
					{"spending details", false, true, true},
					{"both totals only", true, true, false},
					{"both details", true, true, true},
				} {
					t.Run(test.name, func(t *testing.T) {
						require.NoError(t, model.UpdateUserRankingPreference(ctx, 91001, model.UserRankingPreferenceUpdate{
							Participating: &test.tokens, SpendingParticipating: &test.spending, ModelDetailsPublic: &test.details,
						}))
						for _, metric := range []string{model.RankingMetricTokens, model.RankingMetricSpending} {
							result, err := GetUserRankings(ctx, "week", 91001, metric)
							require.NoError(t, err)
							joined := test.tokens
							if metric == model.RankingMetricSpending {
								joined = test.spending
							}
							if !joined {
								assert.Empty(t, result.Users)
								assert.Nil(t, result.Self)
								assert.Zero(t, result.TotalTokens)
								assert.Zero(t, result.TotalQuota)
								continue
							}
							require.Len(t, result.Users, 1)
							require.NotNil(t, result.Self)
							row := result.Self
							assert.Equal(t, 1, row.Rank)
							assert.Equal(t, test.details, row.UsagePublic)
							encoded, err := common.Marshal(row)
							require.NoError(t, err)
							var payload map[string]any
							require.NoError(t, common.Unmarshal(encoded, &payload))
							if test.tokens {
								require.NotNil(t, row.TotalTokens)
								assert.Equal(t, int64(260), *row.TotalTokens)
								assert.Equal(t, int64(260), result.TotalTokens)
							} else {
								assert.NotContains(t, payload, "total_tokens")
								assert.Zero(t, result.TotalTokens)
							}
							if test.spending {
								require.NotNil(t, row.TotalQuota)
								assert.Equal(t, int64(1000), *row.TotalQuota)
								assert.Equal(t, int64(1000), result.TotalQuota)
							} else {
								assert.NotContains(t, payload, "total_quota")
								assert.Zero(t, result.TotalQuota)
							}
							if !test.details {
								assert.NotContains(t, payload, "models")
								continue
							}
							require.Len(t, row.Models, 2)
							assert.Equal(t, "beta", row.Models[0].ModelName)
							assert.Empty(t, row.Models[1].ModelName)
							var tokenSum, quotaSum int64
							for _, detail := range row.Models {
								if test.tokens {
									require.NotNil(t, detail.TotalTokens)
									tokenSum += *detail.TotalTokens
								} else {
									assert.Nil(t, detail.TotalTokens)
								}
								if test.spending {
									require.NotNil(t, detail.TotalQuota)
									quotaSum += *detail.TotalQuota
								} else {
									assert.Nil(t, detail.TotalQuota)
								}
							}
							assert.Equal(t, result.TotalTokens, tokenSum)
							assert.Equal(t, result.TotalQuota, quotaSum)
						}
					})
				}
			})
			// Joining on another instance fills the missing user's cached totals.
			require.NoError(t, model.SetUserRankingPreference(ctx, 91002, true))
			result, err = GetUserRankings(ctx, "week", 91002)
			require.NoError(t, err)
			require.Len(t, result.Users, 2)
			assert.Equal(t, int64(9260), result.TotalTokens)
			require.NotNil(t, result.Self)
			assert.Equal(t, 1, result.Self.Rank)
			assert.Nil(t, result.Self.TotalQuota)
			assert.Empty(t, result.Self.Models)
			require.NoError(t, model.UpdateUserRankingPreference(ctx, 91002, model.UserRankingPreferenceUpdate{SpendingParticipating: &public}))
			spending, err := GetUserRankings(ctx, "week", 91002, model.RankingMetricSpending)
			require.NoError(t, err)
			require.NotNil(t, spending.Self)
			assert.Equal(t, 2, spending.Self.Rank)
			assert.Equal(t, int64(1500), spending.TotalQuota)
			assert.Equal(t, int64(500), *spending.Self.TotalQuota)
			assert.False(t, spending.Self.UsagePublic)
			assert.Empty(t, spending.Self.Models)
			// Withdrawing from one board removes its dimension even inside a
			// public model tooltip on the other board and in the viewer's row.
			require.NoError(t, model.UpdateUserRankingPreference(ctx, 91001, model.UserRankingPreferenceUpdate{Participating: &private}))
			spending, err = GetUserRankings(ctx, "week", 91001, model.RankingMetricSpending)
			require.NoError(t, err)
			require.NotNil(t, spending.Self)
			assert.True(t, spending.Self.UsagePublic)
			assert.Nil(t, spending.Self.TotalTokens)
			assert.Equal(t, int64(1000), *spending.Self.TotalQuota)
			require.Len(t, spending.Self.Models, 2)
			for _, detail := range spending.Self.Models {
				assert.Nil(t, detail.TotalTokens)
				assert.NotNil(t, detail.TotalQuota)
			}
			result, err = GetUserRankings(ctx, "week", 91001)
			require.NoError(t, err)
			assert.Nil(t, result.Self)
			require.Len(t, result.Users, 1)
			assert.Equal(t, int64(9000), result.TotalTokens)
			assert.Equal(t, int64(500), result.TotalQuota)
			require.NoError(t, model.UpdateUserRankingPreference(ctx, 91001, model.UserRankingPreferenceUpdate{SpendingParticipating: &private}))
			require.NoError(t, model.UpdateUserRankingPreference(ctx, 91002, model.UserRankingPreferenceUpdate{SpendingParticipating: &private}))
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
			t.Run("administrator_all_accounts_and_isolated_public_cache", func(t *testing.T) {
				// Keep one public token-only row warm before adding new usage.
				require.NoError(t, model.UpdateUserRankingPreference(ctx, 91001, model.UserRankingPreferenceUpdate{
					Participating: &public, SpendingParticipating: &private, ModelDetailsPublic: &private,
				}))
				before, err := GetUserRankings(ctx, "week", 91001)
				require.NoError(t, err)
				require.Len(t, before.Users, 1)
				assert.Equal(t, int64(260), *before.Users[0].TotalTokens)
				// More than 100 current accounts proves the administrative view
				// retains every zero-usage account instead of applying a top limit.
				zeroUsers := make([]model.User, 98)
				for i := range zeroUsers {
					zeroUsers[i] = model.User{
						Id: 92000 + i, Username: fmt.Sprintf("zero-%03d", i),
						AffCode: fmt.Sprintf("rank-zero-%03d", i), Status: common.UserStatusEnabled,
					}
				}
				require.NoError(t, db.Create(&zeroUsers).Error)
				deleted := model.User{Id: 93000, Username: "deleted-account", AffCode: "rank-deleted", Status: common.UserStatusEnabled}
				require.NoError(t, db.Create(&deleted).Error)
				require.NoError(t, db.Delete(&deleted).Error)
				require.NoError(t, db.Create(&[]model.Log{
					{UserId: 91001, CreatedAt: now - 5, Type: model.LogTypeConsume, ModelName: "only-tokens", PromptTokens: 10},
					{UserId: 91002, CreatedAt: now - 5, Type: model.LogTypeConsume, ModelName: "only-quota", Quota: 25},
					{UserId: deleted.Id, CreatedAt: now - 5, Type: model.LogTypeConsume, ModelName: "deleted", Quota: 999999, PromptTokens: 999999},
				}).Error)
				for _, metric := range []string{model.RankingMetricTokens, model.RankingMetricSpending} {
					admin, err := GetAdminUserRankings(ctx, "week", 91001, metric)
					require.NoError(t, err)
					assert.Equal(t, 101, admin.Participants)
					require.Len(t, admin.Users, 101)
					assert.Equal(t, int64(17270), admin.TotalTokens)
					assert.Equal(t, int64(101524), admin.TotalQuota)
					require.NotNil(t, admin.Self)
					assert.Equal(t, "alice", admin.Self.Name)
					assert.Equal(t, "alice", admin.Self.Username)
					assert.Equal(t, int64(270), *admin.Self.TotalTokens)
					assert.Equal(t, int64(1000), *admin.Self.TotalQuota)
					require.Len(t, admin.Self.Models, 3)
					rows := make(map[string]RankedUser, len(admin.Users))
					for _, row := range admin.Users {
						rows[row.Username] = row
						assert.True(t, row.UsagePublic)
						assert.NotNil(t, row.TotalTokens)
						assert.NotNil(t, row.TotalQuota)
						for _, detail := range row.Models {
							assert.NotNil(t, detail.TotalTokens)
							assert.NotNil(t, detail.TotalQuota)
						}
					}
					assert.Contains(t, rows, "disabled")
					assert.Contains(t, rows, "never-joined")
					assert.Contains(t, rows, "zero-097")
					assert.NotContains(t, rows, deleted.Username)
					assert.Zero(t, *rows["zero-097"].TotalTokens)
					assert.Zero(t, *rows["zero-097"].TotalQuota)
					require.Len(t, rows["never-joined"].Models, 2)
				}
				// Administrative data and identity must not change the public
				// response even while both snapshots remain warm.
				after, err := GetUserRankings(ctx, "week", 91001)
				require.NoError(t, err)
				assert.Equal(t, before, after)
				encoded, err := common.Marshal(after)
				require.NoError(t, err)
				assert.NotContains(t, string(encoded), `"username"`)
				assert.NotContains(t, string(encoded), "only-tokens")
				assert.NotContains(t, string(encoded), "never-joined")
				// Account deletion is live, including in a warm admin snapshot.
				require.NoError(t, db.Delete(&zeroUsers[0]).Error)
				admin, err := GetAdminUserRankings(ctx, "week", 0, model.RankingMetricTokens)
				require.NoError(t, err)
				assert.Len(t, admin.Users, 100)
				assert.Equal(t, 100, admin.Participants)
			})
		})
	}
}

func TestUserRankingPeriodBoundaries(t *testing.T) {
	location := time.FixedZone("server", 8*3600)
	now := time.Date(2026, 10, 16, 0, 5, 0, 0, location)
	for _, test := range []struct {
		period        string
		start         time.Time
		previousStart time.Time
		nextStart     time.Time
	}{
		{"24h", now.Add(-24 * time.Hour), now.Add(-48 * time.Hour), now.Add(time.Minute)},
		{"today", time.Date(2026, 10, 16, 0, 0, 0, 0, location), time.Date(2026, 10, 15, 0, 0, 0, 0, location), time.Date(2026, 10, 17, 0, 0, 0, 0, location)},
		{"week", time.Date(2026, 10, 12, 0, 0, 0, 0, location), time.Date(2026, 10, 5, 0, 0, 0, 0, location), time.Date(2026, 10, 19, 0, 0, 0, 0, location)},
		{"month", time.Date(2026, 10, 1, 0, 0, 0, 0, location), time.Date(2026, 9, 1, 0, 0, 0, 0, location), time.Date(2026, 11, 1, 0, 0, 0, 0, location)},
		{"year", time.Date(2026, 1, 1, 0, 0, 0, 0, location), time.Date(2025, 1, 1, 0, 0, 0, 0, location), time.Date(2027, 1, 1, 0, 0, 0, 0, location)},
	} {
		t.Run(test.period, func(t *testing.T) {
			config, err := rankingConfig(test.period)
			require.NoError(t, err)
			start, end := rankingTimeRange(config, now)
			assert.Equal(t, test.start.Unix(), start)
			assert.Equal(t, now.Unix(), end)
			previousStart, previousEnd := previousRankingTimeRange(config, now)
			assert.Equal(t, test.previousStart.Unix(), previousStart)
			assert.Equal(t, start-1, previousEnd)
			if test.period == "24h" {
				assert.Equal(t, config.cacheKey(now), config.cacheKey(test.nextStart))
			} else {
				assert.Equal(t, config.cacheKey(now), config.cacheKey(test.nextStart.Add(-time.Second)))
				assert.NotEqual(t, config.cacheKey(now), config.cacheKey(test.nextStart))
			}
		})
	}
	week, err := rankingConfig("week")
	require.NoError(t, err)
	monday := time.Date(2026, 10, 19, 0, 0, 0, 0, location)
	sundayStart, _ := rankingTimeRange(week, monday.Add(-time.Second))
	mondayStart, _ := rankingTimeRange(week, monday)
	assert.Equal(t, time.Date(2026, 10, 12, 0, 0, 0, 0, location).Unix(), sundayStart)
	assert.Equal(t, monday.Unix(), mondayStart)
	month, err := rankingConfig("month")
	require.NoError(t, err)
	leapYearMarch := time.Date(2024, 3, 1, 0, 0, 0, 0, location)
	previousStart, previousEnd := previousRankingTimeRange(month, leapYearMarch)
	assert.Equal(t, time.Date(2024, 2, 1, 0, 0, 0, 0, location).Unix(), previousStart)
	assert.Equal(t, time.Date(2024, 2, 29, 23, 59, 59, 0, location).Unix(), previousEnd)
	_, err = GetUserRankings(t.Context(), "week", 0, "invalid")
	require.ErrorIs(t, err, model.ErrInvalidRankingMetric)
}
