package service

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// A single Lua operation checks the shared rate and concurrency budgets. The
// sorted-set leases expire after the complete request deadline plus a margin;
// release removes only this request's random owner, never a later lease.
// Canceled requests retain their leases: closing a client connection does not
// establish that the database stopped executing its SELECT.
const acquireLogQueryScript = `
local nowparts = redis.call('TIME')
local now = tonumber(nowparts[1]) * 1000 + math.floor(tonumber(nowparts[2]) / 1000)
for i = 1, 4 do
  redis.call('ZREMRANGEBYSCORE', KEYS[i], '-inf', now)
end
for i = 1, 4 do
  if tonumber(ARGV[i]) > 0 and redis.call('ZCARD', KEYS[i]) >= tonumber(ARGV[i]) then
    local first = redis.call('ZRANGE', KEYS[i], 0, 0, 'WITHSCORES')
    return math.max(1, math.ceil((tonumber(first[2]) - now) / 1000))
  end
end
for i = 5, 6 do
  local count = tonumber(redis.call('GET', KEYS[i]) or '0')
  local limit = tonumber(ARGV[i])
  if limit > 0 and count >= limit then
    return math.max(1, math.ceil(redis.call('PTTL', KEYS[i]) / 1000))
  end
end
for i = 1, 4 do
  if tonumber(ARGV[i]) > 0 then
    redis.call('ZADD', KEYS[i], now + tonumber(ARGV[7]), ARGV[8])
    if redis.call('PTTL', KEYS[i]) < tonumber(ARGV[7]) then
      redis.call('PEXPIRE', KEYS[i], tonumber(ARGV[7]))
    end
  end
end
for i = 5, 6 do
  if tonumber(ARGV[i]) > 0 then
    if redis.call('INCR', KEYS[i]) == 1 then redis.call('PEXPIRE', KEYS[i], 60000) end
  end
end
return 0`

const releaseLogQueryScript = `
for i = 1, 4 do redis.call('ZREM', KEYS[i], ARGV[1]) end
return 1`

type logQueryRate struct {
	count   int
	expires time.Time
}

var logQueryMemory = struct {
	sync.Mutex
	leases map[string]map[string]time.Time
	rates  map[string]logQueryRate
}{leases: make(map[string]map[string]time.Time), rates: make(map[string]logQueryRate)}

// AcquireLogQuery never queues. Administrators use the same budgets as users.
// Export and JSON/text scans share one global heavy slot and all requests also
// take a normal global/user slot. retryAfter > 0 means no lease was acquired.
func AcquireLogQuery(ctx context.Context, userID int, heavy, export bool) (release func(), retryAfter int, err error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, 0, errors.New("log queries require a deadline")
	}
	ttl := time.Until(deadline) + 5*time.Second
	if ttl <= 5*time.Second || ttl > time.Minute {
		return nil, 0, errors.New("invalid log query deadline")
	}
	heavy = heavy || export
	user := strconv.Itoa(userID)
	// One hash tag keeps all keys in the same Redis Cluster slot.
	keys := []string{"{log-query}:global", "{log-query}:user:" + user, "{log-query}:heavy-global", "{log-query}:heavy-user:" + user, "{log-query}:rate:" + user, "{log-query}:heavy-rate:" + user}
	limits := []int{4, 2, 0, 0, 60, 0}
	if heavy {
		limits[2], limits[3], limits[5] = 1, 1, 10
	}
	owner := common.GetRandomString(24)
	var once sync.Once
	if common.RedisEnabled {
		if common.RDB == nil {
			return nil, 0, errors.New("log query Redis client is unavailable")
		}
		redisCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		args := make([]any, 0, 8)
		for _, limit := range limits {
			args = append(args, limit)
		}
		args = append(args, ttl.Milliseconds(), owner)
		retry, err := common.RDB.Eval(redisCtx, acquireLogQueryScript, keys, args...).Int()
		if err != nil || retry > 0 {
			return nil, retry, err
		}
		client := common.RDB
		return func() {
			once.Do(func() {
				if ctx.Err() != nil {
					return
				}
				releaseCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := client.Eval(releaseCtx, releaseLogQueryScript, keys[:4], owner).Err(); err != nil {
					common.SysError(common.LogText("failed to release log query lease: %s", err.Error()))
				}
			})
		}, 0, nil
	}
	logQueryMemory.Lock()
	defer logQueryMemory.Unlock()
	now := time.Now()
	for key, leases := range logQueryMemory.leases {
		for token, expires := range leases {
			if !expires.After(now) {
				delete(leases, token)
			}
		}
		if len(leases) == 0 {
			delete(logQueryMemory.leases, key)
		}
	}
	for key, rate := range logQueryMemory.rates {
		if !rate.expires.After(now) {
			delete(logQueryMemory.rates, key)
		}
	}
	for i, key := range keys[:4] {
		if limits[i] > 0 && len(logQueryMemory.leases[key]) >= limits[i] {
			return nil, 1, nil
		}
	}
	for i := 4; i < 6; i++ {
		rate := logQueryMemory.rates[keys[i]]
		if limits[i] > 0 && rate.count >= limits[i] {
			return nil, max(1, int(time.Until(rate.expires).Seconds())+1), nil
		}
	}
	for i, key := range keys[:4] {
		if limits[i] == 0 {
			continue
		}
		if logQueryMemory.leases[key] == nil {
			logQueryMemory.leases[key] = make(map[string]time.Time)
		}
		logQueryMemory.leases[key][owner] = now.Add(ttl)
	}
	for i := 4; i < 6; i++ {
		if limits[i] == 0 {
			continue
		}
		rate := logQueryMemory.rates[keys[i]]
		if rate.count == 0 {
			rate.expires = now.Add(time.Minute)
		}
		rate.count++
		logQueryMemory.rates[keys[i]] = rate
	}
	return func() {
		once.Do(func() {
			if ctx.Err() != nil {
				return
			}
			logQueryMemory.Lock()
			defer logQueryMemory.Unlock()
			for _, key := range keys[:4] {
				delete(logQueryMemory.leases[key], owner)
				if len(logQueryMemory.leases[key]) == 0 {
					delete(logQueryMemory.leases, key)
				}
			}
		})
	}, 0, nil
}
