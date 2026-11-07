package active_requests

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
)

const (
	redisPrefix      = "new-api:active_requests:v1:"
	leaseTTL         = 90 * time.Second
	heartbeatEvery   = leaseTTL / 3
	flushEvery       = 250 * time.Millisecond
	redisTimeout     = 200 * time.Millisecond
	redisSyncTimeout = 2 * time.Second
	redisBatchSize   = 256
	cacheTTL         = time.Second
	maxCacheSize     = 128
)

func (t *tracker) run() {
	flush := time.NewTicker(flushEvery)
	heartbeat := time.NewTicker(heartbeatEvery)
	defer flush.Stop()
	defer heartbeat.Stop()
	for {
		select {
		case <-flush.C:
			t.syncRedis(false)
		case <-heartbeat.C:
			t.syncRedis(true)
		}
	}
}

func indexKeys(record Record) []string {
	keys := []string{redisPrefix + "all", redisPrefix + "user:" + strconv.Itoa(record.UserID),
		fmt.Sprintf("%srequest:%x", redisPrefix, sha256.Sum256([]byte(record.RequestID)))}
	if record.Channel != 0 {
		keys = append(keys, redisPrefix+"channel:"+strconv.Itoa(record.Channel))
	}
	if record.Group != "" {
		keys = append(keys, fmt.Sprintf("%sgroup:%x", redisPrefix, sha256.Sum256([]byte(record.Group))))
	}
	return keys
}

func (t *tracker) syncRedis(heartbeat bool) {
	t.mu.Lock()
	pending := t.pending
	t.pending = make(map[string]change)
	if heartbeat {
		now := time.Now().UnixMilli()
		for id, record := range t.entries {
			record.UpdatedAt = now
			t.entries[id] = record
			pending[id] = change{record: record}
		}
	}
	t.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	defer t.retryPending(pending)

	ctx, cancel := context.WithTimeout(context.Background(), redisSyncTimeout)
	defer cancel()
	clock, err := t.client.Time(ctx).Result()
	if err != nil {
		return
	}
	deadline := clock.Add(leaseTTL).UnixMilli()
	ids := make([]string, 0, len(pending))
	for id, update := range pending {
		if update.remove {
			ids = append(ids, id)
		}
	}
	for id, update := range pending {
		if !update.remove {
			ids = append(ids, id)
		}
	}
	for start := 0; start < len(ids); start += redisBatchSize {
		batch := ids[start:min(start+redisBatchSize, len(ids))]
		pipe := t.client.Pipeline()
		indexes := make(map[string]struct{})
		for _, id := range batch {
			update := pending[id]
			key := redisPrefix + "record:" + id
			if update.remove {
				pipe.Del(ctx, key)
			} else {
				data, err := common.Marshal(update.record)
				if err != nil {
					continue
				}
				pipe.Set(ctx, key, data, leaseTTL)
			}
			for _, index := range indexKeys(update.record) {
				if update.remove {
					pipe.ZRem(ctx, index, id)
				} else {
					pipe.ZAdd(ctx, index, &redis.Z{Score: float64(deadline), Member: id})
				}
				indexes[index] = struct{}{}
			}
		}
		for index := range indexes {
			pipe.ZRemRangeByScore(ctx, index, "-inf", strconv.FormatInt(clock.UnixMilli(), 10))
			pipe.Expire(ctx, index, 2*leaseTTL)
		}
		_, err := pipe.Exec(ctx)
		_ = pipe.Close()
		if err != nil {
			return
		}
		for _, id := range batch {
			delete(pending, id)
		}
	}
}

// A failed flush retries the latest state, with deletions first, on the next
// tick. Retention and queue size are bounded even during a prolonged outage.
func (t *tracker) retryPending(pending map[string]change) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now().UnixMilli()
	for id, update := range pending {
		if _, newer := t.pending[id]; newer {
			continue
		}
		if current, alive := t.entries[id]; alive {
			update.record = current
		} else {
			update.remove = true
		}
		if update.expiresAt != 0 && update.expiresAt <= now {
			continue
		}
		t.enqueue(id, update)
	}
}

// sharedResult caches only remote matches, so local Finish and state changes
// are visible immediately even before the asynchronous Redis flush. The cache
// retains at most 128 * 100 records and coalesces simultaneous identical polls.
func (t *tracker) sharedResult(ctx context.Context, filter Filter) (Result, error) {
	t.cacheMu.Lock()
	cached, ok := t.cache[filter]
	t.cacheMu.Unlock()
	if ok && time.Now().Before(cached.expires) {
		return cached.result, cached.err
	}
	key, _ := common.Marshal(filter)
	response := t.queries.DoChan(string(key), func() (any, error) {
		t.cacheMu.Lock()
		cached, ok := t.cache[filter]
		t.cacheMu.Unlock()
		if ok && time.Now().Before(cached.expires) {
			return cached.result, cached.err
		}
		queryCtx, cancel := context.WithTimeout(context.Background(), redisTimeout)
		defer cancel()
		var result Result
		var err error
		select {
		case t.readers <- struct{}{}:
			result, err = t.queryRedis(queryCtx, filter)
			<-t.readers
		case <-queryCtx.Done():
			err = queryCtx.Err()
		}
		t.cacheMu.Lock()
		if len(t.cache) >= maxCacheSize {
			clear(t.cache)
		}
		t.cache[filter] = cachedResult{result: result, err: err, expires: time.Now().Add(cacheTTL)}
		t.cacheMu.Unlock()
		return result, err
	})
	select {
	case value := <-response:
		return value.Val.(Result), value.Err
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

func (t *tracker) queryRedis(ctx context.Context, filter Filter) (Result, error) {
	clock, err := t.client.Time(ctx).Result()
	if err != nil {
		return Result{}, err
	}
	// User and exact request indexes avoid scanning unrelated requests.
	index := redisPrefix + "all"
	switch {
	case filter.RequestID != "":
		index = fmt.Sprintf("%srequest:%x", redisPrefix, sha256.Sum256([]byte(filter.RequestID)))
	case filter.UserID > 0:
		index = redisPrefix + "user:" + strconv.Itoa(filter.UserID)
	case filter.Channel != 0:
		index = redisPrefix + "channel:" + strconv.Itoa(filter.Channel)
	case filter.Group != "":
		index = fmt.Sprintf("%sgroup:%x", redisPrefix, sha256.Sum256([]byte(filter.Group)))
	}
	ids, err := t.client.ZRangeByScore(ctx, index, &redis.ZRangeBy{
		Min: "(" + strconv.FormatInt(clock.UnixMilli(), 10), Max: "+inf",
	}).Result()
	if err != nil {
		return Result{}, err
	}
	var newest recordHeap
	result := Result{}
	for start := 0; start < len(ids); start += redisBatchSize {
		keys := make([]string, 0, redisBatchSize)
		for _, id := range ids[start:min(start+redisBatchSize, len(ids))] {
			if !strings.HasPrefix(id, t.nodeID) {
				keys = append(keys, redisPrefix+"record:"+id)
			}
		}
		if len(keys) == 0 {
			continue
		}
		values, err := t.client.MGet(ctx, keys...).Result()
		if err != nil {
			return Result{}, err
		}
		for _, value := range values {
			data, ok := value.(string)
			if !ok {
				continue
			}
			var record Record
			if common.UnmarshalJsonStr(data, &record) != nil || !filter.matches(record) {
				continue
			}
			result.Total++
			newest.add(record)
		}
	}
	result.Items = newest
	return result, nil
}
