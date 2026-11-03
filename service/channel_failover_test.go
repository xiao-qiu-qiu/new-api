package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func channelFailoverFixture(t *testing.T, db *gorm.DB, memoryCache bool) (*model.Channel, operation_setting.ChannelFailoverSetting) {
	t.Helper()
	oldDB, oldMemory, oldRedis, oldRetries := model.DB, common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes
	oldOptions := maps.Clone(model.CurrentRequestPolicy().Options)
	oldDatabaseType := common.MainDatabaseType()
	oldLogDB, oldLogType, oldMaster := model.LOG_DB, common.LogDatabaseType(), common.IsMasterNode
	common.IsMasterNode = false
	common.SetMainDatabaseType(common.DatabaseType(db.Dialector.Name()))
	common.OptionMapRWMutex.Lock()
	oldOptionMap := maps.Clone(common.OptionMap)
	common.OptionMap = maps.Clone(oldOptions)
	common.OptionMapRWMutex.Unlock()
	oldRuntime := channelFailover
	model.DB, common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes = db, memoryCache, false, 0
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitLogDB())
	channelFailover = newChannelFailoverRuntime()
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.Option{}))
	for _, id := range []int{7141, 15, 7143} {
		createChannelSelectAutoGroupsChannel(t, db, id, "default", "alias")
	}
	require.NoError(t, model.UpdateRequestPolicyOptions(map[string]string{
		"channel_failover_setting.enabled": "true", "channel_failover_setting.primary_channel_id": "7141", "channel_failover_setting.fallback_channel_id": "15",
		"channel_failover_setting.model_regex": "[]", "channel_failover_setting.poll_interval_seconds": "3", "channel_failover_setting.recovery_successes": "2",
		"channel_affinity_setting.enabled": "false", "RetryTimes": "0",
	}))
	setting := model.CurrentRequestPolicy().Failover
	model.InitChannelCache()
	primary, err := model.CacheGetChannel(setting.PrimaryChannelID)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, model.UpdateRequestPolicyOptions(oldOptions))
		common.OptionMapRWMutex.Lock()
		common.OptionMap = oldOptionMap
		common.OptionMapRWMutex.Unlock()
		common.SetMainDatabaseType(oldDatabaseType)
		channelFailover = oldRuntime
		model.DB, common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes = oldDB, oldMemory, oldRedis, oldRetries
		require.NoError(t, model.InitLogDB())
		model.LOG_DB, common.IsMasterNode = oldLogDB, oldMaster
		common.SetLogDatabaseType(oldLogType)
		if oldMemory && oldDB != nil && oldDB.Migrator().HasTable(&model.Channel{}) && oldDB.Migrator().HasTable(&model.Ability{}) {
			model.InitChannelCache()
		}
	})
	return primary, setting
}

func channelFailoverDatabase(t *testing.T, dialector gorm.Dialector) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(dialector, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "failover_runtime_" + strconv.FormatInt(time.Now().UnixNano(), 36) + "_"}})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	versionQuery := "SELECT version()"
	if dialector.Name() == "sqlite" {
		versionQuery = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("database %s version: %s", dialector.Name(), version)
	t.Cleanup(func() {
		require.NoError(t, db.Migrator().DropTable(&model.Option{}, &model.Ability{}, &model.Channel{}))
		sqlDB, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())
	})
	return db
}

func newChannelFailoverContext(t *testing.T) *gin.Context {
	t.Helper()
	c := concurrencySelectionContext(t)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"alias","input":[{"type":"reasoning","encrypted_content":"unchanged"},{"type":"compaction","encrypted_content":"also-unchanged"}]}`))
	return c
}

func selectChannelFailoverForTest(t *testing.T, c *gin.Context) *model.Channel {
	t.Helper()
	channel, group, err := SelectChannelForRequest(c, "alias", &RetryParam{Ctx: c, TokenGroup: "default", ModelName: "alias"})
	require.Nil(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, "default", group)
	return channel
}

func recordChannelFailoverAvailability(t *testing.T, primary *model.Channel, setting operation_setting.ChannelFailoverSetting, state string) *channelFailoverEntry {
	t.Helper()
	c := newChannelFailoverContext(t)
	key := makeChannelFailoverKey(c, primary, "alias", setting)
	channelFailover.observe(key, primary, setting, time.Now())
	entry := channelFailover.entries[key]
	channelFailover.finish(entry, entry.revision, channelAvailability{State: state}, time.Now())
	return entry
}

func TestChannelFailoverRoutingAcrossDatabases(t *testing.T) {
	engines := []struct {
		name string
		open gorm.Dialector
	}{
		{"sqlite", sqlite.Open(":memory:")},
	}
	if dsn := os.Getenv("TEST_MYSQL_DSN"); dsn != "" {
		engines = append(engines, struct {
			name string
			open gorm.Dialector
		}{"mysql", mysql.Open(dsn)})
	}
	if dsn := os.Getenv("TEST_POSTGRES_DSN"); dsn != "" {
		engines = append(engines, struct {
			name string
			open gorm.Dialector
		}{"postgres", postgres.Open(dsn)})
	}
	for _, engine := range engines {
		for _, memoryCache := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cache_%t", engine.name, memoryCache), func(t *testing.T) {
				db := channelFailoverDatabase(t, engine.open)
				primary, setting := channelFailoverFixture(t, db, memoryCache)
				assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, newChannelFailoverContext(t)).Id, "a cold/unknown cache attempts the primary without a session ID")
				entry := recordChannelFailoverAvailability(t, primary, setting, "empty")
				assert.Equal(t, 15, selectChannelFailoverForTest(t, newChannelFailoverContext(t)).Id)
				channelFailover.finish(entry, entry.revision, channelAvailability{State: "ready"}, time.Now())
				assert.Equal(t, 15, selectChannelFailoverForTest(t, newChannelFailoverContext(t)).Id, "one recovery confirmation keeps the backup")
				channelFailover.finish(entry, entry.revision, channelAvailability{State: "ready"}, time.Now())
				assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, newChannelFailoverContext(t)).Id, "two ready results immediately restore the primary")
				channelFailover.finish(entry, entry.revision, channelAvailability{State: "busy"}, time.Now())
				assert.Equal(t, 15, selectChannelFailoverForTest(t, newChannelFailoverContext(t)).Id)
				channelFailover.finish(entry, entry.revision, channelAvailability{State: "ready"}, time.Now())
				assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, newChannelFailoverContext(t)).Id, "busy never latches recovery mode")
				channelFailover.finish(entry, entry.revision, channelAvailability{State: "empty"}, time.Now())
				entry.lastValid = time.Now().Add(-time.Minute)
				assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, newChannelFailoverContext(t)).Id, "stale empty cannot lock traffic to backup")
			})
		}
	}
}

func TestChannelFailoverZeroGlobalRetryAndStructuredCapacity(t *testing.T) {
	db := channelFailoverDatabase(t, sqlite.Open(":memory:"))
	primary, setting := channelFailoverFixture(t, db, true)
	for _, tc := range []struct {
		name      string
		code      string
		status    int
		wantEmpty bool
		wantRetry bool
	}{
		{"capacity", "model_capacity_exhausted", 503, true, true},
		{"ordinary_network_error", "upstream_error", 502, false, true},
		{"ordinary_400", "invalid_request_error", 400, false, false},
		{"ordinary_401", "invalid_api_key", 401, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channelFailover = newChannelFailoverRuntime()
			c := newChannelFailoverContext(t)
			selected := selectChannelFailoverForTest(t, c)
			assert.Equal(t, primary.Id, selected.Id)
			assert.Equal(t, 1, ChannelFailoverRetryBudget(c))
			err := types.WithOpenAIError(types.OpenAIError{Code: tc.code, Type: "api_error", Message: "upstream rejected"}, tc.status)
			decision := DecideRelayRetry(c, err, 0)
			assert.Equal(t, tc.wantRetry, decision.Action == "retry")
			state := requestChannelFailover(c)
			entry := channelFailover.entries[state.key]
			assert.Equal(t, tc.wantEmpty, entry.state == "empty")
			if tc.wantRetry {
				retry := &RetryParam{Ctx: c, ModelName: "alias", TokenGroup: "default", Retry: common.GetPointer(1)}
				backup, _, selectionErr := CacheGetRandomSatisfiedChannel(retry)
				require.NoError(t, selectionErr)
				require.NotNil(t, backup)
				assert.Equal(t, 15, backup.Id)
				assert.Equal(t, "stop", DecideRelayRetry(c, err, 10).Action, "neither member is attempted twice")
			}
		})
	}
	channelFailover = newChannelFailoverRuntime()
	c := newChannelFailoverContext(t)
	selectChannelFailoverForTest(t, c)
	_, _ = c.Writer.WriteString("event: response.created\n\n")
	capacity := types.WithOpenAIError(types.OpenAIError{Code: "model_capacity_exhausted", Type: "api_error"}, 503)
	assert.Equal(t, "response_started", DecideRelayRetry(c, capacity, 1).Reason)
	key := makeChannelFailoverKey(c, primary, "alias", setting)
	assert.Equal(t, "empty", channelFailover.entries[key].state, "a real rejection is still observed without replaying output")
}

func TestChannelFailoverHonorsPinsFiltersGroupsAndUnrelatedSessions(t *testing.T) {
	db := channelFailoverDatabase(t, sqlite.Open(":memory:"))
	primary, setting := channelFailoverFixture(t, db, true)
	recordChannelFailoverAvailability(t, primary, setting, "empty")
	pinned := newChannelFailoverContext(t)
	GetChannelConstraints(pinned).AddPin(dto.ChannelPin{ChannelId: primary.Id, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt})
	channel, _, selectErr := SelectChannelForRequest(pinned, "alias", &RetryParam{Ctx: pinned, ModelName: "alias", TokenGroup: "default"})
	require.Nil(t, selectErr)
	assert.Equal(t, primary.Id, channel.Id)
	assert.Nil(t, requestChannelFailover(pinned))
	assert.Zero(t, ChannelFailoverRetryBudget(pinned))

	// A backup outside this request's selected group cannot receive overflow.
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 15).Update("group", "vip").Error)
	require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", 15).Update("group", "vip").Error)
	model.InitChannelCache()
	assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, newChannelFailoverContext(t)).Id)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 15).Update("group", "default").Error)
	require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", 15).Update("group", "default").Error)
	primary.SetOtherSettings(kitdto.ChannelOtherSettings{SupportedServiceTiers: []string{"default"}})
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", primary.Id).Update("settings", primary.OtherSettings).Error)
	model.InitChannelCache()
	filtered := newChannelFailoverContext(t)
	GetChannelConstraints(filtered).AddFilter(dto.ChannelFilter{Kind: dto.FilterResponsesWebSocket})
	assert.Nil(t, prepareChannelFailover(filtered, "alias", "default", 0), "primary must satisfy transport filters")
	assert.Nil(t, prepareChannelFailover(newChannelFailoverContext(t), "alias", "vip", 0), "primary must have the selected group's ability")
	assert.Nil(t, prepareChannelFailover(newChannelFailoverContext(t), "alias", "default", 7143), "an unrelated session is not taken over")

	require.NoError(t, model.UpdateRequestPolicyOptions(map[string]string{"channel_failover_setting.model_regex": `["^different$"]`}))
	assert.Nil(t, prepareChannelFailover(newChannelFailoverContext(t), "alias", "default", 0))
}

func TestChannelFailoverProbeContractAndIsolation(t *testing.T) {
	InitHttpClient()
	seen := make(chan *http.Request, 8)
	var state atomic.Value
	state.Store("ready")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen <- req.Clone(req.Context())
		body, _ := common.Marshal(channelAvailability{Object: "routing.availability", Model: req.URL.Query().Get("model"), State: state.Load().(string), CheckedAt: time.Now().Unix()})
		_, _ = w.Write(body)
	}))
	defer server.Close()
	setting := operation_setting.ChannelFailoverSetting{Enabled: true, PrimaryChannelID: 1, FallbackChannelID: 15, PollIntervalSeconds: 3, RecoverySuccesses: 2}
	primary := &model.Channel{Id: 1, Type: constant.ChannelTypeOpenAI, Key: "probe-secret", BaseURL: &server.URL, ModelMapping: common.GetPointer(`{"alias":"middle","middle":"gpt-test"}`)}
	c := newChannelFailoverContext(t)
	SetRequestServiceTierFilter(c, "alias", "priority")
	GetChannelConstraints(c).AddFilter(dto.ChannelFilter{Kind: dto.FilterResponsesWebSocket})
	c.Request.URL.Path = "/v1/responses/compact"
	key := makeChannelFailoverKey(c, primary, "alias", setting)
	entry := &channelFailoverEntry{key: key, channel: primary, setting: setting}
	result := probeChannelAvailability(context.Background(), entry)
	assert.Equal(t, "ready", result.State)
	request := <-seen
	assert.Equal(t, "/v1/routing/availability", request.URL.Path)
	assert.Equal(t, http.MethodGet, request.Method)
	assert.Equal(t, "Bearer probe-secret", request.Header.Get("Authorization"))
	assert.Equal(t, "gpt-test", request.URL.Query().Get("model"))
	assert.Equal(t, "priority", request.URL.Query().Get("service_tier"))
	assert.Equal(t, "websocket", request.URL.Query().Get("transport"))
	assert.Equal(t, "true", request.URL.Query().Get("require_compact"))
	assert.Zero(t, request.ContentLength)
	primary.Key = "rotated-secret"
	assert.NotEqual(t, key, makeChannelFailoverKey(c, primary, "alias", setting))
	primary.Key = "probe-secret"
	primary.SetSetting(kitdto.ChannelSettings{Proxy: "http://127.0.0.1:1"})
	assert.NotEqual(t, key, makeChannelFailoverKey(c, primary, "alias", setting))
	primary.Setting = nil
	SetRequestServiceTierFilter(c, "alias", "default")
	assert.NotEqual(t, key, makeChannelFailoverKey(c, primary, "alias", setting))

	// Authenticated checks use the shared proxy transport and preserve its URL
	// path rules; they do not invent a host from a channel name or model.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen <- req.Clone(req.Context())
		_, _ = w.Write([]byte(`{"object":"routing.availability","model":"gpt-test","state":"empty","checked_at":` + strconv.FormatInt(time.Now().Unix(), 10) + `}`))
	}))
	defer proxy.Close()
	primary.BaseURL = common.GetPointer("http://sub2api.invalid/prefix")
	primary.SetSetting(kitdto.ChannelSettings{Proxy: proxy.URL})
	assert.Equal(t, "empty", probeChannelAvailability(context.Background(), entry).State)
	assert.Equal(t, "/prefix/v1/routing/availability", (<-seen).URL.Path)

	var redirected atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { redirected.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	primary.BaseURL, primary.Setting = &redirect.URL, nil
	assert.Equal(t, "unknown", probeChannelAvailability(context.Background(), entry).State)
	assert.Zero(t, redirected.Load(), "credentials never follow a redirect")
	primary.BaseURL = nil
	assert.Equal(t, "unknown", probeChannelAvailability(context.Background(), entry).State)
}

func TestChannelFailoverCacheSingleflightAndStaleProbe(t *testing.T) {
	runtime := newChannelFailoverRuntime()
	runtime.running = true
	setting := operation_setting.ChannelFailoverSetting{PollIntervalSeconds: 3, RecoverySuccesses: 2}
	primary := &model.Channel{Id: 1, Key: "secret"}
	key := channelFailoverKey{ChannelID: 1, Model: "model"}
	now := time.Now()
	assert.Equal(t, "unknown", runtime.observe(key, primary, setting, now))
	assert.Equal(t, "unknown", runtime.observe(key, primary, setting, now))
	assert.Len(t, runtime.jobs, 1, "a hot key queues one background check")
	entry := <-runtime.jobs
	entry.revision++
	entry.state, entry.recovering, entry.lastValid = "empty", true, now
	runtime.finish(entry, 0, channelAvailability{State: "ready"}, now)
	assert.Equal(t, "empty", entry.availability(now), "an older probe cannot override a real request rejection")
	for index := range channelFailoverMaxEntries + 1 {
		runtime.observe(channelFailoverKey{ChannelID: 1, Model: strconv.Itoa(index)}, primary, setting, now.Add(time.Duration(index)*time.Nanosecond))
	}
	assert.Len(t, runtime.entries, channelFailoverMaxEntries)
	assert.NotContains(t, runtime.entries, key, "cache capacity evicts the least recently used key")
	assert.False(t, channelCapacityExhausted(types.NewOpenAIError(errors.New("model_capacity_exhausted in free text"), types.ErrorCodeBadResponse, 503)), "free text never marks account exhaustion")
}

func TestChannelFailoverMultiKeyRequestDoesNotMarkWholePoolEmpty(t *testing.T) {
	db := channelFailoverDatabase(t, sqlite.Open(":memory:"))
	primary, _ := channelFailoverFixture(t, db, true)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", primary.Id).Updates(map[string]any{
		"key": "first-key\nsecond-key", "channel_info": model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2},
	}).Error)
	model.InitChannelCache()
	c := newChannelFailoverContext(t)
	selectChannelFailoverForTest(t, c)
	err := types.WithOpenAIError(types.OpenAIError{Code: "model_capacity_exhausted", Type: "api_error"}, 503)
	assert.Equal(t, "retry", DecideRelayRetry(c, err, 0).Action)
	state := requestChannelFailover(c)
	assert.Equal(t, "unknown", channelFailover.entries[state.key].state, "one key's empty pool cannot poison another key's group")
	assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, newChannelFailoverContext(t)).Id)

	InitHttpClient()
	var secondState atomic.Value
	secondState.Store("ready")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		state := "empty"
		if req.Header.Get("Authorization") == "Bearer second-key" {
			state = secondState.Load().(string)
		}
		body, _ := common.Marshal(channelAvailability{Object: "routing.availability", Model: "alias", State: state, CheckedAt: time.Now().Unix()})
		_, _ = w.Write(body)
	}))
	defer server.Close()
	entry := channelFailover.entries[requestChannelFailover(c).key]
	copyChannel := *entry.channel
	copyChannel.BaseURL = &server.URL
	entry.channel = &copyChannel
	assert.Equal(t, "ready", probeChannelAvailability(context.Background(), entry).State)
	secondState.Store("empty")
	assert.Equal(t, "empty", probeChannelAvailability(context.Background(), entry).State, "only all enabled credentials being empty marks the channel empty")
}

func TestChannelFailoverWorkerDropsRetiredCredentialsAndStops(t *testing.T) {
	InitHttpClient()
	db := channelFailoverDatabase(t, sqlite.Open(":memory:"))
	primary, setting := channelFailoverFixture(t, db, true)
	var retiredCalls atomic.Int32
	retired := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { retiredCalls.Add(1) }))
	defer retired.Close()
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", primary.Id).Update("base_url", retired.URL).Error)
	model.InitChannelCache()
	primary, err := model.CacheGetChannel(primary.Id)
	require.NoError(t, err)
	c := newChannelFailoverContext(t)
	oldKey := makeChannelFailoverKey(c, primary, "alias", setting)
	runtime := channelFailover
	runtime.running = true
	runtime.observe(oldKey, primary, setting, time.Now())

	seen := make(chan string, 1)
	current := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen <- req.Header.Get("Authorization")
		body, _ := common.Marshal(channelAvailability{Object: "routing.availability", Model: "alias", State: "ready", CheckedAt: time.Now().Unix()})
		_, _ = w.Write(body)
	}))
	defer current.Close()
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", primary.Id).Updates(map[string]any{"base_url": current.URL, "key": "current-secret"}).Error)
	model.InitChannelCache()
	primary, err = model.CacheGetChannel(primary.Id)
	require.NoError(t, err)
	newKey := makeChannelFailoverKey(c, primary, "alias", setting)
	runtime.observe(newKey, primary, setting, time.Now())
	runtime.running = false
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); runtime.run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("failover worker pool did not stop")
		}
	})
	select {
	case authorization := <-seen:
		assert.Equal(t, "Bearer current-secret", authorization)
	case <-time.After(5 * time.Second):
		t.Fatal("current configuration was not checked")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("failover worker pool did not stop")
	}
	assert.Zero(t, retiredCalls.Load(), "queued old work cannot send revoked credentials to the retired URL")
	assert.NotContains(t, runtime.entries, oldKey)
	assert.False(t, runtime.running)
}

func TestChannelFailoverOverridesOnlyPrimaryBackupSessionRules(t *testing.T) {
	db := channelFailoverDatabase(t, sqlite.Open(":memory:"))
	primary, setting := channelFailoverFixture(t, db, true)
	require.NoError(t, model.UpdateRequestPolicyOptions(map[string]string{
		"channel_affinity_setting.enabled": "true",
		"channel_affinity_setting.rules":   `[{"name":"failover-session","model_regex":["^alias$"],"session_mode":"strict","key_sources":[{"type":"request_header","key":"X-Session"}],"fallback_channel_ids":[7143],"channel_lifetimes":[{"channel_id":15,"idle_ttl_seconds":300,"max_residence_seconds":3600}]}]`,
	}))
	contextForSession := func(session string) *gin.Context {
		c := newChannelFailoverContext(t)
		c.Request.Header.Set("X-Session", session)
		return c
	}
	seed := contextForSession(t.Name())
	_, found := GetPreferredChannelByAffinity(seed, "alias", "default")
	require.False(t, found)
	RecordChannelAffinity(seed, 15)
	t.Cleanup(func() { ClearCurrentChannelAffinityCache(seed) })
	bound, found := GetPreferredChannelByAffinity(seed, "alias", "default")
	require.True(t, found)
	assert.Equal(t, 15, bound)
	recordChannelFailoverAvailability(t, primary, setting, "empty")
	assert.Equal(t, 15, selectChannelFailoverForTest(t, contextForSession(t.Name())).Id, "the configured fallback chain cannot block the primary/backup policy")
	recordChannelFailoverAvailability(t, primary, setting, "ready")
	recordChannelFailoverAvailability(t, primary, setting, "ready")
	assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, contextForSession(t.Name())).Id, "strict affinity and backup residence TTL cannot delay failback")

	unrelated := contextForSession(t.Name() + "-unrelated")
	GetPreferredChannelByAffinity(unrelated, "alias", "default")
	RecordChannelAffinity(unrelated, 7143)
	t.Cleanup(func() { ClearCurrentChannelAffinityCache(unrelated) })
	c := contextForSession(t.Name() + "-unrelated")
	assert.Equal(t, 7143, selectChannelFailoverForTest(t, c).Id)
	assert.Nil(t, requestChannelFailover(c))
	assert.Equal(t, "strict_session", DecideRelayRetry(c, types.NewOpenAIError(errors.New("failed"), types.ErrorCodeBadResponse, 503), 1).Reason)
	_, _, selectErr := SelectChannelForRequest(c, "alias", &RetryParam{Ctx: c, TokenGroup: "default", ModelName: "alias", Retry: common.GetPointer(1)})
	assert.NotNil(t, selectErr)
	assert.Nil(t, requestChannelFailover(c), "a later retry cannot adopt a policy rejected for the original third-channel binding")
}

func TestChannelFailoverCapacityKeepsPrimaryEnabledForRecovery(t *testing.T) {
	db := channelFailoverDatabase(t, sqlite.Open(":memory:"))
	primary, _ := channelFailoverFixture(t, db, true)
	require.NoError(t, model.UpdateRequestPolicyOptions(map[string]string{"AutomaticDisableChannelEnabled": "true", "AutomaticDisableStatusCodes": "401,429,503"}))
	previousErrorLog := constant.ErrorLogEnabled
	constant.ErrorLogEnabled = false
	t.Cleanup(func() { constant.ErrorLogEnabled = previousErrorLog })
	c := newChannelFailoverContext(t)
	selectChannelFailoverForTest(t, c)
	c.Set("auto_ban", true)
	for _, status := range []int{429, 503} {
		err := types.WithOpenAIError(types.OpenAIError{Type: "api_error", Code: "model_capacity_exhausted"}, status)
		require.True(t, ShouldDisableChannel(err), "the broad global rule is active")
		assert.True(t, channelFailoverTransientCapacity(c, primary.Id, err))
		RecordPolicyFailure(c, primary.Id, err, DecideRelayRetry(c, err, 0))
		events := RequestPolicy(c).Events()
		assert.Equal(t, "unchanged", events[len(events)-1].Health)
		ProcessChannelError(c, *types.NewChannelError(primary.Id, primary.Type, primary.Name, false, primary.Key, true), err, nil)
		var persisted model.Channel
		require.NoError(t, db.First(&persisted, primary.Id).Error)
		assert.Equal(t, common.ChannelStatusEnabled, persisted.Status)
		assert.False(t, channelFailoverTransientCapacity(newChannelFailoverContext(t), primary.Id, err), "unmatched requests retain normal disabling")
		assert.False(t, channelFailoverTransientCapacity(c, 15, err), "the exception applies only to the primary")
	}
	credentialError := types.WithOpenAIError(types.OpenAIError{Type: "authentication_error", Code: "invalid_api_key"}, 401)
	assert.False(t, channelFailoverTransientCapacity(c, primary.Id, credentialError))
	assert.True(t, ShouldDisableChannel(credentialError))
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", primary.Id).Update("status", common.ChannelStatusManuallyDisabled).Error)
	model.InitChannelCache()
	assert.Nil(t, prepareChannelFailover(newChannelFailoverContext(t), "alias", "default", 0), "manual disabling remains authoritative")
}

func TestChannelFailoverOnlyProbesSupportedTextRoutes(t *testing.T) {
	db := channelFailoverDatabase(t, sqlite.Open(":memory:"))
	primary, _ := channelFailoverFixture(t, db, true)
	global := model_setting.GetGlobalSettings()
	previous := *global
	t.Cleanup(func() { *global = previous })
	global.ChatCompletionsToResponsesPolicy.Enabled = false
	for _, path := range []string{"/v1/images/generations", "/v1/images/edits", "/v1/audio/speech", "/v1/audio/transcriptions", "/v1/embeddings", "/v1/videos", "/v1/messages", "/v1/chat/completions"} {
		c := newChannelFailoverContext(t)
		c.Request.URL.Path = path
		assert.Nil(t, prepareChannelFailover(c, "alias", "default", 0), "%s must not inherit Responses capacity with an empty model filter", path)
	}
	assert.Empty(t, channelFailover.entries)
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		c := newChannelFailoverContext(t)
		c.Request.URL.Path = path
		assert.NotNil(t, prepareChannelFailover(c, "alias", "default", 0))
	}
	global.PassThroughRequestEnabled = false
	global.ChatCompletionsToResponsesPolicy = model_setting.ChatCompletionsToResponsesPolicy{Enabled: true, AllChannels: true, ModelPatterns: []string{"^alias$"}}
	converted := newChannelFailoverContext(t)
	converted.Request.URL.Path = "/v1/chat/completions"
	assert.NotNil(t, prepareChannelFailover(converted, "alias", "default", 0))
	primary.SetSetting(kitdto.ChannelSettings{PassThroughBodyEnabled: true})
	passthrough := newChannelFailoverContext(t)
	passthrough.Request.URL.Path = "/v1/chat/completions"
	assert.Nil(t, prepareChannelFailover(passthrough, "alias", "default", 0), "a raw chat passthrough is not a Responses request")
}
