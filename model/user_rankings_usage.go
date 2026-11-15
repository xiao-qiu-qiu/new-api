package model

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/samber/hot"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const rankingUsageBatchSize = 500

type rankingUsageDayKey struct {
	db                        *gorm.DB
	userID                    int
	start, end                int64
	logCount, firstID, lastID int64
}

type rankingUsageDay struct {
	logCount int64
	lastID   int64
	usage    RankingUserUsage
	complete bool
}

type rankingUsageCursor struct {
	userID          int
	afterID, lastID int64
}

// The signature belongs to the key, while the value records completed pages.
// Published maps are immutable: another period can safely resume the same work.
var rankingUsageDays = hot.NewHotCache[rankingUsageDayKey, rankingUsageDay](hot.LRU,
	max(1, common.GetEnvOrDefault("USER_RANKING_DAY_CACHE_CAP", 65536))).Build()

// One SQL/JSON batch runs at a time. The shared cooldown also applies when
// several periods build concurrently, without holding the gate for a year.
var rankingUsageGate = make(chan struct{}, 1)
var rankingUsageNextBatch time.Time // Protected by rankingUsageGate.
var rankingUsageRedisPause atomic.Int64

// GetUserRankingUsage caches complete UTC days and exact boundary-hour ranges.
// Every build checks COUNT/MAX, including for cached boundaries, so backfills
// and cleanup cannot turn an ID cursor into an unchecked incremental watermark.
// Public callers must still apply live consent before returning any totals.
func GetUserRankingUsage(ctx context.Context, userIDs []int, start, end int64) (map[int]RankingUserUsage, error) {
	totals := make(map[int]RankingUserUsage, len(userIDs))
	ids := make([]int, 0, len(userIDs))
	for _, userID := range userIDs {
		if _, exists := totals[userID]; exists {
			continue
		}
		ids = append(ids, userID)
		totals[userID] = RankingUserUsage{Models: make(map[string]RankingModelUsage)}
	}
	if len(ids) == 0 || start > end {
		return totals, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	logDB := LOG_DB
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		// MergeTree IDs and request IDs are not unique. Read bounded hours
		// without seeking or deduplicating, retaining every duplicate row.
		for offset := 0; offset < len(ids); offset += 500 {
			batch := ids[offset:min(offset+500, len(ids))]
			var hours []struct{ Bucket int64 }
			err := withRankingUsageBatch(ctx, logDB, func(db *gorm.DB) error {
				return db.Model(&Log{}).Select("created_at - (created_at % 3600) AS bucket").
					Where("type = ? AND user_id IN ? AND created_at >= ? AND created_at <= ?", LogTypeConsume, batch, start, end).
					Group("bucket").Order("bucket DESC").Scan(&hours).Error
			})
			if err != nil {
				return nil, err
			}
			for _, hour := range hours {
				hourStart, hourEnd := max(start, hour.Bucket), min(end, hour.Bucket+3599)
				err := withRankingUsageBatch(ctx, logDB, func(db *gorm.DB) error {
					usage, err := scanUserRankingUsage(db, batch, hourStart, hourEnd)
					if err != nil {
						return err
					}
					for userID, day := range usage {
						total := totals[userID]
						total.add(day.usage)
						totals[userID] = total
					}
					return nil
				})
				if err != nil {
					return nil, err
				}
			}
		}
		return totals, nil
	}

	const daySeconds int64 = 86400
	firstDay := time.Unix(start, 0).UTC().Truncate(24 * time.Hour).Unix()
	if firstDay < start {
		firstDay += daySeconds
	}
	lastDay := time.Unix(end, 0).UTC().Truncate(24 * time.Hour).Unix()
	if end-lastDay == daySeconds-1 {
		lastDay += daySeconds
	}
	type span struct{ start, end, bucket int64 }
	spans := []span{{start, end, 3600}}
	if firstDay < lastDay {
		spans = []span{{firstDay, lastDay - 1, daySeconds}, {start, firstDay - 1, 3600}, {lastDay, end, 3600}}
	}
	type signature struct {
		UserID   int
		Bucket   int64
		LogCount int64
		FirstID  int64
		LastID   int64
	}
	targets := make([]rankingUsageDayKey, 0)
	peers := make(map[[2]int64][]rankingUsageDayKey)
	for _, bounds := range spans {
		if bounds.start > bounds.end {
			continue
		}
		bucket := fmt.Sprintf("created_at - (created_at %% %d)", bounds.bucket)
		for offset := 0; offset < len(ids); offset += 500 {
			batch := ids[offset:min(offset+500, len(ids))]
			var signatures []signature
			err := withRankingUsageBatch(ctx, logDB, func(db *gorm.DB) error {
				return db.Model(&Log{}).
					Select("user_id, "+bucket+" AS bucket, COUNT(*) AS log_count, MIN(id) AS first_id, MAX(id) AS last_id").
					Where("type = ? AND user_id IN ? AND created_at >= ? AND created_at <= ?", LogTypeConsume, batch, bounds.start, bounds.end).
					Group("user_id, " + bucket).Scan(&signatures).Error
			})
			if err != nil {
				return nil, err
			}
			for _, stamp := range signatures {
				key := rankingUsageDayKey{
					db: logDB, userID: stamp.UserID,
					start: max(bounds.start, stamp.Bucket), end: min(bounds.end, stamp.Bucket+bounds.bucket-1),
					logCount: stamp.LogCount, firstID: stamp.FirstID, lastID: stamp.LastID,
				}
				targets = append(targets, key)
				rangeKey := [2]int64{key.start, key.end}
				peers[rangeKey] = append(peers[rangeKey], key)
			}
		}
	}
	// Recent completed work is immediately useful to the shorter boards too.
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].start != targets[j].start {
			return targets[i].start > targets[j].start
		}
		return targets[i].userID < targets[j].userID
	})
	source := rankingUsageSourceIdentity(logDB)
	for _, key := range targets {
		usage, err := getRankingUsageRange(ctx, key, source, peers[[2]int64{key.start, key.end}])
		if err != nil {
			return nil, err
		}
		total := totals[key.userID]
		total.add(usage)
		totals[key.userID] = total
	}
	return totals, nil
}

func withRankingUsageBatch(ctx context.Context, source *gorm.DB, fn func(*gorm.DB) error) error {
	return withRankingWork(ctx, func() error {
		if LOG_DB != source {
			return errors.New("ranking log database changed during query")
		}
		queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return withLogQueryBudget(queryCtx, func(db *gorm.DB) error {
			if db.Dialector.Name() == "clickhouse" {
				deadline, _ := queryCtx.Deadline()
				// ClickHouse can otherwise use every core for a single query.
				limited := clickhouse.Context(queryCtx, clickhouse.WithSettings(clickhouse.Settings{
					"max_threads":           1,
					"max_execution_time":    max(0.001, time.Until(deadline).Seconds()),
					"timeout_overflow_mode": "throw",
				}))
				db = db.WithContext(limited)
			}
			return fn(db)
		})
	})
}

func withRankingWork(ctx context.Context, work func() error) error {
	select {
	case rankingUsageGate <- struct{}{}:
		defer func() { <-rankingUsageGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if delay := time.Until(rankingUsageNextBatch); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	started := time.Now()
	defer func() {
		rankingUsageNextBatch = time.Now().Add(max(10*time.Millisecond, time.Since(started)))
	}()
	return work()
}

func getRankingUsageRange(ctx context.Context, key rankingUsageDayKey, source string, peers []rankingUsageDayKey) (RankingUserUsage, error) {
	day, found, _ := rankingUsageDays.Get(key)
	redisKey := ""
	if source != "" && key.start%3600 == 0 && key.end%3600 == 3599 {
		redisKey = fmt.Sprintf("user-ranking-usage:v2:%s:%d:%d:%d:%d:%d", source, key.userID, key.start, key.end, key.logCount, key.lastID)
	}
	if !found && redisKey != "" && common.RedisEnabled && common.RDB != nil && time.Now().UnixNano() >= rankingUsageRedisPause.Load() {
		redisCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		raw, err := common.RDB.Get(redisCtx, redisKey).Bytes()
		cancel()
		var cached struct {
			Count int64            `json:"count"`
			Last  int64            `json:"last"`
			Usage RankingUserUsage `json:"usage"`
		}
		if err == nil && common.Unmarshal(raw, &cached) == nil && cached.Count == key.logCount && cached.Last == key.lastID && cached.Usage.Models != nil {
			day = rankingUsageDay{logCount: cached.Count, lastID: cached.Last, usage: cached.Usage, complete: true}
			rankingUsageDays.Set(key, day)
		} else if err != nil && !errors.Is(err, redis.Nil) {
			rankingUsageRedisPause.Store(time.Now().Add(time.Minute).UnixNano())
		}
	}
	for !day.complete {
		completed := make([]rankingUsageDayKey, 0)
		err := withRankingUsageBatch(ctx, key.db, func(db *gorm.DB) error {
			// Re-read after taking the batch gate: another period may have
			// completed this page while we were waiting. Never apply it twice.
			day, _, _ = rankingUsageDays.Get(key)
			if day.complete {
				return nil
			}
			capacity, _ := rankingUsageDays.Capacity()
			limit := min(128, max(1, capacity))
			pending := []rankingUsageDayKey{key}
			for _, peer := range peers {
				if len(pending) >= limit {
					break
				}
				if peer == key {
					continue
				}
				cached, _, _ := rankingUsageDays.Get(peer)
				if !cached.complete {
					pending = append(pending, peer)
				}
			}
			entries := make([]rankingUsageDay, len(pending))
			ids := make([]int, len(pending))
			cursors := make([]rankingUsageCursor, len(pending))
			for i, target := range pending {
				entries[i], _, _ = rankingUsageDays.Get(target)
				ids[i] = target.userID
				afterID := entries[i].lastID
				if entries[i].logCount == 0 && target.firstID > 0 {
					// This is a query bound, not processed progress. MIN uses
					// the exact time/user filter, even for backdated inserts.
					afterID = target.firstID - 1
				}
				cursors[i] = rankingUsageCursor{userID: target.userID, afterID: afterID, lastID: target.lastID}
			}
			page, err := scanUserRankingUsage(db, ids, key.start, key.end, cursors...)
			if err != nil {
				return err
			}
			var pageCount int64
			for _, chunk := range page {
				pageCount += chunk.logCount
			}
			if pageCount == 0 {
				return errors.New("ranking logs changed during scan")
			}
			for i, target := range pending {
				chunk := page[target.userID]
				next := entries[i]
				next.logCount += chunk.logCount
				next.lastID = max(next.lastID, chunk.lastID)
				if next.logCount > target.logCount || (pageCount < rankingUsageBatchSize && next.logCount != target.logCount) ||
					(next.logCount == target.logCount && next.lastID != target.lastID) {
					return errors.New("ranking logs changed during scan")
				}
				if chunk.logCount > 0 {
					next.usage.Models = maps.Clone(next.usage.Models)
					if next.usage.Models == nil {
						next.usage.Models = make(map[string]RankingModelUsage)
					}
					next.usage.add(chunk.usage)
				}
				next.complete = next.logCount == target.logCount && next.lastID == target.lastID
				entries[i] = next
			}
			for i, target := range pending {
				if page[target.userID].logCount == 0 {
					continue
				}
				rankingUsageDays.Set(target, entries[i])
				if entries[i].complete {
					completed = append(completed, target)
				}
			}
			day = entries[0]
			return nil
		})
		if err != nil {
			return RankingUserUsage{}, err
		}
		persistRankingUsageRanges(ctx, source, completed)
	}
	return day.usage, nil
}

func persistRankingUsageRanges(ctx context.Context, source string, keys []rankingUsageDayKey) {
	if len(keys) == 0 || source == "" || !common.RedisEnabled || common.RDB == nil || time.Now().UnixNano() < rankingUsageRedisPause.Load() {
		return
	}
	pipeline := common.RDB.Pipeline()
	defer pipeline.Close()
	for _, key := range keys {
		// Changing end timestamps of live partial hours must not leave a new
		// persistent key per refresh. Those exact ranges remain in the LRU.
		if key.start%3600 != 0 || key.end%3600 != 3599 {
			continue
		}
		day, found, _ := rankingUsageDays.Get(key)
		if !found || !day.complete {
			continue
		}
		data, err := common.Marshal(struct {
			Count int64            `json:"count"`
			Last  int64            `json:"last"`
			Usage RankingUserUsage `json:"usage"`
		}{day.logCount, day.lastID, day.usage})
		if err == nil {
			redisKey := fmt.Sprintf("user-ranking-usage:v2:%s:%d:%d:%d:%d:%d", source, key.userID, key.start, key.end, key.logCount, key.lastID)
			pipeline.Set(ctx, redisKey, data, 30*24*time.Hour)
		}
	}
	if pipeline.Len() == 0 {
		return
	}
	redisCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if _, err := pipeline.Exec(redisCtx); err != nil {
		rankingUsageRedisPause.Store(time.Now().Add(time.Minute).UnixNano())
	}
}

// Redis identities use the actual dialect's DSN, never the primary DB or a
// process-local pointer. Uncommitted transactions and in-memory DBs stay local.
func rankingUsageSourceIdentity(db *gorm.DB) string {
	if db == nil || db.Statement == nil {
		return ""
	}
	if _, transaction := db.Statement.ConnPool.(gorm.TxCommitter); transaction {
		return ""
	}
	var dsn string
	switch driver := db.Dialector.(type) {
	case mysqlMigrationDialector:
		dsn = driver.DSN
	case postgresMigrationDialector:
		dsn = driver.DSN
	case *mysql.Dialector:
		dsn = driver.DSN
	case *postgres.Dialector:
		dsn = driver.DSN
	case *sqlite.Dialector:
		dsn = driver.DSN
		if strings.Contains(dsn, ":memory:") || strings.Contains(dsn, "mode=memory") || strings.HasPrefix(dsn, "file:") {
			return ""
		}
		filename, query, _ := strings.Cut(dsn, "?")
		absolute, err := filepath.Abs(filename)
		if err != nil {
			return ""
		}
		dsn = absolute + "?" + query
	}
	if dsn == "" {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(db.Dialector.Name()+"\x00"+dsn)))
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
