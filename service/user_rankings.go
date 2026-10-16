package service

import (
	"context"
	"maps"
	"sort"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/model"
	"golang.org/x/sync/singleflight"
)

type RankedUser struct {
	Rank           int               `json:"rank"`
	Name           string            `json:"name"`
	Nickname       string            `json:"nickname,omitempty"`
	MaskedUsername string            `json:"masked_username"`
	TotalTokens    *int64            `json:"total_tokens,omitempty"`
	TotalQuota     *int64            `json:"total_quota,omitempty"`
	UsagePublic    bool              `json:"usage_public"` // Model details only; totals follow board participation.
	Models         []RankedUserModel `json:"models,omitempty"`
	IsSelf         bool              `json:"is_self"`
}

type RankedUserModel struct {
	ModelName   string `json:"model_name"`
	TotalTokens *int64 `json:"total_tokens,omitempty"`
	TotalQuota  *int64 `json:"total_quota,omitempty"`
}

type UserRankingsResponse struct {
	Metric       string       `json:"metric"`
	Users        []RankedUser `json:"users"`
	Participants int          `json:"participants"`
	// Snapshot totals include only quantities for boards each user has joined.
	TotalTokens int64       `json:"total_tokens"`
	TotalQuota  int64       `json:"total_quota"`
	PeriodStart int64       `json:"period_start"`
	PeriodEnd   int64       `json:"period_end"`
	UpdatedAt   int64       `json:"updated_at"`
	Self        *RankedUser `json:"self,omitempty"`
}

type userRankingTotals struct {
	totals     map[int]model.RankingUserUsage
	start, end int64
	expires    time.Time
}

var userRankingCache = struct {
	sync.Mutex
	items      map[string]userRankingTotals
	generation int
}{items: make(map[string]userRankingTotals)}
var userRankingBuild singleflight.Group

func InvalidateUserRankings() {
	userRankingCache.Lock()
	clear(userRankingCache.items)
	userRankingCache.generation++
	userRankingCache.Unlock()
}

// The optional metric keeps existing token-board callers source compatible.
func GetUserRankings(ctx context.Context, period string, viewerID int, metrics ...string) (*UserRankingsResponse, error) {
	metric := model.RankingMetricTokens
	if len(metrics) > 0 && metrics[0] != "" {
		metric = metrics[0]
	}
	if metric != model.RankingMetricTokens && metric != model.RankingMetricSpending {
		return nil, model.ErrInvalidRankingMetric
	}
	config, err := rankingConfig(period)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	cacheKey := metric + ":" + config.id
	if metric == model.RankingMetricSpending && config.id == "today" {
		// A calendar-day cache must never survive the server's local midnight.
		cacheKey += ":" + now.Format("2006-01-02")
	}
	value, err, _ := userRankingBuild.Do(cacheKey, func() (any, error) {
		userRankingCache.Lock()
		cached, ok := userRankingCache.items[cacheKey]
		generation := userRankingCache.generation
		userRankingCache.Unlock()
		if ok && time.Now().Before(cached.expires) {
			return cached, nil
		}
		participants, err := model.GetRankingParticipants(ctx, metric)
		if err != nil {
			return nil, err
		}
		ids := make([]int, 0, len(participants))
		for _, participant := range participants {
			ids = append(ids, participant.ID)
		}
		start, end := userRankingTimeRange(config, metric, now)
		queryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		totals, err := model.GetUserRankingUsage(queryCtx, ids, start, end)
		if err != nil {
			return nil, err
		}
		cached = userRankingTotals{totals: totals, start: start, end: end, expires: time.Now().Add(time.Minute)}
		userRankingCache.Lock()
		if generation == userRankingCache.generation {
			// Drop expired calendar-day keys along with other stale snapshots.
			for key, item := range userRankingCache.items {
				if !now.Before(item.expires) {
					delete(userRankingCache.items, key)
				}
			}
			userRankingCache.items[cacheKey] = cached
		}
		userRankingCache.Unlock()
		return cached, nil
	})
	if err != nil {
		return nil, err
	}
	cached := value.(userRankingTotals)
	// Consent and account status are NEVER cached: withdrawal takes effect on
	// the next read on every instance, even while token totals are cached.
	participants, err := model.GetRankingParticipants(ctx, metric)
	if err != nil {
		return nil, err
	}
	// A user can opt in on another instance while this totals cache is warm.
	// Fill only missing users, keeping the shared cached map immutable.
	missing := make([]int, 0)
	for _, participant := range participants {
		if _, exists := cached.totals[participant.ID]; !exists {
			missing = append(missing, participant.ID)
		}
	}
	if len(missing) > 0 {
		queryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		totals, err := model.GetUserRankingUsage(queryCtx, missing, cached.start, cached.end)
		if err != nil {
			return nil, err
		}
		cached.totals = maps.Clone(cached.totals)
		maps.Copy(cached.totals, totals)
	}
	sort.SliceStable(participants, func(i, j int) bool {
		left, right := cached.totals[participants[i].ID], cached.totals[participants[j].ID]
		if metric == model.RankingMetricSpending {
			return left.TotalQuota > right.TotalQuota
		}
		return left.TotalTokens > right.TotalTokens
	})
	result := &UserRankingsResponse{
		Metric: metric,
		Users:  make([]RankedUser, 0), Participants: len(participants),
		PeriodStart: cached.start, PeriodEnd: cached.end, UpdatedAt: cached.end,
	}
	for _, participant := range participants {
		usage := cached.totals[participant.ID]
		if (metric == model.RankingMetricTokens && usage.TotalTokens <= 0) ||
			(metric == model.RankingMetricSpending && usage.TotalQuota <= 0) {
			continue
		}
		maskedUsername := model.MaskRankingName(participant.Username)
		name := participant.Nickname
		if name == "" {
			name = maskedUsername
		}
		row := RankedUser{Rank: len(result.Users) + 1, Name: name, Nickname: participant.Nickname, MaskedUsername: maskedUsername, IsSelf: participant.ID == viewerID}
		publicTokens := participant.Participating
		publicSpending := participant.SpendingParticipating
		if publicTokens {
			row.TotalTokens = &usage.TotalTokens
			result.TotalTokens += usage.TotalTokens
		}
		if publicSpending {
			row.TotalQuota = &usage.TotalQuota
			result.TotalQuota += usage.TotalQuota
		}
		// One shared choice gates model names and per-model quantities on both
		// boards. Each quantity still requires its own board's live consent.
		row.UsagePublic = participant.TokenUsagePublic && participant.SpendingUsagePublic
		if row.UsagePublic {
			for name, item := range usage.Models {
				if (metric == model.RankingMetricTokens && item.TotalTokens <= 0) ||
					(metric == model.RankingMetricSpending && item.TotalQuota <= 0) {
					continue
				}
				detail := RankedUserModel{ModelName: name}
				if publicTokens {
					detail.TotalTokens = &item.TotalTokens
				}
				if publicSpending {
					detail.TotalQuota = &item.TotalQuota
				}
				row.Models = append(row.Models, detail)
			}
			sort.Slice(row.Models, func(i, j int) bool {
				if metric == model.RankingMetricSpending {
					if *row.Models[i].TotalQuota != *row.Models[j].TotalQuota {
						return *row.Models[i].TotalQuota > *row.Models[j].TotalQuota
					}
				} else if *row.Models[i].TotalTokens != *row.Models[j].TotalTokens {
					return *row.Models[i].TotalTokens > *row.Models[j].TotalTokens
				}
				return row.Models[i].ModelName < row.Models[j].ModelName
			})
		}
		result.Users = append(result.Users, row)
		if row.IsSelf {
			result.Self = &row
		}
	}
	if len(result.Users) > 100 {
		result.Users = result.Users[:100]
	}
	return result, nil
}

func userRankingTimeRange(config rankingPeriodConfig, metric string, now time.Time) (int64, int64) {
	if metric == model.RankingMetricSpending && config.id == "today" {
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		return start.Unix(), now.Unix()
	}
	return rankingTimeRange(config, now)
}
