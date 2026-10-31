package service

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/cachex"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/go-redis/redis/v8"
	"github.com/samber/hot"
)

const channelAffinityLifetimeNamespace = "new-api:channel_affinity_lifetime:v1"

var (
	channelAffinityLifetimeOnce  sync.Once
	channelAffinityLifetimeCache *cachex.HybridCache[channelAffinityLifetimeState]
	channelAffinityLifetimeLocks [64]sync.Mutex
	channelAffinityLifetimeNow   = time.Now
)

// The v1 binding remains an integer. Metadata outlives it so expiry is still
// enforced after an old node refreshes v1, and late completions can be fenced.
// Times are Unix milliseconds. Zero deadlines represent a switch away from a
// limited channel; retaining its new epoch fences requests from the old epoch.
type channelAffinityLifetimeState struct {
	ChannelID         int    `json:"channel_id"`
	Epoch             string `json:"epoch"`
	BoundAt           int64  `json:"bound_at"`
	IdleExpiresAt     int64  `json:"idle_expires_at"`
	AbsoluteExpiresAt int64  `json:"absolute_expires_at"`
	IdleTTLSeconds    int    `json:"idle_ttl_seconds"`
}

func (s channelAffinityLifetimeState) limited() bool {
	return s.AbsoluteExpiresAt > 0 && s.IdleExpiresAt > 0 && s.IdleTTLSeconds > 0
}

func (s channelAffinityLifetimeState) expired(now int64) bool {
	return s.limited() && (now >= s.IdleExpiresAt || now >= s.AbsoluteExpiresAt)
}

type channelAffinityLifetimeObservation struct {
	StartedAt   int64
	Epoch       string
	LiveLimited bool
}

type channelAffinityLifetimeCodec struct{}

func (channelAffinityLifetimeCodec) Encode(v channelAffinityLifetimeState) (string, error) {
	b, err := common.Marshal(v)
	return string(b), err
}

func (channelAffinityLifetimeCodec) Decode(raw string) (channelAffinityLifetimeState, error) {
	var v channelAffinityLifetimeState
	err := common.UnmarshalJsonStr(raw, &v)
	if err == nil && (v.ChannelID <= 0 || v.Epoch == "") {
		err = errors.New("invalid channel affinity lifetime metadata")
	}
	return v, err
}

func getChannelAffinityLifetimeCache() *cachex.HybridCache[channelAffinityLifetimeState] {
	channelAffinityLifetimeOnce.Do(func() {
		capacity := operation_setting.GetChannelAffinitySetting().MaxEntries
		if capacity <= 0 {
			capacity = 100_000
		}
		channelAffinityLifetimeCache = cachex.NewHybridCache[channelAffinityLifetimeState](cachex.HybridCacheConfig[channelAffinityLifetimeState]{
			Namespace: cachex.Namespace(channelAffinityLifetimeNamespace),
			Redis:     common.RDB,
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			RedisCodec: channelAffinityLifetimeCodec{},
			Memory: func() *hot.HotCache[string, channelAffinityLifetimeState] {
				return hot.NewHotCache[string, channelAffinityLifetimeState](hot.LRU, capacity).
					WithTTL(24 * time.Hour).WithJanitor().Build()
			},
		})
	})
	return channelAffinityLifetimeCache
}

func channelAffinityLifetimeSuffix(key string) string {
	return strings.TrimPrefix(getChannelAffinityCache().FullKey(key), channelAffinityCacheNamespace+":")
}

func channelAffinityLifetimeLock(key string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(channelAffinityLifetimeSuffix(key)))
	return &channelAffinityLifetimeLocks[h.Sum32()%uint32(len(channelAffinityLifetimeLocks))]
}

type channelAffinityBindingSnapshot struct {
	channelID   int
	found       bool
	lifetime    channelAffinityLifetimeState
	bindingRaw  string
	lifetimeRaw string
}

// The caller holds the stripe lock in memory mode. Redis MGET takes a single
// atomic snapshot; the update script compares both raw values before writing.
func loadChannelAffinityBinding(key string) (channelAffinityBindingSnapshot, error) {
	var snapshot channelAffinityBindingSnapshot
	suffix := channelAffinityLifetimeSuffix(key)
	if common.RedisEnabled && common.RDB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		values, err := common.RDB.MGet(ctx, getChannelAffinityCache().FullKey(key), getChannelAffinityLifetimeCache().FullKey(suffix)).Result()
		if err != nil {
			return snapshot, err
		}
		if raw, ok := values[0].(string); ok {
			snapshot.bindingRaw, snapshot.found = raw, true
			snapshot.channelID, err = strconv.Atoi(raw)
			if err != nil {
				return snapshot, errors.New("invalid channel affinity binding")
			}
		}
		if raw, ok := values[1].(string); ok {
			snapshot.lifetimeRaw = raw
			snapshot.lifetime, err = (channelAffinityLifetimeCodec{}).Decode(raw)
		}
		return snapshot, err
	}
	var err error
	snapshot.channelID, snapshot.found, err = getChannelAffinityCache().Get(key)
	if err != nil {
		return snapshot, err
	}
	snapshot.lifetime, _, err = getChannelAffinityLifetimeCache().Get(suffix)
	return snapshot, err
}

func readChannelAffinityBinding(meta *channelAffinityMeta) (int, bool, error) {
	lock := channelAffinityLifetimeLock(meta.CacheKey)
	lock.Lock()
	defer lock.Unlock()

	observation := &channelAffinityLifetimeObservation{StartedAt: channelAffinityLifetimeNow().UnixMilli()}
	meta.LifetimeObserved = observation
	snapshot, err := loadChannelAffinityBinding(meta.CacheKey)
	if err != nil {
		return 0, false, err
	}
	state := snapshot.lifetime
	observation.Epoch = state.Epoch
	if snapshot.found && state.ChannelID == snapshot.channelID && state.limited() {
		if state.expired(channelAffinityLifetimeNow().UnixMilli()) {
			return 0, false, nil
		}
		observation.LiveLimited = true
	}
	return snapshot.channelID, snapshot.found, nil
}

// A matching live observation may only renew its original epoch. Requests
// which saw no live limited binding may join a newly created epoch on the same
// channel, but cannot reset its deadline or replace another channel's epoch.
func nextChannelAffinityLifetime(meta channelAffinityMeta, snapshot channelAffinityBindingSnapshot, channelID, ttlSeconds int, now int64) (channelAffinityLifetimeState, time.Duration, bool) {
	state := snapshot.lifetime
	observed := meta.LifetimeObserved
	if observed == nil {
		if state.Epoch != "" {
			return state, 0, false
		}
	} else if observed.Epoch != state.Epoch {
		if observed.LiveLimited || !state.limited() || state.expired(now) || state.ChannelID != channelID ||
			!snapshot.found || snapshot.channelID != channelID || state.BoundAt < observed.StartedAt {
			return state, 0, false
		}
	} else if observed.LiveLimited && (state.expired(now) || (snapshot.found && snapshot.channelID != state.ChannelID)) {
		return state, 0, false
	}

	ttl := time.Duration(ttlSeconds) * time.Second
	if state.ChannelID == channelID && state.limited() && !state.expired(now) && (!snapshot.found || snapshot.channelID == channelID) {
		// Existing metadata owns the policy for this residence, even if the
		// configured policy has briefly been removed or changed.
		state.IdleExpiresAt = min(now+int64(state.IdleTTLSeconds)*1000, state.AbsoluteExpiresAt)
		return state, time.Duration(state.IdleExpiresAt-now) * time.Millisecond, true
	}

	var policy operation_setting.ChannelAffinityLifetime
	for _, candidate := range meta.ChannelLifetimes {
		if candidate.ChannelID == channelID {
			policy = candidate
			break
		}
	}
	if policy.ChannelID == 0 && state.ChannelID == channelID && state.limited() {
		policy = operation_setting.ChannelAffinityLifetime{
			ChannelID: channelID, IdleTTLSeconds: state.IdleTTLSeconds,
			MaxResidenceSeconds: int((state.AbsoluteExpiresAt - state.BoundAt) / 1000),
		}
	}
	// Absence of metadata is the compatibility marker for pre-existing v1
	// integer bindings. Never retrofit a limited residence onto those bindings.
	legacy := state.Epoch == "" && snapshot.found && snapshot.channelID == channelID
	if !legacy && policy.IdleTTLSeconds > 0 && policy.MaxResidenceSeconds > 0 {
		state = channelAffinityLifetimeState{
			ChannelID: channelID, Epoch: common.GetUUID(), BoundAt: now,
			IdleTTLSeconds:    policy.IdleTTLSeconds,
			AbsoluteExpiresAt: now + int64(policy.MaxResidenceSeconds)*1000,
		}
		state.IdleExpiresAt = min(now+int64(policy.IdleTTLSeconds)*1000, state.AbsoluteExpiresAt)
		ttl = time.Duration(state.IdleExpiresAt-now) * time.Millisecond
	} else if state.Epoch != "" && (state.ChannelID != channelID || state.limited()) {
		state = channelAffinityLifetimeState{ChannelID: channelID, Epoch: common.GetUUID(), BoundAt: now}
	}
	return state, ttl, true
}

var channelAffinityLifetimeUpdate = redis.NewScript(`
if (redis.call('GET', KEYS[1]) or '') ~= ARGV[1] or
   (redis.call('GET', KEYS[2]) or '') ~= ARGV[2] then
  return 0
end
redis.call('SET', KEYS[1], ARGV[3], 'PX', ARGV[4])
if ARGV[5] == '' then
  redis.call('DEL', KEYS[2])
else
  redis.call('SET', KEYS[2], ARGV[5], 'PX', ARGV[6])
end
return 1
`)

func recordChannelAffinityBinding(meta channelAffinityMeta, channelID, ttlSeconds int) error {
	lock := channelAffinityLifetimeLock(meta.CacheKey)
	lock.Lock()
	defer lock.Unlock()
	suffix := channelAffinityLifetimeSuffix(meta.CacheKey)
	for range 8 {
		snapshot, err := loadChannelAffinityBinding(meta.CacheKey)
		if err != nil {
			return err
		}
		now := channelAffinityLifetimeNow().UnixMilli()
		state, ttl, write := nextChannelAffinityLifetime(meta, snapshot, channelID, ttlSeconds, now)
		if !write {
			return nil
		}
		// Retain the fence for a day (or the legacy rule TTL) beyond the fixed
		// deadline, including policies whose residence itself exceeds a day.
		retention := max(24*time.Hour, time.Duration(ttlSeconds)*time.Second)
		remaining := time.Duration(max(0, state.AbsoluteExpiresAt-now)) * time.Millisecond
		if remaining > time.Duration(math.MaxInt64)-retention {
			retention = time.Duration(math.MaxInt64)
		} else {
			retention += remaining
		}
		if common.RedisEnabled && common.RDB != nil {
			raw := ""
			if state.Epoch != "" {
				raw, err = (channelAffinityLifetimeCodec{}).Encode(state)
				if err != nil {
					return err
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			updated, err := channelAffinityLifetimeUpdate.Run(ctx, common.RDB, []string{
				getChannelAffinityCache().FullKey(meta.CacheKey), getChannelAffinityLifetimeCache().FullKey(suffix),
			}, snapshot.bindingRaw, snapshot.lifetimeRaw, channelID, ttl.Milliseconds(), raw, retention.Milliseconds()).Int()
			cancel()
			if err != nil {
				return err
			}
			if updated == 1 {
				return nil
			}
			continue
		}
		if state.Epoch != "" {
			if err := getChannelAffinityLifetimeCache().SetWithTTL(suffix, state, retention); err != nil {
				return err
			}
		}
		return getChannelAffinityCache().SetWithTTL(meta.CacheKey, channelID, ttl)
	}
	return errors.New("channel affinity binding changed during update")
}

var channelAffinityLifetimeDelete = redis.NewScript(`
local deleted = redis.call('DEL', KEYS[1])
redis.call('DEL', KEYS[2])
return deleted
`)

func deleteChannelAffinityBinding(key string) (bool, error) {
	lock := channelAffinityLifetimeLock(key)
	lock.Lock()
	defer lock.Unlock()
	suffix := channelAffinityLifetimeSuffix(key)
	if common.RedisEnabled && common.RDB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		deleted, err := channelAffinityLifetimeDelete.Run(ctx, common.RDB, []string{
			getChannelAffinityCache().FullKey(key), getChannelAffinityLifetimeCache().FullKey(suffix),
		}).Int()
		return deleted > 0, err
	}
	if _, err := getChannelAffinityLifetimeCache().DeleteMany([]string{suffix}); err != nil {
		return false, err
	}
	deleted, err := getChannelAffinityCache().DeleteMany([]string{key})
	return deleted[getChannelAffinityCache().FullKey(key)], err
}

// Enumerate both namespaces so expired bindings' orphan metadata is included.
// Filter literal prefixes in Go: rule names containing Redis glob characters
// must not clear a different rule. Counts still refer only to v1 main keys.
func clearChannelAffinityBindings(prefix string) (int, error) {
	mainKeys, err := getChannelAffinityCache().Keys()
	if err != nil {
		return 0, err
	}
	metaKeys, err := getChannelAffinityLifetimeCache().Keys()
	if err != nil {
		return 0, err
	}
	suffixes := make(map[string]struct{}, len(mainKeys)+len(metaKeys))
	for _, key := range mainKeys {
		suffixes[channelAffinityLifetimeSuffix(key)] = struct{}{}
	}
	for _, key := range metaKeys {
		suffixes[strings.TrimPrefix(key, channelAffinityLifetimeNamespace+":")] = struct{}{}
	}
	deleted := 0
	for suffix := range suffixes {
		if !strings.HasPrefix(suffix, prefix) {
			continue
		}
		ok, err := deleteChannelAffinityBinding(suffix)
		if err != nil {
			return deleted, err
		}
		if ok {
			deleted++
		}
	}
	return deleted, nil
}
