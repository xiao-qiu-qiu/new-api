package service

import (
	"context"
	"errors"
	"fmt"
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
	Username       string            `json:"username,omitempty"` // Administrative rankings only.
	Nickname       string            `json:"nickname,omitempty"`
	MaskedUsername string            `json:"masked_username"`
	TotalTokens    *int64            `json:"total_tokens,omitempty"`
	TotalQuota     *int64            `json:"total_quota,omitempty"`
	UsagePublic    bool              `json:"usage_public"` // Model details are available to this viewer.
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
	// Public snapshot totals include only quantities for boards each user has joined.
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

type userRankingOptions struct {
	administrator bool
	cachedOnly    bool
	background    bool
}

func (options userRankingOptions) participants(ctx context.Context, metric string) ([]model.RankingParticipant, error) {
	if options.administrator {
		return model.GetAdminRankingUsers(ctx)
	}
	return model.GetRankingParticipants(ctx, metric)
}

func InvalidateUserRankings() {
	userRankingCache.Lock()
	clear(userRankingCache.items)
	generation := userRankingCache.generation
	userRankingCache.generation++
	userRankingCache.Unlock()
	cancelUserRankingBuilds(generation)
}

// The optional metric keeps existing token-board callers source compatible.
func GetUserRankings(ctx context.Context, period string, viewerID int, metrics ...string) (*UserRankingsResponse, error) {
	metric := model.RankingMetricTokens
	if len(metrics) > 0 && metrics[0] != "" {
		metric = metrics[0]
	}
	return getUserRankings(ctx, period, viewerID, metric, userRankingOptions{})
}

// Callers must enforce administrator authorization before requesting this view.
func GetAdminUserRankings(ctx context.Context, period string, viewerID int, metric string) (*UserRankingsResponse, error) {
	return getUserRankings(ctx, period, viewerID, metric, userRankingOptions{administrator: true})
}

func RequestUserRankings(ctx context.Context, period string, viewerID int, metric string) (*UserRankingsResponse, error) {
	return requestUserRankings(ctx, period, viewerID, metric, userRankingOptions{})
}

func RequestAdminUserRankings(ctx context.Context, period string, viewerID int, metric string) (*UserRankingsResponse, error) {
	return requestUserRankings(ctx, period, viewerID, metric, userRankingOptions{administrator: true})
}

func requestUserRankings(ctx context.Context, period string, viewerID int, metric string, options userRankingOptions) (*UserRankingsResponse, error) {
	options.cachedOnly = true
	result, err := getUserRankings(ctx, period, viewerID, metric, options)
	if err != nil && !errors.Is(err, ErrRankingsPending) {
		return nil, err
	}
	config, err := rankingConfig(period)
	if err != nil {
		return nil, err
	}
	audience := "public"
	if options.administrator {
		audience = "admin"
	}
	cacheKey := fmt.Sprintf("%p:%p:%s:%s", model.DB, model.LOG_DB, audience, config.cacheKey(time.Now()))
	userRankingCache.Lock()
	cached, ok := userRankingCache.items[cacheKey]
	generation := userRankingCache.generation
	userRankingCache.Unlock()
	if result != nil && ok && time.Now().Before(cached.expires) {
		return result, nil
	}
	options.cachedOnly, options.background = false, true
	wait := 150 * time.Millisecond
	if result != nil {
		wait = 0
	}
	buildErr := runRankingBuild(ctx, fmt.Sprintf("users:%d:%s", generation, cacheKey), func(buildCtx context.Context) error {
		userRankingCache.Lock()
		currentGeneration := userRankingCache.generation
		userRankingCache.Unlock()
		if currentGeneration != generation {
			return context.Canceled
		}
		_, err := getUserRankings(buildCtx, period, 0, metric, options)
		return err
	}, wait)
	if result != nil {
		return result, nil
	}
	if buildErr != nil {
		if errors.Is(buildErr, context.Canceled) && ctx.Err() == nil {
			return nil, ErrRankingsPending
		}
		return nil, buildErr
	}
	options.cachedOnly, options.background = true, false
	return getUserRankings(ctx, period, viewerID, metric, options)
}

func getUserRankings(ctx context.Context, period string, viewerID int, metric string, options userRankingOptions) (*UserRankingsResponse, error) {
	if metric != model.RankingMetricTokens && metric != model.RankingMetricSpending {
		return nil, model.ErrInvalidRankingMetric
	}
	config, err := rankingConfig(period)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	audience := "public"
	if options.administrator {
		audience = "admin"
	}
	// Both metrics are derived from the same usage; build it once even when
	// older clients request the two boards concurrently.
	cacheKey := fmt.Sprintf("%p:%p:%s:%s", model.DB, model.LOG_DB, audience, config.cacheKey(now))
	queryTimeout := 20 * time.Second
	if options.background {
		queryTimeout = rankingBackgroundTimeout
	}
	build := func() (any, error) {
		userRankingCache.Lock()
		cached, ok := userRankingCache.items[cacheKey]
		generation := userRankingCache.generation
		userRankingCache.Unlock()
		if ok && time.Now().Before(cached.expires) {
			return cached, nil
		}
		participants, err := options.participants(ctx, metric)
		if err != nil {
			return nil, err
		}
		ids := make([]int, 0, len(participants))
		for _, participant := range participants {
			ids = append(ids, participant.ID)
		}
		if !options.administrator {
			otherMetric := model.RankingMetricTokens
			if metric == model.RankingMetricTokens {
				otherMetric = model.RankingMetricSpending
			}
			otherParticipants, err := options.participants(ctx, otherMetric)
			if err != nil {
				return nil, err
			}
			included := make(map[int]bool, len(ids))
			for _, id := range ids {
				included[id] = true
			}
			for _, participant := range otherParticipants {
				if !included[participant.ID] {
					ids = append(ids, participant.ID)
				}
			}
		}
		start, end := rankingTimeRange(config, now)
		queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
		defer cancel()
		totals, err := model.GetUserRankingUsage(queryCtx, ids, start, end)
		if err != nil {
			return nil, err
		}
		cached = userRankingTotals{totals: totals, start: start, end: end, expires: time.Now().Add(time.Minute)}
		userRankingCache.Lock()
		if generation == userRankingCache.generation {
			// Drop expired calendar-period keys along with other stale snapshots.
			for key, item := range userRankingCache.items {
				if now.After(item.expires.Add(rankingSnapshotMaxStale)) {
					delete(userRankingCache.items, key)
				}
			}
			userRankingCache.items[cacheKey] = cached
		}
		userRankingCache.Unlock()
		return cached, nil
	}
	var cached userRankingTotals
	if options.cachedOnly {
		userRankingCache.Lock()
		var ok bool
		cached, ok = userRankingCache.items[cacheKey]
		userRankingCache.Unlock()
		if !ok || now.After(cached.expires.Add(rankingSnapshotMaxStale)) {
			return nil, ErrRankingsPending
		}
	}
	for !options.cachedOnly {
		if options.background {
			value, err, _ := userRankingBuild.Do(cacheKey, build)
			if errors.Is(err, context.Canceled) && ctx.Err() == nil {
				continue
			}
			if err != nil {
				return nil, err
			}
			cached = value.(userRankingTotals)
			break
		}
		result := userRankingBuild.DoChan(cacheKey, build)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case built := <-result:
			// A tab/metric switch can cancel the request that started a shared
			// build. Its live waiters should start a new build, not inherit 503.
			if errors.Is(built.Err, context.Canceled) && ctx.Err() == nil {
				continue
			}
			if built.Err != nil {
				return nil, built.Err
			}
			cached = built.Val.(userRankingTotals)
		}
		break
	}
	// Consent and account status are NEVER cached: withdrawal takes effect on
	// the next read on every instance, even while token totals are cached.
	participants, err := options.participants(ctx, metric)
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
		if options.cachedOnly {
			return nil, ErrRankingsPending
		}
		queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
		defer cancel()
		totals, err := model.GetUserRankingUsage(queryCtx, missing, cached.start, cached.end)
		if err != nil {
			return nil, err
		}
		cached.totals = maps.Clone(cached.totals)
		maps.Copy(cached.totals, totals)
		userRankingCache.Lock()
		if current, ok := userRankingCache.items[cacheKey]; ok && current.end == cached.end && current.expires.Equal(cached.expires) {
			current.totals = maps.Clone(current.totals)
			maps.Copy(current.totals, totals)
			userRankingCache.items[cacheKey] = current
		}
		userRankingCache.Unlock()
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
		if !options.administrator && ((metric == model.RankingMetricTokens && usage.TotalTokens <= 0) ||
			(metric == model.RankingMetricSpending && usage.TotalQuota <= 0)) {
			continue
		}
		maskedUsername := model.MaskRankingName(participant.Username)
		name := participant.Nickname
		if name == "" {
			name = maskedUsername
		}
		row := RankedUser{Rank: len(result.Users) + 1, Name: name, Nickname: participant.Nickname, MaskedUsername: maskedUsername, IsSelf: participant.ID == viewerID}
		if options.administrator {
			row.Username = participant.Username
			row.Name = participant.Username
		}
		publicTokens := options.administrator || participant.Participating
		publicSpending := options.administrator || participant.SpendingParticipating
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
		row.UsagePublic = options.administrator || (participant.TokenUsagePublic && participant.SpendingUsagePublic)
		if row.UsagePublic {
			for name, item := range usage.Models {
				if !options.administrator && ((metric == model.RankingMetricTokens && item.TotalTokens <= 0) ||
					(metric == model.RankingMetricSpending && item.TotalQuota <= 0)) {
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
	if !options.administrator && len(result.Users) > 100 {
		result.Users = result.Users[:100]
	}
	return result, nil
}
