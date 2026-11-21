package service

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
)

const (
	nonStreamGuardRedisTimeout = 200 * time.Millisecond
	nonStreamGuardLocalLimit   = 4096
)

var nonStreamGuardObserve = redis.NewScript(`
if redis.call('EXISTS', KEYS[2]) ~= 0 then
  if redis.call('GET', KEYS[3]) == '2' then return 2 end
  return 0
end
if not redis.call('SET', KEYS[3], '1', 'NX', 'PX', 1000) then return 0 end
if ARGV[1] == '0' then
  redis.call('DEL', KEYS[1])
  return 0
end

local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
local cutoff = now - tonumber(ARGV[3])
local first = redis.call('LINDEX', KEYS[1], 0)
while first and tonumber(first) <= cutoff do
  redis.call('LPOP', KEYS[1])
  first = redis.call('LINDEX', KEYS[1], 0)
end

if redis.call('RPUSH', KEYS[1], now) >= tonumber(ARGV[2]) then
  redis.call('SET', KEYS[2], '1', 'PX', ARGV[4])
  redis.call('DEL', KEYS[1])
  redis.call('SET', KEYS[3], '2', 'PX', 1000)
  return 2
end
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return 1
`)

type nonStreamGuardPolicy struct {
	minOutputTokens int
	maxCachedTokens int
	consecutive     int
	window          time.Duration
	block           time.Duration
}

func activeNonStreamGuardPolicy(settings operation_setting.DistillationGuardPolicy) (nonStreamGuardPolicy, bool) {
	if !settings.Enabled {
		return nonStreamGuardPolicy{}, false
	}
	const maxDurationMinutes = int64(1<<63-1) / int64(time.Minute)
	if settings.ConsecutiveRequests < 1 || settings.ConsecutiveRequests > 1000 ||
		settings.WindowMinutes < 1 || int64(settings.WindowMinutes) > maxDurationMinutes ||
		settings.BlockMinutes < 1 || int64(settings.BlockMinutes) > maxDurationMinutes {
		return nonStreamGuardPolicy{}, false
	}
	return nonStreamGuardPolicy{
		minOutputTokens: settings.MinOutputTokens,
		maxCachedTokens: settings.MaxCachedTokens,
		consecutive:     settings.ConsecutiveRequests,
		window:          time.Duration(settings.WindowMinutes) * time.Minute,
		block:           time.Duration(settings.BlockMinutes) * time.Minute,
	}, true
}

func (policy nonStreamGuardPolicy) key(userID int) string {
	return fmt.Sprintf("new-api:nonstream_guard:v1:{%d:%d:%d:%d:%d:%d}", userID, policy.minOutputTokens,
		policy.maxCachedTokens, policy.consecutive, policy.window.Milliseconds(), policy.block.Milliseconds())
}

var localNonStreamGuard = nonStreamGuardMemory{now: time.Now, states: make(map[string]nonStreamGuardState)}
var nextNonStreamGuardRedisWarning atomic.Int64

func NonStreamBlockedUntil(ctx context.Context, userID int) time.Time {
	policy, enabled := activeNonStreamGuardPolicy(operation_setting.GetDistillationGuard())
	if !enabled || userID <= 0 {
		return time.Time{}
	}
	key := policy.key(userID)
	if !common.RedisEnabled {
		return localNonStreamGuard.blockedUntil(key)
	}
	if common.RDB == nil {
		return time.Time{}
	}
	return redisNonStreamBlockedUntil(ctx, common.RDB, key)
}

func ObserveNonStreamGeneration(ctx context.Context, userID int, hit bool, settings operation_setting.DistillationGuardPolicy) {
	policy, enabled := activeNonStreamGuardPolicy(settings)
	if !enabled || userID <= 0 || settings != operation_setting.GetDistillationGuard() {
		return
	}
	key := policy.key(userID)
	var newlyBlocked bool
	if !common.RedisEnabled {
		newlyBlocked = localNonStreamGuard.observe(key, policy, hit)
	} else if common.RDB != nil {
		newlyBlocked = observeNonStreamRedis(ctx, common.RDB, key, policy, hit)
	}
	if newlyBlocked {
		common.SysLog(common.LogText("non-stream guard event=blocked user_id=%d block_seconds=%d window_seconds=%d consecutive_requests=%d min_output_tokens=%d max_cached_tokens=%d",
			userID, int64(policy.block/time.Second), int64(policy.window/time.Second), policy.consecutive, policy.minOutputTokens, policy.maxCachedTokens))
	}
}

func redisNonStreamBlockedUntil(ctx context.Context, client *redis.Client, key string) time.Time {
	redisCtx, cancel := context.WithTimeout(ctx, nonStreamGuardRedisTimeout)
	defer cancel()
	ttl, err := client.PTTL(redisCtx, key+":block").Result()
	if err != nil {
		if ctx.Err() == nil {
			warnNonStreamGuardRedis(err)
		}
		return time.Time{}
	}
	if ttl < 0 {
		return time.Time{}
	}
	return time.Now().Add(max(ttl, time.Millisecond))
}

func observeNonStreamRedis(ctx context.Context, client *redis.Client, key string, policy nonStreamGuardPolicy, hit bool) bool {
	observation, err := uuid.NewRandom()
	if err != nil {
		return false
	}
	redisCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nonStreamGuardRedisTimeout)
	defer cancel()
	keys := []string{key + ":streak", key + ":block", key + ":seen:" + observation.String()}
	hitNumber := 0
	if hit {
		hitNumber = 1
	}
	result, err := nonStreamGuardObserve.Run(redisCtx, client, keys, hitNumber, policy.consecutive,
		policy.window.Milliseconds(), policy.block.Milliseconds()).Int()
	if err != nil {
		warnNonStreamGuardRedis(err)
		return false
	}
	return result == 2
}

func warnNonStreamGuardRedis(err error) {
	now := time.Now().Unix()
	next := nextNonStreamGuardRedisWarning.Load()
	if now >= next && nextNonStreamGuardRedisWarning.CompareAndSwap(next, now+60) {
		common.SysError(common.LogText("non-stream guard Redis request failed: %v", err))
	}
}

type nonStreamGuardState struct {
	hits         []time.Time
	blockedUntil time.Time
	expiresAt    time.Time
}

type nonStreamGuardMemory struct {
	mu        sync.Mutex
	now       func() time.Time
	states    map[string]nonStreamGuardState
	nextSweep time.Time
}

func (memory *nonStreamGuardMemory) blockedUntil(key string) time.Time {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	now := memory.now()
	memory.sweep(now)
	if state := memory.states[key]; state.blockedUntil.After(now) {
		return state.blockedUntil
	}
	return time.Time{}
}

func (memory *nonStreamGuardMemory) observe(key string, policy nonStreamGuardPolicy, hit bool) bool {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	now := memory.now()
	memory.sweep(now)
	state, exists := memory.states[key]
	if state.blockedUntil.After(now) {
		return false
	}
	if !hit {
		delete(memory.states, key)
		return false
	}
	if !exists && len(memory.states) >= nonStreamGuardLocalLimit {
		return false
	}

	cutoff := now.Add(-policy.window)
	first := 0
	for first < len(state.hits) && !state.hits[first].After(cutoff) {
		first++
	}
	if first > 0 {
		state.hits = state.hits[:copy(state.hits, state.hits[first:])]
	}
	state.hits = append(state.hits, now)
	state.expiresAt = now.Add(policy.window)
	newlyBlocked := len(state.hits) >= policy.consecutive
	if newlyBlocked {
		state.hits = nil
		state.blockedUntil = now.Add(policy.block)
		state.expiresAt = state.blockedUntil
	}
	memory.states[key] = state
	return newlyBlocked
}

func (memory *nonStreamGuardMemory) sweep(now time.Time) {
	if now.Before(memory.nextSweep) {
		return
	}
	for key, state := range memory.states {
		if !state.expiresAt.After(now) {
			delete(memory.states, key)
		}
	}
	memory.nextSweep = now.Add(time.Second)
}
