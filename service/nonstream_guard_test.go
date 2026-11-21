package service

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNonStreamGuardWindowAndBlock(t *testing.T) {
	policy := nonStreamGuardPolicy{consecutive: 3, window: 5 * time.Minute, block: 10 * time.Minute}
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			ctx := t.Context()
			now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
			advance := func(elapsed time.Duration) { now = now.Add(elapsed) }
			memory := nonStreamGuardMemory{now: func() time.Time { return now }, states: make(map[string]nonStreamGuardState)}
			observe := func(key string, hit bool) bool { return memory.observe(key, policy, hit) }
			blocked := memory.blockedUntil
			if backend == "redis" {
				server := miniredis.RunT(t)
				server.SetTime(now)
				client := redis.NewClient(&redis.Options{Addr: server.Addr()})
				t.Cleanup(func() { _ = client.Close() })
				advance = func(elapsed time.Duration) {
					now = now.Add(elapsed)
					server.SetTime(now)
					server.FastForward(elapsed)
				}
				observe = func(key string, hit bool) bool { return observeNonStreamRedis(ctx, client, key, policy, hit) }
				blocked = func(key string) time.Time { return redisNonStreamBlockedUntil(ctx, client, key) }
			}
			key := policy.key(10)
			assert.False(t, observe(key, true))
			advance(2 * time.Minute)
			observe(key, true)
			advance(3 * time.Minute)
			observe(key, true)
			require.True(t, blocked(key).IsZero(), "the hit exactly one window old does not count")
			assert.True(t, observe(key, true), "only crossing the threshold reports a new block")
			require.False(t, blocked(key).IsZero(), "the third live hit starts a block")
			assert.True(t, blocked(policy.key(11)).IsZero(), "another user is unaffected")

			advance(policy.block - time.Millisecond)
			assert.False(t, observe(key, false))
			assert.False(t, observe(key, true))
			require.False(t, blocked(key).IsZero(), "a successful non-hit does not clear an active block")
			advance(time.Millisecond)
			require.True(t, blocked(key).IsZero(), "an observation during a block does not extend it")
			observe(key, true)
			observe(key, true)
			require.True(t, blocked(key).IsZero(), "the completed block leaves no streak")
			observe(key, false)
			observe(key, true)
			require.True(t, blocked(key).IsZero(), "a successful non-hit interrupts the streak")
			observe(key, true)
			observe(key, true)
			assert.False(t, blocked(key).IsZero())
		})
	}
}

func TestNonStreamGuardPolicyIsolationAndRedisFailOpen(t *testing.T) {
	ctx := t.Context()
	oldPolicy := operation_setting.GetDistillationGuard()
	oldRedisEnabled, oldRedis := common.RedisEnabled, common.RDB
	t.Cleanup(func() {
		operation_setting.SetDistillationGuard(oldPolicy)
		common.RedisEnabled, common.RDB = oldRedisEnabled, oldRedis
	})
	policy := operation_setting.DefaultDistillationGuard()
	policy.ConsecutiveRequests = 1
	operation_setting.SetDistillationGuard(policy)
	common.RedisEnabled = false

	const userID = 875312
	internalPolicy, enabled := activeNonStreamGuardPolicy(policy)
	require.True(t, enabled)
	t.Cleanup(func() {
		localNonStreamGuard.mu.Lock()
		defer localNonStreamGuard.mu.Unlock()
		for candidate := userID; candidate <= userID+2; candidate++ {
			delete(localNonStreamGuard.states, internalPolicy.key(candidate))
		}
	})
	ObserveNonStreamGeneration(ctx, userID, true, policy)
	deadline := NonStreamBlockedUntil(ctx, userID)
	require.False(t, deadline.IsZero())
	changed := policy
	changed.MinOutputTokens++
	operation_setting.SetDistillationGuard(changed)
	assert.True(t, NonStreamBlockedUntil(ctx, userID).IsZero())
	ObserveNonStreamGeneration(ctx, userID+2, true, policy)
	assert.True(t, NonStreamBlockedUntil(ctx, userID+2).IsZero(), "an old classification cannot enter the new policy")
	changed = policy
	changed.Enabled = false
	operation_setting.SetDistillationGuard(changed)
	ObserveNonStreamGeneration(ctx, userID, false, changed)
	assert.True(t, NonStreamBlockedUntil(ctx, userID).IsZero())
	operation_setting.SetDistillationGuard(policy)
	assert.Equal(t, deadline, NonStreamBlockedUntil(ctx, userID))
	assert.True(t, NonStreamBlockedUntil(ctx, userID+2).IsZero(), "a stale classification must not survive reactivation")

	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	require.NoError(t, client.Close())
	common.RedisEnabled, common.RDB = true, client
	assert.True(t, NonStreamBlockedUntil(ctx, userID).IsZero(), "a Redis failure must not consult a local block")
	ObserveNonStreamGeneration(ctx, userID+1, true, policy)
	common.RedisEnabled = false
	assert.True(t, NonStreamBlockedUntil(ctx, userID+1).IsZero(), "a Redis failure must not start a node-local streak")
}

func TestNonStreamGuardConcurrentRedisObservers(t *testing.T) {
	for _, backend := range []string{"miniredis", "redis"} {
		t.Run(backend, func(t *testing.T) {
			var address string
			if backend == "redis" {
				address = os.Getenv("NONSTREAM_GUARD_TEST_REDIS_ADDR")
				if address == "" {
					t.Skip("set NONSTREAM_GUARD_TEST_REDIS_ADDR to a test Redis instance")
				}
			} else {
				address = miniredis.RunT(t).Addr()
			}
			ctx := t.Context()
			clients := make([]*redis.Client, 2)
			for index := range clients {
				clients[index] = redis.NewClient(&redis.Options{Addr: address})
				t.Cleanup(func() { _ = clients[index].Close() })
				require.NoError(t, clients[index].Ping(ctx).Err())
			}
			policy := nonStreamGuardPolicy{consecutive: 10, window: 5 * time.Minute, block: 10 * time.Minute}
			key := "new-api:nonstream_guard:test:{" + uuid.NewString() + "}"
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = clients[0].Del(cleanup, key+":streak", key+":block").Err()
			})
			var observers sync.WaitGroup
			for index := range policy.consecutive - 1 {
				observers.Go(func() { observeNonStreamRedis(ctx, clients[index%len(clients)], key, policy, true) })
			}
			observers.Wait()
			assert.True(t, redisNonStreamBlockedUntil(ctx, clients[0], key).IsZero())
			var newBlocks atomic.Int32
			for _, client := range clients {
				observers.Go(func() {
					if observeNonStreamRedis(ctx, client, key, policy, true) {
						newBlocks.Add(1)
					}
				})
			}
			observers.Wait()
			assert.EqualValues(t, 1, newBlocks.Load(), "only one observer can report the new block")
			require.False(t, redisNonStreamBlockedUntil(ctx, clients[0], key).IsZero(), "observations on both clients count toward one threshold")

			retryKey := key + ":retry"
			retryKeys := []string{retryKey + ":streak", retryKey + ":block", retryKey + ":seen"}
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = clients[0].Del(cleanup, retryKeys...).Err()
			})
			policy.consecutive = 2
			for _, client := range clients {
				require.NoError(t, nonStreamGuardObserve.Run(ctx, client, retryKeys, 1, policy.consecutive,
					policy.window.Milliseconds(), policy.block.Milliseconds()).Err())
			}
			assert.True(t, redisNonStreamBlockedUntil(ctx, clients[0], retryKey).IsZero(), "a replayed observation counts only once")
			observeNonStreamRedis(ctx, clients[1], retryKey, policy, true)
			assert.False(t, redisNonStreamBlockedUntil(ctx, clients[0], retryKey).IsZero())
		})
	}
}
