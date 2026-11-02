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
	// Survives automatic residence changes, fencing every request observed
	// before a manual reset or migration, including legacy/unbound requests.
	ManualEpoch string `json:"manual_epoch,omitempty"`
}

func (s channelAffinityLifetimeState) limited() bool {
	return s.AbsoluteExpiresAt > 0 && s.IdleExpiresAt > 0 && s.IdleTTLSeconds > 0
}

func (s channelAffinityLifetimeState) expired(now int64) bool {
	return s.limited() && (now >= s.IdleExpiresAt || now >= s.AbsoluteExpiresAt)
}

type channelAffinityLifetimeObservation struct {
	StartedAt       int64
	Epoch           string
	LiveLimited     bool
	ManualEpoch     string
	ManagementEpoch string
	LiveBinding     bool
	ClearObserved   channelAffinityClearObservation
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
	channelID     int
	found         bool
	lifetime      channelAffinityLifetimeState
	bindingRaw    string
	lifetimeRaw   string
	management    channelAffinityManagedBinding
	managementRaw string
	bindingTTL    time.Duration
}

// The caller holds the stripe lock in memory mode. Redis MGET takes a single
// atomic snapshot; the update script compares both raw values before writing.
func loadChannelAffinityBinding(key string) (channelAffinityBindingSnapshot, error) {
	var snapshot channelAffinityBindingSnapshot
	suffix := channelAffinityLifetimeSuffix(key)
	if common.RedisEnabled && common.RDB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		values, err := common.RDB.MGet(ctx, getChannelAffinityCache().FullKey(key), getChannelAffinityLifetimeCache().FullKey(suffix), getChannelAffinityManagementCache().FullKey(channelAffinityBindingID(key))).Result()
		if err != nil {
			return snapshot, err
		}
		snapshot, err = decodeChannelAffinityBindingSnapshot(values)
		if err != nil {
			return snapshot, err
		}
		if snapshot.found && snapshot.management.CacheKey == "" {
			snapshot.bindingTTL, err = common.RDB.PTTL(ctx, getChannelAffinityCache().FullKey(key)).Result()
		}
		return snapshot, err
	}
	var err error
	snapshot.channelID, snapshot.found, err = getChannelAffinityCache().Get(key)
	if err != nil {
		return snapshot, err
	}
	snapshot.lifetime, _, err = getChannelAffinityLifetimeCache().Get(suffix)
	if err != nil {
		return snapshot, err
	}
	snapshot.management, _, err = getChannelAffinityManagementCache().Get(channelAffinityBindingID(key))
	return snapshot, err
}

func decodeChannelAffinityBindingSnapshot(values []any) (channelAffinityBindingSnapshot, error) {
	var snapshot channelAffinityBindingSnapshot
	var err error
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
		if err != nil {
			return snapshot, err
		}
	}
	if raw, ok := values[2].(string); ok {
		snapshot.managementRaw = raw
		snapshot.management, err = (channelAffinityManagementCodec{}).Decode(raw)
	}
	return snapshot, err
}

var channelAffinityManagementRegister = redis.NewScript(`
if (redis.call('GET', KEYS[1]) or '') ~= ARGV[1] or
   (redis.call('GET', KEYS[2]) or '') ~= ARGV[2] or
   (redis.call('GET', KEYS[3]) or '') ~= ARGV[3] then return 0 end
if (redis.call('GET', KEYS[4]) or '') ~= ARGV[6] or
   (redis.call('GET', KEYS[5]) or '') ~= ARGV[7] or
   (redis.call('GET', KEYS[6]) or '') ~= ARGV[8] then return 0 end
redis.call('SET', KEYS[3], ARGV[4], 'PX', ARGV[5])
return 1
`)

func readChannelAffinityBinding(meta *channelAffinityMeta) (int, bool, error) {
	lock := channelAffinityLifetimeLock(meta.CacheKey)
	lock.Lock()
	defer lock.Unlock()

	observation := &channelAffinityLifetimeObservation{StartedAt: channelAffinityLifetimeNow().UnixMilli()}
	meta.LifetimeObserved = observation
	clearObserved, err := loadChannelAffinityClearObservation(meta.RuleName, meta.CacheKey)
	if err != nil {
		return 0, false, err
	}
	observation.ClearObserved = clearObserved
	for range 8 {
		snapshot, err := loadChannelAffinityBinding(meta.CacheKey)
		if err != nil {
			return 0, false, err
		}
		state := snapshot.lifetime
		observation.Epoch, observation.ManualEpoch = state.Epoch, state.ManualEpoch
		observation.ManagementEpoch = snapshot.management.ResidenceEpoch
		now := channelAffinityLifetimeNow().UnixMilli()
		observation.LiveBinding = snapshot.live(now)
		observation.LiveLimited = snapshot.live(now) && state.ChannelID == snapshot.channelID && state.limited()
		if !snapshot.live(now) {
			return 0, false, nil
		}
		expiresAt := snapshot.management.ExpiresAtMillis
		if snapshot.management.Binding.ChannelID != snapshot.channelID {
			expiresAt = 0
		}
		if expiresAt == 0 {
			ttl := time.Duration(meta.TTLSeconds) * time.Second
			if ttl <= 0 {
				ttl = time.Hour
			}
			if snapshot.bindingTTL > 0 {
				ttl = snapshot.bindingTTL
			}
			expiresAt = now + ttl.Milliseconds()
		}
		if state.ChannelID == snapshot.channelID && state.limited() {
			expiresAt = min(expiresAt, state.IdleExpiresAt, state.AbsoluteExpiresAt)
		}
		value := nextChannelAffinityManagement(*meta, snapshot, snapshot.channelID, expiresAt, now, true)
		retention := max(24*time.Hour, time.Duration(max(meta.TTLSeconds, 1))*time.Second) + time.Duration(max(0, state.AbsoluteExpiresAt-now))*time.Millisecond
		if common.RedisEnabled && common.RDB != nil {
			raw, err := (channelAffinityManagementCodec{}).Encode(value)
			if err != nil {
				return 0, false, err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			updated, err := channelAffinityManagementRegister.Run(ctx, common.RDB, []string{
				getChannelAffinityCache().FullKey(meta.CacheKey), getChannelAffinityLifetimeCache().FullKey(channelAffinityLifetimeSuffix(meta.CacheKey)), getChannelAffinityManagementCache().FullKey(value.Binding.ID),
				channelAffinityClearFenceCache.FullKey("all"), channelAffinityClearFenceCache.FullKey("rule:" + meta.RuleName), channelAffinityClearFenceCache.FullKey(clearObserved.BindingKey),
			}, snapshot.bindingRaw, snapshot.lifetimeRaw, snapshot.managementRaw, raw, retention.Milliseconds(), clearObserved.All, clearObserved.Rule, clearObserved.Binding).Int()
			cancel()
			if err != nil {
				return 0, false, err
			}
			if updated != 1 {
				continue
			}
		} else {
			channelAffinityClearFenceLock.Lock()
			current, err := loadChannelAffinityMemoryClearObservation(clearObserved)
			if err == nil && current == clearObserved {
				err = getChannelAffinityManagementCache().SetWithTTL(value.Binding.ID, value, retention)
			}
			channelAffinityClearFenceLock.Unlock()
			if err != nil || current != clearObserved {
				return 0, false, err
			}
		}
		observation.ManagementEpoch = value.ResidenceEpoch
		return snapshot.channelID, true, nil
	}
	return 0, false, errors.New("channel affinity binding changed during observation")
}

// A matching live observation may only renew its original epoch. Requests
// which saw no live limited binding may join a newly created epoch on the same
// channel, but cannot reset its deadline or replace another channel's epoch.
func nextChannelAffinityLifetime(meta channelAffinityMeta, snapshot channelAffinityBindingSnapshot, channelID, ttlSeconds int, now int64) (channelAffinityLifetimeState, time.Duration, bool) {
	state := snapshot.lifetime
	observed := meta.LifetimeObserved
	if observed != nil && observed.LiveBinding && observed.ManagementEpoch != "" && observed.ManagementEpoch != snapshot.management.ResidenceEpoch {
		return state, 0, false
	}
	if state.ManualEpoch != "" && (observed == nil || observed.ManualEpoch != state.ManualEpoch) {
		return state, 0, false
	}
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
	legacy := state.Epoch == "" && snapshot.found && snapshot.channelID == channelID && (snapshot.management.CacheKey == "" || snapshot.live(now))
	if !legacy && policy.IdleTTLSeconds > 0 && policy.MaxResidenceSeconds > 0 {
		state = channelAffinityLifetimeState{
			ChannelID: channelID, Epoch: common.GetUUID(), BoundAt: now,
			ManualEpoch:       state.ManualEpoch,
			IdleTTLSeconds:    policy.IdleTTLSeconds,
			AbsoluteExpiresAt: now + int64(policy.MaxResidenceSeconds)*1000,
		}
		state.IdleExpiresAt = min(now+int64(policy.IdleTTLSeconds)*1000, state.AbsoluteExpiresAt)
		ttl = time.Duration(state.IdleExpiresAt-now) * time.Millisecond
	} else if state.Epoch != "" && (state.ChannelID != channelID || state.limited()) {
		state = channelAffinityLifetimeState{ChannelID: channelID, Epoch: common.GetUUID(), BoundAt: now, ManualEpoch: state.ManualEpoch}
	}
	return state, ttl, true
}

func recordChannelAffinityBinding(meta channelAffinityMeta, channelID, ttlSeconds int) error {
	lock := channelAffinityLifetimeLock(meta.CacheKey)
	lock.Lock()
	defer lock.Unlock()
	meta.TTLSeconds = ttlSeconds
	for range 8 {
		observedClear := channelAffinityClearObservation{RuleName: meta.RuleName, BindingKey: channelAffinityBindingFenceKey(meta.CacheKey)}
		if meta.LifetimeObserved != nil {
			observedClear = meta.LifetimeObserved.ClearObserved
		}
		currentClear, err := loadChannelAffinityClearObservation(meta.RuleName, meta.CacheKey)
		if err != nil {
			return err
		}
		if observedClear != currentClear {
			return nil
		}
		snapshot, err := loadChannelAffinityBinding(meta.CacheKey)
		if err != nil {
			return err
		}
		now := channelAffinityLifetimeNow().UnixMilli()
		bindingTTLSeconds := ttlSeconds
		if snapshot.live(now) && snapshot.management.TTLSeconds > 0 {
			bindingTTLSeconds = min(bindingTTLSeconds, snapshot.management.TTLSeconds)
		}
		state, ttl, write := nextChannelAffinityLifetime(meta, snapshot, channelID, bindingTTLSeconds, now)
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
		legacy := snapshot.management.CacheKey == "" && snapshot.found
		value := nextChannelAffinityManagement(meta, snapshot, channelID, now+ttl.Milliseconds(), now, legacy)
		updated, err := writeChannelAffinityManagedBinding(meta.CacheKey, snapshot, channelID, ttl, state, value, retention, observedClear)
		if err != nil {
			return err
		}
		if updated {
			return nil
		}
	}
	return errors.New("channel affinity binding changed during update")
}

func deleteChannelAffinityBinding(key string, observations ...*channelAffinityLifetimeObservation) (bool, error) {
	lock := channelAffinityLifetimeLock(key)
	lock.Lock()
	defer lock.Unlock()
	for range 8 {
		var fences []channelAffinityClearObservation
		if len(observations) > 0 && observations[0] != nil {
			observed := observations[0].ClearObserved
			current, err := loadChannelAffinityClearObservation(observed.RuleName, key)
			if err != nil || current != observed {
				return false, err
			}
			fences = append(fences, observed)
		}
		snapshot, err := loadChannelAffinityBinding(key)
		if err != nil {
			return false, err
		}
		if len(observations) > 0 && observations[0] != nil && observations[0].ManualEpoch != snapshot.lifetime.ManualEpoch {
			return false, nil
		}
		if !snapshot.found && snapshot.lifetime.Epoch == "" && snapshot.management.CacheKey == "" {
			return false, nil
		}
		// Every clear creates a tombstone. Deleting the epoch would let a slow
		// completion recreate a legacy or unlimited binding immediately.
		state := snapshot.lifetime
		if snapshot.found {
			state.ChannelID = snapshot.channelID
		}
		if state.ChannelID <= 0 {
			state.ChannelID = snapshot.management.Binding.ChannelID
		}
		if state.ChannelID <= 0 {
			return false, errors.New("invalid channel affinity lifetime metadata")
		}
		state.Epoch, state.ManualEpoch, state.BoundAt = common.GetUUID(), common.GetUUID(), channelAffinityLifetimeNow().UnixMilli()
		state.IdleExpiresAt, state.AbsoluteExpiresAt, state.IdleTTLSeconds = 0, 0, 0
		value := snapshot.management
		if value.CacheKey == "" {
			value.CacheKey = getChannelAffinityCache().FullKey(key)
			value.Binding.ID = channelAffinityBindingID(key)
			value.Binding.ChannelID = state.ChannelID
		}
		value.Active, value.ExpiresAtMillis, value.Binding.ExpiresAt = false, 0, 0
		value.Binding.Version, value.ResidenceEpoch = common.GetUUID(), common.GetUUID()
		updated, err := writeChannelAffinityManagedBinding(key, snapshot, 0, 0, state, value, 24*time.Hour, fences...)
		if err != nil {
			return false, err
		}
		if !updated {
			continue
		}
		// The request performing an automatic unusable-channel clear may bind
		// its own successful fallback. Other requests keep their old observations.
		if len(observations) > 0 && observations[0] != nil {
			observations[0].Epoch, observations[0].ManualEpoch = state.Epoch, state.ManualEpoch
			observations[0].ManagementEpoch = value.ResidenceEpoch
			observations[0].LiveBinding, observations[0].LiveLimited = false, false
			observations[0].ClearObserved.Binding = state.ManualEpoch
		}
		return snapshot.found, nil
	}
	return false, errors.New("channel affinity binding changed during deletion")
}

// Enumerate both namespaces so expired bindings' orphan metadata is included.
// Filter literal prefixes in Go: rule names containing Redis glob characters
// must not clear a different rule. Counts still refer only to v1 main keys.
func clearChannelAffinityBindings(prefix string) (int, error) {
	getChannelAffinityManagementCache()
	clearKey := "all"
	if prefix != "" {
		clearKey = "rule:" + strings.TrimSuffix(prefix, ":")
	}
	channelAffinityClearFenceLock.Lock()
	err := channelAffinityClearFenceCache.SetWithTTL(clearKey, common.GetUUID(), 0)
	channelAffinityClearFenceLock.Unlock()
	if err != nil {
		return 0, err
	}
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
