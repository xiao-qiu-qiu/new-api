package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/clickhouse"
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
			db := openUserRankingTestDatabase(t, engine)
			previousDB, previousLogs := model.DB, model.LOG_DB
			previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
			model.DB, model.LOG_DB = db, db
			common.SetDatabaseTypes(common.DatabaseType(engine), common.DatabaseType(engine))
			t.Cleanup(func() {
				model.DB, model.LOG_DB = previousDB, previousLogs
				common.SetDatabaseTypes(previousMainType, previousLogType)
				InvalidateUserRankings()
			})
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
				Updates(map[string]any{"quota": nil, "model_name": nil, "prompt_tokens": nil, "completion_tokens": nil}).Error)
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

func openUserRankingTestDatabase(t *testing.T, engine string) *gorm.DB {
	t.Helper()
	var driver gorm.Dialector
	switch engine {
	case "clickhouse":
		dsn := os.Getenv("NEWAPI_TEST_CLICKHOUSE_DSN")
		if dsn == "" {
			t.Skip("NEWAPI_TEST_CLICKHOUSE_DSN is not configured")
		}
		driver = clickhouse.Open(dsn)
	case "mysql":
		dsn := os.Getenv("NEWAPI_TEST_MYSQL_DSN")
		if dsn == "" {
			t.Skip("NEWAPI_TEST_MYSQL_DSN is not configured")
		}
		driver = mysql.Open(dsn)
	case "postgres":
		dsn := os.Getenv("NEWAPI_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("NEWAPI_TEST_POSTGRES_DSN is not configured")
		}
		driver = postgres.Open(dsn)
	default:
		driver = sqlite.Open(":memory:")
	}
	db, err := gorm.Open(driver, &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { assert.NoError(t, sqlDB.Close()) })
	versionQuery := "SELECT version()"
	if engine == "sqlite" {
		versionQuery = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("Database version (%s): %s", engine, version)
	return db
}

type rankingPayloadObserver struct {
	queries int
	before  func(*gorm.DB)
}

func (observer *rankingPayloadObserver) observe(tx *gorm.DB) {
	if tx.Statement.Table != "logs" {
		return
	}
	selection := strings.ToLower(strings.Join(tx.Statement.Selects, ","))
	if selection != "" && selection != "*" && !strings.Contains(selection, "other") {
		return
	}
	observer.queries++
	if observer.before != nil {
		observer.before(tx)
	}
}

// Each case gets a distinct LOG_DB pointer and rolls its log writes back. The
// primary database is deliberately separate, including for MySQL/PostgreSQL.
func newUserRankingUsageFixture(t *testing.T, db *gorm.DB) (*gorm.DB, *rankingPayloadObserver) {
	t.Helper()
	logs := db.Begin()
	require.NoError(t, logs.Error)
	t.Cleanup(func() { assert.NoError(t, logs.Rollback().Error) })
	mainDB := openUserRankingTestDatabase(t, "sqlite")
	require.NoError(t, mainDB.AutoMigrate(&model.User{}, &model.UserRankingPreference{}, &model.Log{}))
	previousDB, previousLogs := model.DB, model.LOG_DB
	previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	model.DB, model.LOG_DB = mainDB, logs
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseType(db.Dialector.Name()))
	InvalidateUserRankings()
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogs
		common.SetDatabaseTypes(previousMainType, previousLogType)
		InvalidateUserRankings()
	})
	observer := &rankingPayloadObserver{}
	const callback = "test:ranking_payload"
	require.NoError(t, logs.Callback().Row().Before("gorm:row").Register(callback, observer.observe))
	require.NoError(t, logs.Callback().Query().Before("gorm:query").Register(callback, observer.observe))
	t.Cleanup(func() {
		InvalidateUserRankings()
		waitForRankingBuilds(t)
		assert.NoError(t, logs.Callback().Row().Remove(callback))
		assert.NoError(t, logs.Callback().Query().Remove(callback))
	})
	return logs, observer
}

func TestUserRankingUsageDatabaseMatrix(t *testing.T) {
	const userID = 94001
	const daySeconds int64 = 86400
	day := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC).Unix()
	for _, engine := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := openUserRankingTestDatabase(t, engine)
			require.NoError(t, db.AutoMigrate(&model.Log{}))
			t.Run("complete_day_reuses_payload_and_preserves_token_semantics", func(t *testing.T) {
				logs, observer := newUserRankingUsageFixture(t, db)
				entries := []model.Log{
					{ModelName: "openai", PromptTokens: 100, CompletionTokens: 20, Quota: 7, Other: `{"cache_tokens":80}`},
					{ModelName: "claude", PromptTokens: 100, CompletionTokens: 20, Quota: 11, Other: `{"usage_semantic":"anthropic","cache_tokens":80,"cache_creation_tokens":30,"cache_creation_tokens_5m":10,"cache_creation_tokens_1h":20}`},
					{ModelName: "claude", PromptTokens: 100, CompletionTokens: 20, Quota: 13, Other: `{"input_tokens_total":150,"claude":true,"cache_tokens":80}`},
					{ModelName: "claude", PromptTokens: 100, CompletionTokens: 20, Quota: 17, Other: `{"claude":true,"cache_tokens":80,"cache_write_tokens":30}`},
					{PromptTokens: 10, CompletionTokens: 5, Quota: -2},
				}
				for i := range entries {
					entries[i].UserId, entries[i].CreatedAt, entries[i].Type = userID, day+1, model.LogTypeConsume
				}
				require.NoError(t, logs.Create(&entries).Error)
				want := model.RankingUserUsage{TotalTokens: 765, TotalQuota: 48, Models: map[string]model.RankingModelUsage{
					"openai": {TotalTokens: 120, TotalQuota: 7}, "claude": {TotalTokens: 630, TotalQuota: 41}, "": {TotalTokens: 15},
				}}
				first, err := model.GetUserRankingUsage(t.Context(), []int{userID}, day, day+daySeconds-1)
				require.NoError(t, err)
				assert.Equal(t, want, first[userID])
				assert.Positive(t, observer.queries)
				observer.queries = 0
				cached, err := model.GetUserRankingUsage(t.Context(), []int{userID}, day, day+daySeconds-1)
				require.NoError(t, err)
				assert.Equal(t, first, cached)
				assert.Zero(t, observer.queries, "unchanged complete days must not select other again")
			})
			t.Run("backfill_and_deletions_refresh_day_signatures", func(t *testing.T) {
				logs, observer := newUserRankingUsageFixture(t, db)
				entries := []model.Log{
					{UserId: userID, CreatedAt: day + 1, Type: model.LogTypeConsume, ModelName: "a", PromptTokens: 10, Quota: 2},
					{UserId: userID, CreatedAt: day + 2, Type: model.LogTypeConsume, ModelName: "a", PromptTokens: 20, Quota: 3},
					{UserId: userID, CreatedAt: day + 3, Type: model.LogTypeError, PromptTokens: 9999},
					{UserId: userID + 1, CreatedAt: day + 3, Type: model.LogTypeConsume, PromptTokens: 9999},
				}
				require.NoError(t, logs.Create(&entries).Error)
				backfill := &model.Log{UserId: userID, CreatedAt: day + 1, Type: model.LogTypeConsume, ModelName: "a", PromptTokens: 40, Quota: 5}
				replacement := &model.Log{UserId: userID, CreatedAt: day + 2, Type: model.LogTypeConsume, ModelName: "a", PromptTokens: 80, Quota: 7}
				for _, test := range []struct {
					name           string
					remove, insert *model.Log
					tokens, quota  int64
				}{
					{"initial", nil, nil, 30, 5},
					{"backfill_old_day", nil, backfill, 70, 10},
					{"delete_nonmaximum_id", &entries[0], nil, 60, 8},
					{"same_count_new_maximum_id", &entries[1], replacement, 120, 12},
					{"delete_maximum_id", replacement, nil, 40, 5},
					{"delete_all_consumption", backfill, nil, 0, 0},
					{"backfill_previously_empty_day", nil, &model.Log{UserId: userID, CreatedAt: day + 4, Type: model.LogTypeConsume, ModelName: "a", PromptTokens: 2, Quota: 1}, 2, 1},
				} {
					t.Run(test.name, func(t *testing.T) {
						if test.remove != nil {
							require.NoError(t, logs.Delete(test.remove).Error)
						}
						if test.insert != nil {
							require.NoError(t, logs.Create(test.insert).Error)
						}
						got, err := model.GetUserRankingUsage(t.Context(), []int{userID}, day, day+daySeconds-1)
						require.NoError(t, err)
						assert.Equal(t, test.tokens, got[userID].TotalTokens)
						assert.Equal(t, test.quota, got[userID].TotalQuota)
						if test.tokens == 0 {
							assert.Empty(t, got[userID].Models)
						} else {
							assert.Equal(t, map[string]model.RankingModelUsage{"a": {TotalTokens: test.tokens, TotalQuota: test.quota}}, got[userID].Models)
						}
						observer.queries = 0
						again, err := model.GetUserRankingUsage(t.Context(), []int{userID}, day, day+daySeconds-1)
						require.NoError(t, err)
						assert.Equal(t, got, again)
						assert.Zero(t, observer.queries)
					})
				}
			})
			t.Run("inclusive_utc_boundaries_and_live_partial_days", func(t *testing.T) {
				logs, observer := newUserRankingUsageFixture(t, db)
				for i, offset := range []int64{-1, 0, 1, daySeconds - 1, daySeconds, daySeconds + 1, 2*daySeconds - 1, 2 * daySeconds, 2*daySeconds + 1, 3*daySeconds - 1, 3 * daySeconds} {
					require.NoError(t, logs.Create(&model.Log{UserId: userID, CreatedAt: day + offset, Type: model.LogTypeConsume, PromptTokens: 1 << i, Quota: 1 << i}).Error)
				}
				location := time.FixedZone("UTC+05:45", 5*3600+45*60)
				for _, test := range []struct {
					name             string
					start, end, want int64
				}{
					{"exact_day", day, day + daySeconds - 1, 14},
					{"inclusive_next_midnight", day, day + daySeconds, 30},
					{"next_full_day", day + daySeconds, day + 2*daySeconds - 1, 112},
					{"partial_day", day + 1, day + daySeconds - 1, 12},
					{"partial_full_partial", day + 1, day + 2*daySeconds + 1, 508},
					{"three_full_days", day, day + 3*daySeconds - 1, 1022},
					{"offset_timezone", time.Date(2026, 1, 10, 5, 45, 1, 0, location).Unix(), time.Date(2026, 1, 12, 5, 45, 0, 0, location).Unix(), 252},
				} {
					t.Run(test.name, func(t *testing.T) {
						got, err := model.GetUserRankingUsage(t.Context(), []int{userID}, test.start, test.end)
						require.NoError(t, err)
						assert.Equal(t, test.want, got[userID].TotalTokens)
						assert.Equal(t, test.want, got[userID].TotalQuota)
					})
				}
				for _, entry := range []model.Log{
					{UserId: userID, CreatedAt: day + 2, Type: model.LogTypeConsume, PromptTokens: 2048},
					{UserId: userID, CreatedAt: day + 2*daySeconds + 1, Type: model.LogTypeConsume, PromptTokens: 4096},
				} {
					require.NoError(t, logs.Create(&entry).Error)
				}
				for attempt := range 2 {
					observer.queries = 0
					got, err := model.GetUserRankingUsage(t.Context(), []int{userID}, day+1, day+2*daySeconds+1)
					require.NoError(t, err)
					assert.Equal(t, int64(6652), got[userID].TotalTokens)
					if attempt == 0 {
						assert.Positive(t, observer.queries, "backfills must refresh exact boundary ranges")
					} else {
						assert.Zero(t, observer.queries, "unchanged exact boundaries must reuse parsed usage")
					}
				}
			})
			t.Run("paged_day_resumes_without_recounting_completed_rows", func(t *testing.T) {
				logs, observer := newUserRankingUsageFixture(t, db)
				userIDs := []int{userID, userID + 1}
				const firstPageID = 2_000_000
				require.NoError(t, logs.Create(&[]model.Log{
					{Id: 1_000_000, UserId: userID, CreatedAt: day - 10*daySeconds, Type: model.LogTypeConsume, PromptTokens: 999999},
					{Id: 1_000_001, UserId: userID + 1, CreatedAt: day + 10*daySeconds, Type: model.LogTypeConsume, PromptTokens: 999999},
					{Id: firstPageID + 1, UserId: userID, CreatedAt: day + 5*daySeconds, Type: model.LogTypeConsume, PromptTokens: 999999},
					{Id: firstPageID + 3, UserId: userID, CreatedAt: day + 1, Type: model.LogTypeError, PromptTokens: 999999},
					{Id: firstPageID + 5, UserId: userID + 2, CreatedAt: day + 1, Type: model.LogTypeConsume, PromptTokens: 999999},
				}).Error)
				entries := make([]model.Log, 1001)
				for i := range entries {
					entries[i] = model.Log{
						Id: firstPageID + 2*i, UserId: userIDs[i%len(userIDs)], CreatedAt: day + 1001 - int64(i),
						Type: model.LogTypeConsume, PromptTokens: 1, Quota: 2,
					}
				}
				require.NoError(t, logs.CreateInBatches(entries, 100).Error)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				observer.before = func(tx *gorm.DB) {
					if observer.queries == 2 {
						cancel()
						tx.AddError(ctx.Err())
					}
				}
				_, err := model.GetUserRankingUsage(ctx, userIDs, day, day+daySeconds-1)
				require.ErrorIs(t, err, context.Canceled)
				observer.queries = 0
				started, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				observer.before = func(tx *gorm.DB) {
					once.Do(func() {
						close(started)
						select {
						case <-release:
						case <-tx.Statement.Context.Done():
							tx.AddError(tx.Statement.Context.Err())
						}
					})
				}
				type response struct {
					usage map[int]model.RankingUserUsage
					err   error
				}
				firstDone, broaderDone := make(chan response, 1), make(chan response, 1)
				go func() {
					usage, err := model.GetUserRankingUsage(t.Context(), userIDs, day, day+daySeconds-1)
					firstDone <- response{usage, err}
				}()
				<-started
				waitingCtx := &rankingWaitContext{Context: t.Context(), waiting: make(chan struct{})}
				go func() {
					usage, err := model.GetUserRankingUsage(waitingCtx, userIDs, day-daySeconds, day+2*daySeconds-1)
					broaderDone <- response{usage, err}
				}()
				<-waitingCtx.waiting
				assert.Equal(t, 1, observer.queries, "another period must wait while one payload batch is active")
				close(release)
				first, broader := <-firstDone, <-broaderDone
				require.NoError(t, first.err)
				require.NoError(t, broader.err)
				assert.Equal(t, first.usage, broader.usage)
				assert.Equal(t, int64(501), first.usage[userID].TotalTokens)
				assert.Equal(t, int64(1002), first.usage[userID].TotalQuota)
				assert.Equal(t, int64(500), first.usage[userID+1].TotalTokens)
				assert.Equal(t, int64(1000), first.usage[userID+1].TotalQuota)
				assert.Equal(t, 2, observer.queries, "retry must scan only the two unfinished pages")
				observer.before, observer.queries = nil, 0
				cached, err := model.GetUserRankingUsage(t.Context(), userIDs, day-daySeconds, day+2*daySeconds-1)
				require.NoError(t, err)
				assert.Equal(t, first.usage, cached)
				assert.Zero(t, observer.queries, "another period must reuse the completed day")
				// Imported history can arrive after a warm snapshot with a lower ID
				// and a later timestamp than every row already scanned.
				require.NoError(t, logs.Create(&model.Log{
					Id: firstPageID - 20, UserId: userID, CreatedAt: day + daySeconds - 1,
					Type: model.LogTypeConsume, ModelName: "late-low-id", PromptTokens: 17, Quota: 19,
				}).Error)
				refreshed, err := model.GetUserRankingUsage(t.Context(), userIDs, day, day+daySeconds-1)
				require.NoError(t, err)
				assert.Equal(t, int64(518), refreshed[userID].TotalTokens)
				assert.Equal(t, int64(1021), refreshed[userID].TotalQuota)
				assert.Equal(t, model.RankingModelUsage{TotalTokens: 17, TotalQuota: 19}, refreshed[userID].Models["late-low-id"])
				assert.Equal(t, first.usage[userID+1], refreshed[userID+1])
				assert.Positive(t, observer.queries, "a low-ID backfill must invalidate the completed signature")
				observer.queries = 0
				again, err := model.GetUserRankingUsage(t.Context(), userIDs, day, day+daySeconds-1)
				require.NoError(t, err)
				assert.Equal(t, refreshed, again)
				assert.Zero(t, observer.queries)
			})
			t.Run("cancellation_keeps_completed_day_progress", func(t *testing.T) {
				logs, observer := newUserRankingUsageFixture(t, db)
				for i := range 3 {
					require.NoError(t, logs.Create(&model.Log{UserId: userID, CreatedAt: day + int64(i)*daySeconds, Type: model.LogTypeConsume, PromptTokens: 10}).Error)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				observer.before = func(tx *gorm.DB) {
					if observer.queries == 2 {
						cancel()
						tx.AddError(ctx.Err())
					}
				}
				_, err := model.GetUserRankingUsage(ctx, []int{userID}, day, day+3*daySeconds-1)
				require.ErrorIs(t, err, context.Canceled)
				observer.before, observer.queries = nil, 0
				got, err := model.GetUserRankingUsage(t.Context(), []int{userID}, day, day+3*daySeconds-1)
				require.NoError(t, err)
				assert.Equal(t, int64(30), got[userID].TotalTokens)
				assert.Equal(t, 2, observer.queries, "the completed day must survive cancellation of the next day")
			})
			t.Run("uses_log_database_and_isolates_database_pointers", func(t *testing.T) {
				logs, _ := newUserRankingUsageFixture(t, db)
				require.NoError(t, logs.Create(&model.Log{UserId: userID, CreatedAt: day, Type: model.LogTypeConsume, PromptTokens: 20}).Error)
				require.NoError(t, model.DB.Create(&model.Log{UserId: userID, CreatedAt: day, Type: model.LogTypeConsume, PromptTokens: 900}).Error)
				for _, source := range []struct {
					db   *gorm.DB
					want int64
				}{{logs, 20}, {model.DB, 900}, {logs, 20}} {
					model.LOG_DB = source.db
					common.SetLogDatabaseType(common.DatabaseType(source.db.Dialector.Name()))
					got, err := model.GetUserRankingUsage(t.Context(), []int{userID}, day, day+daySeconds-1)
					require.NoError(t, err)
					assert.Equal(t, source.want, got[userID].TotalTokens)
				}
			})
			t.Run("both_metrics_share_union_without_sharing_consent", func(t *testing.T) {
				logs, observer := newUserRankingUsageFixture(t, db)
				require.NoError(t, model.DB.Create(&[]model.User{
					{Id: userID, Username: "token-only", AffCode: "ranking-token", Status: common.UserStatusEnabled},
					{Id: userID + 1, Username: "spending-only", AffCode: "ranking-spending", Status: common.UserStatusEnabled},
				}).Error)
				require.NoError(t, model.DB.Create(&[]model.UserRankingPreference{
					{UserID: userID, Participating: true, Nickname: "token-only", TokenUsagePublic: true, SpendingUsagePublic: true},
					{UserID: userID + 1, SpendingParticipating: true, Nickname: "spending-only", TokenUsagePublic: true, SpendingUsagePublic: true},
				}).Error)
				require.NoError(t, logs.Create(&[]model.Log{
					{UserId: userID, CreatedAt: time.Now().Unix() - 10, Type: model.LogTypeConsume, ModelName: "tokens", PromptTokens: 100, Quota: 900},
					{UserId: userID + 1, CreatedAt: time.Now().Unix() - 10, Type: model.LogTypeConsume, ModelName: "spending", PromptTokens: 9000, Quota: 25},
				}).Error)
				for _, first := range []string{model.RankingMetricTokens, model.RankingMetricSpending} {
					InvalidateUserRankings()
					other := model.RankingMetricTokens
					if first == model.RankingMetricTokens {
						other = model.RankingMetricSpending
					}
					for i, metric := range []string{first, other} {
						observer.queries = 0
						got, err := GetUserRankings(t.Context(), "24h", 0, metric)
						require.NoError(t, err)
						require.Len(t, got.Users, 1)
						row := got.Users[0]
						require.Len(t, row.Models, 1)
						if metric == model.RankingMetricTokens {
							assert.Equal(t, "token-only", row.Name)
							assert.Equal(t, int64(100), got.TotalTokens)
							assert.Zero(t, got.TotalQuota)
							assert.Nil(t, row.TotalQuota)
							assert.Nil(t, row.Models[0].TotalQuota)
						} else {
							assert.Equal(t, "spending-only", row.Name)
							assert.Equal(t, int64(25), got.TotalQuota)
							assert.Zero(t, got.TotalTokens)
							assert.Nil(t, row.TotalTokens)
							assert.Nil(t, row.Models[0].TotalTokens)
						}
						if i == 1 {
							assert.Zero(t, observer.queries, "the first metric must already have loaded the other metric's users")
						}
					}
				}
				require.NoError(t, model.UpdateUserRankingPreference(t.Context(), userID, model.UserRankingPreferenceUpdate{Participating: new(false)}))
				got, err := GetUserRankings(t.Context(), "24h", 0, model.RankingMetricTokens)
				require.NoError(t, err)
				assert.Empty(t, got.Users, "withdrawal must take effect while the shared snapshot remains warm")
				assert.Zero(t, got.TotalTokens)
			})
		})
	}
}

// Done signals that a caller has entered the flight's cancellable wait. This
// gives the cancellation regression a fixed ordering without sleeps.
type rankingWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *rankingWaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

func TestUserRankingSharedFlightCancellation(t *testing.T) {
	db := openUserRankingTestDatabase(t, "sqlite")
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	logs, observer := newUserRankingUsageFixture(t, db)
	const userID = 95001
	require.NoError(t, model.DB.Create(&model.User{Id: userID, Username: "flight-user", AffCode: "ranking-flight", Status: common.UserStatusEnabled}).Error)
	require.NoError(t, model.SetUserRankingPreference(t.Context(), userID, true, true))
	require.NoError(t, logs.Create(&model.Log{UserId: userID, CreatedAt: time.Now().Unix() - 10, Type: model.LogTypeConsume, PromptTokens: 10, Quota: 5}).Error)
	ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	leaderCtx, cancelLeader := context.WithCancel(ctx)
	defer cancelLeader()
	started := make(chan struct{})
	observer.before = func(tx *gorm.DB) {
		if observer.queries == 1 {
			close(started)
			<-leaderCtx.Done()
			tx.AddError(leaderCtx.Err())
		}
	}
	leaderDone := make(chan error, 1)
	go func() {
		_, err := GetUserRankings(leaderCtx, "24h", userID, model.RankingMetricTokens)
		leaderDone <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("leader did not reach its controlled log scan")
	}
	type response struct {
		value *UserRankingsResponse
		err   error
	}
	waiterCtx := &rankingWaitContext{Context: ctx, waiting: make(chan struct{})}
	waiterDone := make(chan response, 1)
	go func() {
		value, err := GetUserRankings(waiterCtx, "24h", userID, model.RankingMetricSpending)
		waiterDone <- response{value, err}
	}()
	select {
	case <-waiterCtx.waiting:
	case <-ctx.Done():
		t.Fatal("spending waiter did not enter the shared flight")
	}
	ownCtx, cancelOwn := context.WithCancel(ctx)
	defer cancelOwn()
	ownWait := &rankingWaitContext{Context: ownCtx, waiting: make(chan struct{})}
	ownDone := make(chan error, 1)
	go func() {
		_, err := GetUserRankings(ownWait, "24h", userID, model.RankingMetricTokens)
		ownDone <- err
	}()
	select {
	case <-ownWait.waiting:
	case <-ctx.Done():
		t.Fatal("independent waiter did not enter the shared flight")
	}
	cancelOwn()
	select {
	case err := <-ownDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-ctx.Done():
		t.Fatal("cancelled waiter remained blocked behind the leader")
	}
	cancelLeader()
	require.ErrorIs(t, <-leaderDone, context.Canceled)
	got := <-waiterDone
	require.NoError(t, got.err)
	require.NotNil(t, got.value)
	assert.Equal(t, int64(5), got.value.TotalQuota)
	require.NotNil(t, got.value.Self)
	assert.True(t, got.value.Self.IsSelf)
}

// A fixture must not restore global database handles before detached work exits.
func waitForRankingBuilds(t *testing.T) {
	t.Helper()
	rankingBuilds.Lock()
	done := make([]<-chan struct{}, 0, len(rankingBuilds.items))
	for _, job := range rankingBuilds.items {
		done = append(done, job.done)
	}
	rankingBuilds.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, finished := range done {
		select {
		case <-finished:
		case <-ctx.Done():
			t.Fatal("ranking background work did not finish before fixture cleanup")
		}
	}
}

func TestUserRankingBackgroundRequests(t *testing.T) {
	for _, engine := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := openUserRankingTestDatabase(t, engine)
			require.NoError(t, db.AutoMigrate(&model.Log{}))
			logs, observer := newUserRankingUsageFixture(t, db)
			const userID = 96001
			require.NoError(t, model.DB.Create(&[]model.User{
				{Id: userID, Username: "background-user", AffCode: "background-user", Status: common.UserStatusEnabled},
				{Id: userID + 1, Username: "later-user", AffCode: "later-user", Status: common.UserStatusEnabled},
			}).Error)
			require.NoError(t, model.DB.Create(&model.UserRankingPreference{
				UserID: userID, Participating: true, SpendingParticipating: true, TokenUsagePublic: true, SpendingUsagePublic: true,
			}).Error)
			now := time.Now().Unix()
			require.NoError(t, logs.Create(&[]model.Log{
				{UserId: userID, CreatedAt: now - 10, Type: model.LogTypeConsume, ModelName: "visible", PromptTokens: 30, Quota: 4},
				{UserId: userID + 1, CreatedAt: now - 10, Type: model.LogTypeConsume, ModelName: "later", PromptTokens: 50, Quota: 8},
			}).Error)
			started, release := make(chan context.Context, 1), make(chan struct{})
			var once sync.Once
			observer.before = func(tx *gorm.DB) {
				once.Do(func() {
					started <- tx.Statement.Context
					select {
					case <-release:
					case <-tx.Statement.Context.Done():
						tx.AddError(tx.Statement.Context.Err())
					}
				})
			}
			requestCtx, cancelRequest := context.WithCancel(t.Context())
			defer cancelRequest()
			requestDone := make(chan error, 1)
			go func() {
				_, err := RequestUserRankings(requestCtx, "24h", userID, model.RankingMetricTokens)
				requestDone <- err
			}()
			barrierCtx, stopBarrier := context.WithTimeout(t.Context(), 10*time.Second)
			defer stopBarrier()
			var buildCtx context.Context
			select {
			case buildCtx = <-started:
			case <-barrierCtx.Done():
				t.Fatal("ranking request did not reach its controlled payload query")
			}
			cancelRequest()
			require.ErrorIs(t, <-requestDone, context.Canceled)
			assert.NoError(t, buildCtx.Err(), "an HTTP cancellation must not cancel the shared build")
			_, err := RequestUserRankings(t.Context(), "24h", userID, model.RankingMetricSpending)
			require.ErrorIs(t, err, ErrRankingsPending)
			assert.Equal(t, 1, observer.queries, "both metrics must wait for the same payload scan")
			close(release)
			waitForRankingBuilds(t)
			observer.before, observer.queries = nil, 0
			for _, metric := range []string{model.RankingMetricTokens, model.RankingMetricSpending} {
				got, err := RequestUserRankings(t.Context(), "24h", userID, metric)
				require.NoError(t, err)
				require.Len(t, got.Users, 1)
				assert.Equal(t, int64(30), got.TotalTokens)
				assert.Equal(t, int64(4), got.TotalQuota)
			}
			assert.Zero(t, observer.queries, "warm responses must not read log payloads")
			public, private := true, false
			require.NoError(t, model.UpdateUserRankingPreference(t.Context(), userID, model.UserRankingPreferenceUpdate{
				SpendingParticipating: &private, ModelDetailsPublic: &private,
			}))
			got, err := RequestUserRankings(t.Context(), "24h", userID, model.RankingMetricTokens)
			require.NoError(t, err)
			require.NotNil(t, got.Self)
			assert.Nil(t, got.Self.TotalQuota)
			assert.Empty(t, got.Self.Models)
			assert.Zero(t, got.TotalQuota)
			// Force an expired snapshot while its refresh is held at a fixed barrier.
			userRankingCache.Lock()
			for key, cached := range userRankingCache.items {
				cached.expires = time.Now().Add(-time.Second)
				userRankingCache.items[key] = cached
			}
			userRankingCache.Unlock()
			require.NoError(t, logs.Create(&model.Log{UserId: userID, CreatedAt: now - 5, Type: model.LogTypeConsume, PromptTokens: 10, Quota: 2}).Error)
			refreshStarted, refreshRelease := make(chan struct{}), make(chan struct{})
			var refreshOnce sync.Once
			observer.before = func(tx *gorm.DB) {
				refreshOnce.Do(func() {
					close(refreshStarted)
					select {
					case <-refreshRelease:
					case <-tx.Statement.Context.Done():
						tx.AddError(tx.Statement.Context.Err())
					}
				})
			}
			stale, err := RequestUserRankings(t.Context(), "24h", userID, model.RankingMetricTokens)
			require.NoError(t, err)
			assert.Equal(t, int64(30), stale.TotalTokens, "the complete stale snapshot must remain usable during refresh")
			<-refreshStarted
			require.NoError(t, model.UpdateUserRankingPreference(t.Context(), userID, model.UserRankingPreferenceUpdate{Participating: &private}))
			withdrawn, err := RequestUserRankings(t.Context(), "24h", userID, model.RankingMetricTokens)
			require.NoError(t, err)
			assert.Empty(t, withdrawn.Users, "withdrawal must apply while stale totals are still served")
			close(refreshRelease)
			waitForRankingBuilds(t)
			observer.before, observer.queries = nil, 0
			// A participant joining a warm snapshot is filled once, then shared.
			require.NoError(t, model.UpdateUserRankingPreference(t.Context(), userID+1, model.UserRankingPreferenceUpdate{Participating: &public}))
			_, err = RequestUserRankings(t.Context(), "24h", userID+1, model.RankingMetricTokens)
			if err != nil {
				require.ErrorIs(t, err, ErrRankingsPending)
			}
			waitForRankingBuilds(t)
			observer.queries = 0
			joined, err := RequestUserRankings(t.Context(), "24h", userID+1, model.RankingMetricTokens)
			require.NoError(t, err)
			require.NotNil(t, joined.Self)
			assert.Equal(t, int64(50), joined.TotalTokens)
			assert.Zero(t, observer.queries, "the completed missing-user fill must be published to the shared snapshot")
			// A cancelled worker remains active until its actual query callback exits.
			require.NoError(t, logs.Create(&model.Log{UserId: userID, CreatedAt: now - 3, Type: model.LogTypeConsume, PromptTokens: 1}).Error)
			workerStarted, workerCancelled, releaseExit := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var workerOnce, exitOnce sync.Once
			t.Cleanup(func() { exitOnce.Do(func() { close(releaseExit) }) })
			observer.before = func(tx *gorm.DB) {
				workerOnce.Do(func() {
					close(workerStarted)
					<-tx.Statement.Context.Done()
					close(workerCancelled)
					<-releaseExit
					tx.AddError(tx.Statement.Context.Err())
				})
			}
			go func() {
				_, err := RequestAdminUserRankings(t.Context(), "24h", 0, model.RankingMetricTokens)
				requestDone <- err
			}()
			<-workerStarted
			InvalidateUserRankings()
			<-workerCancelled
			require.ErrorIs(t, <-requestDone, ErrRankingsPending, "worker completion must wait for the underlying callback to exit")
			exitOnce.Do(func() { close(releaseExit) })
			waitForRankingBuilds(t)
		})
	}
}

func TestUserRankingGenerationCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	userRankingCache.Lock()
	generation := userRankingCache.generation
	userRankingCache.Unlock()
	oldKey := fmt.Sprintf("users:%d:test-generation-old", generation)
	newKey := fmt.Sprintf("users:%d:test-generation-new", generation+1)
	started := make(chan context.Context, 2)
	release := make(chan struct{})
	t.Cleanup(func() {
		rankingBuilds.Lock()
		for _, key := range []string{oldKey, newKey} {
			if job := rankingBuilds.items[key]; job != nil {
				job.cancel()
			}
		}
		rankingBuilds.Unlock()
		waitForRankingBuilds(t)
		rankingBuilds.Lock()
		delete(rankingBuilds.items, oldKey)
		delete(rankingBuilds.items, newKey)
		rankingBuilds.Unlock()
	})
	for _, key := range []string{oldKey, newKey} {
		err := runRankingBuild(ctx, key, func(buildCtx context.Context) error {
			started <- buildCtx
			select {
			case <-release:
				return nil
			case <-buildCtx.Done():
				return buildCtx.Err()
			}
		}, 0)
		require.ErrorIs(t, err, ErrRankingsPending)
	}
	first, second := <-started, <-started
	cancelUserRankingBuilds(generation)
	// Whichever goroutine started first, exactly the old generation is cancelled.
	assert.NotEqual(t, first.Err(), second.Err())
	newErr := runRankingBuild(ctx, newKey, func(context.Context) error {
		t.Error("the live generation was replaced")
		return nil
	}, 0)
	require.ErrorIs(t, newErr, ErrRankingsPending)
	close(release)
	waitForRankingBuilds(t)
}

func TestUserRankingBackgroundWorkerBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	t.Cleanup(func() {
		rankingBuilds.Lock()
		for key, job := range rankingBuilds.items {
			if strings.HasPrefix(key, "test:worker-bound:") {
				job.cancel()
			}
		}
		rankingBuilds.Unlock()
		waitForRankingBuilds(t)
	})
	started, release := make(chan struct{}, 16), make(chan struct{})
	for i := range 16 {
		err := runRankingBuild(ctx, fmt.Sprintf("test:worker-bound:%d", i), func(buildCtx context.Context) error {
			started <- struct{}{}
			select {
			case <-release:
				return nil
			case <-buildCtx.Done():
				return buildCtx.Err()
			}
		}, 0)
		require.ErrorIs(t, err, ErrRankingsPending)
	}
	for range 16 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("background workers did not reach their controlled barriers")
		}
	}
	err := runRankingBuild(ctx, "test:worker-bound:overflow", func(context.Context) error {
		t.Error("a seventeenth concurrent background build was started")
		return nil
	}, 0)
	require.ErrorIs(t, err, ErrRankingsPending)
	close(release)
	waitForRankingBuilds(t)
}

func TestUserRankingModelBucketDatabaseMatrix(t *testing.T) {
	for _, engine := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := openUserRankingTestDatabase(t, engine)
			require.NoError(t, db.AutoMigrate(&model.QuotaData{}, &model.Ability{}, &model.Channel{}, &model.Model{}, &model.Vendor{}))
			require.NoError(t, db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.QuotaData{}).Error)
			previousDB, previousType := model.DB, common.MainDatabaseType()
			model.DB = db
			common.SetMainDatabaseType(common.DatabaseType(engine))
			model.InvalidatePricingCache()
			t.Cleanup(func() {
				waitForRankingBuilds(t)
				model.DB = previousDB
				common.SetMainDatabaseType(previousType)
				model.InvalidatePricingCache()
			})
			now := time.Date(2026, 1, 12, 12, 30, 0, 0, time.UTC)
			config, err := rankingConfig("24h")
			require.NoError(t, err)
			start, end := rankingTimeRange(config, now)
			entries := []model.QuotaData{
				{ModelName: "alpha", CreatedAt: start + 1, TokenUsed: 100},
				{ModelName: "alpha", CreatedAt: start + 2, TokenUsed: -20},
				{ModelName: "alpha", CreatedAt: start + 3600, TokenUsed: -30},
				{ModelName: "alpha", CreatedAt: start + 7200, TokenUsed: 5},
				{ModelName: "alpha", CreatedAt: start + 7201, TokenUsed: -5},
				{ModelName: "beta", CreatedAt: start + 1, TokenUsed: 50},
				{ModelName: "zero", CreatedAt: start + 1, TokenUsed: 20},
				{ModelName: "zero", CreatedAt: start + 3600, TokenUsed: -20},
				{ModelName: "negative", CreatedAt: start + 1, TokenUsed: 10},
				{ModelName: "negative", CreatedAt: start + 3600, TokenUsed: -20},
				{CreatedAt: start + 1, TokenUsed: 1000},
				{ModelName: "alpha", CreatedAt: start - 1, TokenUsed: 25},
				{ModelName: "beta", CreatedAt: start - 1, TokenUsed: 100},
			}
			require.NoError(t, db.Create(&entries).Error)
			legacy, err := model.GetRankingQuotaTotalsWithContext(t.Context(), start, end)
			require.NoError(t, err)
			assert.Equal(t, []model.RankingQuotaTotal{{ModelName: "alpha", TotalTokens: 50}, {ModelName: "beta", TotalTokens: 50}}, legacy)
			got, err := buildRankingsSnapshotWithContext(t.Context(), config, now)
			require.NoError(t, err)
			require.Len(t, got.Models, 2)
			for i, item := range legacy {
				assert.Equal(t, item.ModelName, got.Models[i].ModelName)
				assert.Equal(t, item.TotalTokens, got.Models[i].TotalTokens)
			}
			assert.Equal(t, 100.0, got.Models[0].GrowthPct)
			assert.Equal(t, -50.0, got.Models[1].GrowthPct)
			require.Len(t, got.Vendors, 1)
			assert.Equal(t, int64(100), got.Vendors[0].TotalTokens)
			var historyTotal int64
			for _, point := range got.ModelsHistory.Points {
				assert.Positive(t, point.Tokens)
				historyTotal += point.Tokens
			}
			assert.Equal(t, int64(130), historyTotal, "positive-only history must not inflate signed period totals")
			// Request tests use recent data so their rolling window survives calendar changes.
			require.NoError(t, db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.QuotaData{}).Error)
			recent := time.Now().Unix() - 10
			require.NoError(t, db.Create(&[]model.QuotaData{
				{ModelName: "alpha", CreatedAt: recent, TokenUsed: 75},
				{ModelName: "beta", CreatedAt: recent, TokenUsed: 150},
			}).Error)
			started, release := make(chan context.Context, 1), make(chan struct{})
			var once sync.Once
			const callback = "test:ranking_model_background"
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table != "quota_data" {
					return
				}
				once.Do(func() {
					started <- tx.Statement.Context
					select {
					case <-release:
					case <-tx.Statement.Context.Done():
						tx.AddError(tx.Statement.Context.Err())
					}
				})
			}))
			t.Cleanup(func() {
				rankingBuilds.Lock()
				for key, job := range rankingBuilds.items {
					if strings.HasPrefix(key, fmt.Sprintf("models:%p:", db)) {
						job.cancel()
					}
				}
				rankingBuilds.Unlock()
				waitForRankingBuilds(t)
				assert.NoError(t, db.Callback().Query().Remove(callback))
			})
			requestCtx, cancelRequest := context.WithCancel(t.Context())
			defer cancelRequest()
			requestDone := make(chan error, 1)
			go func() {
				_, err := RequestRankingsSnapshot(requestCtx, "24h")
				requestDone <- err
			}()
			barrierCtx, stopBarrier := context.WithTimeout(t.Context(), 10*time.Second)
			defer stopBarrier()
			var buildCtx context.Context
			select {
			case buildCtx = <-started:
			case <-barrierCtx.Done():
				t.Fatal("model ranking request did not reach its controlled quota query")
			}
			cancelRequest()
			require.ErrorIs(t, <-requestDone, context.Canceled)
			assert.NoError(t, buildCtx.Err())
			close(release)
			waitForRankingBuilds(t)
			ready, err := RequestRankingsSnapshot(t.Context(), "24h")
			require.NoError(t, err)
			require.Len(t, ready.Models, 2)
			assert.Equal(t, int64(150), ready.Models[0].TotalTokens)
			assert.Equal(t, int64(75), ready.Models[1].TotalTokens)
			rankingCacheMu.Lock()
			for key, cached := range rankingCache {
				if cached.data == ready {
					cached.expiresAt = time.Now().Add(-time.Second)
					rankingCache[key] = cached
				}
			}
			rankingCacheMu.Unlock()
			once = sync.Once{}
			started, release = make(chan context.Context, 1), make(chan struct{})
			stale, err := RequestRankingsSnapshot(t.Context(), "24h")
			require.NoError(t, err)
			assert.Same(t, ready, stale, "an expired complete snapshot must return while refresh is blocked")
			select {
			case <-started:
			case <-barrierCtx.Done():
				t.Fatal("stale model ranking refresh did not reach its controlled quota query")
			}
			close(release)
			waitForRankingBuilds(t)
		})
	}
}

func TestUserRankingClickHouseUsage(t *testing.T) {
	logs := openUserRankingTestDatabase(t, "clickhouse")
	require.False(t, logs.Migrator().HasTable(&model.Log{}), "ClickHouse DSN must point to this task's isolated database")
	require.NoError(t, logs.Exec(`CREATE TABLE logs (
		id Int64, user_id Int32, created_at Int64, type Int32,
		prompt_tokens Nullable(Int32), completion_tokens Nullable(Int32),
		quota Nullable(Int32), other Nullable(String), model_name Nullable(String)
	) ENGINE = MergeTree() ORDER BY (created_at, user_id)`).Error)
	t.Cleanup(func() { assert.NoError(t, logs.Migrator().DropTable(&model.Log{})) })
	mainDB := openUserRankingTestDatabase(t, "sqlite")
	require.NoError(t, mainDB.AutoMigrate(&model.Log{}))
	previousDB, previousLogs := model.DB, model.LOG_DB
	previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	model.DB, model.LOG_DB = mainDB, logs
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeClickHouse)
	InvalidateUserRankings()
	t.Cleanup(func() {
		InvalidateUserRankings()
		waitForRankingBuilds(t)
		model.DB, model.LOG_DB = previousDB, previousLogs
		common.SetDatabaseTypes(previousMainType, previousLogType)
	})
	const userID = 97001
	day := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC).Unix()
	for _, entry := range []struct {
		user, offset              int64
		prompt, completion, quota any
		other, modelName          any
	}{
		{userID, 1, 100, 20, 7, `{"cache_tokens":80}`, "openai"},
		{userID, 1, 100, 20, 11, `{"usage_semantic":"anthropic","cache_tokens":80,"cache_creation_tokens":30}`, "claude"},
		{userID, 3601, nil, 5, nil, nil, nil},
		{userID + 1, 1, 9999, 0, 9999, "", "other-user"},
	} {
		require.NoError(t, logs.Exec("INSERT INTO logs (id, user_id, created_at, type, prompt_tokens, completion_tokens, quota, other, model_name) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			0, entry.user, day+entry.offset, model.LogTypeConsume, entry.prompt, entry.completion, entry.quota, entry.other, entry.modelName).Error)
	}
	require.NoError(t, mainDB.Create(&model.Log{UserId: userID, CreatedAt: day + 1, Type: model.LogTypeConsume, PromptTokens: 99999}).Error)
	for range 2 {
		got, err := model.GetUserRankingUsage(t.Context(), []int{userID}, day, day+7200)
		require.NoError(t, err)
		assert.Equal(t, model.RankingUserUsage{TotalTokens: 355, TotalQuota: 18, Models: map[string]model.RankingModelUsage{
			"openai": {TotalTokens: 120, TotalQuota: 7}, "claude": {TotalTokens: 230, TotalQuota: 11}, "": {TotalTokens: 5},
		}}, got[userID], "zero and duplicate ClickHouse IDs must retain every log row")
	}
}

func TestUserRankingRedisPersistsAcrossLogConnections(t *testing.T) {
	cache := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: cache.Addr(), MaxRetries: -1})
	previousRedis, previousEnabled := common.RDB, common.RedisEnabled
	previousLogs, previousLogType := model.LOG_DB, common.LogDatabaseType()
	common.RDB, common.RedisEnabled = client, true
	common.SetLogDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		common.RDB, common.RedisEnabled = previousRedis, previousEnabled
		model.LOG_DB = previousLogs
		common.SetLogDatabaseType(previousLogType)
		assert.NoError(t, client.Close())
	})
	root := t.TempDir()
	source, other := filepath.Join(root, "source.db"), filepath.Join(root, "other.db")
	const userID = 98001
	hour := time.Date(2026, 1, 10, 1, 0, 0, 0, time.UTC).Unix()
	for _, test := range []struct {
		name, path           string
		insert, remove, want int
		payload, disconnect  bool
	}{
		{"cold_source", source, 42, 0, 42, true, false},
		{"same_dsn_new_handle", source, 0, 0, 42, false, false},
		{"other_dsn_same_ids_and_count", other, 90, 0, 90, true, false},
		{"backfill_changes_signature", source, 8, 0, 50, true, false},
		{"delete_nonmaximum_id", source, 0, 1, 8, true, false},
		{"redis_disconnect_falls_back", source, 0, 0, 8, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(test.path), &gorm.Config{})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			pool.SetMaxOpenConns(1)
			t.Cleanup(func() { assert.NoError(t, pool.Close()) })
			require.NoError(t, db.AutoMigrate(&model.Log{}))
			model.LOG_DB = db
			if test.insert > 0 {
				require.NoError(t, db.Create(&model.Log{UserId: userID, CreatedAt: hour + 1, Type: model.LogTypeConsume, PromptTokens: test.insert, Quota: test.insert}).Error)
			}
			if test.remove > 0 {
				require.NoError(t, db.Delete(&model.Log{}, test.remove).Error)
			}
			if test.disconnect {
				cache.Close()
			}
			observer := &rankingPayloadObserver{}
			const callback = "test:ranking_redis_payload"
			require.NoError(t, db.Callback().Row().Before("gorm:row").Register(callback, observer.observe))
			t.Cleanup(func() { assert.NoError(t, db.Callback().Row().Remove(callback)) })
			got, err := model.GetUserRankingUsage(t.Context(), []int{userID}, hour, hour+3599)
			require.NoError(t, err)
			assert.Equal(t, int64(test.want), got[userID].TotalTokens)
			assert.Equal(t, int64(test.want), got[userID].TotalQuota)
			if test.payload {
				assert.Positive(t, observer.queries)
			} else {
				assert.Zero(t, observer.queries, "a new connection must load completed usage from Redis")
			}
			if !test.disconnect {
				assert.NotEmpty(t, cache.Keys())
			}
		})
	}
}
