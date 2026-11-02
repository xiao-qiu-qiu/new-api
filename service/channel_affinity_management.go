package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/cachex"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/go-redis/redis/v8"
	"github.com/samber/hot"
)

const channelAffinityManagementNamespace = "new-api:channel_affinity_management:v1"
const channelAffinityClearFenceNamespace = "new-api:channel_affinity_clear_fence:v1"

var (
	channelAffinityManagementOnce  sync.Once
	channelAffinityManagementCache *cachex.HybridCache[channelAffinityManagedBinding]
	channelAffinityClearFenceCache *cachex.HybridCache[string]
	channelAffinityClearFenceLock  sync.RWMutex
)

type ChannelAffinityBindingFilter struct {
	ChannelID, UserID, TokenID                             int
	Model, BindingID, RuleName, UsingGroup, KeyFingerprint string
	Page, PageSize                                         int
}

type ChannelAffinityBindingRef struct {
	ID        string `json:"id"`
	Version   string `json:"version"`
	ChannelID int    `json:"channel_id"`
}

type ChannelAffinityBinding struct {
	ID               string   `json:"id"`
	Version          string   `json:"version"`
	ChannelID        int      `json:"channel_id"`
	ChannelName      string   `json:"channel_name"`
	UserID           int      `json:"user_id"`
	UserName         string   `json:"user_name"`
	TokenID          int      `json:"token_id"`
	TokenName        string   `json:"token_name"`
	RuleName         string   `json:"rule_name"`
	UsingGroup       string   `json:"using_group"`
	Models           []string `json:"models"`
	SessionMode      string   `json:"session_mode"`
	ExpiresAt        int64    `json:"expires_at"`
	LastSeenAt       int64    `json:"last_seen_at"`
	Shared           bool     `json:"shared"`
	KeyFingerprint   string   `json:"key_fp"`
	OwnershipKnown   bool     `json:"ownership_known"`
	MetadataComplete bool     `json:"metadata_complete"`
	UsingGroups      []string `json:"using_groups"`
	RequestPaths     []string `json:"request_paths"`
	RuleNames        []string `json:"rule_names"`
}

type ChannelAffinityBindingListResult struct {
	Items    []ChannelAffinityBinding `json:"items"`
	Total    int                      `json:"total"`
	Page     int                      `json:"page"`
	PageSize int                      `json:"page_size"`
}

type ChannelAffinityTarget struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type ChannelAffinityBindingMutationItem struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type ChannelAffinityBindingMutationResult struct {
	Succeeded  int                                  `json:"succeeded"`
	Expired    int                                  `json:"expired"`
	Conflicted int                                  `json:"conflicted"`
	Failed     int                                  `json:"failed"`
	Items      []ChannelAffinityBindingMutationItem `json:"items"`
}

// Routes retain complete request combinations rather than the last model or a
// Cartesian product of unrelated groups and models. Auto groups are alternatives
// within one request, just as they are in SelectChannelForRequest.
type channelAffinityRoute struct {
	RuleName    string              `json:"rule_name"`
	UsingGroup  string              `json:"using_group"`
	Groups      []string            `json:"groups"`
	Model       string              `json:"model"`
	Path        string              `json:"path"`
	SessionMode string              `json:"session_mode"`
	Filters     []dto.ChannelFilter `json:"filters"`
}

// This private cache value must never be returned or logged. The public binding
// contains only an opaque identity and a one-way fingerprint of the session.
type channelAffinityManagedBinding struct {
	Binding         ChannelAffinityBinding `json:"binding"`
	CacheKey        string                 `json:"cache_key"`
	Routes          []channelAffinityRoute `json:"routes"`
	ExpiresAtMillis int64                  `json:"expires_at_millis"`
	Active          bool                   `json:"active"`
	OwnerObserved   bool                   `json:"owner_observed"`
	ObservedUserID  int                    `json:"observed_user_id"`
	ObservedTokenID int                    `json:"observed_token_id"`
	ResidenceEpoch  string                 `json:"residence_epoch"`
	TTLSeconds      int                    `json:"ttl_seconds"`
}

type channelAffinityManagementCodec struct{}

func (channelAffinityManagementCodec) Encode(v channelAffinityManagedBinding) (string, error) {
	b, err := common.Marshal(v)
	return string(b), err
}

func (channelAffinityManagementCodec) Decode(raw string) (channelAffinityManagedBinding, error) {
	var value channelAffinityManagedBinding
	err := common.UnmarshalJsonStr(raw, &value)
	if err == nil && (value.CacheKey == "" || value.Binding.ID != channelAffinityBindingID(value.CacheKey)) {
		err = errors.New("invalid channel affinity management metadata")
	}
	return value, err
}

func getChannelAffinityManagementCache() *cachex.HybridCache[channelAffinityManagedBinding] {
	channelAffinityManagementOnce.Do(func() {
		capacity := operation_setting.GetChannelAffinitySetting().MaxEntries
		if capacity <= 0 {
			capacity = 100_000
		}
		channelAffinityManagementCache = cachex.NewHybridCache[channelAffinityManagedBinding](cachex.HybridCacheConfig[channelAffinityManagedBinding]{
			Namespace: cachex.Namespace(channelAffinityManagementNamespace), Redis: common.RDB,
			RedisEnabled: func() bool { return common.RedisEnabled && common.RDB != nil },
			RedisCodec:   channelAffinityManagementCodec{},
			Memory: func() *hot.HotCache[string, channelAffinityManagedBinding] {
				return hot.NewHotCache[string, channelAffinityManagedBinding](hot.LRU, capacity).WithTTL(24 * time.Hour).WithJanitor().Build()
			},
		})
		// Administrators create only one global/per-rule generation; these must
		// survive binding expiry so old unbound requests cannot undo a clear.
		channelAffinityClearFenceCache = cachex.NewHybridCache[string](cachex.HybridCacheConfig[string]{
			Namespace: cachex.Namespace(channelAffinityClearFenceNamespace), Redis: common.RDB,
			RedisEnabled: func() bool { return common.RedisEnabled && common.RDB != nil }, RedisCodec: cachex.StringCodec{},
			Memory: func() *hot.HotCache[string, string] { return hot.NewHotCache[string, string](hot.LRU, 100_000).Build() },
		})
	})
	return channelAffinityManagementCache
}

type channelAffinityClearObservation struct{ RuleName, All, Rule, BindingKey, Binding string }

// Only 256 generations are needed, independent of the binding LRU. Collisions
// fence old in-flight writes; they never invalidate another binding's reads.
func channelAffinityBindingFenceKey(key string) string {
	digest := sha256.Sum256([]byte(getChannelAffinityCache().FullKey(key)))
	return "binding:" + hex.EncodeToString(digest[:1])
}

var channelAffinityFenceInitialize = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if not value then
  value = ARGV[1]
  redis.call('SET', KEYS[1], value)
end
return value
`)

// A missing/evicted generation is initialized to a new nonempty value, never
// the empty baseline that an old unbound request could otherwise reuse.
func loadChannelAffinityClearObservation(ruleName, key string) (channelAffinityClearObservation, error) {
	getChannelAffinityManagementCache()
	value := channelAffinityClearObservation{RuleName: ruleName, BindingKey: channelAffinityBindingFenceKey(key)}
	if common.RedisEnabled && common.RDB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		values, err := common.RDB.MGet(ctx, channelAffinityClearFenceCache.FullKey("all"), channelAffinityClearFenceCache.FullKey("rule:"+ruleName), channelAffinityClearFenceCache.FullKey(value.BindingKey)).Result()
		if err != nil {
			return value, err
		}
		value.All, _ = values[0].(string)
		value.Rule, _ = values[1].(string)
		value.Binding, _ = values[2].(string)
		if value.Binding == "" {
			value.Binding, err = channelAffinityFenceInitialize.Run(ctx, common.RDB, []string{channelAffinityClearFenceCache.FullKey(value.BindingKey)}, common.GetUUID()).Text()
		}
		return value, err
	}
	channelAffinityClearFenceLock.Lock()
	defer channelAffinityClearFenceLock.Unlock()
	return loadChannelAffinityMemoryClearObservation(value)
}

// The caller holds channelAffinityClearFenceLock through any dependent write.
func loadChannelAffinityMemoryClearObservation(value channelAffinityClearObservation) (channelAffinityClearObservation, error) {
	var err error
	value.All, _, err = channelAffinityClearFenceCache.Get("all")
	if err != nil {
		return value, err
	}
	value.Rule, _, err = channelAffinityClearFenceCache.Get("rule:" + value.RuleName)
	if err != nil {
		return value, err
	}
	value.Binding, _, err = channelAffinityClearFenceCache.Get(value.BindingKey)
	if err == nil && value.Binding == "" {
		value.Binding = common.GetUUID()
		err = channelAffinityClearFenceCache.SetWithTTL(value.BindingKey, value.Binding, 0)
	}
	return value, err
}

func channelAffinityBindingID(key string) string {
	if key == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(cachex.Namespace(channelAffinityCacheNamespace).FullKey(key)))
	return hex.EncodeToString(digest[:])
}

func safeChannelAffinityName(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	runes := []rune(strings.TrimSpace(value))
	return string(runes[:min(len(runes), 128)])
}

func cloneChannelAffinityFilters(filters []dto.ChannelFilter) []dto.ChannelFilter {
	cloned := slices.Clone(filters)
	for i := range cloned {
		cloned[i].TaskPluginKeys = slices.Clone(cloned[i].TaskPluginKeys)
		cloned[i].TaskPluginChannelTypes = slices.Clone(cloned[i].TaskPluginChannelTypes)
	}
	return cloned
}

func (s channelAffinityBindingSnapshot) live(now int64) bool {
	if !s.found || s.lifetime.ChannelID == s.channelID && s.lifetime.expired(now) {
		return false
	}
	if s.management.CacheKey == "" {
		return true
	}
	if s.management.Active && s.management.Binding.ChannelID != s.channelID {
		return s.lifetime.ManualEpoch == ""
	}
	return s.management.Active && s.management.Binding.ChannelID == s.channelID && s.management.ExpiresAtMillis > now
}

func nextChannelAffinityManagement(meta channelAffinityMeta, snapshot channelAffinityBindingSnapshot, channelID int, expiresAt, now int64, legacy bool) channelAffinityManagedBinding {
	value := snapshot.management
	value.Routes = slices.Clone(value.Routes)
	value.Binding.Models = slices.Clone(value.Binding.Models)
	value.Binding.UsingGroups = slices.Clone(value.Binding.UsingGroups)
	value.Binding.RequestPaths = slices.Clone(value.Binding.RequestPaths)
	changed := false
	if value.CacheKey == "" || !snapshot.live(now) {
		value = channelAffinityManagedBinding{ResidenceEpoch: common.GetUUID()}
		value.CacheKey = getChannelAffinityCache().FullKey(meta.CacheKey)
		value.Binding = ChannelAffinityBinding{
			ID: channelAffinityBindingID(meta.CacheKey), RuleName: meta.RuleName,
			UsingGroup: meta.UsingGroup, SessionMode: meta.SessionMode, KeyFingerprint: meta.KeyFingerprint,
			OwnershipKnown: !legacy && meta.UserID > 0, MetadataComplete: true,
			Models: []string{}, UsingGroups: []string{}, RequestPaths: []string{}, RuleNames: []string{},
		}
		changed = true
	}
	if !snapshot.live(now) || value.Binding.ChannelID != channelID {
		changed = true
	}
	ttlSeconds := meta.TTLSeconds
	if ttlSeconds <= 0 {
		ttlSeconds = 3600
	}
	if value.TTLSeconds == 0 || ttlSeconds < value.TTLSeconds {
		value.TTLSeconds, changed = ttlSeconds, true
	}
	if meta.UserID > 0 {
		if value.OwnerObserved && (value.ObservedUserID != meta.UserID || value.ObservedTokenID != meta.TokenID) {
			if !value.Binding.Shared {
				changed = true
			}
			value.Binding.Shared = true
		} else if !value.OwnerObserved {
			value.OwnerObserved, value.ObservedUserID, value.ObservedTokenID = true, meta.UserID, meta.TokenID
		}
		if value.Binding.OwnershipKnown && !value.Binding.Shared {
			value.Binding.UserID, value.Binding.TokenID = meta.UserID, meta.TokenID
			value.Binding.UserName, value.Binding.TokenName = safeChannelAffinityName(meta.UserName), safeChannelAffinityName(meta.TokenName)
		}
	} else if value.Binding.OwnershipKnown {
		value.Binding.OwnershipKnown, changed = false, true
	}
	if value.Binding.Shared || !value.Binding.OwnershipKnown {
		value.Binding.UserID, value.Binding.TokenID = 0, 0
		value.Binding.UserName, value.Binding.TokenName = "", ""
	}
	groups := slices.Clone(meta.Groups)
	if len(groups) == 0 && meta.UsingGroup != "auto" && meta.UsingGroup != "" {
		groups = []string{meta.UsingGroup}
	}
	filters := cloneChannelAffinityFilters(meta.Filters)
	// Always check the observed path, including callers that do not install the
	// distributor's filter (e.g. a legacy request or WebSocket integration).
	if meta.RequestPath != "" && !slices.ContainsFunc(filters, func(f dto.ChannelFilter) bool {
		return f.Kind == dto.FilterRequestPath && f.RequestPath == meta.RequestPath
	}) {
		filters = append(filters, dto.ChannelFilter{Kind: dto.FilterRequestPath, RequestPath: meta.RequestPath})
	}
	route := channelAffinityRoute{RuleName: meta.RuleName, UsingGroup: meta.UsingGroup, Groups: groups, Model: meta.ModelName, Path: meta.RequestPath, SessionMode: meta.SessionMode, Filters: filters}
	raw, _ := common.Marshal(route)
	known := slices.ContainsFunc(value.Routes, func(previous channelAffinityRoute) bool {
		encoded, _ := common.Marshal(previous)
		return string(encoded) == string(raw)
	})
	if !known {
		changed = true
		// Bound cache value growth. Overflow fails migration closed; it must never
		// silently forget a model/protocol and advertise an unsuitable target.
		if len(value.Routes) < 256 {
			value.Routes = append(value.Routes, route)
		} else {
			value.Binding.MetadataComplete = false
			value.Binding.Version = common.GetUUID()
			value.Binding.ChannelID, value.Binding.ExpiresAt, value.Binding.LastSeenAt = channelID, expiresAt/1000, now/1000
			value.ExpiresAtMillis, value.Active = expiresAt, true
			return value
		}
		if meta.ModelName != "" && !slices.Contains(value.Binding.Models, meta.ModelName) {
			value.Binding.Models = append(value.Binding.Models, meta.ModelName)
			slices.Sort(value.Binding.Models)
		}
		if meta.UsingGroup != "" && !slices.Contains(value.Binding.UsingGroups, meta.UsingGroup) {
			value.Binding.UsingGroups = append(value.Binding.UsingGroups, meta.UsingGroup)
			slices.Sort(value.Binding.UsingGroups)
		}
		if meta.RequestPath != "" && !slices.Contains(value.Binding.RequestPaths, meta.RequestPath) {
			value.Binding.RequestPaths = append(value.Binding.RequestPaths, meta.RequestPath)
			slices.Sort(value.Binding.RequestPaths)
		}
		if !slices.Contains(value.Binding.RuleNames, meta.RuleName) {
			value.Binding.RuleNames = append(slices.Clone(value.Binding.RuleNames), meta.RuleName)
			slices.Sort(value.Binding.RuleNames)
		}
	}
	if changed || value.Binding.Version == "" {
		value.Binding.Version = common.GetUUID()
	}
	value.Binding.ChannelID, value.Binding.ExpiresAt, value.Binding.LastSeenAt = channelID, expiresAt/1000, now/1000
	value.ExpiresAtMillis, value.Active = expiresAt, true
	return value
}

func validateChannelAffinityRefs(refs []ChannelAffinityBindingRef) error {
	if len(refs) == 0 || len(refs) > 200 {
		return common.NewMessage("Select between 1 and 200 channel affinity bindings")
	}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		decoded, err := hex.DecodeString(ref.ID)
		if err != nil || len(decoded) != sha256.Size || ref.ID != strings.ToLower(ref.ID) || ref.ChannelID <= 0 || len(ref.Version) != 32 {
			return common.NewMessage("Invalid channel affinity binding reference")
		}
		if _, err := hex.DecodeString(ref.Version); err != nil {
			return common.NewMessage("Invalid channel affinity binding reference")
		}
		if seen[ref.ID] {
			return common.NewMessage("Duplicate channel affinity binding reference")
		}
		seen[ref.ID] = true
	}
	return nil
}

func channelAffinityRefStatus(snapshot channelAffinityBindingSnapshot, ref ChannelAffinityBindingRef, now int64) string {
	if snapshot.found && snapshot.management.Active && snapshot.channelID != ref.ChannelID {
		return "conflicted"
	}
	if !snapshot.live(now) {
		return "expired"
	}
	if snapshot.management.Binding.Version != ref.Version || snapshot.channelID != ref.ChannelID {
		return "conflicted"
	}
	return ""
}

// Admin scans read bounded batches. Relay reads and writes still address one
// binding directly and never enumerate this namespace.
func loadChannelAffinityManagementBatch(keys []string) ([]channelAffinityManagedBinding, error) {
	cache := getChannelAffinityManagementCache()
	values := make([]channelAffinityManagedBinding, 0, len(keys))
	if !common.RedisEnabled || common.RDB == nil {
		for _, key := range keys {
			value, found, err := cache.Get(key)
			if err != nil {
				return nil, err
			}
			if found {
				values = append(values, value)
			}
		}
		return values, nil
	}
	fullKeys := make([]string, len(keys))
	for i, key := range keys {
		fullKeys[i] = cache.FullKey(key)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rawValues, err := common.RDB.MGet(ctx, fullKeys...).Result()
	if err != nil {
		return nil, err
	}
	for _, raw := range rawValues {
		if raw, ok := raw.(string); ok {
			value, err := (channelAffinityManagementCodec{}).Decode(raw)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
	}
	return values, nil
}

func loadChannelAffinityBindingBatch(values []channelAffinityManagedBinding) ([]channelAffinityBindingSnapshot, error) {
	snapshots := make([]channelAffinityBindingSnapshot, len(values))
	if !common.RedisEnabled || common.RDB == nil {
		for i, value := range values {
			lock := channelAffinityLifetimeLock(value.CacheKey)
			lock.Lock()
			snapshot, err := loadChannelAffinityBinding(value.CacheKey)
			lock.Unlock()
			if err != nil {
				return nil, err
			}
			snapshots[i] = snapshot
		}
		return snapshots, nil
	}
	keys := make([]string, 0, len(values)*3)
	for _, value := range values {
		keys = append(keys, getChannelAffinityCache().FullKey(value.CacheKey), getChannelAffinityLifetimeCache().FullKey(channelAffinityLifetimeSuffix(value.CacheKey)), getChannelAffinityManagementCache().FullKey(value.Binding.ID))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rawValues, err := common.RDB.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	for i := range values {
		snapshots[i], err = decodeChannelAffinityBindingSnapshot(rawValues[i*3 : i*3+3])
		if err != nil {
			return nil, err
		}
	}
	return snapshots, nil
}

func channelAffinityBindingMatchesFilter(binding ChannelAffinityBinding, filter ChannelAffinityBindingFilter) bool {
	return (filter.ChannelID <= 0 || binding.ChannelID == filter.ChannelID) &&
		(filter.UserID <= 0 || binding.OwnershipKnown && !binding.Shared && binding.UserID == filter.UserID) &&
		(filter.TokenID <= 0 || binding.OwnershipKnown && !binding.Shared && binding.TokenID == filter.TokenID) &&
		(filter.Model == "" || slices.Contains(binding.Models, filter.Model)) &&
		(filter.KeyFingerprint == "" || binding.KeyFingerprint == filter.KeyFingerprint) &&
		(filter.RuleName == "" || slices.Contains(binding.RuleNames, filter.RuleName)) &&
		(filter.UsingGroup == "" || slices.Contains(binding.UsingGroups, filter.UsingGroup))
}

func ListChannelAffinityBindings(filter ChannelAffinityBindingFilter) (ChannelAffinityBindingListResult, error) {
	result := ChannelAffinityBindingListResult{Items: []ChannelAffinityBinding{}, Page: max(1, filter.Page), PageSize: filter.PageSize}
	if result.PageSize <= 0 {
		result.PageSize = 20
	}
	result.PageSize = min(result.PageSize, 200)
	if filter.ChannelID < 0 || filter.UserID < 0 || filter.TokenID < 0 {
		return result, common.NewMessage("Invalid channel affinity binding filter")
	}
	cache := getChannelAffinityManagementCache()
	var keys []string
	if filter.BindingID != "" {
		decoded, err := hex.DecodeString(filter.BindingID)
		if err != nil || len(decoded) != sha256.Size {
			return result, common.NewMessage("Invalid channel affinity binding filter")
		}
		keys = []string{filter.BindingID}
	} else {
		var err error
		keys, err = cache.Keys()
		if err != nil {
			return result, err
		}
	}
	candidates := make([]channelAffinityManagedBinding, 0)
	seen := make(map[string]bool, len(keys))
	for start := 0; start < len(keys); start += 256 {
		values, err := loadChannelAffinityManagementBatch(keys[start:min(start+256, len(keys))])
		if err != nil {
			return result, err
		}
		for _, value := range values {
			if seen[value.Binding.ID] {
				continue
			}
			seen[value.Binding.ID] = true
			if !value.Active || value.ExpiresAtMillis <= channelAffinityLifetimeNow().UnixMilli() || !channelAffinityBindingMatchesFilter(value.Binding, filter) {
				continue
			}
			candidates = append(candidates, value)
		}
	}
	slices.SortFunc(candidates, func(left, right channelAffinityManagedBinding) int {
		a, b := left.Binding, right.Binding
		if a.LastSeenAt != b.LastSeenAt {
			if a.LastSeenAt > b.LastSeenAt {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	// First prefilter the metadata, then atomically verify the matching v1 and
	// sidecar values in batches. Only the requested page resolves channel names.
	offset := -1
	if result.Page-1 <= len(candidates)/result.PageSize {
		offset = (result.Page - 1) * result.PageSize
	}
	for start := 0; start < len(candidates); start += 256 {
		snapshots, err := loadChannelAffinityBindingBatch(candidates[start:min(start+256, len(candidates))])
		if err != nil {
			return result, err
		}
		for _, snapshot := range snapshots {
			binding := snapshot.management.Binding
			if !snapshot.live(channelAffinityLifetimeNow().UnixMilli()) || !channelAffinityBindingMatchesFilter(binding, filter) {
				continue
			}
			if offset >= 0 && result.Total >= offset && len(result.Items) < result.PageSize {
				binding.Models, binding.UsingGroups, binding.RequestPaths, binding.RuleNames = slices.Clone(binding.Models), slices.Clone(binding.UsingGroups), slices.Clone(binding.RequestPaths), slices.Clone(binding.RuleNames)
				result.Items = append(result.Items, binding)
			}
			result.Total++
		}
	}
	ids := make([]int, 0, len(result.Items))
	for _, binding := range result.Items {
		if !slices.Contains(ids, binding.ChannelID) {
			ids = append(ids, binding.ChannelID)
		}
	}
	names := make(map[int]string, len(ids))
	if common.MemoryCacheEnabled {
		for _, id := range ids {
			if channel, err := model.CacheGetChannel(id); err == nil && channel != nil {
				names[id] = channel.Name
			}
		}
	} else if len(ids) > 0 && model.DB != nil {
		channels, err := model.GetChannelsByIds(ids)
		if err != nil {
			return result, err
		}
		for _, channel := range channels {
			names[channel.Id] = channel.Name
		}
	}
	for i := range result.Items {
		result.Items[i].ChannelName = names[result.Items[i].ChannelID]
	}
	return result, nil
}

// ExportAllConfigs serializes under the config manager's read lock. Decode into
// owned values so rule edits cannot change eligibility or TTL mid-operation.
func channelAffinitySettingsSnapshot() (*operation_setting.ChannelAffinitySetting, error) {
	values := make(map[string]string)
	// Option updates mutate registered structs under OptionMapRWMutex; config
	// reloads use the manager lock. Take them in the same order as option writes.
	common.OptionMapRWMutex.RLock()
	exported := config.GlobalConfig.ExportAllConfigs()
	common.OptionMapRWMutex.RUnlock()
	for key, value := range exported {
		if suffix, ok := strings.CutPrefix(key, "channel_affinity_setting."); ok {
			values[suffix] = value
		}
	}
	var snapshot operation_setting.ChannelAffinitySetting
	if err := config.UpdateConfigFromMap(&snapshot, values); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func channelAffinityTargetEligible(value channelAffinityManagedBinding, channel *model.Channel, setting *operation_setting.ChannelAffinitySetting) bool {
	if channel == nil || channel.Status != common.ChannelStatusEnabled || !value.Binding.MetadataComplete || len(value.Routes) == 0 {
		return false
	}
	if setting == nil || !setting.Enabled {
		return false
	}
	for _, route := range value.Routes {
		if route.Model == "" || len(route.Groups) == 0 {
			return false
		}
		index := slices.IndexFunc(setting.Rules, func(rule operation_setting.ChannelAffinityRule) bool { return rule.Name == route.RuleName })
		if index < 0 {
			return false
		}
		mode, _ := EffectiveSessionMode(setting, setting.Rules[index])
		if mode == "off" {
			return false
		}
		if !model.IsChannelEnabledForAnyGroupModel(route.Groups, route.Model, channel.Id) {
			return false
		}
		if !channelAffinityTargetSupportsPath(channel, route) {
			return false
		}
		if ok, _ := model.ChannelSatisfiesFilters(channel, route.Model, route.Filters); !ok {
			return false
		}
	}
	return true
}

func GetChannelAffinityTargets(refs []ChannelAffinityBindingRef) ([]ChannelAffinityTarget, error) {
	if err := validateChannelAffinityRefs(refs); err != nil {
		return nil, err
	}
	setting, err := channelAffinitySettingsSnapshot()
	if err != nil {
		return nil, err
	}
	bindings := make([]channelAffinityManagedBinding, 0, len(refs))
	for _, ref := range refs {
		value, found, err := getChannelAffinityManagementCache().Get(ref.ID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, common.NewMessage("Channel affinity binding has expired")
		}
		lock := channelAffinityLifetimeLock(value.CacheKey)
		lock.Lock()
		snapshot, err := loadChannelAffinityBinding(value.CacheKey)
		lock.Unlock()
		if err != nil {
			return nil, err
		}
		switch channelAffinityRefStatus(snapshot, ref, channelAffinityLifetimeNow().UnixMilli()) {
		case "expired":
			return nil, common.NewMessage("Channel affinity binding has expired")
		case "conflicted":
			return nil, common.NewMessage("Channel affinity binding has changed; refresh and try again")
		}
		bindings = append(bindings, snapshot.management)
	}
	if model.DB == nil {
		return nil, common.NewMessage("Channel cache is not initialized")
	}
	channels, err := model.GetAllChannels(0, -1, false, true)
	if err != nil {
		return nil, err
	}
	targets := make([]ChannelAffinityTarget, 0)
	for _, candidate := range channels {
		channel := candidate
		if common.MemoryCacheEnabled {
			channel, err = model.CacheGetChannel(candidate.Id)
			if err != nil {
				continue
			}
		}
		if slices.ContainsFunc(bindings, func(value channelAffinityManagedBinding) bool {
			return !channelAffinityTargetEligible(value, channel, setting)
		}) {
			continue
		}
		targets = append(targets, ChannelAffinityTarget{ID: channel.Id, Name: channel.Name})
	}
	slices.SortFunc(targets, func(a, b ChannelAffinityTarget) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return targets, nil
}

// All three values are compared in one Redis script. Changes to request
// metadata between target validation and the write also force revalidation.
var channelAffinityManagementWrite = redis.NewScript(`
if (redis.call('GET', KEYS[1]) or '') ~= ARGV[1] or
   (redis.call('GET', KEYS[2]) or '') ~= ARGV[2] or
   (redis.call('GET', KEYS[3]) or '') ~= ARGV[3] then return 0 end
if ARGV[12] == '1' and ((redis.call('GET', KEYS[4]) or '') ~= ARGV[9] or
   (redis.call('GET', KEYS[5]) or '') ~= ARGV[10] or
   (redis.call('GET', KEYS[6]) or '') ~= ARGV[11]) then return 0 end
if ARGV[13] ~= '' then redis.call('SET', KEYS[6], ARGV[13]) end
if ARGV[4] == '' then redis.call('DEL', KEYS[1])
else redis.call('SET', KEYS[1], ARGV[4], 'PX', ARGV[5]) end
if ARGV[6] == '' then redis.call('DEL', KEYS[2])
else redis.call('SET', KEYS[2], ARGV[6], 'PX', ARGV[8]) end
redis.call('SET', KEYS[3], ARGV[7], 'PX', ARGV[8])
return 1
`)

func writeChannelAffinityManagedBinding(key string, snapshot channelAffinityBindingSnapshot, channelID int, ttl time.Duration, state channelAffinityLifetimeState, value channelAffinityManagedBinding, retention time.Duration, fences ...channelAffinityClearObservation) (bool, error) {
	suffix := channelAffinityLifetimeSuffix(key)
	fence := channelAffinityClearObservation{BindingKey: channelAffinityBindingFenceKey(key)}
	checkFence := 0
	if len(fences) > 0 {
		fence, checkFence = fences[0], 1
	}
	nextFence := ""
	if state.ManualEpoch != snapshot.lifetime.ManualEpoch {
		nextFence = state.ManualEpoch
	}
	if common.RedisEnabled && common.RDB != nil {
		lifetimeRaw := ""
		var err error
		if state.Epoch != "" {
			lifetimeRaw, err = (channelAffinityLifetimeCodec{}).Encode(state)
			if err != nil {
				return false, err
			}
		}
		managementRaw, err := (channelAffinityManagementCodec{}).Encode(value)
		if err != nil {
			return false, err
		}
		bindingRaw := ""
		if channelID > 0 {
			bindingRaw, err = (cachex.IntCodec{}).Encode(channelID)
			if err != nil {
				return false, err
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		keys := []string{
			getChannelAffinityCache().FullKey(key), getChannelAffinityLifetimeCache().FullKey(suffix), getChannelAffinityManagementCache().FullKey(value.Binding.ID),
			channelAffinityClearFenceCache.FullKey("all"), channelAffinityClearFenceCache.FullKey("rule:" + fence.RuleName), channelAffinityClearFenceCache.FullKey(fence.BindingKey),
		}
		args := []any{snapshot.bindingRaw, snapshot.lifetimeRaw, snapshot.managementRaw, bindingRaw, max(1, ttl.Milliseconds()), lifetimeRaw, managementRaw, retention.Milliseconds(), fence.All, fence.Rule, fence.Binding, checkFence, nextFence}
		updated, err := channelAffinityManagementWrite.Run(ctx, common.RDB, keys, args...).Int()
		return updated == 1, err
	}
	channelAffinityClearFenceLock.Lock()
	defer channelAffinityClearFenceLock.Unlock()
	if checkFence == 1 {
		current, err := loadChannelAffinityMemoryClearObservation(fence)
		if err != nil {
			return false, err
		}
		if current != fence {
			return false, nil
		}
	}
	if nextFence != "" {
		if err := channelAffinityClearFenceCache.SetWithTTL(fence.BindingKey, nextFence, 0); err != nil {
			return false, err
		}
	}
	if state.Epoch != "" {
		if err := getChannelAffinityLifetimeCache().SetWithTTL(suffix, state, retention); err != nil {
			return false, err
		}
	}
	if err := getChannelAffinityManagementCache().SetWithTTL(value.Binding.ID, value, retention); err != nil {
		return false, err
	}
	if channelID > 0 {
		return true, getChannelAffinityCache().SetWithTTL(key, channelID, ttl)
	}
	_, err := getChannelAffinityCache().DeleteMany([]string{key})
	return err == nil, err
}

func MutateChannelAffinityBindings(action string, refs []ChannelAffinityBindingRef, targetChannelID int) (ChannelAffinityBindingMutationResult, error) {
	result := ChannelAffinityBindingMutationResult{Items: []ChannelAffinityBindingMutationItem{}}
	if action != "reset" && action != "migrate" {
		return result, common.NewMessage("Unknown channel affinity action")
	}
	if err := validateChannelAffinityRefs(refs); err != nil {
		return result, err
	}
	if action == "migrate" && targetChannelID <= 0 || action == "reset" && targetChannelID != 0 {
		return result, common.NewMessage("Invalid channel affinity target")
	}
	setting, err := channelAffinitySettingsSnapshot()
	if err != nil {
		return result, err
	}
	for _, ref := range refs {
		item := ChannelAffinityBindingMutationItem{ID: ref.ID}
		status, err := mutateChannelAffinityBinding(action, ref, targetChannelID, setting)
		if err != nil {
			status = "failed"
		}
		item.Status = status
		switch status {
		case "succeeded":
			result.Succeeded++
		case "expired":
			result.Expired++
			item.Message = "Channel affinity binding has expired"
		case "conflicted":
			result.Conflicted++
			item.Message = "Channel affinity binding has changed; refresh and try again"
		default:
			result.Failed++
			item.Message = "Channel affinity operation failed or target is not eligible"
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func mutateChannelAffinityBinding(action string, ref ChannelAffinityBindingRef, targetChannelID int, setting *operation_setting.ChannelAffinitySetting) (string, error) {
	value, found, err := getChannelAffinityManagementCache().Get(ref.ID)
	if err != nil {
		return "failed", err
	}
	if !found {
		return "expired", nil
	}
	lock := channelAffinityLifetimeLock(value.CacheKey)
	lock.Lock()
	defer lock.Unlock()
	for range 8 {
		snapshot, err := loadChannelAffinityBinding(value.CacheKey)
		if err != nil {
			return "failed", err
		}
		now := channelAffinityLifetimeNow().UnixMilli()
		if status := channelAffinityRefStatus(snapshot, ref, now); status != "" {
			return status, nil
		}
		value = snapshot.management
		ttlSeconds := setting.DefaultTTLSeconds
		if ttlSeconds <= 0 {
			ttlSeconds = 3600
		}
		state := channelAffinityLifetimeState{ChannelID: ref.ChannelID, Epoch: common.GetUUID(), ManualEpoch: common.GetUUID(), BoundAt: now}
		channelID := 0
		ttl := time.Duration(ttlSeconds) * time.Second
		if action == "migrate" {
			if model.DB == nil && !common.MemoryCacheEnabled {
				return "failed", nil
			}
			channel, err := model.CacheGetChannel(targetChannelID)
			if err != nil || !channelAffinityTargetEligible(value, channel, setting) {
				return "failed", nil
			}
			channelID, state.ChannelID = targetChannelID, targetChannelID
			idleSeconds, residenceSeconds := 0, 0
			ttlSeconds = 0
			for _, route := range value.Routes {
				index := slices.IndexFunc(setting.Rules, func(rule operation_setting.ChannelAffinityRule) bool { return rule.Name == route.RuleName })
				if index < 0 {
					return "failed", nil
				}
				rule := setting.Rules[index]
				ruleTTL := rule.TTLSeconds
				if ruleTTL <= 0 {
					ruleTTL = setting.DefaultTTLSeconds
				}
				if ruleTTL <= 0 {
					ruleTTL = 3600
				}
				if ttlSeconds == 0 || ruleTTL < ttlSeconds {
					ttlSeconds = ruleTTL
				}
				for _, policy := range rule.ChannelLifetimes {
					if policy.ChannelID != targetChannelID {
						continue
					}
					if idleSeconds == 0 || policy.IdleTTLSeconds < idleSeconds {
						idleSeconds = policy.IdleTTLSeconds
					}
					if residenceSeconds == 0 || policy.MaxResidenceSeconds < residenceSeconds {
						residenceSeconds = policy.MaxResidenceSeconds
					}
				}
			}
			ttl = time.Duration(ttlSeconds) * time.Second
			if idleSeconds > 0 && residenceSeconds > 0 {
				state.IdleTTLSeconds, state.AbsoluteExpiresAt = idleSeconds, now+int64(residenceSeconds)*1000
				state.IdleExpiresAt = min(now+int64(idleSeconds)*1000, state.AbsoluteExpiresAt)
				ttl = time.Duration(state.IdleExpiresAt-now) * time.Millisecond
			}
		}
		value.Binding.Version, value.Binding.ChannelID = common.GetUUID(), state.ChannelID
		value.ResidenceEpoch = common.GetUUID()
		value.TTLSeconds = ttlSeconds
		value.Active = action == "migrate"
		value.ExpiresAtMillis = 0
		if value.Active {
			value.ExpiresAtMillis = now + ttl.Milliseconds()
		}
		value.Binding.ExpiresAt = value.ExpiresAtMillis / 1000
		retention := max(24*time.Hour, time.Duration(ttlSeconds)*time.Second) + time.Duration(max(0, state.AbsoluteExpiresAt-now))*time.Millisecond
		updated, err := writeChannelAffinityManagedBinding(value.CacheKey, snapshot, channelID, ttl, state, value, retention)
		if err != nil {
			return "failed", err
		}
		if updated {
			return "succeeded", nil
		}
	}
	return "conflicted", nil
}
