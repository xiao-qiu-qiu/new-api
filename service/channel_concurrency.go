package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
)

const (
	channelConcurrencyContextKey = "channel_concurrency_lease"
	channelConcurrencyLeaseTTL   = 90 * time.Second
	channelConcurrencyHeartbeat  = channelConcurrencyLeaseTTL / 3
	channelConcurrencyTimeout    = 2 * time.Second
)

var channelConcurrencyLocal = struct {
	sync.Mutex
	counts map[int]int
}{counts: make(map[int]int)}

// Scores are lease deadlines in Redis milliseconds. Every mutation uses Redis
// TIME, so instances need neither synchronized clocks nor a shared process ID.
var channelConcurrencyAcquireScript = redis.NewScript(`
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[1]) then return 0 end
redis.call('ZADD', KEYS[1], now + tonumber(ARGV[2]), ARGV[3])
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`)

var channelConcurrencyRenewScript = redis.NewScript(`
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
local deadline = redis.call('ZSCORE', KEYS[1], ARGV[2])
if not deadline then return 0 end
if tonumber(deadline) <= now then
  redis.call('ZREM', KEYS[1], ARGV[2])
  return 0
end
redis.call('ZADD', KEYS[1], 'XX', now + tonumber(ARGV[1]), ARGV[2])
redis.call('PEXPIRE', KEYS[1], ARGV[1])
return 1
`)

// The handler owns the Gin entry; callbacks own only this independent lease.
// Capturing Gin in cancellation or renewal callbacks would race its pool reuse.
type channelConcurrencyLease struct {
	channelID int
	client    *redis.Client
	key       string
	token     string
	once      sync.Once
	released  atomic.Bool

	stopMu      sync.Mutex
	guard       context.Context
	cancelGuard context.CancelCauseFunc
	stopGuard   func() bool
	stopRenew   context.CancelFunc
	renewDone   chan struct{}
}

// TryAcquireChannelConcurrency reserves one upstream attempt. Repeated calls
// for the current channel reuse its lease; switching releases the previous one.
// Call this and ReleaseChannelConcurrency only from the request's owner goroutine.
// Client cancellation does not release capacity: drain and asynchronous task
// submissions may still use upstream. Every forwarding owner must defer release.
// Detached upstream contexts must observe ContextKeyChannelConcurrencyGuard;
// only lease loss cancels that guard, never a normal release or client loss.
func TryAcquireChannelConcurrency(c *gin.Context, channel *model.Channel) (bool, error) {
	if channel == nil {
		ReleaseChannelConcurrency(c)
		return false, errors.New("channel concurrency requires a valid channel")
	}
	limit := channel.GetSetting().MaxConcurrency
	if limit == 0 {
		ReleaseChannelConcurrency(c)
		return true, nil
	}
	if limit < 0 || limit > 1000000 {
		ReleaseChannelConcurrency(c)
		return false, errors.New("channel concurrency limit must be between 0 and 1000000")
	}
	if c == nil || c.Request == nil || channel.Id <= 0 {
		ReleaseChannelConcurrency(c)
		return false, errors.New("channel concurrency requires a request and a valid channel")
	}
	value, _ := c.Get(channelConcurrencyContextKey)
	previous, _ := value.(*channelConcurrencyLease)
	if previous != nil && previous.channelID != channel.Id {
		ReleaseChannelConcurrency(c)
		previous = nil
	}
	requestCtx := c.Request.Context()
	if previous != nil && !previous.released.Load() {
		if previous.guard != nil && previous.guard.Err() != nil {
			return false, context.Cause(previous.guard)
		}
		return true, nil
	}
	if err := requestCtx.Err(); err != nil {
		return false, context.Cause(requestCtx)
	}

	lease := &channelConcurrencyLease{channelID: channel.Id}
	if common.RedisEnabled {
		if common.RDB == nil {
			return false, errors.New("channel concurrency Redis client is unavailable")
		}
		token, err := uuid.NewRandom()
		if err != nil {
			return false, fmt.Errorf("create channel concurrency lease: %w", err)
		}
		lease.client = common.RDB
		lease.key = "channel_concurrency:" + strconv.Itoa(channel.Id)
		lease.token = token.String()
		ctx, cancel := context.WithTimeout(requestCtx, channelConcurrencyTimeout)
		acquired, err := channelConcurrencyAcquireScript.Run(ctx, lease.client,
			[]string{lease.key}, limit, channelConcurrencyLeaseTTL.Milliseconds(), lease.token).Int()
		cancel()
		if err != nil {
			// A lost reply may still have reserved capacity. Remove only our token;
			// if Redis remains unavailable its deadline handles eventual cleanup.
			lease.release()
			return false, fmt.Errorf("acquire channel concurrency lease: %w", err)
		}
		if acquired != 1 {
			return false, nil
		}
		lease.guard, lease.cancelGuard = context.WithCancelCause(context.WithoutCancel(requestCtx))
		leaseCtx, cancelRequest := context.WithCancelCause(requestCtx)
		lease.stopGuard = context.AfterFunc(lease.guard, func() {
			cancelRequest(context.Cause(lease.guard))
		})
		c.Request = c.Request.WithContext(context.WithValue(leaseCtx, constant.ContextKeyChannelConcurrencyGuard, lease.guard))
		renewCtx, stopRenew := context.WithCancel(context.Background())
		lease.stopRenew = stopRenew
		lease.renewDone = make(chan struct{})
		go lease.renew(renewCtx)
	} else {
		channelConcurrencyLocal.Lock()
		if channelConcurrencyLocal.counts[channel.Id] >= limit {
			channelConcurrencyLocal.Unlock()
			return false, nil
		}
		channelConcurrencyLocal.counts[channel.Id]++
		channelConcurrencyLocal.Unlock()
	}
	c.Set(channelConcurrencyContextKey, lease)
	if err := requestCtx.Err(); err != nil {
		// Admission was cancelled before forwarding began; this owner has no
		// detached upstream work yet, so returning its slot here is safe.
		ReleaseChannelConcurrency(c)
		return false, context.Cause(requestCtx)
	}
	return true, nil
}

// ReleaseChannelConcurrency is safe to call repeatedly. It never cancels the
// guard or the request, which may need another channel for its next attempt.
func ReleaseChannelConcurrency(c *gin.Context) {
	if c == nil {
		return
	}
	value, _ := c.Get(channelConcurrencyContextKey)
	lease, _ := value.(*channelConcurrencyLease)
	if lease == nil {
		return
	}
	c.Set(channelConcurrencyContextKey, nil)
	lease.release()
}

func (lease *channelConcurrencyLease) release() {
	lease.once.Do(func() {
		lease.stopMu.Lock()
		lease.released.Store(true)
		if lease.stopGuard != nil {
			lease.stopGuard()
		}
		lease.stopMu.Unlock()
		if lease.stopRenew != nil {
			lease.stopRenew()
			<-lease.renewDone
		}
		if lease.client != nil {
			ctx, cancel := context.WithTimeout(context.Background(), channelConcurrencyTimeout)
			defer cancel()
			if err := lease.client.ZRem(ctx, lease.key, lease.token).Err(); err != nil {
				common.SysError(common.LogText("failed to release channel concurrency lease: channel_id=%d, error=%v", lease.channelID, err))
			}
			return
		}
		channelConcurrencyLocal.Lock()
		defer channelConcurrencyLocal.Unlock()
		if channelConcurrencyLocal.counts[lease.channelID] <= 1 {
			delete(channelConcurrencyLocal.counts, lease.channelID)
		} else {
			channelConcurrencyLocal.counts[lease.channelID]--
		}
	})
}

func (lease *channelConcurrencyLease) renew(ctx context.Context) {
	defer close(lease.renewDone)
	ticker := time.NewTicker(channelConcurrencyHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			opCtx, cancel := context.WithTimeout(ctx, channelConcurrencyTimeout)
			owned, err := channelConcurrencyRenewScript.Run(opCtx, lease.client,
				[]string{lease.key}, channelConcurrencyLeaseTTL.Milliseconds(), lease.token).Int()
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err != nil || owned != 1 {
				if err == nil {
					err = errors.New("channel concurrency lease expired or was removed")
				}
				lease.stopMu.Lock()
				if !lease.released.Load() {
					lease.cancelGuard(fmt.Errorf("renew channel concurrency lease: %w", err))
				}
				lease.stopMu.Unlock()
				return
			}
		}
	}
}
