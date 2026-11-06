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
	Rank           int    `json:"rank"`
	Name           string `json:"name"`
	Nickname       string `json:"nickname,omitempty"`
	MaskedUsername string `json:"masked_username"`
	TotalTokens    int64  `json:"total_tokens"`
	IsSelf         bool   `json:"is_self"`
}

type UserRankingsResponse struct {
	Users        []RankedUser `json:"users"`
	Participants int          `json:"participants"`
	TotalTokens  int64        `json:"total_tokens"`
	PeriodStart  int64        `json:"period_start"`
	PeriodEnd    int64        `json:"period_end"`
	UpdatedAt    int64        `json:"updated_at"`
	Self         *RankedUser  `json:"self,omitempty"`
}

type userRankingTotals struct {
	totals     map[int]int64
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

func GetUserRankings(ctx context.Context, period string, viewerID int) (*UserRankingsResponse, error) {
	config, err := rankingConfig(period)
	if err != nil {
		return nil, err
	}
	value, err, _ := userRankingBuild.Do(config.id, func() (any, error) {
		userRankingCache.Lock()
		cached, ok := userRankingCache.items[config.id]
		generation := userRankingCache.generation
		userRankingCache.Unlock()
		if ok && time.Now().Before(cached.expires) {
			return cached, nil
		}
		participants, err := model.GetRankingParticipants(ctx)
		if err != nil {
			return nil, err
		}
		ids := make([]int, 0, len(participants))
		for _, participant := range participants {
			ids = append(ids, participant.ID)
		}
		start, end := rankingTimeRange(config, time.Now())
		queryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		totals, err := model.GetUserRankingTotals(queryCtx, ids, start, end)
		if err != nil {
			return nil, err
		}
		cached = userRankingTotals{totals: totals, start: start, end: end, expires: time.Now().Add(time.Minute)}
		userRankingCache.Lock()
		if generation == userRankingCache.generation {
			userRankingCache.items[config.id] = cached
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
	participants, err := model.GetRankingParticipants(ctx)
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
		totals, err := model.GetUserRankingTotals(queryCtx, missing, cached.start, cached.end)
		if err != nil {
			return nil, err
		}
		cached.totals = maps.Clone(cached.totals)
		maps.Copy(cached.totals, totals)
	}
	sort.SliceStable(participants, func(i, j int) bool {
		return cached.totals[participants[i].ID] > cached.totals[participants[j].ID]
	})
	result := &UserRankingsResponse{
		Users: make([]RankedUser, 0), Participants: len(participants),
		PeriodStart: cached.start, PeriodEnd: cached.end, UpdatedAt: cached.end,
	}
	for _, participant := range participants {
		tokens := cached.totals[participant.ID]
		if tokens <= 0 {
			continue
		}
		maskedUsername := model.MaskRankingName(participant.Username)
		name := participant.Nickname
		if name == "" {
			name = maskedUsername
		}
		row := RankedUser{Rank: len(result.Users) + 1, Name: name, Nickname: participant.Nickname, MaskedUsername: maskedUsername, TotalTokens: tokens, IsSelf: participant.ID == viewerID}
		result.TotalTokens += tokens
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
