package model

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/samber/hot"
	"gorm.io/gorm"
)

type rankingUsageDayKey struct {
	userID int
	start  int64
}

type rankingUsageDay struct {
	logCount int64
	lastID   int64
	usage    RankingUserUsage
}

// Only compact totals are retained, never log JSON or public disclosure choices.
// The gate prevents different periods/audiences from parsing history in parallel
// and allows a request waiting for another scan to respect its deadline.
var rankingUsageGate = make(chan struct{}, 1)
var rankingUsageDB *gorm.DB

// Capacity counts user-days, so size it for a full year of active users. Larger
// installations can raise the bound without changing the cache's semantics.
var rankingUsageDays = hot.NewHotCache[rankingUsageDayKey, rankingUsageDay](hot.LRU,
	max(1, common.GetEnvOrDefault("USER_RANKING_DAY_CACHE_CAP", 65536))).Build()

// GetUserRankingUsage reuses complete UTC days across periods and metrics.
// Consumption logs are append-only except for cleanup. Indexed count/ID checks
// detect additions (including backfills) and deletions on every snapshot build.
// Public callers must still apply live consent before returning any totals.
func GetUserRankingUsage(ctx context.Context, userIDs []int, start, end int64) (map[int]RankingUserUsage, error) {
	totals := make(map[int]RankingUserUsage, len(userIDs))
	for _, userID := range userIDs {
		totals[userID] = RankingUserUsage{Models: make(map[string]RankingModelUsage)}
	}
	if len(userIDs) == 0 || start > end {
		return totals, nil
	}
	select {
	case rankingUsageGate <- struct{}{}:
		defer func() { <-rankingUsageGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if rankingUsageDB != LOG_DB {
		rankingUsageDays.Purge()
		rankingUsageDB = LOG_DB
	}

	// ClickHouse does not provide the unique, increasing IDs used by these
	// signatures. Keep its existing streaming semantics.
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		usage, err := scanUserRankingUsage(ctx, userIDs, start, end)
		if err != nil {
			return nil, err
		}
		for userID, day := range usage {
			totals[userID] = day.usage
		}
		return totals, nil
	}

	const daySeconds int64 = 24 * 60 * 60
	firstDay := time.Unix(start, 0).UTC().Truncate(24 * time.Hour).Unix()
	if firstDay < start {
		firstDay += daySeconds
	}
	lastDay := time.Unix(end, 0).UTC().Truncate(24 * time.Hour).Unix()
	if end-lastDay == daySeconds-1 {
		lastDay += daySeconds
	}

	// At most two partial days retain exact inclusive request boundaries.
	ranges := [][2]int64{{start, end}}
	if firstDay < lastDay {
		ranges = [][2]int64{{start, firstDay - 1}, {lastDay, end}}
	}
	for _, bounds := range ranges {
		if bounds[0] > bounds[1] {
			continue
		}
		usage, err := scanUserRankingUsage(ctx, userIDs, bounds[0], bounds[1])
		if err != nil {
			return nil, err
		}
		for userID, day := range usage {
			total := totals[userID]
			total.add(day.usage)
			totals[userID] = total
		}
	}
	if firstDay >= lastDay {
		return totals, nil
	}

	type signature struct {
		UserID   int
		Bucket   int64
		LogCount int64
		LastID   int64
	}
	missing := make(map[int64][]int)
	for offset := 0; offset < len(userIDs); offset += 500 {
		batch := userIDs[offset:min(offset+500, len(userIDs))]
		var signatures []signature
		err := LOG_DB.WithContext(ctx).Model(&Log{}).
			Select("user_id, created_at - (created_at % 86400) AS bucket, COUNT(*) AS log_count, MAX(id) AS last_id").
			Where("type = ? AND user_id IN ? AND created_at >= ? AND created_at < ?", LogTypeConsume, batch, firstDay, lastDay).
			Group("user_id, created_at - (created_at % 86400)").
			Scan(&signatures).Error
		if err != nil {
			return nil, err
		}
		for _, stamp := range signatures {
			key := rankingUsageDayKey{userID: stamp.UserID, start: stamp.Bucket}
			day, ok, _ := rankingUsageDays.Get(key)
			if !ok || day.logCount != stamp.LogCount || day.lastID != stamp.LastID {
				missing[stamp.Bucket] = append(missing[stamp.Bucket], stamp.UserID)
				continue
			}
			total := totals[stamp.UserID]
			total.add(day.usage)
			totals[stamp.UserID] = total
		}
	}
	for dayStart, ids := range missing {
		usage, err := scanUserRankingUsage(ctx, ids, dayStart, dayStart+daySeconds-1)
		if err != nil {
			return nil, err
		}
		for userID, day := range usage {
			// Publish each completed day immediately. A later timeout must not
			// discard work and force the next request to start the year again.
			rankingUsageDays.Set(rankingUsageDayKey{userID: userID, start: dayStart}, day)
			total := totals[userID]
			total.add(day.usage)
			totals[userID] = total
		}
	}
	return totals, nil
}

func (usage *RankingUserUsage) add(other RankingUserUsage) {
	usage.TotalTokens += other.TotalTokens
	usage.TotalQuota += other.TotalQuota
	for name, model := range other.Models {
		item := usage.Models[name]
		item.TotalTokens += model.TotalTokens
		item.TotalQuota += model.TotalQuota
		usage.Models[name] = item
	}
}
