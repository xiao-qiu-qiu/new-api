package active_requests

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newTestTracker(t *testing.T, client *redis.Client) *tracker {
	t.Helper()
	registry := newTracker(client)
	if registry.client != nil {
		t.Cleanup(func() { _ = registry.client.Close() })
	}
	return registry
}

func TestHandleLifetimeAndContext(t *testing.T) {
	registry := newTestTracker(t, nil)
	assert.Nil(t, registry.start(Record{UserID: 1}))
	assert.Nil(t, registry.start(Record{RequestID: "anonymous"}))
	handle := registry.start(Record{RequestID: "request", UserID: 1})
	require.NotNil(t, handle)
	handle.SetChannel(7, "primary", "premium")
	handle.SetStream(true)
	handle.SetAttempt(2)
	handle.SetState(StatusStreaming)
	handle.MarkClientGone()
	handle.SetState(StatusStreaming)
	handle.SetState(StatusRetrying)
	handle.SetState("invalid")

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	requestCtx, cancel := context.WithCancel(context.Background())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestCtx)
	SetContext(ctx, handle)
	require.Same(t, handle, FromContext(ctx))
	cancel()
	result, err := registry.query(context.Background(), Filter{UserID: 1})
	require.NoError(t, err)
	require.Len(t, result.Items, 1, "client cancellation must not finish upstream work")
	record := result.Items[0]
	assert.Equal(t, "request", record.RequestID)
	assert.Equal(t, 7, record.Channel)
	assert.Equal(t, "primary", record.ChannelName)
	assert.Equal(t, "premium", record.Group)
	assert.Equal(t, StatusDraining, record.Status)
	assert.Equal(t, 2, record.Attempt)
	assert.True(t, record.IsStream)
	assert.True(t, record.ClientGone)
	assert.Positive(t, record.StartedAt)
	assert.GreaterOrEqual(t, record.UpdatedAt, record.StartedAt)

	// A duplicate external request ID cannot let an older owner finish a new request.
	second := registry.start(Record{RequestID: "request", UserID: 2})
	handle.Finish()
	handle.Finish()
	handle.SetState(StatusWaiting)
	result, err = registry.query(context.Background(), Filter{})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	assert.Equal(t, 2, result.Items[0].UserID)
	second.Finish()
	result, err = registry.query(context.Background(), Filter{})
	require.NoError(t, err)
	assert.Zero(t, result.Total)
	assert.NotNil(t, result.Items)

	var absent *Handle
	absent.SetState(StatusWaiting)
	absent.SetChannel(1, "", "")
	absent.SetStream(true)
	absent.SetAttempt(1)
	absent.MarkClientGone()
	absent.Finish()
	SetContext(nil, absent)
	SetContext(ctx, absent)
	assert.Nil(t, FromContext(nil))
	assert.Nil(t, FromContext(ctx))
}

func TestQueryFilters(t *testing.T) {
	registry := newTestTracker(t, nil)
	for _, record := range []Record{
		{RequestID: "first", UserID: 1, Username: "alice", TokenName: "my!token", ModelName: "gpt_4o", Group: "standard", Channel: 7, StartedAt: 1700000000999},
		{RequestID: "second", UserID: 2, Username: "bob", TokenName: "work", ModelName: "gpt-other", Group: "premium", Channel: 8, StartedAt: 1700000001000},
		{RequestID: "third", UserID: 1, Username: "alice", TokenName: "work", ModelName: "claude", Group: "standard", Channel: 7, StartedAt: 1700000002000},
	} {
		registry.start(record)
	}
	for _, tc := range []struct {
		name   string
		filter Filter
		ids    []string
	}{
		{"all", Filter{}, []string{"third", "second", "first"}},
		{"user isolation", Filter{UserID: 1}, []string{"third", "first"}},
		{"request cannot bypass user", Filter{UserID: 1, RequestID: "second"}, nil},
		{"invalid identity", Filter{UserID: -1}, nil},
		{"substring", Filter{ModelName: "gpt"}, []string{"second", "first"}},
		{"literal underscore", Filter{ModelName: "gpt_4"}, []string{"first"}},
		{"literal exclamation", Filter{TokenName: "!tok"}, []string{"first"}},
		{"prefix wildcard", Filter{ModelName: "gpt-%"}, []string{"second"}},
		{"suffix wildcard", Filter{ModelName: "%4o"}, []string{"first"}},
		{"substring wildcard", Filter{ModelName: "%pt_4%"}, []string{"first"}},
		{"middle wildcard", Filter{ModelName: "g%4%o"}, []string{"first"}},
		{"username", Filter{Username: "lic"}, []string{"third", "first"}},
		{"combined", Filter{TokenName: "work", Channel: 7, Group: "standard"}, []string{"third"}},
		{"exact request", Filter{RequestID: "second"}, []string{"second"}},
		{"request substring forbidden", Filter{RequestID: "sec"}, nil},
		{"exact group", Filter{Group: "stand"}, nil},
		{"inclusive second", Filter{StartTimestamp: 1700000000, EndTimestamp: 1700000000}, []string{"first"}},
		{"timestamp range", Filter{StartTimestamp: 1700000001, EndTimestamp: 1700000002}, []string{"third", "second"}},
		{"consume", Filter{Type: 2}, []string{"third", "second", "first"}},
		{"error type", Filter{Type: 5}, nil},
		{"unobservable upstream", Filter{UpstreamRequestID: "upstream"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := registry.query(context.Background(), tc.filter)
			require.NoError(t, err)
			ids := make([]string, 0, len(result.Items))
			for _, item := range result.Items {
				ids = append(ids, item.RequestID)
			}
			assert.Equal(t, len(tc.ids), result.Total)
			assert.Equal(t, append([]string{}, tc.ids...), ids)
			assert.Positive(t, result.ServerTime)
		})
	}
	for _, pattern := range []string{"a%%b", "ab%c%d%e", "%a"} {
		_, err := registry.query(context.Background(), Filter{ModelName: pattern})
		require.Error(t, err, pattern)
	}
	assert.False(t, matchesName("aba", "ab%ba"), "literal pieces cannot overlap")
}

func TestQueryLimitAndConcurrentOwners(t *testing.T) {
	registry := newTestTracker(t, nil)
	handles := make([]*Handle, 105)
	var workers sync.WaitGroup
	for i := range handles {
		handles[i] = registry.start(Record{RequestID: fmt.Sprintf("request-%03d", i), UserID: 1, StartedAt: 1700000000000 + int64(i)})
		workers.Go(func() {
			handles[i].SetState(StatusWaiting)
			handles[i].SetChannel(i+1, "channel", "group")
			handles[i].SetAttempt(1)
		})
	}
	workers.Wait()
	result, err := registry.query(context.Background(), Filter{UserID: 1})
	require.NoError(t, err)
	assert.Equal(t, 105, result.Total)
	require.Len(t, result.Items, MaxResults)
	assert.Equal(t, "request-104", result.Items[0].RequestID)
	assert.Equal(t, "request-005", result.Items[99].RequestID)
	for _, handle := range handles {
		workers.Go(func() {
			handle.Finish()
			handle.SetState(StatusRetrying)
		})
	}
	workers.Wait()
	result, err = registry.query(context.Background(), Filter{})
	require.NoError(t, err)
	assert.Empty(t, result.Items)
	assert.Zero(t, result.Total)
}

func TestQueryNameCaseMatchesLogDatabase(t *testing.T) {
	previous := common.LogDatabaseType()
	t.Cleanup(func() { common.SetLogDatabaseType(previous) })
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	registry := newTestTracker(t, nil)
	registry.start(Record{RequestID: "case", UserID: 1, ModelName: "gpt-test", Username: "alice", TokenName: "work"})
	registry.start(Record{RequestID: "unicode", UserID: 2, ModelName: "éclair"})
	for _, kind := range []common.DatabaseType{common.DatabaseTypeSQLite, common.DatabaseTypePostgreSQL, common.DatabaseTypeClickHouse} {
		t.Run(string(kind), func(t *testing.T) {
			common.SetLogDatabaseType(kind)
			for _, filter := range []Filter{{ModelName: "GPT"}, {Username: "%ALI%"}, {TokenName: "WOR%"}} {
				result, err := registry.query(t.Context(), filter)
				require.NoError(t, err)
				expected := 0
				if kind == common.DatabaseTypeSQLite {
					expected = 1
				}
				assert.Equal(t, expected, result.Total, "%+v", filter)
			}
		})
	}
	common.SetLogDatabaseType(common.DatabaseTypeSQLite)
	for _, tc := range []struct{ value, pattern, search string }{
		{"gpt-test", "%GPT%", "GPT"},
		{"alice", "%ALI%", "%ALI%"},
		{"work", "WOR%", "WOR%"},
		{"éclair", "%É%", "É"},
	} {
		var databaseMatch bool
		require.NoError(t, db.Raw("SELECT ? LIKE ? ESCAPE '!'", tc.value, tc.pattern).Scan(&databaseMatch).Error)
		filter := Filter{ModelName: tc.search}
		if tc.value == "alice" {
			filter = Filter{Username: tc.search}
		} else if tc.value == "work" {
			filter = Filter{TokenName: tc.search}
		}
		result, err := registry.query(t.Context(), filter)
		require.NoError(t, err)
		assert.Equal(t, databaseMatch, result.Total == 1, "%s LIKE %s", tc.value, tc.pattern)
	}
}

func TestRedisSharingHeartbeatExpiryAndFinish(t *testing.T) {
	server := miniredis.RunT(t)
	now := time.Now()
	server.SetTime(now)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	owner := newTestTracker(t, client)
	observer := newTestTracker(t, client)
	handle := owner.start(Record{RequestID: "shared", UserID: 1, ModelName: "gpt-test", Channel: 7, Group: "first"})
	owner.syncRedis(false)
	for _, filter := range []Filter{{}, {UserID: 1}, {RequestID: "shared"}, {Channel: 7}, {Group: "first"}} {
		result, err := observer.queryRedis(context.Background(), filter)
		require.NoError(t, err)
		require.Len(t, result.Items, 1)
		assert.Equal(t, 1, result.Total)
	}
	result, err := owner.query(context.Background(), Filter{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Total, "local and Redis copies must not be counted twice")

	server.FastForward(60 * time.Second)
	server.SetTime(now.Add(60 * time.Second))
	owner.syncRedis(true)
	server.FastForward(60 * time.Second)
	server.SetTime(now.Add(120 * time.Second))
	result, err = observer.queryRedis(context.Background(), Filter{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Total, "heartbeat keeps long-running requests visible")

	handle.SetChannel(8, "backup", "second")
	handle.SetState(StatusRetrying)
	owner.syncRedis(false)
	result, err = observer.queryRedis(context.Background(), Filter{Channel: 7})
	require.NoError(t, err)
	assert.Zero(t, result.Total, "stale index membership must not match a previous channel")
	result, err = observer.queryRedis(context.Background(), Filter{Group: "second"})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	assert.Equal(t, StatusRetrying, result.Items[0].Status)
	handle.Finish()
	owner.syncRedis(false)
	result, err = observer.queryRedis(context.Background(), Filter{})
	require.NoError(t, err)
	assert.Zero(t, result.Total)

	owner.start(Record{RequestID: "crashed-owner", UserID: 2})
	owner.syncRedis(false)
	server.FastForward(leaseTTL)
	server.SetTime(now.Add(120*time.Second + leaseTTL))
	result, err = observer.queryRedis(context.Background(), Filter{})
	require.NoError(t, err)
	assert.Zero(t, result.Total, "an owner without heartbeats must expire")
}

type blockedRedis struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	failing atomic.Bool
}

func (hook *blockedRedis) BeforeProcess(ctx context.Context, _ redis.Cmder) (context.Context, error) {
	hook.once.Do(func() {
		close(hook.entered)
		<-hook.release
	})
	if hook.failing.Load() {
		return ctx, errors.New("Redis unavailable")
	}
	return ctx, nil
}
func (*blockedRedis) AfterProcess(context.Context, redis.Cmder) error { return nil }
func (hook *blockedRedis) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	if hook.failing.Load() {
		return ctx, errors.New("Redis unavailable")
	}
	return ctx, nil
}
func (*blockedRedis) AfterProcessPipeline(context.Context, []redis.Cmder) error { return nil }

func TestUnavailableRedisDoesNotBlockRequestOwners(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	registry := newTestTracker(t, client)
	hook := &blockedRedis{entered: make(chan struct{}), release: make(chan struct{})}
	handle := registry.start(Record{RequestID: "unavailable", UserID: 1})
	registry.syncRedis(false)
	registry.client.AddHook(hook)
	hook.failing.Store(true)
	handle.SetState(StatusWaiting)
	done := make(chan struct{})
	go func() {
		registry.syncRedis(false)
		close(done)
	}()
	<-hook.entered
	handle.SetState(StatusWaiting)
	handle.Finish()
	next := registry.start(Record{RequestID: "still-works", UserID: 1})
	next.MarkClientGone()
	close(hook.release)
	<-done
	registry.syncRedis(false) // A failed deletion must remain queued for recovery.
	result, err := registry.query(context.Background(), Filter{})
	require.Error(t, err, "unavailable shared state must not appear to be an empty remote list")
	assert.Empty(t, result.Items)
	hook.failing.Store(false)
	registry.syncRedis(false)
	observer := newTestTracker(t, client)
	result, err = observer.query(context.Background(), Filter{})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	assert.Equal(t, "still-works", result.Items[0].RequestID)
	assert.True(t, result.Items[0].ClientGone)
	assert.Equal(t, StatusDraining, result.Items[0].Status)
}
