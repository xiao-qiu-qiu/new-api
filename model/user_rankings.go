package model

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Consent is independent of notification settings and never defaults to opt-in.
type UserRankingPreference struct {
	UserID                int    `json:"-" gorm:"primaryKey;autoIncrement:false"`
	Participating         bool   `json:"participating"`
	SpendingParticipating bool   `json:"spending_participating"`
	TokenUsagePublic      bool   `json:"token_usage_public"`
	SpendingUsagePublic   bool   `json:"spending_usage_public"`
	ModelDetailsPublic    bool   `json:"model_details_public" gorm:"-"`
	Prompted              bool   `json:"prompted"`
	Nickname              string `json:"nickname" gorm:"size:96"`
	NicknameChangedAt     int64  `json:"nickname_changed_at"`
	NextNicknameChangeAt  int64  `json:"next_nickname_change_at" gorm:"-"`
}

const RankingNicknameCooldown = 30 * 24 * time.Hour

const (
	RankingMetricTokens   = "tokens"
	RankingMetricSpending = "spending"
)

var ErrInvalidRankingMetric = errors.New("Invalid ranking metric")

// Pointer fields distinguish an omitted choice from explicitly withdrawing it.
type UserRankingPreferenceUpdate struct {
	Participating         *bool `json:"participating"`
	SpendingParticipating *bool `json:"spending_participating"`
	TokenUsagePublic      *bool `json:"token_usage_public"`
	SpendingUsagePublic   *bool `json:"spending_usage_public"`
	ModelDetailsPublic    *bool `json:"model_details_public"`
}

var ErrRankingNicknameCooldown = errors.New("You can change your ranking nickname once every 30 days")
var ErrInvalidRankingNickname = errors.New("Ranking nicknames must be at most 24 characters and contain no control characters")

type RankingParticipant struct {
	ID                    int
	Username              string
	Nickname              string
	Participating         bool
	SpendingParticipating bool
	TokenUsagePublic      bool
	SpendingUsagePublic   bool
}

func GetUserRankingPreference(ctx context.Context, userID int) (UserRankingPreference, error) {
	preference := UserRankingPreference{UserID: userID}
	err := DB.WithContext(ctx).First(&preference, "user_id = ?", userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = nil
	}
	// Both legacy choices must allow disclosure before sharing model details.
	preference.ModelDetailsPublic = preference.TokenUsagePublic && preference.SpendingUsagePublic
	if preference.NicknameChangedAt > 0 {
		preference.NextNicknameChangeAt = preference.NicknameChangedAt + int64(RankingNicknameCooldown/time.Second)
	}
	return preference, err
}

// The conditional UPDATE enforces the cooldown across concurrent requests and instances.
func SetRankingNickname(ctx context.Context, userID int, nickname string, now time.Time) error {
	nickname = strings.TrimSpace(nickname)
	if !utf8.ValidString(nickname) || utf8.RuneCountInString(nickname) > 24 || strings.ContainsFunc(nickname, unicode.IsControl) {
		return ErrInvalidRankingNickname
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		preference := UserRankingPreference{UserID: userID}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&preference).Error; err != nil {
			return err
		}
		if err := tx.First(&preference, "user_id = ?", userID).Error; err != nil {
			return err
		}
		if preference.Nickname == nickname {
			return nil
		}
		result := tx.Model(&UserRankingPreference{}).
			Where("user_id = ? AND (nickname_changed_at IS NULL OR nickname_changed_at = 0 OR nickname_changed_at <= ?)", userID, now.Add(-RankingNicknameCooldown).Unix()).
			Updates(map[string]any{"nickname": nickname, "nickname_changed_at": now.Unix()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrRankingNicknameCooldown
		}
		return nil
	})
}

// Additional choices keep the legacy order: spending participation and the two
// disclosure flags. Disclosure now requires both flags to be explicitly true.
func SetUserRankingPreference(ctx context.Context, userID int, participating bool, choices ...bool) error {
	update := UserRankingPreferenceUpdate{Participating: &participating}
	if len(choices) > 0 {
		update.SpendingParticipating = &choices[0]
	}
	if len(choices) > 1 {
		update.TokenUsagePublic = &choices[1]
	}
	if len(choices) > 2 {
		update.SpendingUsagePublic = &choices[2]
	}
	return UpdateUserRankingPreference(ctx, userID, update)
}

func UpdateUserRankingPreference(ctx context.Context, userID int, update UserRankingPreferenceUpdate) error {
	preference := UserRankingPreference{UserID: userID, Prompted: true}
	columns := []string{"prompted"}
	if update.Participating != nil {
		preference.Participating = *update.Participating
		columns = append(columns, "participating")
	}
	if update.SpendingParticipating != nil {
		preference.SpendingParticipating = *update.SpendingParticipating
		columns = append(columns, "spending_participating")
	}
	if update.ModelDetailsPublic != nil || update.TokenUsagePublic != nil || update.SpendingUsagePublic != nil {
		// The shared choice takes precedence. Legacy clients must submit both
		// flags as true together; a partial legacy write can only hide details.
		public := update.TokenUsagePublic != nil && update.SpendingUsagePublic != nil &&
			*update.TokenUsagePublic && *update.SpendingUsagePublic
		if update.ModelDetailsPublic != nil {
			public = *update.ModelDetailsPublic
		}
		preference.TokenUsagePublic = public
		preference.SpendingUsagePublic = public
		columns = append(columns, "token_usage_public", "spending_usage_public")
	}
	return DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns(columns),
	}).Create(&preference).Error
}

func GetRankingParticipants(ctx context.Context, metrics ...string) ([]RankingParticipant, error) {
	column := "p.participating"
	if len(metrics) > 0 && metrics[0] != "" && metrics[0] != RankingMetricTokens {
		if metrics[0] != RankingMetricSpending {
			return nil, ErrInvalidRankingMetric
		}
		column = "p.spending_participating"
	}
	participants := make([]RankingParticipant, 0)
	err := DB.WithContext(ctx).Model(&User{}).
		Select("users.id, users.username, p.nickname, p.participating, p.spending_participating, p.token_usage_public, p.spending_usage_public").
		Joins("JOIN user_ranking_preferences p ON p.user_id = users.id").
		Where(column+" = ? AND users.status = ?", true, common.UserStatusEnabled).
		Order("users.id").Scan(&participants).Error
	return participants, err
}

// Administrative rankings include every current account, regardless of public
// participation or account status. GORM retains the User soft-delete scope.
func GetAdminRankingUsers(ctx context.Context) ([]RankingParticipant, error) {
	users := make([]RankingParticipant, 0)
	err := DB.WithContext(ctx).Model(&User{}).
		Select("users.id, users.username, p.nickname").
		Joins("LEFT JOIN user_ranking_preferences p ON p.user_id = users.id").
		Order("users.id").Scan(&users).Error
	return users, err
}

// Normalize the same explicit usage semantics that consumption logs record.
// OpenAI prompt tokens already include cache hits; Anthropic input tokens do not.
func RankingTokenTotal(prompt, completion int64, other string) int64 {
	var usage struct {
		InputTotal    *int64 `json:"input_tokens_total"`
		Semantic      string `json:"usage_semantic"`
		Claude        bool   `json:"claude"`
		CacheRead     int64  `json:"cache_tokens"`
		CacheWrite    int64  `json:"cache_write_tokens"`
		CacheCreation int64  `json:"cache_creation_tokens"`
		Cache5m       int64  `json:"cache_creation_tokens_5m"`
		Cache1h       int64  `json:"cache_creation_tokens_1h"`
	}
	_ = common.UnmarshalJsonStr(other, &usage)
	input := max(prompt, 0)
	if usage.InputTotal != nil && *usage.InputTotal >= 0 {
		input = *usage.InputTotal
	} else if usage.Semantic == "anthropic" || (usage.Semantic == "" && usage.Claude) {
		write := max(usage.CacheWrite, usage.CacheCreation, max(usage.Cache5m, 0)+max(usage.Cache1h, 0))
		input += max(usage.CacheRead, 0) + max(write, 0)
	}
	return input + max(completion, 0)
}

// Stream only the required fields from the log database. This works with a
// separate log database and avoids database-specific JSON arithmetic.
func GetUserRankingTotals(ctx context.Context, userIDs []int, start, end int64) (map[int]int64, error) {
	usage, err := GetUserRankingUsage(ctx, userIDs, start, end)
	if err != nil {
		return nil, err
	}
	totals := make(map[int]int64, len(userIDs))
	for userID, item := range usage {
		totals[userID] = item.TotalTokens
	}
	return totals, nil
}

type RankingModelUsage struct {
	TotalTokens int64
	TotalQuota  int64
}

type RankingUserUsage struct {
	TotalTokens int64
	TotalQuota  int64
	Models      map[string]RankingModelUsage
}

// This is internal usage only. Public rankings must apply live consent before
// returning totals or model names; administrative access requires AdminAuth.
func GetUserRankingUsage(ctx context.Context, userIDs []int, start, end int64) (map[int]RankingUserUsage, error) {
	totals := make(map[int]RankingUserUsage, len(userIDs))
	for _, userID := range userIDs {
		totals[userID] = RankingUserUsage{Models: make(map[string]RankingModelUsage)}
	}
	for offset := 0; offset < len(userIDs); offset += 500 {
		batch := userIDs[offset:min(offset+500, len(userIDs))]
		rows, err := LOG_DB.WithContext(ctx).Model(&Log{}).
			Select("user_id, prompt_tokens, completion_tokens, other, model_name, quota").
			Where("type = ? AND user_id IN ? AND created_at >= ? AND created_at <= ?", LogTypeConsume, batch, start, end).
			Rows()
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var userID int
			var prompt, completion int64
			var quota *int64
			var other, modelName *string
			if err := rows.Scan(&userID, &prompt, &completion, &other, &modelName, &quota); err != nil {
				rows.Close()
				return nil, err
			}
			metadata := ""
			if other != nil {
				metadata = *other
			}
			tokens := RankingTokenTotal(prompt, completion, metadata)
			var consumed int64
			if quota != nil {
				consumed = max(*quota, 0)
			}
			usage := totals[userID]
			usage.TotalTokens += tokens
			usage.TotalQuota += consumed
			name := ""
			if modelName != nil {
				name = *modelName
			}
			// Unknown models still contribute to both totals and share bars.
			item := usage.Models[name]
			item.TotalTokens += tokens
			item.TotalQuota += consumed
			usage.Models[name] = item
			totals[userID] = usage
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return totals, nil
}

func MaskRankingName(name string) string {
	characters := []rune(strings.TrimSpace(name))
	if len(characters) <= 2 {
		return "**"
	}
	return string(characters[0]) + strings.Repeat("*", len(characters)-2) + string(characters[len(characters)-1])
}
