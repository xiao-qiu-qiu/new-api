package service

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPinnedTaskPluginChannelTypesUsesPinnedGenerationIndex(t *testing.T) {
	registry := jsplugin.NewRegistry()
	plugin, err := registry.Register(channelSelectTaskPluginSource("legacy-select", constant.ChannelTypeKling), jsplugin.Options{})
	require.NoError(t, err)

	c, _ := gin.CreateTestContext(nil)
	c.Set(jsplugin.ContextKeyPinnedPlugin, jsplugin.PinnedPlugin{
		Generation: registry.Generation(),
		Plugin:     plugin,
	})

	types, keys := pinnedTaskPluginIdentities(c, "legacy-select")
	assert.Equal(t, []int{constant.ChannelTypeKling}, types)
	assert.Equal(t, []string{"legacy-select"}, keys)
	types, keys = pinnedTaskPluginIdentities(c, "another-plugin")
	assert.Empty(t, types)
	assert.Empty(t, keys)
	types, keys = pinnedTaskPluginIdentities(nil, "legacy-select")
	assert.Empty(t, types)
	assert.Empty(t, keys)
}

func TestPinnedTaskPluginChannelTypesLeavesGenericChannelsKeyed(t *testing.T) {
	registry := jsplugin.NewRegistry()
	plugin, err := registry.Register(channelSelectTaskPluginSource("generic-select", constant.ChannelTypeTaskPlugin), jsplugin.Options{})
	require.NoError(t, err)

	c, _ := gin.CreateTestContext(nil)
	c.Set(jsplugin.ContextKeyPinnedPlugin, jsplugin.PinnedPlugin{
		Generation: registry.Generation(),
		Plugin:     plugin,
	})

	types, keys := pinnedTaskPluginIdentities(c, "generic-select")
	assert.Empty(t, types)
	assert.Equal(t, []string{"generic-select"}, keys)
}

func TestPinnedTaskPluginChannelTypesIncludesSharedEndpointProviders(t *testing.T) {
	registry := jsplugin.NewRegistry()
	_, err := registry.Register(channelSelectEndpointPluginSource("gemini-select", constant.ChannelTypeGemini), jsplugin.Options{})
	require.NoError(t, err)
	_, err = registry.Register(channelSelectEndpointPluginSource("vertex-select", constant.ChannelTypeVertexAi), jsplugin.Options{})
	require.NoError(t, err)
	candidates := registry.Generation().LookupEndpointCandidates("POST", "/v1/responses", "task-model")
	require.Len(t, candidates, 2)

	c, _ := gin.CreateTestContext(nil)
	c.Set(jsplugin.ContextKeyPinnedPlugin, jsplugin.PinnedPlugin{
		Generation: registry.Generation(),
		Plugin:     candidates[0].Plugin,
	})
	c.Set(jsplugin.ContextKeyPinnedEndpoint, jsplugin.PinnedEndpoint{
		Generation: registry.Generation(),
		Plugin:     candidates[0].Plugin,
		Protocol:   candidates[0].Protocol,
		Operation:  candidates[0].Operation,
		Model:      "task-model",
		Candidates: candidates,
	})

	AppendTaskPluginIdentityFilter(c, candidates[0].Plugin.Meta.Key)
	filters := GetChannelConstraints(c).Filters
	require.Len(t, filters, 1)
	assert.Equal(t, []int{constant.ChannelTypeGemini, constant.ChannelTypeVertexAi}, filters[0].TaskPluginChannelTypes)
	assert.Equal(t, []string{"gemini-select", "vertex-select"}, filters[0].TaskPluginKeys)
}

func channelSelectTaskPluginSource(key string, channelType int) string {
	return fmt.Sprintf(`
export const meta = {
  apiVersion: 1,
  key: %q,
  name: %q,
  version: "1.0.0",
  author: {name: "Test"},
  %s
  models: ["task-model"],
  fetchMode: "per_task",
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {taskId: "task"}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {status: "SUCCESS"}; }
`, key, key, channelSelectChannelTypesField(channelType))
}

func channelSelectEndpointPluginSource(key string, channelType int) string {
	return fmt.Sprintf(`
export const meta = {
  apiVersion: 1,
  key: %q,
  name: %q,
  version: "1.0.0",
  author: {name: "Test"},
  %s
  models: ["task-model"],
  fetchMode: "per_task",
  protocols: [{name: "openai_responses", supports: ["stream", "sync", "background"]}],
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {taskId: "task"}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {status: "SUCCESS"}; }
export const protocols = {openai_responses: {
  decodeRequest: function(ctx) { return {kind: "submit", model: "task-model", requestBody: ctx.body.value}; },
  renderEvents: function() { return {events: [], state: null, done: false}; },
  renderFinal: function() { return {output: []}; },
}};
`, key, key, channelSelectChannelTypesField(channelType))
}

func channelSelectChannelTypesField(channelType int) string {
	if channelType <= 0 || channelType == constant.ChannelTypeTaskPlugin {
		return ""
	}
	return fmt.Sprintf("channelTypes: [%d],", channelType)
}

func TestPinnedTaskPluginChannelTypesIncludesCompatibleTypes(t *testing.T) {
	registry := jsplugin.NewRegistry()
	plugin, err := registry.Register(channelSelectCompatiblePluginSource("sora-select", constant.ChannelTypeSora, constant.ChannelTypeOpenAI), jsplugin.Options{})
	require.NoError(t, err)

	c, _ := gin.CreateTestContext(nil)
	c.Set(jsplugin.ContextKeyPinnedPlugin, jsplugin.PinnedPlugin{
		Generation: registry.Generation(),
		Plugin:     plugin,
	})

	types, keys := pinnedTaskPluginIdentities(c, "sora-select")
	assert.Equal(t, []int{constant.ChannelTypeSora, constant.ChannelTypeOpenAI}, types)
	assert.Equal(t, []string{"sora-select"}, keys)
}

func channelSelectCompatiblePluginSource(key string, channelType, compatibleType int) string {
	return fmt.Sprintf(`
export const meta = {
  apiVersion: 1,
  key: %q,
  name: %q,
  version: "1.0.0",
  author: {name: "Test"},
  channelTypes: [%d, %d],
  models: ["task-model"],
  fetchMode: "per_task",
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {taskId: "task"}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {status: "SUCCESS"}; }
`, key, key, channelType, compatibleType)
}

func TestSharedType61IdentityFilterContainsAllCandidateKeys(t *testing.T) {
	registry := jsplugin.NewRegistry()
	for _, key := range []string{"alpha", "beta"} {
		_, err := registry.Register(channelSelectEndpointPluginSource(key, 0), jsplugin.Options{})
		require.NoError(t, err)
	}
	generation := registry.Generation()
	candidates := generation.LookupEndpointCandidates("POST", "/v1/responses", "task-model")
	require.Len(t, candidates, 2)
	c, _ := gin.CreateTestContext(nil)
	c.Set(jsplugin.ContextKeyPinnedEndpoint, jsplugin.PinnedEndpoint{Generation: generation, Plugin: candidates[0].Plugin, Candidates: candidates})
	AppendTaskPluginIdentityFilter(c, "alpha")
	filters := GetChannelConstraints(c).Filters
	require.Len(t, filters, 1)
	assert.Equal(t, "alpha", filters[0].TaskPluginKey)
	assert.Equal(t, []string{"alpha", "beta"}, filters[0].TaskPluginKeys)
	assert.Empty(t, filters[0].TaskPluginChannelTypes)
}

// Channel concurrency limiter regressions.

func useChannelConcurrencyBackend(t *testing.T, backend string) *redis.Client {
	t.Helper()
	previousEnabled, previousClient := common.RedisEnabled, common.RDB
	var client *redis.Client
	switch backend {
	case "local":
	case "redis":
		server := miniredis.RunT(t)
		client = redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	case "real_redis":
		redisURL := os.Getenv("CHANNEL_CONCURRENCY_TEST_REDIS_URL")
		if redisURL == "" {
			t.Skip("CHANNEL_CONCURRENCY_TEST_REDIS_URL is not set")
		}
		options, err := redis.ParseURL(redisURL)
		require.NoError(t, err)
		options.MaxRetries = -1
		client = redis.NewClient(options)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		require.NoError(t, client.Ping(ctx).Err())
	default:
		t.Fatalf("unknown concurrency backend %q", backend)
	}
	common.RedisEnabled, common.RDB = backend != "local", client
	t.Cleanup(func() {
		common.RedisEnabled, common.RDB = previousEnabled, previousClient
		if client != nil {
			assert.NoError(t, client.Close())
		}
	})
	return client
}

func newChannelConcurrencyTestContext(t *testing.T) (*gin.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)
	t.Cleanup(func() {
		ReleaseChannelConcurrency(c)
		cancel()
	})
	return c, cancel
}

func TestChannelConcurrencyLeaseOwnership(t *testing.T) {
	for _, backend := range []string{"local", "redis", "real_redis"} {
		t.Run(backend, func(t *testing.T) {
			useChannelConcurrencyBackend(t, backend)
			a := &model.Channel{Id: 910071}
			a.SetSetting(dto.ChannelSettings{MaxConcurrency: 1})
			b := &model.Channel{Id: 910072}
			b.SetSetting(dto.ChannelSettings{MaxConcurrency: 1})
			first, cancelFirst := newChannelConcurrencyTestContext(t)
			second, _ := newChannelConcurrencyTestContext(t)
			third, _ := newChannelConcurrencyTestContext(t)

			acquired, err := TryAcquireChannelConcurrency(first, a)
			require.NoError(t, err)
			require.True(t, acquired)
			acquired, err = TryAcquireChannelConcurrency(first, a)
			require.NoError(t, err)
			require.True(t, acquired, "the same request must reuse its lease")
			acquired, err = TryAcquireChannelConcurrency(second, a)
			require.NoError(t, err)
			assert.False(t, acquired)
			acquired, err = TryAcquireChannelConcurrency(second, b)
			require.NoError(t, err)
			require.True(t, acquired, "channels have independent capacity")
			acquired, err = TryAcquireChannelConcurrency(second, a)
			require.NoError(t, err)
			assert.False(t, acquired)
			acquired, err = TryAcquireChannelConcurrency(third, b)
			require.NoError(t, err)
			require.True(t, acquired, "a failed switch must still release the old channel")

			cancelFirst()
			acquired, err = TryAcquireChannelConcurrency(second, a)
			require.NoError(t, err)
			assert.False(t, acquired, "client cancellation must retain capacity until upstream finishes")
			ReleaseChannelConcurrency(first)
			ReleaseChannelConcurrency(first)
			acquired, err = TryAcquireChannelConcurrency(second, a)
			require.NoError(t, err)
			require.True(t, acquired)
			acquired, err = TryAcquireChannelConcurrency(third, a)
			require.NoError(t, err)
			assert.False(t, acquired, "duplicate release must not remove another lease")
			ReleaseChannelConcurrency(second)
			ReleaseChannelConcurrency(second)
			acquired, err = TryAcquireChannelConcurrency(third, a)
			require.NoError(t, err)
			assert.True(t, acquired)
		})
	}
}

func TestChannelConcurrencyAtomicAdmission(t *testing.T) {
	for _, backend := range []string{"local", "redis", "real_redis"} {
		t.Run(backend, func(t *testing.T) {
			useChannelConcurrencyBackend(t, backend)
			channel := &model.Channel{Id: 910073}
			channel.SetSetting(dto.ChannelSettings{MaxConcurrency: 2})
			contexts := make([]*gin.Context, 4)
			for i := range contexts {
				contexts[i], _ = newChannelConcurrencyTestContext(t)
			}
			start := make(chan struct{})
			acquired := make([]bool, len(contexts))
			errs := make([]error, len(contexts))
			var wg sync.WaitGroup
			for i, c := range contexts {
				wg.Go(func() {
					<-start
					acquired[i], errs[i] = TryAcquireChannelConcurrency(c, channel)
				})
			}
			close(start)
			wg.Wait()
			count := 0
			for i := range acquired {
				require.NoError(t, errs[i])
				if acquired[i] {
					count++
				}
			}
			assert.Equal(t, 2, count, "concurrent callers must not exceed the hard limit")
		})
	}
}

func TestChannelConcurrencyUnlimitedValidationAndContextReuse(t *testing.T) {
	useChannelConcurrencyBackend(t, "local")
	channel := &model.Channel{Id: 910074}
	c, cancelOld := newChannelConcurrencyTestContext(t)
	other, _ := newChannelConcurrencyTestContext(t)
	for _, limit := range []int{0, 1000000, -1, 1000001} {
		channel.SetSetting(dto.ChannelSettings{MaxConcurrency: limit})
		acquired, err := TryAcquireChannelConcurrency(c, channel)
		if limit < 0 || limit > 1000000 {
			require.Error(t, err)
			assert.False(t, acquired)
		} else {
			require.NoError(t, err)
			assert.True(t, acquired)
		}
		ReleaseChannelConcurrency(c)
	}
	channel.SetSetting(dto.ChannelSettings{MaxConcurrency: 1})
	acquired, err := TryAcquireChannelConcurrency(c, channel)
	require.NoError(t, err)
	require.True(t, acquired)
	ReleaseChannelConcurrency(c)
	// Simulate the pool replacing the request while its old cancellation remains
	// callable. The retired callback must never inspect or mutate this Gin value.
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	acquired, err = TryAcquireChannelConcurrency(c, channel)
	require.NoError(t, err)
	require.True(t, acquired)
	cancelOld()
	acquired, err = TryAcquireChannelConcurrency(other, channel)
	require.NoError(t, err)
	assert.False(t, acquired)
	ReleaseChannelConcurrency(c)
	cancelled, cancel := newChannelConcurrencyTestContext(t)
	cancel()
	acquired, err = TryAcquireChannelConcurrency(cancelled, channel)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, acquired)
}

func TestChannelConcurrencyUnlimitedWithoutRequest(t *testing.T) {
	useChannelConcurrencyBackend(t, "local")
	channel := &model.Channel{Id: 910076}
	channel.SetSetting(dto.ChannelSettings{MaxConcurrency: 1})
	c, _ := newChannelConcurrencyTestContext(t)
	acquired, err := TryAcquireChannelConcurrency(c, channel)
	require.NoError(t, err)
	require.True(t, acquired)
	c.Request = nil
	channel.SetSetting(dto.ChannelSettings{MaxConcurrency: 0})
	common.RedisEnabled, common.RDB = true, nil
	acquired, err = TryAcquireChannelConcurrency(c, channel)
	require.NoError(t, err)
	assert.True(t, acquired)
	acquired, err = TryAcquireChannelConcurrency(nil, channel)
	require.NoError(t, err)
	assert.True(t, acquired)
	common.RedisEnabled = false
	channel.SetSetting(dto.ChannelSettings{MaxConcurrency: 1})
	other, _ := newChannelConcurrencyTestContext(t)
	acquired, err = TryAcquireChannelConcurrency(other, channel)
	require.NoError(t, err)
	assert.True(t, acquired, "disabling the limit must release the previous bounded lease")
}

func TestChannelConcurrencyGuardOwnership(t *testing.T) {
	for _, backend := range []string{"redis", "real_redis"} {
		t.Run(backend, func(t *testing.T) {
			useChannelConcurrencyBackend(t, backend)
			channel := &model.Channel{Id: 910077}
			channel.SetSetting(dto.ChannelSettings{MaxConcurrency: 1})
			c, cancelClient := newChannelConcurrencyTestContext(t)
			acquired, err := TryAcquireChannelConcurrency(c, channel)
			require.NoError(t, err)
			require.True(t, acquired)
			guard, ok := c.Request.Context().Value(constant.ContextKeyChannelConcurrencyGuard).(context.Context)
			require.True(t, ok)
			detached := context.WithoutCancel(c.Request.Context())
			assert.Same(t, guard, detached.Value(constant.ContextKeyChannelConcurrencyGuard))
			ReleaseChannelConcurrency(c)
			assert.NoError(t, guard.Err(), "normal release cannot cancel a detached submission's guard")
			assert.NoError(t, c.Request.Context().Err(), "normal release must permit retries")

			acquired, err = TryAcquireChannelConcurrency(c, channel)
			require.NoError(t, err)
			require.True(t, acquired)
			cancelClient()
			guard = c.Request.Context().Value(constant.ContextKeyChannelConcurrencyGuard).(context.Context)
			assert.NoError(t, guard.Err(), "client loss must not cancel the capacity guard")
			ReleaseChannelConcurrency(c)

			c, _ = newChannelConcurrencyTestContext(t)
			other, _ := newChannelConcurrencyTestContext(t)
			acquired, err = TryAcquireChannelConcurrency(c, channel)
			require.NoError(t, err)
			require.True(t, acquired)
			guard = c.Request.Context().Value(constant.ContextKeyChannelConcurrencyGuard).(context.Context)
			value, _ := c.Get(channelConcurrencyContextKey)
			lease, ok := value.(*channelConcurrencyLease)
			require.True(t, ok)
			lost := fmt.Errorf("test lease ownership lost")
			lease.cancelGuard(lost)
			requestCtx := c.Request.Context()
			require.Eventually(t, func() bool { return requestCtx.Err() != nil }, time.Second, time.Millisecond)
			assert.ErrorIs(t, context.Cause(guard), lost)
			assert.ErrorIs(t, context.Cause(c.Request.Context()), lost)
			acquired, err = TryAcquireChannelConcurrency(other, channel)
			require.NoError(t, err)
			assert.False(t, acquired, "lease loss must allow the forwarding owner to stop before release")
			ReleaseChannelConcurrency(c)
			acquired, err = TryAcquireChannelConcurrency(other, channel)
			require.NoError(t, err)
			assert.True(t, acquired)
		})
	}
}

func TestChannelConcurrencyRedisFailureDoesNotFallBack(t *testing.T) {
	client := useChannelConcurrencyBackend(t, "redis")
	channel := &model.Channel{Id: 910075}
	channel.SetSetting(dto.ChannelSettings{MaxConcurrency: 1})
	c, _ := newChannelConcurrencyTestContext(t)
	failedClient := redis.NewClient(client.Options())
	require.NoError(t, failedClient.Close())
	common.RDB = failedClient
	acquired, err := TryAcquireChannelConcurrency(c, channel)
	require.Error(t, err)
	assert.False(t, acquired)
	common.RDB = nil
	acquired, err = TryAcquireChannelConcurrency(c, channel)
	require.Error(t, err)
	assert.False(t, acquired)
}

func TestChannelConcurrencyRedisLeaseScripts(t *testing.T) {
	for _, backend := range []string{"redis", "real_redis"} {
		t.Run(backend, func(t *testing.T) {
			client := useChannelConcurrencyBackend(t, backend)
			ctx := context.Background()
			key := "channel_concurrency:test:" + uuid.NewString()
			t.Cleanup(func() { assert.NoError(t, client.Del(ctx, key).Err()) })
			acquired, err := channelConcurrencyAcquireScript.Run(ctx, client, []string{key}, 1, 60000, "first").Int()
			require.NoError(t, err)
			require.Equal(t, 1, acquired)
			ttl, err := client.PTTL(ctx, key).Result()
			require.NoError(t, err)
			assert.Positive(t, ttl, "a crashed process must leave an expiring key")
			assert.LessOrEqual(t, ttl, time.Minute)
			acquired, err = channelConcurrencyAcquireScript.Run(ctx, client, []string{key}, 1, 60000, "second").Int()
			require.NoError(t, err)
			assert.Zero(t, acquired)
			now, err := client.Time(ctx).Result()
			require.NoError(t, err)
			require.NoError(t, client.ZAdd(ctx, key, &redis.Z{Score: float64(now.UnixMilli() - 1), Member: "first"}).Err())
			acquired, err = channelConcurrencyAcquireScript.Run(ctx, client, []string{key}, 1, 60000, "second").Int()
			require.NoError(t, err)
			require.Equal(t, 1, acquired, "expired process leases must be reclaimable")
			renewed, err := channelConcurrencyRenewScript.Run(ctx, client, []string{key}, 60000, "first").Int()
			require.NoError(t, err)
			assert.Zero(t, renewed, "a late heartbeat cannot revive a removed token")
			deadline, err := client.ZScore(ctx, key, "second").Result()
			require.NoError(t, err)
			renewed, err = channelConcurrencyRenewScript.Run(ctx, client, []string{key}, 120000, "second").Int()
			require.NoError(t, err)
			assert.Equal(t, 1, renewed)
			nextDeadline, err := client.ZScore(ctx, key, "second").Result()
			require.NoError(t, err)
			assert.Greater(t, nextDeadline, deadline, "stream heartbeats must extend the lease")
			lease := &channelConcurrencyLease{client: client, key: key, token: "second"}
			lease.release()
			lease.release()
			renewed, err = channelConcurrencyRenewScript.Run(ctx, client, []string{key}, 60000, "second").Int()
			require.NoError(t, err)
			assert.Zero(t, renewed)
			assert.Zero(t, client.ZCard(ctx, key).Val())
			acquired, err = channelConcurrencyAcquireScript.Run(ctx, client, []string{key}, 1, 60000, "expired").Int()
			require.NoError(t, err)
			require.Equal(t, 1, acquired)
			now, err = client.Time(ctx).Result()
			require.NoError(t, err)
			require.NoError(t, client.ZAdd(ctx, key, &redis.Z{Score: float64(now.UnixMilli() - 1), Member: "expired"}).Err())
			renewed, err = channelConcurrencyRenewScript.Run(ctx, client, []string{key}, 60000, "expired").Int()
			require.NoError(t, err)
			assert.Zero(t, renewed, "an expired lease cannot be renewed even before another acquisition")
		})
	}
}

func TestChannelConcurrencyRealRedisHeartbeatLeaseLoss(t *testing.T) {
	client := useChannelConcurrencyBackend(t, "real_redis")
	options := *client.Options()
	independent := redis.NewClient(&options)
	t.Cleanup(func() { assert.NoError(t, independent.Close()) })
	channel := &model.Channel{Id: 910078}
	channel.SetSetting(dto.ChannelSettings{MaxConcurrency: 1})
	c, _ := newChannelConcurrencyTestContext(t)
	acquired, err := TryAcquireChannelConcurrency(c, channel)
	require.NoError(t, err)
	require.True(t, acquired)
	requestCtx := c.Request.Context()
	guard, ok := requestCtx.Value(constant.ContextKeyChannelConcurrencyGuard).(context.Context)
	require.True(t, ok)
	require.NoError(t, guard.Err())
	require.NoError(t, requestCtx.Err())
	value, _ := c.Get(channelConcurrencyContextKey)
	lease, ok := value.(*channelConcurrencyLease)
	require.True(t, ok)
	deleteCtx, cancelDelete := context.WithTimeout(context.Background(), 2*time.Second)
	removed, err := independent.Del(deleteCtx, lease.key).Result()
	cancelDelete()
	require.NoError(t, err)
	require.EqualValues(t, 1, removed)

	// Exercise the unchanged production heartbeat, including its Redis reply and
	// guard callback; neither an injected cancellation nor a shortened timer.
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancelWait()
	select {
	case <-guard.Done():
	case <-waitCtx.Done():
		t.Fatal("the real heartbeat did not cancel the guard after its lease was deleted")
	}
	select {
	case <-requestCtx.Done():
	case <-waitCtx.Done():
		t.Fatal("the lost-lease guard did not cancel the forwarding request")
	}
	require.ErrorIs(t, guard.Err(), context.Canceled)
	require.ErrorContains(t, context.Cause(guard), "channel concurrency lease expired or was removed")
	require.ErrorIs(t, context.Cause(requestCtx), context.Cause(guard))
	acquired, err = TryAcquireChannelConcurrency(c, channel)
	require.ErrorIs(t, err, context.Cause(guard))
	assert.False(t, acquired, "a request with a lost lease cannot reuse its reservation")
	ReleaseChannelConcurrency(c)
	fresh, _ := newChannelConcurrencyTestContext(t)
	acquired, err = TryAcquireChannelConcurrency(fresh, channel)
	require.NoError(t, err)
	assert.True(t, acquired, "a fresh request can acquire capacity after the old owner releases")
}
