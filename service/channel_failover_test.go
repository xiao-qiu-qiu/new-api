package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
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
		"channel_failover_setting.fallback_rules": "[]",
		"channel_affinity_setting.enabled":        "false", "RetryTimes": "0",
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

func selectChannelFailoverForTest(t *testing.T, c *gin.Context, models ...string) *model.Channel {
	t.Helper()
	modelName := "alias"
	if len(models) > 0 {
		modelName = models[0]
	}
	channel, group, err := SelectChannelForRequest(c, modelName, &RetryParam{Ctx: c, TokenGroup: "default", ModelName: modelName})
	require.Nil(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, "default", group)
	return channel
}

func recordChannelFailoverAvailability(t *testing.T, primary *model.Channel, setting operation_setting.ChannelFailoverSetting, state string, models ...string) *channelFailoverEntry {
	t.Helper()
	c := newChannelFailoverContext(t)
	modelName := "alias"
	if len(models) > 0 {
		modelName = models[0]
	}
	key := makeChannelFailoverKey(c, primary, modelName, setting)
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
				busy := newChannelFailoverContext(t)
				state := prepareChannelFailover(busy, "alias", "default", 0)
				require.NotNil(t, state)
				state.busyWaitDeadline = time.Now().Add(-time.Second)
				assert.Equal(t, 15, selectChannelFailoverForTest(t, busy).Id)
				channelFailover.finish(entry, entry.revision, channelAvailability{State: "ready"}, time.Now())
				assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, newChannelFailoverContext(t)).Id, "busy never latches recovery mode")
				channelFailover.finish(entry, entry.revision, channelAvailability{State: "empty"}, time.Now())
				channelFailover.finish(entry, entry.revision, channelAvailability{State: "busy", Reason: "account_rotation"}, time.Now())
				assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, newChannelFailoverContext(t)).Id, "rotation waits on the primary even after an empty pool")
				channelFailover.finish(entry, entry.revision, channelAvailability{State: "ready"}, time.Now())
				assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, newChannelFailoverContext(t)).Id, "rotation does not leave an empty-pool recovery latch")
				channelFailover.finish(entry, entry.revision, channelAvailability{State: "empty"}, time.Now())
				entry.lastValid = time.Now().Add(-time.Minute)
				assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, newChannelFailoverContext(t)).Id, "stale empty cannot lock traffic to backup")
			})
		}
	}
}

func TestChannelFailoverBusyWait(t *testing.T) {
	db := channelFailoverDatabase(t, sqlite.Open(":memory:"))
	primary, _ := channelFailoverFixture(t, db, true)
	require.NoError(t, model.UpdateRequestPolicyOptions(map[string]string{"channel_failover_setting.poll_interval_seconds": "60"}))
	require.NoError(t, db.Model(&model.Channel{}).Where("id IN ?", []int{primary.Id, 15}).Update("setting", `{"max_concurrency":1}`).Error)
	model.InitChannelCache()
	primary, err := model.CacheGetChannel(primary.Id)
	require.NoError(t, err)
	backup, err := model.CacheGetChannel(15)
	require.NoError(t, err)
	setting := model.CurrentRequestPolicy().Failover
	for _, tc := range []struct {
		name         string
		after        time.Duration
		availability channelAvailability
		cancel       bool
		deadline     bool
		stale        bool
		wantID       int
		wantState    string
		wantErr      error
	}{
		{name: "busy_until_timeout", after: 29 * time.Second, wantID: 15, wantState: "busy"},
		{name: "ready_during_wait", after: 4 * time.Second, availability: channelAvailability{State: "ready"}, wantID: primary.Id, wantState: "ready"},
		{name: "unknown_during_wait", after: 4 * time.Second, availability: channelAvailability{State: "unknown"}, wantID: primary.Id, wantState: "unknown"},
		{name: "cancelled_during_wait", after: 4 * time.Second, cancel: true, wantErr: context.Canceled},
		{name: "deadline_during_wait", after: 4 * time.Second, deadline: true, wantErr: context.DeadlineExceeded},
		{name: "rotation_during_wait", after: 4 * time.Second, availability: channelAvailability{State: "busy", Reason: "account_rotation"}, wantID: primary.Id, wantState: "rotation_wait"},
		{name: "empty_during_wait", after: 4 * time.Second, availability: channelAvailability{State: "empty"}, wantID: 15, wantState: "empty"},
		{name: "stale_busy_tries_primary", after: 10 * time.Second, stale: true, wantID: primary.Id, wantState: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				channelFailover = newChannelFailoverRuntime()
				entry := recordChannelFailoverAvailability(t, primary, setting, "busy")
				if tc.stale {
					entry.setting.PollIntervalSeconds = 3
				}
				c := newChannelFailoverContext(t)
				defer ReleaseChannelConcurrency(c)
				ctx, cancel := context.WithCancel(c.Request.Context())
				defer cancel()
				if tc.deadline {
					var cancelDeadline context.CancelFunc
					ctx, cancelDeadline = context.WithTimeout(ctx, tc.after)
					defer cancelDeadline()
				}
				c.Request = c.Request.WithContext(ctx)
				start := time.Now()
				state := prepareChannelFailover(c, "alias", "default", 0)
				require.NotNil(t, state)
				assert.Equal(t, start, time.Now(), "preparing a busy request does not wait")
				var selected *model.Channel
				var group string
				var selectionErr error
				completed := false
				go func() {
					selected, group, selectionErr = CacheGetRandomSatisfiedChannel(&RetryParam{Ctx: c, ModelName: "alias", TokenGroup: "default"})
					completed = true
				}()
				synctest.Wait()
				require.False(t, completed, "busy selection must wait before attempting either channel")
				for _, channel := range []*model.Channel{primary, backup} {
					probe := newChannelFailoverContext(t)
					require.NoError(t, acquireChannelConcurrency(probe, channel), "waiting must leave channel %d's only slot available", channel.Id)
					ReleaseChannelConcurrency(probe)
				}
				time.Sleep(tc.after)
				synctest.Wait()
				if !tc.deadline {
					require.False(t, completed, "selection must still be waiting before the availability change or 30-second budget")
				}
				switch {
				case tc.cancel:
					cancel()
				case tc.availability.State != "":
					channelFailover.finish(entry, entry.revision, tc.availability, time.Now())
					time.Sleep(250 * time.Millisecond)
				case tc.stale:
					time.Sleep(250 * time.Millisecond)
				case !tc.deadline:
					time.Sleep(time.Second)
				}
				synctest.Wait()
				require.True(t, completed, "selection must finish at the virtual timeout, cancellation, or next availability poll")
				assert.Equal(t, "default", group)
				if tc.wantErr != nil {
					assert.ErrorIs(t, selectionErr, tc.wantErr)
					assert.Nil(t, selected)
					assert.Empty(t, state.visited, "cancellation must not attempt the primary or backup")
					return
				}
				require.NoError(t, selectionErr)
				require.NotNil(t, selected)
				assert.Equal(t, tc.wantID, selected.Id)
				assert.Equal(t, tc.wantState, state.state)
				if tc.wantID == primary.Id {
					assert.False(t, state.visited[15], "recovering availability must not send this request to the backup")
				}
			})
		})
	}
	t.Run("request_budgets_are_independent", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			channelFailover = newChannelFailoverRuntime()
			recordChannelFailoverAvailability(t, primary, setting, "busy")
			firstContext := newChannelFailoverContext(t)
			secondContext := newChannelFailoverContext(t)
			ctx, cancel := context.WithCancel(firstContext.Request.Context())
			defer cancel()
			firstContext.Request = firstContext.Request.WithContext(ctx)
			secondContext.Request = secondContext.Request.WithContext(ctx)
			firstState := prepareChannelFailover(firstContext, "alias", "default", 0)
			require.NotNil(t, firstState)
			var first, second *model.Channel
			var firstErr, secondErr error
			firstDone, secondDone := false, false
			go func() {
				first, _, firstErr = CacheGetRandomSatisfiedChannel(&RetryParam{Ctx: firstContext, ModelName: "alias", TokenGroup: "default"})
				ReleaseChannelConcurrency(firstContext)
				firstDone = true
			}()
			synctest.Wait()
			require.False(t, firstDone)
			time.Sleep(10 * time.Second)
			secondState := prepareChannelFailover(secondContext, "alias", "default", 0)
			require.NotNil(t, secondState)
			assert.Equal(t, firstState.key, secondState.key)
			go func() {
				second, _, secondErr = CacheGetRandomSatisfiedChannel(&RetryParam{Ctx: secondContext, ModelName: "alias", TokenGroup: "default"})
				ReleaseChannelConcurrency(secondContext)
				secondDone = true
			}()
			synctest.Wait()
			time.Sleep(20 * time.Second)
			synctest.Wait()
			require.True(t, firstDone)
			require.NoError(t, firstErr)
			require.NotNil(t, first)
			assert.Equal(t, 15, first.Id)
			require.False(t, secondDone, "one request exhausting its budget must not shorten another request's wait")
			time.Sleep(10 * time.Second)
			synctest.Wait()
			require.True(t, secondDone)
			require.NoError(t, secondErr)
			require.NotNil(t, second)
			assert.Equal(t, 15, second.Id)
		})
	})
	t.Run("reentry_preserves_request_budget", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			channelFailover = newChannelFailoverRuntime()
			entry := recordChannelFailoverAvailability(t, primary, setting, "busy")
			c := newChannelFailoverContext(t)
			defer ReleaseChannelConcurrency(c)
			ctx, cancel := context.WithCancel(c.Request.Context())
			defer cancel()
			c.Request = c.Request.WithContext(ctx)
			state := prepareChannelFailover(c, "alias", "default", 0)
			require.NotNil(t, state)
			var waitErr error
			waitDone := false
			go func() {
				waitErr = state.waitForBusyPrimary(c.Request.Context())
				waitDone = true
			}()
			synctest.Wait()
			time.Sleep(4 * time.Second)
			channelFailover.finish(entry, entry.revision, channelAvailability{State: "ready"}, time.Now())
			time.Sleep(250 * time.Millisecond)
			synctest.Wait()
			require.True(t, waitDone)
			require.NoError(t, waitErr)
			channelFailover.finish(entry, entry.revision, channelAvailability{State: "busy"}, time.Now())
			var selected *model.Channel
			var selectionErr error
			completed := false
			go func() {
				selected, _, selectionErr = CacheGetRandomSatisfiedChannel(&RetryParam{Ctx: c, ModelName: "alias", TokenGroup: "default"})
				completed = true
			}()
			synctest.Wait()
			time.Sleep(25 * time.Second)
			synctest.Wait()
			require.False(t, completed)
			time.Sleep(750 * time.Millisecond)
			synctest.Wait()
			require.True(t, completed, "re-entering selection must use the original 30-second budget")
			require.NoError(t, selectionErr)
			require.NotNil(t, selected)
			assert.Equal(t, 15, selected.Id)
		})
	})
}

func TestChannelFailoverModelBackups(t *testing.T) {
	db := channelFailoverDatabase(t, sqlite.Open(":memory:"))
	channelFailoverFixture(t, db, true)
	for _, id := range []int{3, 38, 39, 57, 76} {
		createChannelSelectAutoGroupsChannel(t, db, id, "default", "gpt-6-astra")
		require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", id).Update("models", "gpt-6-astra,gpt-6-sol,codex-auto-review").Error)
		for _, modelName := range []string{"gpt-6-sol", "codex-auto-review"} {
			require.NoError(t, db.Create(&model.Ability{ChannelId: id, Group: "default", Model: modelName, Enabled: true}).Error)
		}
	}
	require.NoError(t, model.UpdateRequestPolicyOptions(map[string]string{
		"channel_failover_setting.primary_channel_id":  "3",
		"channel_failover_setting.fallback_channel_id": "38",
		"channel_failover_setting.model_regex":         `["^gpt-6-","^codex-auto-review$"]`,
		"channel_failover_setting.fallback_rules":      `[{"model_regex":["^gpt-6-astra$"],"channel_id":39},{"model_regex":["^codex-auto-review$"],"channel_id":76},{"model_regex":["^gpt-6-astra$"],"channel_id":38}]`,
		"channel_affinity_setting.enabled":             "true",
		"channel_affinity_setting.rules":               `[{"name":"model-backup","model_regex":["^gpt-6-","^codex-auto-review$"],"session_mode":"strict","key_sources":[{"type":"request_header","key":"X-Session"}],"fallback_channel_ids":[57],"channel_lifetimes":[{"channel_id":38,"idle_ttl_seconds":300,"max_residence_seconds":3600},{"channel_id":39,"idle_ttl_seconds":300,"max_residence_seconds":3600},{"channel_id":76,"idle_ttl_seconds":300,"max_residence_seconds":3600}]}]`,
	}))
	model.InitChannelCache()
	primary, err := model.CacheGetChannel(3)
	require.NoError(t, err)
	setting := model.CurrentRequestPolicy().Failover
	for _, tc := range []struct {
		model  string
		backup int
	}{{"gpt-6-astra", 39}, {"gpt-6-sol", 38}, {"codex-auto-review", 76}} {
		t.Run(tc.model, func(t *testing.T) {
			c := newChannelFailoverContext(t)
			assert.Equal(t, 3, selectChannelFailoverForTest(t, c, tc.model).Id)
			assert.Equal(t, 1, ChannelFailoverRetryBudget(c))
			capacity := types.WithOpenAIError(types.OpenAIError{Code: "model_capacity_exhausted", Type: "api_error"}, 503)
			assert.Equal(t, "retry", DecideRelayRetry(c, capacity, 0).Action)
			backup, _, selectionErr := CacheGetRandomSatisfiedChannel(&RetryParam{Ctx: c, ModelName: tc.model, TokenGroup: "default", Retry: common.GetPointer(1)})
			require.NoError(t, selectionErr)
			require.NotNil(t, backup)
			assert.Equal(t, tc.backup, backup.Id)
			assert.Equal(t, tc.model, backup.ResolveServiceTierModel(tc.model), "backup selection preserves the requested model")
			assert.Equal(t, "stop", DecideRelayRetry(c, capacity, 10).Action, "only the selected pair may be retried")
			assert.Equal(t, tc.backup, selectChannelFailoverForTest(t, newChannelFailoverContext(t), tc.model).Id)
			for _, oldBackup := range []int{38, 39, 76} {
				session := fmt.Sprintf("%s-%d", t.Name(), oldBackup)
				seed := newChannelFailoverContext(t)
				seed.Request.Header.Set("X-Session", session)
				GetPreferredChannelByAffinity(seed, tc.model, "default")
				RecordChannelAffinity(seed, oldBackup)
				t.Cleanup(func() { ClearCurrentChannelAffinityCache(seed) })
				for _, ready := range []bool{false, true} {
					recordChannelFailoverAvailability(t, primary, setting, "empty", tc.model)
					want := tc.backup
					if ready {
						recordChannelFailoverAvailability(t, primary, setting, "ready", tc.model)
						recordChannelFailoverAvailability(t, primary, setting, "ready", tc.model)
						want = 3
					}
					request := newChannelFailoverContext(t)
					request.Request.Header.Set("X-Session", session)
					assert.Equal(t, want, selectChannelFailoverForTest(t, request, tc.model).Id, "old backup %d, primary ready %t", oldBackup, ready)
					ws := newChannelFailoverContext(t)
					assert.True(t, ChannelFailoverMayReroute(ws, tc.model, "default", oldBackup))
					assert.Equal(t, want, selectChannelFailoverForTest(t, ws, tc.model).Id)
				}
			}
			unrelated := newChannelFailoverContext(t)
			unrelated.Request.Header.Set("X-Session", t.Name()+"-third")
			GetPreferredChannelByAffinity(unrelated, tc.model, "default")
			RecordChannelAffinity(unrelated, 57)
			t.Cleanup(func() { ClearCurrentChannelAffinityCache(unrelated) })
			request := newChannelFailoverContext(t)
			request.Request.Header.Set("X-Session", t.Name()+"-third")
			assert.Equal(t, 57, selectChannelFailoverForTest(t, request, tc.model).Id)
			assert.False(t, HasChannelFailover(request))
			assert.Equal(t, "strict_session", DecideRelayRetry(request, capacity, 1).Reason)
			assert.False(t, ChannelFailoverMayReroute(newChannelFailoverContext(t), tc.model, "default", 57))
		})
	}
	// A specific backup rule cannot widen the global model filter.
	require.NoError(t, model.UpdateRequestPolicyOptions(map[string]string{"channel_failover_setting.model_regex": `["^gpt-6-sol$"]`}))
	assert.Nil(t, prepareChannelFailover(newChannelFailoverContext(t), "gpt-6-astra", "default", 38))
	assert.False(t, ChannelFailoverMayReroute(newChannelFailoverContext(t), "gpt-6-astra", "default", 39))
	// A matching but disabled backup must not leak traffic to the default or a third channel.
	require.NoError(t, model.UpdateRequestPolicyOptions(map[string]string{"channel_failover_setting.model_regex": "[]"}))
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 39).Update("status", common.ChannelStatusManuallyDisabled).Error)
	model.InitChannelCache()
	recordChannelFailoverAvailability(t, primary, model.CurrentRequestPolicy().Failover, "empty", "gpt-6-astra")
	c := newChannelFailoverContext(t)
	assert.Equal(t, 3, selectChannelFailoverForTest(t, c, "gpt-6-astra").Id)
	assert.Equal(t, "stop", DecideRelayRetry(c, types.NewOpenAIError(errors.New("failed"), types.ErrorCodeBadResponse, 503), 1).Action)
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
	assert.NotContains(t, request.URL.Query(), "api_format")
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

	t.Run("native_chat_format", func(t *testing.T) {
		db := channelFailoverDatabase(t, sqlite.Open(":memory:"))
		primary, setting := channelFailoverFixture(t, db, true)
		global := model_setting.GetGlobalSettings()
		previous := *global
		t.Cleanup(func() { *global = previous })
		global.PassThroughRequestEnabled = false
		global.ChatCompletionsToResponsesPolicy = model_setting.ChatCompletionsToResponsesPolicy{}
		seen := make(chan *http.Request, 1)
		var echoFormat atomic.Value
		echoFormat.Store("chat_completions")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			seen <- req.Clone(req.Context())
			response := map[string]any{
				"object": "routing.availability", "model": req.URL.Query().Get("model"),
				"state": "empty", "checked_at": time.Now().Unix(),
			}
			if format := echoFormat.Load().(string); format != "" {
				response["api_format"] = format
			}
			body, _ := common.Marshal(response)
			_, _ = w.Write(body)
		}))
		defer server.Close()
		primary.Type, primary.BaseURL = constant.ChannelTypeSub2API, &server.URL
		chat := newChannelFailoverContext(t)
		chat.Request.URL.Path = "/v1/chat/completions"
		key := makeChannelFailoverKey(chat, primary, "alias", setting)
		channelFailover.observe(key, primary, setting, time.Now())
		entry := channelFailover.entries[key]
		require.NotNil(t, entry)
		result := probeChannelAvailability(context.Background(), entry)
		require.Equal(t, "empty", result.State)
		assert.Equal(t, "chat_completions", (<-seen).URL.Query().Get("api_format"))
		channelFailover.finish(entry, entry.revision, result, time.Now())
		recordChannelFailoverAvailability(t, primary, setting, "ready")
		assert.Equal(t, setting.FallbackChannelID, selectChannelFailoverForTest(t, chat).Id, "native Chat retains its empty account pool independently of Responses")
		assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, newChannelFailoverContext(t)).Id, "an empty Chat pool must not hide ready Responses accounts")

		for _, format := range []string{"responses", ""} {
			echoFormat.Store(format)
			assert.Equal(t, "unknown", probeChannelAvailability(context.Background(), entry).State, "a mismatched or missing format echo cannot mark native Chat empty")
			assert.Equal(t, "chat_completions", (<-seen).URL.Query().Get("api_format"))
		}
		global.ChatCompletionsToResponsesPolicy = model_setting.ChatCompletionsToResponsesPolicy{Enabled: true, AllChannels: true, ModelPatterns: []string{"^alias$"}}
		converted := newChannelFailoverContext(t)
		converted.Request.URL.Path = "/v1/chat/completions"
		convertedKey := makeChannelFailoverKey(converted, primary, "alias", setting)
		convertedEntry := &channelFailoverEntry{key: convertedKey, channel: primary, setting: setting}
		assert.Equal(t, "empty", probeChannelAvailability(context.Background(), convertedEntry).State)
		assert.NotContains(t, (<-seen).URL.Query(), "api_format", "converted Chat keeps the legacy Responses probe contract")
		assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, converted).Id, "converted Chat uses Responses availability instead of the native Chat pool")
	})
}

func TestChannelFailoverProbeMaintenanceFence(t *testing.T) {
	client := useChannelConcurrencyBackend(t, "redis")
	InitHttpClient()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		body, _ := common.Marshal(channelAvailability{Object: "routing.availability", Model: "alias", State: "ready", CheckedAt: time.Now().Unix()})
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	primary := &model.Channel{Id: 910081, Type: constant.ChannelTypeOpenAI, Key: "probe-key", BaseURL: &server.URL}
	entry := &channelFailoverEntry{channel: primary, key: channelFailoverKey{ChannelID: 910082, Model: "alias"}}
	ctx := context.Background()
	fenceKey := channelMaintenanceFenceKeys(nil, primary.Id)[0]
	require.NoError(t, client.Set(ctx, channelMaintenanceFenceKeys(nil, entry.key.ChannelID)[0], "other-deployment", 0).Err())
	assert.Equal(t, "ready", probeChannelAvailability(ctx, entry).State, "only the actual upstream channel's fence pauses its probe")
	require.EqualValues(t, 1, calls.Load())
	require.NoError(t, client.Set(ctx, fenceKey, "deployment", 0).Err())
	assert.Equal(t, "unknown", probeChannelAvailability(ctx, entry).State)
	assert.EqualValues(t, 1, calls.Load(), "maintenance must not contact the upstream")
	capabilities := ChannelMaintenanceCapabilities()
	assert.Equal(t, 1, capabilities["version"])
	assert.Equal(t, true, capabilities["probe_pause"])
	require.NoError(t, client.Del(ctx, fenceKey).Err())
	assert.Equal(t, "ready", probeChannelAvailability(ctx, entry).State)
	assert.EqualValues(t, 2, calls.Load(), "removing the fence restores probing")
	primary.SetSetting(kitdto.ChannelSettings{Proxy: "://invalid"})
	assert.Equal(t, "unknown", probeChannelAvailability(ctx, entry).State)
	assert.EqualValues(t, 2, calls.Load(), "client construction failure must not contact the upstream")
}

func TestChannelFailoverProbeMaintenanceClosesOnlyIdleConnections(t *testing.T) {
	redisClient := useChannelConcurrencyBackend(t, "redis")
	idle, closed := make(chan net.Conn, 8), make(chan net.Conn, 8)
	release := make(chan struct{})
	var finish sync.Once
	var calls atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/active":
			_, _ = w.Write([]byte("start"))
			w.(http.Flusher).Flush()
			select {
			case <-release:
				_, _ = w.Write([]byte("finished"))
			case <-req.Context().Done():
			}
		case "/idle":
			_, _ = w.Write([]byte("idle"))
		default:
			calls.Add(1)
		}
	}))
	server.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		switch state {
		case http.StateIdle:
			idle <- conn
		case http.StateClosed:
			closed <- conn
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	client := server.Client()
	t.Cleanup(client.CloseIdleConnections)
	previousClient := httpClient
	httpClient = client
	t.Cleanup(func() { httpClient = previousClient })
	t.Cleanup(func() { finish.Do(func() { close(release) }) })
	active, err := client.Get(server.URL + "/active")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, active.Body.Close()) })
	start := make([]byte, len("start"))
	_, err = io.ReadFull(active.Body, start)
	require.NoError(t, err)
	assert.Equal(t, "start", string(start))
	warmup, err := client.Get(server.URL + "/idle")
	require.NoError(t, err)
	drainClose(t, warmup)
	var idleConn net.Conn
	select {
	case idleConn = <-idle:
	case <-time.After(5 * time.Second):
		t.Fatal("warmup connection did not become idle")
	}
	primary := &model.Channel{Id: 910081, Type: constant.ChannelTypeOpenAI, Key: "probe-key", BaseURL: &server.URL}
	entry := &channelFailoverEntry{channel: primary, key: channelFailoverKey{Model: "alias"}}
	require.NoError(t, redisClient.Set(context.Background(), channelMaintenanceFenceKeys(nil, primary.Id)[0], "deployment", 0).Err())
	assert.Equal(t, "unknown", probeChannelAvailability(context.Background(), entry).State)
	assert.Zero(t, calls.Load())
	select {
	case conn := <-closed:
		assert.Same(t, idleConn, conn, "maintenance closes the existing idle connection")
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance left the idle TCP connection open")
	}
	finish.Do(func() { close(release) })
	body, err := io.ReadAll(active.Body)
	require.NoError(t, err, "closing idle connections must not interrupt the active stream")
	assert.Equal(t, "finished", string(body))
	assert.Same(t, client, GetHttpClient(), "maintenance preserves the shared client")
	warmup, err = client.Get(server.URL + "/idle")
	require.NoError(t, err, "the shared transport remains usable after closing idle connections")
	drainClose(t, warmup)
}

func TestChannelFailoverProbeMaintenanceFenceRace(t *testing.T) {
	for _, tc := range []struct {
		name, response, want string
		beforeDo             bool
		calls                int32
	}{
		{name: "before_do", beforeDo: true, want: "unknown"},
		{name: "between_keys", response: "empty", want: "unknown", calls: 1},
		{name: "inflight_ready", response: "ready", want: "ready", calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := useChannelConcurrencyBackend(t, "redis")
			InitHttpClient()
			primary := &model.Channel{Id: 910081, Type: constant.ChannelTypeOpenAI, Key: "first-key\nsecond-key", ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2}}
			fenceKey := channelMaintenanceFenceKeys(nil, primary.Id)[0]
			var calls atomic.Int32
			var closeRequested atomic.Bool
			closed := make(chan struct{}, 2)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				closeRequested.Store(req.Close)
				if err := client.Set(req.Context(), fenceKey, "deployment", 0).Err(); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				body, _ := common.Marshal(channelAvailability{Object: "routing.availability", Model: "alias", State: tc.response, CheckedAt: time.Now().Unix()})
				_, _ = w.Write(body)
			}))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateClosed {
					closed <- struct{}{}
				}
			}
			server.Start()
			t.Cleanup(server.Close)
			primary.BaseURL = &server.URL
			entry := &channelFailoverEntry{channel: primary, key: channelFailoverKey{Model: "alias"}}
			if tc.beforeDo {
				previousHeaders := ApplyChannelFailoverProbeHeaders
				ApplyChannelFailoverProbeHeaders = func(_ *relaycommon.RelayInfo, req *http.Request) error {
					return client.Set(req.Context(), fenceKey, "deployment", 0).Err()
				}
				t.Cleanup(func() { ApplyChannelFailoverProbeHeaders = previousHeaders })
			}
			assert.Equal(t, tc.want, probeChannelAvailability(context.Background(), entry).State)
			require.Equal(t, tc.calls, calls.Load(), "no new credential probe may start after the fence")
			if tc.calls != 0 {
				assert.True(t, closeRequested.Load(), "a probe already in flight must not leave a new idle connection")
				select {
				case <-closed:
				case <-time.After(5 * time.Second):
					t.Fatal("the in-flight probe left its TCP connection open")
				}
			}
		})
	}
}

func TestChannelFailoverProbeMaintenanceRedisUnavailable(t *testing.T) {
	useChannelConcurrencyBackend(t, "redis")
	InitHttpClient()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(server.Close)
	primary := &model.Channel{Id: 910081, Type: constant.ChannelTypeOpenAI, Key: "probe-key", BaseURL: &server.URL}
	entry := &channelFailoverEntry{channel: primary, key: channelFailoverKey{Model: "alias"}}
	client := common.RDB
	t.Run("missing_client", func(t *testing.T) {
		common.RDB = nil
		t.Cleanup(func() { common.RDB = client })
		assert.Equal(t, "unknown", probeChannelAvailability(context.Background(), entry).State)
	})
	t.Run("redis_error", func(t *testing.T) {
		unavailable := redis.NewClient(client.Options())
		require.NoError(t, unavailable.Close())
		common.RDB = unavailable
		t.Cleanup(func() { common.RDB = client })
		assert.Equal(t, "unknown", probeChannelAvailability(context.Background(), entry).State)
	})
	t.Run("bounded_read", func(t *testing.T) {
		deadline := make(chan time.Time, 1)
		unavailable := redis.NewClient(&redis.Options{MaxRetries: -1, Dialer: func(ctx context.Context, _, _ string) (net.Conn, error) {
			limit, _ := ctx.Deadline()
			deadline <- limit
			return nil, errors.New("test Redis unavailable")
		}})
		common.RDB = unavailable
		t.Cleanup(func() {
			common.RDB = client
			assert.NoError(t, unavailable.Close())
		})
		assert.True(t, channelMaintenanceProbePaused(context.Background(), primary.Id))
		limit := <-deadline
		require.False(t, limit.IsZero(), "even a background parent must produce a bounded Redis read")
		assert.WithinDuration(t, time.Now(), limit, channelConcurrencyTimeout)
		assert.Equal(t, "unknown", probeChannelAvailability(context.Background(), entry).State)
	})
	assert.Zero(t, calls.Load(), "unknown maintenance state must not contact the upstream")
}

func TestChannelFailoverRotationWaitStaysOnPrimary(t *testing.T) {
	db := channelFailoverDatabase(t, sqlite.Open(":memory:"))
	primary, setting := channelFailoverFixture(t, db, true)
	InitHttpClient()
	var response atomic.Value
	response.Store(channelAvailability{State: "busy", Reason: "account_rotation"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		availability := response.Load().(channelAvailability)
		availability.Object, availability.Model, availability.CheckedAt = "routing.availability", "alias", time.Now().Unix()
		body, err := common.Marshal(availability)
		if err != nil {
			http.Error(w, "encoding failed", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)
	}))
	defer server.Close()
	entry := recordChannelFailoverAvailability(t, primary, setting, "empty")
	probeChannel := *primary
	probeChannel.BaseURL = &server.URL
	entry.channel = &probeChannel
	result := probeChannelAvailability(context.Background(), entry)
	require.Equal(t, "account_rotation", result.Reason)
	channelFailover.finish(entry, entry.revision, result, time.Now())

	for _, code := range []string{"upstream_unavailable", "model_capacity_exhausted"} {
		t.Run(code, func(t *testing.T) {
			channelFailover.finish(entry, entry.revision, result, time.Now())
			c := newChannelFailoverContext(t)
			assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, c).Id)
			err := types.WithOpenAIError(types.OpenAIError{Code: code, Type: "api_error"}, http.StatusServiceUnavailable)
			decision := DecideRelayRetry(c, err, 8)
			assert.Equal(t, "stop", decision.Action, "waiting requests must not spill into the backup on failure")
			assert.Equal(t, "primary_rotation_wait", decision.Reason)
			assert.Equal(t, "rotation_wait", entry.availability(time.Now()), "a rotation wait rejection cannot turn later requests into an empty-pool failover")
			_, _, retryErr := CacheGetRandomSatisfiedChannel(&RetryParam{Ctx: c, ModelName: "alias", TokenGroup: "default", Retry: common.GetPointer(1)})
			assert.NotNil(t, retryErr, "even a direct retry cannot select the backup")
		})
	}
	for _, tc := range []struct {
		initialState string
		afterRetry   bool
	}{
		{"ready", false}, {"unknown", false}, {"ready", true},
	} {
		t.Run(fmt.Sprintf("rotation_during_%s_request/after_retry_%t", tc.initialState, tc.afterRetry), func(t *testing.T) {
			channelFailover.finish(entry, entry.revision, result, time.Now())
			channelFailover.finish(entry, entry.revision, channelAvailability{State: tc.initialState}, time.Now())
			c := newChannelFailoverContext(t)
			require.Equal(t, primary.Id, selectChannelFailoverForTest(t, c).Id)
			capacity := types.WithOpenAIError(types.OpenAIError{Code: "model_capacity_exhausted", Type: "api_error"}, 503)
			if tc.afterRetry {
				require.Equal(t, "retry", DecideRelayRetry(c, capacity, 8).Action)
			}
			channelFailover.finish(entry, entry.revision, result, time.Now())
			if !tc.afterRetry {
				assert.Equal(t, "stop", DecideRelayRetry(c, capacity, 8).Action)
			}
			_, _, retryErr := CacheGetRandomSatisfiedChannel(&RetryParam{Ctx: c, ModelName: "alias", TokenGroup: "default", Retry: common.GetPointer(1)})
			assert.NotNil(t, retryErr, "a newer rotation observation takes precedence over the request's old availability")
			assert.Equal(t, "rotation_wait", entry.availability(time.Now()), "old capacity errors cannot overwrite a newer rotation observation")
		})
	}
	t.Run("cold_cache_rotation_rejection", func(t *testing.T) {
		channelFailover.finish(entry, entry.revision, channelAvailability{State: "unknown"}, time.Now())
		c := newChannelFailoverContext(t)
		require.Equal(t, primary.Id, selectChannelFailoverForTest(t, c).Id)
		revision := entry.revision
		rotation := RelayErrorHandler(c.Request.Context(), &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"api_error","code":"account_rotation","message":"No available accounts"}}`)),
		}, false)
		assert.True(t, channelFailoverTransientCapacity(c, primary.Id, rotation), "rotation must not automatically disable the primary")
		decision := DecideRelayRetry(c, rotation, 8)
		assert.Equal(t, "stop", decision.Action)
		assert.Equal(t, "primary_rotation_wait", decision.Reason)
		channelFailover.finish(entry, revision, channelAvailability{State: "empty"}, time.Now())
		assert.Equal(t, "rotation_wait", entry.availability(time.Now()), "a stale probe cannot undo the explicit rotation rejection")
		assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, newChannelFailoverContext(t)).Id)
		assert.False(t, channelRotationWaiting(types.NewOpenAIError(errors.New("account_rotation"), types.ErrorCodeBadResponse, 503)), "free text must not change failover behavior")
	})
	t.Run("rotation_before_first_selection", func(t *testing.T) {
		channelFailover.finish(entry, entry.revision, channelAvailability{State: "busy", Reason: "capacity_busy"}, time.Now())
		c := newChannelFailoverContext(t)
		require.NotNil(t, prepareChannelFailover(c, "alias", "default", 0))
		channelFailover.finish(entry, entry.revision, result, time.Now())
		assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, c).Id, "a newer rotation signal also overrides an unstarted backup plan")
	})

	response.Store(channelAvailability{State: "busy", Reason: "capacity_busy"})
	channelFailover.finish(entry, entry.revision, probeChannelAvailability(context.Background(), entry), time.Now())
	busy := newChannelFailoverContext(t)
	state := prepareChannelFailover(busy, "alias", "default", 0)
	require.NotNil(t, state)
	state.busyWaitDeadline = time.Now().Add(-time.Second)
	assert.Equal(t, 15, selectChannelFailoverForTest(t, busy).Id, "ordinary saturation still uses the backup after the wait budget")
	response.Store(channelAvailability{State: "ready", Reason: "account_rotation"})
	channelFailover.finish(entry, entry.revision, probeChannelAvailability(context.Background(), entry), time.Now())
	channelFailover.finish(entry, entry.revision, probeChannelAvailability(context.Background(), entry), time.Now())
	c := newChannelFailoverContext(t)
	assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, c).Id)
	assert.Equal(t, "retry", DecideRelayRetry(c, types.WithOpenAIError(types.OpenAIError{Code: "upstream_unavailable"}, 503), 8).Action, "a reason without busy does not lock normal requests")
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

func TestChannelFailoverRotationWaitAtConcurrencyLimit(t *testing.T) {
	db := channelFailoverDatabase(t, sqlite.Open(":memory:"))
	primary, setting := channelFailoverFixture(t, db, true)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", primary.Id).Update("setting", `{"max_concurrency":1}`).Error)
	model.InitChannelCache()
	primary, err := model.CacheGetChannel(primary.Id)
	require.NoError(t, err)
	held := newChannelFailoverContext(t)
	require.NoError(t, acquireChannelConcurrency(held, primary))
	entry := recordChannelFailoverAvailability(t, primary, setting, "ready")
	channelFailover.finish(entry, entry.revision, channelAvailability{State: "busy", Reason: "account_rotation"}, time.Now())
	c := newChannelFailoverContext(t)
	selected, _, selectionErr := SelectChannelForRequest(c, "alias", &RetryParam{Ctx: c, ModelName: "alias", TokenGroup: "default"})
	assert.Nil(t, selected)
	require.NotNil(t, selectionErr)
	assert.Equal(t, http.StatusTooManyRequests, selectionErr.StatusCode)
	assert.Equal(t, types.ErrorCode("channel_concurrency_limit"), selectionErr.Code)
	assert.False(t, requestChannelFailover(c).visited[15], "rotation cannot overflow into a free backup")
	ReleaseChannelConcurrency(held)
	next := newChannelFailoverContext(t)
	assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, next).Id, "freeing a slot admits the next request to the primary")
	ReleaseChannelConcurrency(next)
	channelFailover.finish(entry, entry.revision, channelAvailability{State: "busy", Reason: "capacity_busy"}, time.Now())
	busy := newChannelFailoverContext(t)
	state := prepareChannelFailover(busy, "alias", "default", 0)
	require.NotNil(t, state)
	state.busyWaitDeadline = time.Now().Add(-time.Second)
	assert.Equal(t, 15, selectChannelFailoverForTest(t, busy).Id, "ordinary upstream saturation still permits the backup after the wait budget")
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
	var firstState, secondState atomic.Value
	firstState.Store(channelAvailability{State: "empty"})
	secondState.Store(channelAvailability{State: "ready"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		availability := firstState.Load().(channelAvailability)
		if req.Header.Get("Authorization") == "Bearer second-key" {
			availability = secondState.Load().(channelAvailability)
		}
		availability.Object, availability.Model, availability.CheckedAt = "routing.availability", "alias", time.Now().Unix()
		body, _ := common.Marshal(availability)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	entry := channelFailover.entries[requestChannelFailover(c).key]
	copyChannel := *entry.channel
	copyChannel.BaseURL = &server.URL
	entry.channel = &copyChannel
	assert.Equal(t, "ready", probeChannelAvailability(context.Background(), entry).State)
	secondState.Store(channelAvailability{State: "empty"})
	assert.Equal(t, "empty", probeChannelAvailability(context.Background(), entry).State, "only all enabled credentials being empty marks the channel empty")
	for _, tc := range []struct {
		name          string
		first, second channelAvailability
		state, reason string
	}{
		{"rotation_then_busy", channelAvailability{State: "busy", Reason: "account_rotation"}, channelAvailability{State: "busy", Reason: "capacity_busy"}, "busy", "account_rotation"},
		{"busy_then_rotation", channelAvailability{State: "busy", Reason: "capacity_busy"}, channelAvailability{State: "busy", Reason: "account_rotation"}, "busy", "account_rotation"},
		{"rotation_then_ready", channelAvailability{State: "busy", Reason: "account_rotation"}, channelAvailability{State: "ready"}, "ready", ""},
		{"rotation_then_unknown", channelAvailability{State: "busy", Reason: "account_rotation"}, channelAvailability{State: "unknown"}, "unknown", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			firstState.Store(tc.first)
			secondState.Store(tc.second)
			result := probeChannelAvailability(context.Background(), entry)
			assert.Equal(t, tc.state, result.State)
			assert.Equal(t, tc.reason, result.Reason)
		})
	}
	for _, staleState := range []string{"empty", "busy"} {
		t.Run("rotation_invalidates_old_"+staleState, func(t *testing.T) {
			channelFailover.finish(entry, entry.revision, channelAvailability{State: "unknown"}, time.Now())
			c := newChannelFailoverContext(t)
			require.Equal(t, primary.Id, selectChannelFailoverForTest(t, c).Id)
			revision := entry.revision
			stale := channelAvailability{State: staleState, Reason: "capacity_busy"}
			channelFailover.finish(entry, revision, stale, time.Now())
			rotation := types.WithOpenAIError(types.OpenAIError{Code: "account_rotation", Type: "api_error"}, 503)
			decision := DecideRelayRetry(c, rotation, 8)
			assert.Equal(t, "stop", decision.Action)
			assert.Equal(t, "primary_rotation_wait", decision.Reason)
			channelFailover.finish(entry, revision, stale, time.Now())
			assert.Equal(t, "unknown", entry.availability(time.Now()), "a single rotating key invalidates stale pool observations without marking the whole pool rotating")
			require.Equal(t, primary.Id, selectChannelFailoverForTest(t, newChannelFailoverContext(t)).Id)
			firstState.Store(channelAvailability{State: "busy", Reason: "account_rotation"})
			secondState.Store(channelAvailability{State: "ready"})
			channelFailover.finish(entry, entry.revision, probeChannelAvailability(context.Background(), entry), time.Now())
			next := newChannelFailoverContext(t)
			require.Equal(t, primary.Id, selectChannelFailoverForTest(t, next).Id)
			assert.Equal(t, "ready", entry.availability(time.Now()), "another ready key clears uncertainty without a recovery latch")
			assert.Equal(t, "retry", DecideRelayRetry(next, err, 8).Action, "another key's capacity failure retains its own fallback decision")
		})
	}
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

func TestChannelFailoverRuleUpdateRetiresAvailability(t *testing.T) {
	InitHttpClient()
	db := channelFailoverDatabase(t, sqlite.Open(":memory:"))
	primary, _ := channelFailoverFixture(t, db, true)
	var calls atomic.Int32
	seen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		seen <- req.URL.Query().Get("model")
		body, _ := common.Marshal(channelAvailability{Object: "routing.availability", Model: "mapped-model", State: "ready", CheckedAt: time.Now().Unix()})
		_, _ = w.Write(body)
	}))
	defer server.Close()
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", primary.Id).Updates(map[string]any{"base_url": server.URL, "model_mapping": `{"alias":"mapped-model"}`}).Error)
	model.InitChannelCache()
	primary, err := model.CacheGetChannel(primary.Id)
	require.NoError(t, err)
	require.NoError(t, model.UpdateRequestPolicyOptions(map[string]string{"channel_failover_setting.fallback_rules": `[{"model_regex":["^alias$"],"channel_id":38}]`}))
	old := recordChannelFailoverAvailability(t, primary, model.CurrentRequestPolicy().Failover, "empty")
	old.nextCheck = time.Time{}
	runtime := channelFailover
	runtime.running = true
	runtime.observe(old.key, primary, old.setting, time.Now())
	require.NoError(t, model.UpdateRequestPolicyOptions(map[string]string{"channel_failover_setting.fallback_rules": `[{"model_regex":["^alias$"],"channel_id":39}]`}))
	c := newChannelFailoverContext(t)
	assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, c).Id, "the previous rule's empty state cannot survive an update")
	fresh := requestChannelFailover(c)
	require.NotNil(t, fresh)
	assert.Equal(t, "unknown", fresh.state)
	assert.Equal(t, 39, fresh.key.FallbackChannelID)
	assert.NotEqual(t, old.key.Version, fresh.key.Version)
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
	case modelName := <-seen:
		assert.Equal(t, "mapped-model", modelName, "worker selects rules using the original model and probes the mapped model")
	case <-time.After(5 * time.Second):
		t.Fatal("updated model backup was not checked")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("failover worker pool did not stop")
	}
	assert.EqualValues(t, 1, calls.Load(), "queued work from the old rule must be discarded before probing")
	assert.NotContains(t, runtime.entries, old.key)
	assert.Contains(t, runtime.entries, fresh.key)
	runtime.finish(old, old.revision, channelAvailability{State: "ready"}, time.Now())
	assert.NotContains(t, runtime.entries, old.key, "a retired probe result cannot revive its old rule")
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
	entry := recordChannelFailoverAvailability(t, primary, setting, "busy")
	channelFailover.finish(entry, entry.revision, channelAvailability{State: "busy", Reason: "account_rotation"}, time.Now())
	assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, contextForSession(t.Name())).Id, "an old backup binding cannot move rotation waits away from the primary")
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
	primary, setting := channelFailoverFixture(t, db, true)
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
	for _, initialState := range []string{"ready", "rotation_wait"} {
		t.Run("temporary_503_during_"+initialState, func(t *testing.T) {
			entry := recordChannelFailoverAvailability(t, primary, setting, "busy")
			channelFailover.finish(entry, entry.revision, channelAvailability{State: "busy", Reason: "account_rotation"}, time.Now())
			if initialState == "ready" {
				channelFailover.finish(entry, entry.revision, channelAvailability{State: "ready"}, time.Now())
			}
			c := newChannelFailoverContext(t)
			require.Equal(t, primary.Id, selectChannelFailoverForTest(t, c).Id)
			c.Set("auto_ban", true)
			channelFailover.finish(entry, entry.revision, channelAvailability{State: "busy", Reason: "account_rotation"}, time.Now())
			err := types.WithOpenAIError(types.OpenAIError{Type: "api_error", Code: "upstream_unavailable"}, 503)
			require.True(t, ShouldDisableChannel(err))
			assert.True(t, channelFailoverTransientCapacity(c, primary.Id, err), "health protection also reads a newer rotation probe before retry evaluation")
			// Match HTTP and initial WebSocket failures: decide, record, then process.
			decision := DecideRelayRetry(c, err, 8)
			assert.Equal(t, "primary_rotation_wait", decision.Reason)
			RecordPolicyFailure(c, primary.Id, err, decision)
			events := RequestPolicy(c).Events()
			assert.Equal(t, "unchanged", events[len(events)-1].Health)
			ProcessChannelError(c, *types.NewChannelError(primary.Id, primary.Type, primary.Name, false, primary.Key, true), err, nil)
			var persisted model.Channel
			require.NoError(t, db.First(&persisted, primary.Id).Error)
			assert.Equal(t, common.ChannelStatusEnabled, persisted.Status)
			assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, newChannelFailoverContext(t)).Id)
			channelFailover.finish(entry, entry.revision, channelAvailability{State: "ready"}, time.Now())
			assert.False(t, channelFailoverTransientCapacity(newChannelFailoverContext(t), primary.Id, err))
			next := newChannelFailoverContext(t)
			selectChannelFailoverForTest(t, next)
			assert.False(t, channelFailoverTransientCapacity(next, primary.Id, err), "a recovered primary resumes ordinary health handling")
		})
	}
	for _, upstream := range []types.OpenAIError{
		{Type: "authentication_error", Code: "invalid_api_key"},
		{Type: "api_error", Code: "insufficient_quota"},
		{Type: "permission_error", Code: "upstream_unavailable"},
	} {
		entry := recordChannelFailoverAvailability(t, primary, setting, "rotation_wait")
		c := newChannelFailoverContext(t)
		selectChannelFailoverForTest(t, c)
		c.Set("auto_ban", true)
		err := types.WithOpenAIError(upstream, 503)
		assert.False(t, channelFailoverTransientCapacity(c, primary.Id, err), "permanent errors cannot inherit a rotation exemption even with status 503")
		RecordPolicyFailure(c, primary.Id, err, DecideRelayRetry(c, err, 8))
		events := RequestPolicy(c).Events()
		assert.Equal(t, "channel_disable_requested", events[len(events)-1].Health)
		channelFailover.finish(entry, entry.revision, channelAvailability{State: "ready"}, time.Now())
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
	primary, setting := channelFailoverFixture(t, db, true)
	global := model_setting.GetGlobalSettings()
	previous := *global
	t.Cleanup(func() { *global = previous })
	global.PassThroughRequestEnabled = false
	global.ChatCompletionsToResponsesPolicy = model_setting.ChatCompletionsToResponsesPolicy{}
	for _, path := range []string{"/v1/images/generations", "/v1/images/edits", "/v1/audio/speech", "/v1/audio/transcriptions", "/v1/embeddings", "/v1/videos", "/v1/messages"} {
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
	for _, tc := range []struct {
		name               string
		channelType        int
		path               string
		convert            bool
		globalPassthrough  bool
		channelPassthrough bool
		wantFailover       bool
	}{
		{name: "openai_native_chat", channelType: constant.ChannelTypeOpenAI, path: "/v1/chat/completions"},
		{name: "openai_converted_chat", channelType: constant.ChannelTypeOpenAI, path: "/v1/chat/completions", convert: true, wantFailover: true},
		{name: "openai_global_passthrough", channelType: constant.ChannelTypeOpenAI, path: "/v1/chat/completions", convert: true, globalPassthrough: true},
		{name: "openai_playground_channel_passthrough", channelType: constant.ChannelTypeOpenAI, path: "/pg/chat/completions", convert: true, channelPassthrough: true},
		{name: "sub2api_native_chat", channelType: constant.ChannelTypeSub2API, path: "/v1/chat/completions", wantFailover: true},
		{name: "sub2api_playground_chat", channelType: constant.ChannelTypeSub2API, path: "/pg/chat/completions", wantFailover: true},
		{name: "sub2api_global_passthrough", channelType: constant.ChannelTypeSub2API, path: "/v1/chat/completions", globalPassthrough: true},
		{name: "sub2api_playground_channel_passthrough", channelType: constant.ChannelTypeSub2API, path: "/pg/chat/completions", channelPassthrough: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary.Type = tc.channelType
			primary.Priority = common.GetPointer(int64(100))
			primary.SetSetting(kitdto.ChannelSettings{PassThroughBodyEnabled: tc.channelPassthrough})
			global.PassThroughRequestEnabled = tc.globalPassthrough
			global.ChatCompletionsToResponsesPolicy = model_setting.ChatCompletionsToResponsesPolicy{Enabled: tc.convert, AllChannels: true, ModelPatterns: []string{"^alias$"}}
			c := newChannelFailoverContext(t)
			c.Request.URL.Path = tc.path
			key := makeChannelFailoverKey(c, primary, "alias", setting)
			channelFailover.observe(key, primary, setting, time.Now())
			entry := channelFailover.entries[key]
			require.NotNil(t, entry)
			channelFailover.finish(entry, entry.revision, channelAvailability{State: "empty"}, time.Now())
			selected := selectChannelFailoverForTest(t, c)
			assert.Equal(t, tc.wantFailover, HasChannelFailover(c))
			if !tc.wantFailover {
				assert.Nil(t, prepareChannelFailover(c, "alias", "default", 0))
				assert.Equal(t, primary.Id, selected.Id, "ineligible Chat requests retain ordinary priority routing")
				return
			}
			assert.Equal(t, setting.FallbackChannelID, selected.Id, "a known empty primary must be skipped on the first attempt")
			events := RequestPolicy(c).Events()
			require.NotEmpty(t, events)
			selection := events[len(events)-1]
			assert.Equal(t, setting.FallbackChannelID, selection.ChannelID)
			assert.Equal(t, PolicyDecision{Action: "select", Reason: "backup_empty", Source: "channel_failover"}, selection.Decision)
			channelFailover.finish(entry, entry.revision, channelAvailability{State: "ready"}, time.Now())
			c = newChannelFailoverContext(t)
			c.Request.URL.Path = tc.path
			assert.Equal(t, setting.FallbackChannelID, selectChannelFailoverForTest(t, c).Id, "one recovery confirmation keeps the backup")
			channelFailover.finish(entry, entry.revision, channelAvailability{State: "ready"}, time.Now())
			c = newChannelFailoverContext(t)
			c.Request.URL.Path = tc.path
			assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, c).Id, "two recovery confirmations restore the primary")
		})
	}
}

// Observe a completed fence read, so tests can cancel or finish a deployment
// only after admission has actually entered maintenance waiting.
type channelMaintenanceWaitHook struct {
	once    sync.Once
	waiting chan struct{}
}

func (*channelMaintenanceWaitHook) BeforeProcess(ctx context.Context, _ redis.Cmder) (context.Context, error) {
	return ctx, nil
}

func (h *channelMaintenanceWaitHook) AfterProcess(_ context.Context, cmd redis.Cmder) error {
	if cmd.Name() == "exists" {
		if result, ok := cmd.(*redis.IntCmd); ok && result.Err() == nil && result.Val() > 0 {
			h.once.Do(func() { close(h.waiting) })
		}
	}
	return nil
}

func (*channelMaintenanceWaitHook) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	return ctx, nil
}

func (*channelMaintenanceWaitHook) AfterProcessPipeline(context.Context, []redis.Cmder) error {
	return nil
}

func TestChannelMaintenanceUnlimitedInflight(t *testing.T) {
	for _, backend := range []string{"real_redis", "redis"} {
		t.Run(backend, func(t *testing.T) {
			client := useChannelConcurrencyBackend(t, backend)
			ctx := context.Background()
			channel := &model.Channel{Id: 910080}
			channel.SetSetting(kitdto.ChannelSettings{MaxConcurrency: 0})
			activeKey := "channel_concurrency:910080"
			fenceKey := "newapi:maintenance:channel:910080"
			require.NoError(t, client.Del(ctx, activeKey, fenceKey).Err())
			t.Cleanup(func() { assert.NoError(t, client.Del(ctx, activeKey, fenceKey).Err()) })
			first, cancelFirst := newChannelConcurrencyTestContext(t)
			second, _ := newChannelConcurrencyTestContext(t)
			incoming, _ := newChannelConcurrencyTestContext(t)
			for _, c := range []*gin.Context{first, second} {
				acquired, err := TryAcquireChannelConcurrency(c, channel)
				require.NoError(t, err)
				require.True(t, acquired)
			}
			active, err := client.ZCard(ctx, activeKey).Result()
			require.NoError(t, err)
			assert.EqualValues(t, 2, active, "unlimited requests still count as inflight")
			ReleaseChannelConcurrency(second)
			require.NoError(t, client.Set(ctx, fenceKey, "deployment", 0).Err())
			acquired, err := TryAcquireChannelConcurrency(incoming, channel)
			require.Error(t, err)
			assert.False(t, acquired, "a fence rejects new admission even when concurrency is unlimited")
			cancelFirst()
			active, err = client.ZCard(ctx, activeKey).Result()
			require.NoError(t, err)
			assert.EqualValues(t, 1, active, "cancelled clients may still have upstream work to drain")
			ReleaseChannelConcurrency(first)
			active, err = client.ZCard(ctx, activeKey).Result()
			require.NoError(t, err)
			assert.Zero(t, active)
			acquired, err = TryAcquireChannelConcurrency(incoming, channel)
			require.Error(t, err)
			assert.False(t, acquired, "draining the last request must not remove the deployment fence")
			require.NoError(t, client.Del(ctx, fenceKey).Err())
			acquired, err = TryAcquireChannelConcurrency(incoming, channel)
			require.NoError(t, err)
			require.True(t, acquired)
			active, err = client.ZCard(ctx, activeKey).Result()
			require.NoError(t, err)
			assert.EqualValues(t, 1, active)
			ReleaseChannelConcurrency(incoming)
			active, err = client.ZCard(ctx, activeKey).Result()
			require.NoError(t, err)
			assert.Zero(t, active)
		})
	}
}

func TestChannelMaintenanceRoutingWaitsWithoutCapacity(t *testing.T) {
	for _, backend := range []string{"real_redis", "redis"} {
		t.Run(backend, func(t *testing.T) {
			// Skip an unavailable optional backend before creating its fixtures.
			useChannelConcurrencyBackend(t, backend)
			for _, route := range []struct {
				name   string
				state  string
				pin    int
				retry  bool
				ws     bool
				target int
			}{
				{name: "primary_ready", state: "ready", target: 7141},
				{name: "primary_empty", state: "empty", target: 15},
				{name: "fixed_primary", state: "ready", pin: 7141, target: 7141},
				{name: "fixed_backup", state: "ready", pin: 15, target: 15},
				{name: "ordinary_retry_backup", state: "ready", retry: true, target: 15},
				{name: "ordinary_retry_third_channel", state: "ready", retry: true, target: 7143},
				{name: "reused_websocket_primary", state: "ready", ws: true, target: 7141},
				{name: "reused_websocket_backup", state: "ready", ws: true, target: 15},
			} {
				for _, resume := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/resume_%t", route.name, resume), func(t *testing.T) {
						db := channelFailoverDatabase(t, sqlite.Open(":memory:"))
						primary, setting := channelFailoverFixture(t, db, true)
						if route.retry {
							for id, priority := range map[int]int{primary.Id: 200, route.target: 100} {
								require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", id).Update("priority", priority).Error)
								require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", id).Update("priority", priority).Error)
							}
							model.InitChannelCache()
						}
						recordChannelFailoverAvailability(t, primary, setting, route.state)
						client := useChannelConcurrencyBackend(t, backend)
						ctx := context.Background()
						fenceKey := "newapi:maintenance:channel:7141"
						keys := []string{fenceKey, "channel_concurrency:7141", "channel_concurrency:15", "channel_concurrency:7143"}
						require.NoError(t, client.Del(ctx, keys...).Err())
						t.Cleanup(func() { assert.NoError(t, client.Del(ctx, keys...).Err()) })
						c := newChannelFailoverContext(t)
						requestCtx, cancel := context.WithCancel(c.Request.Context())
						c.Request = c.Request.WithContext(requestCtx)
						common.SetContextKey(c, constant.ContextKeyOriginalModel, "alias")
						common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
						if route.pin != 0 {
							GetChannelConstraints(c).AddPin(dto.ChannelPin{ChannelId: route.pin, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt})
						}
						target, err := model.CacheGetChannel(route.target)
						require.NoError(t, err)
						if route.ws {
							require.Nil(t, AcquireChannelConcurrency(c, target))
							ReleaseChannelConcurrency(c) // the previous create on this connection finished
						}
						require.NoError(t, client.Set(ctx, fenceKey, "deployment", 0).Err())
						hook := &channelMaintenanceWaitHook{waiting: make(chan struct{})}
						client.AddHook(hook)
						type result struct {
							channel      *model.Channel
							selectionErr *ChannelSelectError
							err          error
							apiErr       *types.NewAPIError
						}
						completed := make(chan result, 1)
						release := make(chan struct{})
						ownerDone := make(chan struct{})
						var finish sync.Once
						t.Cleanup(func() {
							cancel()
							finish.Do(func() { close(release) })
							<-ownerDone
						})
						go func() {
							defer close(ownerDone)
							defer ReleaseChannelConcurrency(c)
							var got result
							param := &RetryParam{Ctx: c, ModelName: "alias", TokenGroup: "default"}
							switch {
							case route.ws:
								got.apiErr = AcquireChannelConcurrency(c, target)
							case route.retry:
								param.SetRetry(1)
								got.channel, _, got.err = CacheGetRandomSatisfiedChannel(param)
							default:
								got.channel, _, got.selectionErr = SelectChannelForRequest(c, "alias", param)
							}
							completed <- got
							<-release
						}()
						watchdog := time.NewTimer(5 * time.Second)
						defer watchdog.Stop()
						select {
						case <-hook.waiting:
						case got := <-completed:
							require.FailNow(t, "request bypassed maintenance waiting", "%+v", got)
						case <-watchdog.C:
							t.Fatal("request did not reach the deployment fence")
						}
						for _, key := range keys[1:] {
							active, err := client.ZCard(ctx, key).Result()
							require.NoError(t, err)
							assert.Zero(t, active, "waiting must not reserve capacity on %s", key)
						}
						select {
						case got := <-completed:
							require.FailNow(t, "request returned while deployment was fenced", "%+v", got)
						default:
						}
						if resume {
							require.NoError(t, client.Del(ctx, fenceKey).Err())
						} else {
							cancel()
						}
						var got result
						select {
						case got = <-completed:
						case <-watchdog.C:
							t.Fatal("request did not leave maintenance waiting")
						}
						if resume {
							require.NoError(t, got.err)
							require.Nil(t, got.selectionErr)
							require.Nil(t, got.apiErr)
							if !route.ws {
								require.NotNil(t, got.channel)
								assert.Equal(t, target.Id, got.channel.Id)
							}
							active, err := client.ZCard(ctx, "channel_concurrency:"+strconv.Itoa(target.Id)).Result()
							require.NoError(t, err)
							assert.EqualValues(t, 1, active, "resuming reserves exactly one upstream attempt")
						} else {
							assert.Nil(t, got.channel, "cancellation cannot select a backup or a third channel")
							if route.retry {
								assert.ErrorIs(t, got.err, context.Canceled)
							}
							if route.ws {
								require.NotNil(t, got.apiErr, "a cancelled WebSocket turn must not be admitted without a lease")
								assert.True(t, types.IsSkipRetryError(got.apiErr))
							}
						}
						finish.Do(func() { close(release) })
						<-ownerDone
						for _, key := range keys[1:] {
							active, err := client.ZCard(ctx, key).Result()
							require.NoError(t, err)
							assert.Zero(t, active, "the request owner released %s", key)
						}
						// Gin belongs to the owner until ownerDone closes.
						var reasons []string
						for _, event := range RequestPolicy(c).Events() {
							if event.Decision.Source == "channel_maintenance" {
								assert.Equal(t, "observe", event.Decision.Action)
								reasons = append(reasons, event.Decision.Reason)
							}
						}
						wantReasons := []string{"deployment_wait"}
						if resume {
							wantReasons = append(wantReasons, "deployment_resumed")
						}
						assert.Equal(t, wantReasons, reasons)
					})
				}
			}
		})
	}
}

func TestChannelMaintenanceExhaustedBudgetStopsRetry(t *testing.T) {
	for _, backend := range []string{"real_redis", "redis"} {
		t.Run(backend, func(t *testing.T) {
			db := channelFailoverDatabase(t, sqlite.Open(":memory:"))
			primary, setting := channelFailoverFixture(t, db, true)
			client := useChannelConcurrencyBackend(t, backend)
			ctx := context.Background()
			fenceKey := "newapi:maintenance:channel:7141"
			keys := []string{fenceKey, "channel_concurrency:7141", "channel_concurrency:15", "channel_concurrency:7143"}
			require.NoError(t, client.Del(ctx, keys...).Err())
			t.Cleanup(func() { assert.NoError(t, client.Del(ctx, keys...).Err()) })
			recordChannelFailoverAvailability(t, primary, setting, "ready")
			c := newChannelFailoverContext(t)
			assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, c).Id)
			ReleaseChannelConcurrency(c)
			common.SetContextKey(c, constant.ContextKeyOriginalModel, "alias")
			common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
			// The request already spent its maintenance budget on an earlier attempt.
			c.Set(channelMaintenanceDeadlineKey, time.Now().Add(-time.Minute))
			require.NoError(t, client.Set(ctx, fenceKey, "deployment", 0).Err())
			channel, _, err := CacheGetRandomSatisfiedChannel(&RetryParam{Ctx: c, ModelName: "alias", TokenGroup: "default", Retry: common.GetPointer(1)})
			assert.Nil(t, channel)
			require.Error(t, err)
			apiErr := ChannelConcurrencyError(c, err)
			for _, failure := range []*types.NewAPIError{apiErr, AcquireChannelConcurrency(c, primary)} {
				require.NotNil(t, failure)
				assert.Equal(t, types.ErrorCode("channel_maintenance_timeout"), failure.GetErrorCode())
				assert.Equal(t, http.StatusServiceUnavailable, failure.StatusCode)
				assert.True(t, types.IsSkipRetryError(failure))
				assert.Equal(t, PolicyDecision{Action: "stop", Reason: "deployment_timeout", Source: "channel_maintenance"}, DecideRelayRetry(c, failure, 10), "a remaining backup/retry budget cannot replay a maintenance timeout")
			}
			assert.NoError(t, c.Request.Context().Err(), "the maintenance budget, not client cancellation, stopped the request")
			next := newChannelFailoverContext(t)
			next.Set(channelMaintenanceDeadlineKey, time.Now().Add(-time.Minute))
			channel, _, selectionErr := SelectChannelForRequest(next, "alias", &RetryParam{Ctx: next, ModelName: "alias", TokenGroup: "default"})
			assert.Nil(t, channel)
			require.NotNil(t, selectionErr)
			assert.Equal(t, types.ErrorCode("channel_maintenance_timeout"), selectionErr.Code)
			assert.Equal(t, http.StatusServiceUnavailable, selectionErr.StatusCode)
			for _, key := range keys[1:] {
				active, err := client.ZCard(ctx, key).Result()
				require.NoError(t, err)
				assert.Zero(t, active)
			}
		})
	}
}

func TestChannelMaintenanceRedisLossStopsRetry(t *testing.T) {
	for _, backend := range []string{"real_redis", "redis"} {
		t.Run(backend, func(t *testing.T) {
			db := channelFailoverDatabase(t, sqlite.Open(":memory:"))
			primary, setting := channelFailoverFixture(t, db, true)
			client := useChannelConcurrencyBackend(t, backend)
			ctx := context.Background()
			fenceKey := "newapi:maintenance:channel:7141"
			keys := []string{fenceKey, "channel_concurrency:7141", "channel_concurrency:15", "channel_concurrency:7143"}
			require.NoError(t, client.Del(ctx, keys...).Err())
			t.Cleanup(func() { assert.NoError(t, client.Del(ctx, keys...).Err()) })
			recordChannelFailoverAvailability(t, primary, setting, "ready")
			c := newChannelFailoverContext(t)
			assert.Equal(t, primary.Id, selectChannelFailoverForTest(t, c).Id)
			ReleaseChannelConcurrency(c)
			requestCtx, cancel := context.WithCancel(c.Request.Context())
			c.Request = c.Request.WithContext(requestCtx)
			// Close only the request client's pool; the shared Redis service stays up.
			waitClient := redis.NewClient(client.Options())
			common.RDB = waitClient
			clientClosed := false
			t.Cleanup(func() {
				if !clientClosed {
					assert.NoError(t, waitClient.Close())
				}
			})
			require.NoError(t, client.Set(ctx, fenceKey, "deployment", 0).Err())
			hook := &channelMaintenanceWaitHook{waiting: make(chan struct{})}
			waitClient.AddHook(hook)
			type result struct {
				channel  *model.Channel
				apiErr   *types.NewAPIError
				decision PolicyDecision
			}
			completed := make(chan result, 1)
			ownerDone := make(chan struct{})
			t.Cleanup(func() { cancel(); <-ownerDone })
			go func() {
				defer close(ownerDone)
				defer ReleaseChannelConcurrency(c)
				channel, _, err := CacheGetRandomSatisfiedChannel(&RetryParam{Ctx: c, ModelName: "alias", TokenGroup: "default", Retry: common.GetPointer(1)})
				apiErr := ChannelConcurrencyError(c, err)
				completed <- result{channel: channel, apiErr: apiErr, decision: DecideRelayRetry(c, apiErr, 10)}
			}()
			watchdog := time.NewTimer(5 * time.Second)
			defer watchdog.Stop()
			select {
			case <-hook.waiting:
			case got := <-completed:
				require.FailNow(t, "request did not wait for maintenance", "%+v", got)
			case <-watchdog.C:
				t.Fatal("request did not reach the deployment fence")
			}
			for _, key := range keys[1:] {
				active, err := client.ZCard(ctx, key).Result()
				require.NoError(t, err)
				assert.Zero(t, active)
			}
			require.NoError(t, waitClient.Close())
			clientClosed = true
			var got result
			select {
			case got = <-completed:
			case <-watchdog.C:
				t.Fatal("Redis loss did not stop maintenance waiting")
			}
			<-ownerDone
			assert.Nil(t, got.channel, "Redis loss cannot select a backup or a third channel")
			require.NotNil(t, got.apiErr)
			assert.Equal(t, types.ErrorCode("channel_maintenance_unavailable"), got.apiErr.GetErrorCode())
			assert.Equal(t, http.StatusServiceUnavailable, got.apiErr.StatusCode)
			assert.True(t, types.IsSkipRetryError(got.apiErr))
			assert.Equal(t, PolicyDecision{Action: "stop", Reason: "deployment_state_unavailable", Source: "channel_maintenance"}, got.decision)
			for _, key := range keys[1:] {
				active, err := client.ZCard(ctx, key).Result()
				require.NoError(t, err)
				assert.Zero(t, active)
			}
		})
	}
}
