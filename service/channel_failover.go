package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

const (
	channelFailoverContextKey = "channel_failover_request"
	channelFailoverMaxEntries = 1024
	channelFailoverWorkers    = 4
	channelFailoverTimeout    = 2 * time.Second
	channelFailoverIdleTTL    = 5 * time.Minute
)

// ApplyChannelFailoverProbeHeaders is wired to the relay's shared header
// resolver at startup. Background checks have no client headers to forward.
var ApplyChannelFailoverProbeHeaders func(*relaycommon.RelayInfo, *http.Request) error

type channelAvailability struct {
	Object            string `json:"object"`
	Model             string `json:"model"`
	State             string `json:"state"`
	Reason            string `json:"reason"`
	CheckedAt         int64  `json:"checked_at"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
}

// Only the digest identifies credentials/configuration in cache keys. Neither
// keys nor upstream response text enter policy events or logs.
type channelFailoverKey struct {
	ChannelID         int
	FallbackChannelID int
	Version           [32]byte
	RequestModel      string
	Model             string
	Tier              string
	Transport         string
	Compact           bool
}

type channelFailoverEntry struct {
	key         channelFailoverKey
	channel     *model.Channel
	setting     operation_setting.ChannelFailoverSetting
	lastUsed    time.Time
	lastChecked time.Time
	lastValid   time.Time
	nextCheck   time.Time
	state       string
	recovering  bool
	readyCount  int
	revision    uint64
	inflight    bool
}

type channelFailoverRuntime struct {
	mu      sync.Mutex
	entries map[channelFailoverKey]*channelFailoverEntry
	jobs    chan *channelFailoverEntry
	running bool
}

var channelFailover = newChannelFailoverRuntime()

func newChannelFailoverRuntime() *channelFailoverRuntime {
	return &channelFailoverRuntime{entries: make(map[channelFailoverKey]*channelFailoverEntry), jobs: make(chan *channelFailoverEntry, channelFailoverWorkers)}
}

// RunChannelFailoverRuntime owns a bounded scheduler and worker pool until ctx
// ends. Requests only register/read keys and enqueue work; they never probe.
func RunChannelFailoverRuntime(ctx context.Context) {
	channelFailover.run(ctx)
}

func (r *channelFailoverRuntime) run(ctx context.Context) {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return
	}
	r.running = true
	r.mu.Unlock()
	var workers sync.WaitGroup
	for range channelFailoverWorkers {
		workers.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case entry := <-r.jobs:
					checkContext, cancelCheck := context.WithTimeout(ctx, channelFailoverTimeout)
					setting := model.CurrentRequestPolicy().Failover
					fallbackID := channelFailoverBackup(setting, entry.key.RequestModel)
					var current *model.Channel
					var err error
					if common.MemoryCacheEnabled {
						current, err = model.CacheGetChannel(entry.key.ChannelID)
					} else {
						current = &model.Channel{}
						err = model.DB.WithContext(checkContext).First(current, entry.key.ChannelID).Error
					}
					if err != nil || current == nil || fallbackID == 0 || setting.PrimaryChannelID != entry.key.ChannelID || current.Status != common.ChannelStatusEnabled || channelFailoverConfigVersion(current, setting, fallbackID) != entry.key.Version {
						cancelCheck()
						r.mu.Lock()
						if r.entries[entry.key] == entry {
							delete(r.entries, entry.key)
						}
						r.mu.Unlock()
						continue
					}
					r.mu.Lock()
					revision := entry.revision
					r.mu.Unlock()
					result := probeChannelAvailability(checkContext, entry)
					cancelCheck()
					r.finish(entry, revision, result, time.Now())
				}
			}
		})
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	defer func() {
		workers.Wait()
		r.mu.Lock()
		r.running = false
		for _, entry := range r.entries {
			entry.inflight = false
		}
		for len(r.jobs) > 0 {
			<-r.jobs
		}
		r.mu.Unlock()
	}()
	var previousPolicy *model.RequestPolicySnapshot
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			policy := model.CurrentRequestPolicy()
			setting := policy.Failover
			r.mu.Lock()
			for key, entry := range r.entries {
				fallbackID := channelFailoverBackup(setting, key.RequestModel)
				// Immutable snapshots let unchanged policies skip configuration hashing.
				if fallbackID == 0 || now.Sub(entry.lastUsed) > channelFailoverIdleTTL || setting.PrimaryChannelID != key.ChannelID || policy != previousPolicy && channelFailoverConfigVersion(entry.channel, setting, fallbackID) != key.Version {
					delete(r.entries, key)
					continue
				}
				r.enqueue(entry, now)
			}
			r.mu.Unlock()
			previousPolicy = policy
		}
	}
}

// Caller holds r.mu. inflight provides per-key singleflight, including queued
// jobs; a full queue leaves the entry eligible for a later scheduler tick.
func (r *channelFailoverRuntime) enqueue(entry *channelFailoverEntry, now time.Time) {
	if !r.running || entry.inflight || now.Before(entry.nextCheck) {
		return
	}
	select {
	case r.jobs <- entry:
		entry.inflight = true
	default:
	}
}

func makeChannelFailoverKey(c *gin.Context, channel *model.Channel, modelName string, setting operation_setting.ChannelFailoverSetting) channelFailoverKey {
	tier := "default"
	transport := "http"
	for _, filter := range GetChannelConstraints(c).Filters {
		if filter.Kind == dto.FilterServiceTier {
			if filter.ServiceTier != "" {
				tier = filter.ServiceTier
			}
			if filter.OpenAITierPolicy == model.OpenAITierPolicyPriority || filter.OpenAITierPolicy == model.OpenAITierPolicyUltrafast {
				tier = filter.OpenAITierPolicy
			}
		}
		if filter.Kind == dto.FilterResponsesWebSocket {
			transport = "websocket"
		}
	}
	compact := c.Request != nil && c.Request.URL != nil && strings.HasSuffix(c.Request.URL.Path, "/responses/compact")
	var override struct {
		ServiceTier string `json:"service_tier"`
	}
	if channel.ParamOverride != nil && common.UnmarshalJsonStr(*channel.ParamOverride, &override) == nil && override.ServiceTier != "" {
		tier = override.ServiceTier
	}
	fallbackID := channelFailoverBackup(setting, modelName)
	return channelFailoverKey{ChannelID: channel.Id, FallbackChannelID: fallbackID, Version: channelFailoverConfigVersion(channel, setting, fallbackID), RequestModel: modelName, Model: channel.ResolveServiceTierModel(modelName), Tier: tier, Transport: transport, Compact: compact}
}

func channelFailoverConfigVersion(channel *model.Channel, setting operation_setting.ChannelFailoverSetting, fallbackID int) [32]byte {
	// Include every declarative routing/credential field, but exclude mutable
	// polling indexes and counters. A changed key, URL, mapping, proxy, header
	// override, or policy starts with a fresh unknown availability state.
	config, _ := common.Marshal([]any{channel.Type, channel.Key, channel.BaseURL, channel.ModelMapping,
		channel.Setting, channel.OtherSettings, channel.ParamOverride, channel.HeaderOverride,
		channel.OpenAIOrganization, channel.ChannelInfo.IsMultiKey, channel.ChannelInfo.MultiKeyStatusList, setting, fallbackID})
	return sha256.Sum256(config)
}

func (r *channelFailoverRuntime) observe(key channelFailoverKey, channel *model.Channel, setting operation_setting.ChannelFailoverSetting, now time.Time) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[key]
	if entry == nil {
		if len(r.entries) >= channelFailoverMaxEntries {
			var oldestKey channelFailoverKey
			var oldest time.Time
			for existingKey, existing := range r.entries {
				if oldest.IsZero() || existing.lastUsed.Before(oldest) {
					oldestKey, oldest = existingKey, existing.lastUsed
				}
			}
			delete(r.entries, oldestKey)
		}
		entry = &channelFailoverEntry{key: key, channel: channel, setting: setting, state: "unknown"}
		r.entries[key] = entry
	}
	entry.lastUsed = now
	r.enqueue(entry, now)
	return entry.availability(now)
}

func (entry *channelFailoverEntry) availability(now time.Time) string {
	maxAge := max(3*time.Duration(entry.setting.PollIntervalSeconds)*time.Second, 10*time.Second)
	if entry.lastValid.IsZero() || now.Sub(entry.lastValid) > maxAge {
		return "unknown"
	}
	if entry.recovering && (entry.state == "ready" || entry.state == "unknown") {
		return "recovering"
	}
	return entry.state
}

func (r *channelFailoverRuntime) finish(entry *channelFailoverEntry, revision uint64, result channelAvailability, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries[entry.key] != entry {
		return
	}
	entry.inflight = false
	entry.lastChecked = now
	entry.nextCheck = now.Add(time.Duration(entry.setting.PollIntervalSeconds) * time.Second)
	// A request can report capacity exhaustion while an older probe is in
	// flight. That stale ready result cannot undo the request's newer signal.
	if revision != entry.revision {
		return
	}
	entry.state = result.State
	if result.State != "unknown" {
		entry.lastValid = now
	}
	switch result.State {
	case "ready":
		entry.readyCount++
		if entry.readyCount >= entry.setting.RecoverySuccesses {
			entry.recovering = false
		}
	case "empty":
		entry.recovering = true
		entry.readyCount = 0
	default:
		entry.readyCount = 0
	}
}

func probeChannelAvailability(parent context.Context, entry *channelFailoverEntry) channelAvailability {
	unknown := channelAvailability{State: "unknown"}
	ctx, cancel := context.WithTimeout(parent, channelFailoverTimeout)
	defer cancel()
	channel := entry.channel
	baseURL := channel.GetBaseURL()
	if baseURL == "" || channel.HeaderOverride != nil && strings.Contains(*channel.HeaderOverride, "{client_header:") {
		return unknown
	}
	// Keep the same relay URL construction rules, including the explicit
	// trailing-slash convention. An operator's /v1 suffix is not rewritten.
	rawURL := relaycommon.GetFullRequestURL(baseURL, "/v1/routing/availability", channel.Type)
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return unknown
	}
	query := u.Query()
	query.Set("model", entry.key.Model)
	query.Set("service_tier", entry.key.Tier)
	query.Set("transport", entry.key.Transport)
	query.Set("require_compact", strconv.FormatBool(entry.key.Compact))
	u.RawQuery = query.Encode()
	settings := channel.GetSetting()
	client, err := GetHttpClientWithProxySettings(settings.Proxy, settings)
	if err != nil || client == nil {
		return unknown
	}
	// Never forward the channel credential to a redirect target, even on the
	// same host. Copy the shared client instead of changing its policy.
	probeClient := *client
	probeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	keys := []string{channel.Key}
	if channel.ChannelInfo.IsMultiKey {
		keys = nil
		for index, key := range channel.GetKeys() {
			if status, exists := channel.ChannelInfo.MultiKeyStatusList[index]; !exists || status == common.ChannelStatusEnabled {
				keys = append(keys, key)
			}
		}
	}
	if len(keys) == 0 {
		return unknown
	}
	// A multi-key channel is empty only when every enabled credential is
	// explicitly empty. Unknown credentials cannot hide a usable account pool.
	result := channelAvailability{State: "empty"}
	for _, key := range keys {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil || key == "" {
			return unknown
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Accept", "application/json")
		if channel.OpenAIOrganization != nil && *channel.OpenAIOrganization != "" {
			req.Header.Set("OpenAI-Organization", *channel.OpenAIOrganization)
		}
		info := &relaycommon.RelayInfo{IsChannelTest: true, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel.Id, ChannelType: channel.Type, ApiKey: key, HeadersOverride: channel.GetHeaderOverride(), ChannelSetting: settings}}
		if ApplyChannelFailoverProbeHeaders != nil {
			if err := ApplyChannelFailoverProbeHeaders(info, req); err != nil {
				return unknown
			}
		} else if len(info.HeadersOverride) != 0 {
			return unknown
		}
		response, err := probeClient.Do(req)
		if err != nil {
			return unknown
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 16*1024+1))
		_ = response.Body.Close()
		var availability channelAvailability
		if response.StatusCode != http.StatusOK || readErr != nil || len(data) > 16*1024 || common.Unmarshal(data, &availability) != nil ||
			availability.Object != "routing.availability" || availability.Model != entry.key.Model || availability.CheckedAt <= 0 ||
			time.Since(time.Unix(availability.CheckedAt, 0)) > 10*time.Second || time.Until(time.Unix(availability.CheckedAt, 0)) > 5*time.Second {
			return unknown
		}
		switch availability.State {
		case "ready":
			return availability
		case "busy":
			result = availability
		case "empty":
		default:
			return unknown
		}
	}
	return result
}

type channelFailoverRequest struct {
	key       channelFailoverKey
	setting   operation_setting.ChannelFailoverSetting
	modelName string
	group     string
	order     []int
	visited   map[int]bool
	current   int
	state     string
}

func requestChannelFailover(c *gin.Context) *channelFailoverRequest {
	if value, exists := c.Get(channelFailoverContextKey); exists {
		state, _ := value.(*channelFailoverRequest)
		return state
	}
	return nil
}

// The global filter gates every rule. The first matching rule replaces the
// default backup, while invalid pairs leave normal channel selection in charge.
func channelFailoverBackup(setting operation_setting.ChannelFailoverSetting, modelName string) int {
	if !setting.Enabled || setting.PrimaryChannelID <= 0 || len(setting.ModelRegex) > 0 && !matchAnyRegexCached(setting.ModelRegex, modelName) {
		return 0
	}
	fallbackID := setting.FallbackChannelID
	for _, rule := range setting.FallbackRules {
		if matchAnyRegexCached(rule.ModelRegex, modelName) {
			fallbackID = rule.ChannelID
			break
		}
	}
	if fallbackID <= 0 || fallbackID == setting.PrimaryChannelID {
		return 0
	}
	return fallbackID
}

// Sessions on any configured backup can return to the primary after a model
// rule changes. Channels outside the configured pool retain their affinity.
func isChannelFailoverMember(setting operation_setting.ChannelFailoverSetting, channelID int) bool {
	if channelID == setting.PrimaryChannelID || channelID == setting.FallbackChannelID {
		return true
	}
	for _, rule := range setting.FallbackRules {
		if channelID == rule.ChannelID {
			return true
		}
	}
	return false
}

// prepareChannelFailover runs after pins and affinity lookup. Existing sessions
// bound to a third channel keep their session rules and fallback chains.
func prepareChannelFailover(c *gin.Context, modelName, usingGroup string, preferredID int) *channelFailoverRequest {
	if state := requestChannelFailover(c); state != nil {
		return state
	}
	setting := model.CurrentRequestPolicy().Failover
	fallbackID := channelFailoverBackup(setting, modelName)
	if fallbackID == 0 || preferredID != 0 && !isChannelFailoverMember(setting, preferredID) {
		return nil
	}
	primary, err := model.CacheGetChannel(setting.PrimaryChannelID)
	if err != nil || primary == nil || primary.Status != common.ChannelStatusEnabled ||
		(primary.Type != constant.ChannelTypeOpenAI && primary.Type != constant.ChannelTypeSub2API) {
		return nil
	}
	if c.Request == nil || c.Request.URL == nil {
		return nil
	}
	switch c.Request.URL.Path {
	case "/v1/responses", "/v1/responses/compact":
	case "/v1/chat/completions", "/pg/chat/completions":
		if model_setting.GetGlobalSettings().PassThroughRequestEnabled || primary.GetSetting().PassThroughBodyEnabled ||
			!ShouldChatCompletionsUseResponsesGlobal(primary.Id, primary.Type, modelName) {
			return nil
		}
	default:
		// The availability contract describes native Responses scheduling.
		// Other endpoints retain their own provider capabilities and routing.
		return nil
	}
	if ok, _ := model.ChannelSatisfiesFilters(primary, modelName, GetChannelConstraints(c).Filters); !ok {
		return nil
	}
	groups := []string{usingGroup}
	if usingGroup == "auto" {
		groups = GetRequestAutoGroups(c, common.GetContextKeyString(c, constant.ContextKeyUserGroup))
	}
	group := ""
	for _, candidate := range groups {
		if model.IsChannelEnabledForGroupModel(candidate, modelName, primary.Id) {
			group = candidate
			break
		}
	}
	if group == "" {
		return nil
	}
	key := makeChannelFailoverKey(c, primary, modelName, setting)
	availability := channelFailover.observe(key, primary, setting, time.Now())
	order := []int{setting.PrimaryChannelID, fallbackID}
	if availability == "empty" || availability == "busy" || availability == "recovering" {
		if fallback := eligibleFailoverChannel(c, fallbackID, group, modelName); fallback != nil {
			order = []int{fallbackID}
		}
	}
	state := &channelFailoverRequest{key: key, setting: setting, modelName: modelName, group: group, order: order, visited: make(map[int]bool), state: availability}
	c.Set(channelFailoverContextKey, state)
	return state
}

func eligibleFailoverChannel(c *gin.Context, channelID int, group, modelName string) *model.Channel {
	channel, err := model.CacheGetChannel(channelID)
	if err != nil || channel == nil || channel.Status != common.ChannelStatusEnabled || !model.IsChannelEnabledForGroupModel(group, modelName, channelID) {
		return nil
	}
	if matches, _ := model.ChannelSatisfiesFilters(channel, modelName, GetChannelConstraints(c).Filters); !matches {
		return nil
	}
	return channel
}

func selectChannelFailover(c *gin.Context, state *channelFailoverRequest) (*model.Channel, string, error) {
	ReleaseChannelConcurrency(c)
	var selectionErr error
	for _, channelID := range state.order {
		if state.visited[channelID] {
			continue
		}
		state.visited[channelID] = true
		channel := eligibleFailoverChannel(c, channelID, state.group, state.modelName)
		if channel == nil {
			continue
		}
		if err := acquireChannelConcurrency(c, channel); err != nil {
			selectionErr = err
			if errors.Is(err, errChannelConcurrencyLimited) {
				continue
			}
			return nil, state.group, err
		}
		state.current = channelID
		common.SetContextKey(c, constant.ContextKeyAutoGroup, state.group)
		MarkChannelAffinityUsed(c, state.group, channelID)
		reason := "primary_" + state.state
		if channelID == state.key.FallbackChannelID {
			reason = "backup_" + state.state
			if state.visited[state.setting.PrimaryChannelID] {
				reason = "backup_after_primary_failure"
			}
		}
		RequestPolicy(c).AddEvent(PolicyEvent{ChannelID: channelID, Group: state.group, Decision: PolicyDecision{Action: "select", Reason: reason, Source: "channel_failover"}})
		return channel, state.group, nil
	}
	if selectionErr == nil {
		selectionErr = errors.New("channel_failover_exhausted")
	}
	return nil, state.group, selectionErr
}

func channelCapacityExhausted(err *types.NewAPIError) bool {
	if err == nil || err.GetErrorType() == types.ErrorTypeNewAPIError {
		return false
	}
	upstream := err.ToOpenAIError()
	code, _ := upstream.Code.(string)
	switch code {
	case "model_capacity_exhausted", "no_available_accounts", "no_available_account", "no_schedulable_accounts":
		return true
	default:
		return false
	}
}

// Capacity is a pool scheduling signal, not a failed credential. Keeping the
// primary enabled lets availability checks observe its recovery even when a
// broad global automatic-disable rule includes 429 or 503.
func channelFailoverTransientCapacity(c *gin.Context, channelID int, err *types.NewAPIError) bool {
	state := requestChannelFailover(c)
	return state != nil && state.setting.PrimaryChannelID == channelID && channelCapacityExhausted(err)
}

func channelFailoverRetry(c *gin.Context, err *types.NewAPIError, retryTimes int) (PolicyDecision, bool) {
	state := requestChannelFailover(c)
	if state == nil {
		return PolicyDecision{}, false
	}
	decision := PolicyDecision{Action: "stop", Source: "channel_failover"}
	capacity := channelCapacityExhausted(err)
	if capacity && state.current == state.setting.PrimaryChannelID {
		channelFailover.mu.Lock()
		if entry := channelFailover.entries[state.key]; entry != nil && !entry.channel.ChannelInfo.IsMultiKey {
			entry.revision++
			entry.state, entry.readyCount, entry.recovering = "empty", 0, true
			entry.lastValid = time.Now()
		}
		channelFailover.mu.Unlock()
		RequestPolicy(c).AddEvent(PolicyEvent{ChannelID: state.current, Decision: PolicyDecision{Action: "observe", Reason: "primary_capacity_exhausted", Source: "channel_failover"}})
	}
	// Any emitted output is final for this attempt. The WS relay also calls
	// this decision before forwarding a rejection and never after acceptance.
	if c.Writer != nil && c.Writer.Written() {
		decision.Reason = "response_started"
		return decision, true
	}
	if c.Request != nil && c.Request.Context().Err() != nil {
		decision.Reason = "request_cancelled"
		return decision, true
	}
	if retryTimes <= 0 && len(state.visited) >= 2 {
		decision.Reason = "attempt_budget_exhausted"
		return decision, true
	}
	if types.IsSkipRetryError(err) && !capacity || operation_setting.IsAlwaysSkipRetryCode(err.GetErrorCode()) {
		decision.Reason = "non_retryable_error"
		return decision, true
	}
	next := false
	for _, channelID := range state.order {
		if !state.visited[channelID] && eligibleFailoverChannel(c, channelID, state.group, state.modelName) != nil {
			next = true
			break
		}
	}
	if !next {
		decision.Reason = "primary_backup_exhausted"
		return decision, true
	}
	if capacity {
		decision.Action, decision.Reason = "retry", "primary_capacity_exhausted"
		return decision, true
	}
	if types.IsChannelError(err) || !operation_setting.IsAlwaysSkipRetryStatusCode(err.StatusCode) && operation_setting.ShouldRetryByStatusCode(err.StatusCode) {
		decision.Action, decision.Reason = "retry", "primary_retryable_error"
		return decision, true
	}
	decision.Reason = "status_not_retryable"
	return decision, true
}

// ChannelFailoverMayReroute lets persistent Responses connections re-enter the
// shared selector between creates. Explicit pins and third-channel sessions
// keep their existing connection semantics.
func ChannelFailoverMayReroute(c *gin.Context, modelName, usingGroup string, channelID int) bool {
	if _, pinned, _ := GetChannelConstraints(c).ResolvedPin(); pinned {
		return false
	}
	setting := model.CurrentRequestPolicy().Failover
	if !isChannelFailoverMember(setting, channelID) {
		return false
	}
	preferredID, found := GetPreferredChannelByAffinity(c, modelName, usingGroup)
	if !found {
		preferredID = channelID
	}
	return prepareChannelFailover(c, modelName, usingGroup, preferredID) != nil
}

// ChannelFailoverRetryBudget gives a matched primary/backup pair its one
// failover opportunity even when the global retry setting is zero. Selection
// still visits each member at most once; unrelated requests retain the global
// budget and pins retain their own retry semantics.
func ChannelFailoverRetryBudget(c *gin.Context) int {
	if _, pinned, _ := GetChannelConstraints(c).ResolvedPin(); !pinned && requestChannelFailover(c) != nil {
		return max(common.RetryTimes, 1)
	}
	return common.RetryTimes
}

func HasChannelFailover(c *gin.Context) bool {
	return requestChannelFailover(c) != nil
}
