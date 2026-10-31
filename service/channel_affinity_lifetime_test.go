package service

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type affinityLifetimeFixture struct {
	t      *testing.T
	now    time.Time
	server *miniredis.Miniredis
}

func newAffinityLifetimeFixture(t *testing.T, redisEnabled bool) *affinityLifetimeFixture {
	t.Helper()
	f := &affinityLifetimeFixture{t: t, now: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
	setting := operation_setting.GetChannelAffinitySetting()
	previousSetting := *setting
	previousRedis, previousRDB := common.RedisEnabled, common.RDB
	previousCache, previousLifetimeCache := channelAffinityCache, channelAffinityLifetimeCache
	previousNow := channelAffinityLifetimeNow
	*setting = operation_setting.ChannelAffinitySetting{
		Enabled: true, SwitchOnSuccess: true, DefaultTTLSeconds: 3600, MaxEntries: 64,
		Rules: []operation_setting.ChannelAffinityRule{{
			Name: "cpa-lifetime", ModelRegex: []string{"^lifetime-model$"}, SessionMode: "prefer",
			KeySources:         []operation_setting.ChannelAffinityKeySource{{Type: "request_header", Key: "X-Session"}},
			FallbackChannelIDs: []int{56, 40, 19}, IncludeRuleName: true,
			ChannelLifetimes: []operation_setting.ChannelAffinityLifetime{{
				ChannelID: 19, IdleTTLSeconds: 480, MaxResidenceSeconds: 900,
			}},
		}},
	}
	common.RedisEnabled, common.RDB = redisEnabled, nil
	if redisEnabled {
		f.server = miniredis.RunT(t)
		client := redis.NewClient(&redis.Options{Addr: f.server.Addr()})
		common.RDB = client
		t.Cleanup(func() { assert.NoError(t, client.Close()) })
	}
	channelAffinityCacheOnce, channelAffinityLifetimeOnce = sync.Once{}, sync.Once{}
	channelAffinityCache, channelAffinityLifetimeCache = nil, nil
	channelAffinityLifetimeNow = func() time.Time { return f.now }
	t.Cleanup(func() {
		*setting = previousSetting
		common.RedisEnabled, common.RDB = previousRedis, previousRDB
		channelAffinityLifetimeNow = previousNow
		channelAffinityCacheOnce, channelAffinityLifetimeOnce = sync.Once{}, sync.Once{}
		channelAffinityCache, channelAffinityLifetimeCache = nil, nil
		if previousCache != nil {
			channelAffinityCacheOnce.Do(func() { channelAffinityCache = previousCache })
		}
		if previousLifetimeCache != nil {
			channelAffinityLifetimeOnce.Do(func() { channelAffinityLifetimeCache = previousLifetimeCache })
		}
	})
	return f
}

func (f *affinityLifetimeFixture) advance(d time.Duration) {
	f.now = f.now.Add(d)
	if f.server != nil {
		f.server.FastForward(d)
	}
}

func (f *affinityLifetimeFixture) context(session string) *gin.Context {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Session", session)
	return ctx
}

func (f *affinityLifetimeFixture) request(session string) (*gin.Context, int, bool) {
	ctx := f.context(session)
	id, found := GetPreferredChannelByAffinity(ctx, "lifetime-model", "default")
	return ctx, id, found
}

func (f *affinityLifetimeFixture) complete(ctx *gin.Context, channelID int) {
	ctx.Set("channel_id", channelID)
	RecordChannelAffinity(ctx, channelID)
}

func (f *affinityLifetimeFixture) state(ctx *gin.Context) (channelAffinityLifetimeState, bool) {
	f.t.Helper()
	meta, ok := getChannelAffinityMeta(ctx)
	require.True(f.t, ok)
	state, found, err := getChannelAffinityLifetimeCache().Get(channelAffinityLifetimeSuffix(meta.CacheKey))
	require.NoError(f.t, err)
	return state, found
}

func TestChannelAffinityLifetime(t *testing.T) {
	for _, redisEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("redis=%t", redisEnabled), func(t *testing.T) {
			t.Run("legacy_and_unlimited_channels_keep_sliding_TTL", func(t *testing.T) {
				f := newAffinityLifetimeFixture(t, redisEnabled)
				setting := operation_setting.GetChannelAffinitySetting()
				legacyKey := buildChannelAffinityCacheKeySuffix(setting.Rules[0], "lifetime-model", "default", "legacy")
				require.NoError(t, getChannelAffinityCache().SetWithTTL(legacyKey, 19, time.Hour))
				legacy, id, found := f.request("legacy")
				require.True(t, found)
				assert.Equal(t, 19, id)
				f.advance(901 * time.Second)
				f.complete(legacy, 19)
				_, found = f.state(legacy)
				assert.False(t, found, "existing integer CPA bindings must not be converted")
				_, id, found = f.request("legacy")
				assert.True(t, found)
				assert.Equal(t, 19, id)
				if f.server != nil {
					assert.Equal(t, time.Hour, f.server.TTL(getChannelAffinityCache().FullKey(legacyKey)))
				}

				for _, id := range []int{56, 40} {
					key := fmt.Sprintf("unlimited-%d", id)
					ctx, _, _ := f.request(key)
					f.complete(ctx, id)
					f.advance(500 * time.Second)
					ctx, got, found := f.request(key)
					require.True(t, found)
					assert.Equal(t, id, got)
					f.complete(ctx, id)
					_, found = f.state(ctx)
					assert.False(t, found)
					if f.server != nil {
						meta, _ := getChannelAffinityMeta(ctx)
						assert.Equal(t, time.Hour, f.server.TTL(meta.CacheKey))
					}
				}

				setting.Rules[0].ChannelLifetimes = nil
				ctx, _, _ := f.request("unconfigured-cpa")
				f.complete(ctx, 19)
				f.advance(901 * time.Second)
				_, id, found = f.request("unconfigured-cpa")
				assert.True(t, found)
				assert.Equal(t, 19, id)
				_, found = f.state(ctx)
				assert.False(t, found)
			})

			t.Run("success_renews_idle_and_caps_at_fixed_residence", func(t *testing.T) {
				f := newAffinityLifetimeFixture(t, redisEnabled)
				ctx, _, found := f.request("bounded")
				require.False(t, found)
				f.complete(ctx, 19)
				initial, found := f.state(ctx)
				require.True(t, found)
				assert.Equal(t, f.now.UnixMilli(), initial.BoundAt)
				assert.Equal(t, f.now.Add(480*time.Second).UnixMilli(), initial.IdleExpiresAt)
				assert.Equal(t, f.now.Add(900*time.Second).UnixMilli(), initial.AbsoluteExpiresAt)
				meta, _ := getChannelAffinityMeta(ctx)
				if f.server != nil {
					raw, err := f.server.Get(meta.CacheKey)
					require.NoError(t, err)
					assert.Equal(t, "19", raw, "v1 stays compatible with the integer codec")
					assert.Equal(t, 480*time.Second, f.server.TTL(meta.CacheKey))
					assert.GreaterOrEqual(t, f.server.TTL(getChannelAffinityLifetimeCache().FullKey(channelAffinityLifetimeSuffix(meta.CacheKey))), time.Hour)
				}
				for _, step := range []struct {
					advance time.Duration
					idleTTL time.Duration
				}{{400 * time.Second, 480 * time.Second}, {400 * time.Second, 100 * time.Second}} {
					f.advance(step.advance)
					ctx, id, found := f.request("bounded")
					require.True(t, found)
					assert.Equal(t, 19, id)
					f.complete(ctx, 19)
					state, found := f.state(ctx)
					require.True(t, found)
					assert.Equal(t, initial.Epoch, state.Epoch)
					assert.Equal(t, initial.BoundAt, state.BoundAt)
					assert.Equal(t, initial.AbsoluteExpiresAt, state.AbsoluteExpiresAt)
					assert.Equal(t, f.now.Add(step.idleTTL).UnixMilli(), state.IdleExpiresAt)
					if f.server != nil {
						assert.Equal(t, step.idleTTL, f.server.TTL(meta.CacheKey))
					}
				}
				f.advance(100 * time.Second)
				_, _, found = f.request("bounded")
				assert.False(t, found, "the absolute boundary is exclusive even after successful renewals")
			})

			t.Run("expiry_restarts_chain_and_fences_old_completions", func(t *testing.T) {
				f := newAffinityLifetimeFixture(t, redisEnabled)
				db := setupChannelSelectAutoGroupsTest(t)
				for _, id := range []int{56, 40, 19} {
					createChannelSelectAutoGroupsChannel(t, db, id, "default", "lifetime-model")
				}
				model.InitChannelCache()
				for _, expiry := range []string{"idle", "absolute"} {
					t.Run(expiry, func(t *testing.T) {
						seed, _, _ := f.request(expiry)
						f.complete(seed, 19)
						initial, _ := f.state(seed)
						late, id, found := f.request(expiry)
						require.True(t, found)
						assert.Equal(t, 19, id)
						if expiry == "absolute" {
							for range 2 {
								f.advance(400 * time.Second)
								active, _, _ := f.request(expiry)
								f.complete(active, 19)
							}
							f.advance(100 * time.Second)
						} else {
							f.advance(480 * time.Second)
						}
						// Simulate an old node renewing only the v1 integer after expiry.
						meta, _ := getChannelAffinityMeta(seed)
						require.NoError(t, getChannelAffinityCache().SetWithTTL(meta.CacheKey, 19, time.Hour))
						f.complete(late, 19)
						ctx := f.context(expiry)
						channel, _, selectErr := SelectChannelForRequest(ctx, "lifetime-model", &RetryParam{Ctx: ctx, TokenGroup: "default", ModelName: "lifetime-model"})
						require.Nil(t, selectErr)
						require.NotNil(t, channel)
						assert.Equal(t, 56, channel.Id)
						expired, _ := f.state(ctx)
						assert.Equal(t, initial.Epoch, expired.Epoch, "reads never erase the completion fence")
						assert.True(t, expired.expired(f.now.UnixMilli()), "the slow request must not revive its residence")

						// This new request went back through the chain and reached CPA.
						follower, _, found := f.request(expiry)
						require.False(t, found)
						f.complete(ctx, 19)
						fresh, found := f.state(ctx)
						require.True(t, found)
						assert.NotEqual(t, initial.Epoch, fresh.Epoch)
						assert.Equal(t, f.now.Add(900*time.Second).UnixMilli(), fresh.AbsoluteExpiresAt)
						f.advance(time.Second)
						f.complete(follower, 19)
						joined, _ := f.state(follower)
						assert.Equal(t, fresh.Epoch, joined.Epoch)
						assert.Equal(t, fresh.BoundAt, joined.BoundAt)
						assert.Equal(t, fresh.AbsoluteExpiresAt, joined.AbsoluteExpiresAt, "concurrent requests after expiry share the new residence")
						fresh = joined
						for _, result := range []int{19, 40} {
							f.complete(late, result)
							state, _ := f.state(ctx)
							assert.Equal(t, fresh, state, "late completion cannot replace a new epoch")
						}
						_, id, found = f.request(expiry)
						assert.True(t, found)
						assert.Equal(t, 19, id)
					})
				}
			})

			t.Run("concurrent_successes_share_first_deadline", func(t *testing.T) {
				f := newAffinityLifetimeFixture(t, redisEnabled)
				first, _, _ := f.request("concurrent")
				second, _, _ := f.request("concurrent")
				third, _, _ := f.request("concurrent")
				base := f.now.UnixMilli()
				var tick atomic.Int64
				channelAffinityLifetimeNow = func() time.Time { return time.UnixMilli(base + tick.Add(1)) }
				var workers sync.WaitGroup
				for _, ctx := range []*gin.Context{first, second, third} {
					workers.Go(func() { f.complete(ctx, 19) })
				}
				workers.Wait()
				state, found := f.state(first)
				require.True(t, found)
				assert.Equal(t, base+1, state.BoundAt)
				assert.Equal(t, base+1+900_000, state.AbsoluteExpiresAt)
				assert.Equal(t, base+3+480_000, state.IdleExpiresAt)
			})

			t.Run("switch_tombstone_and_policy_removal_keep_fence", func(t *testing.T) {
				f := newAffinityLifetimeFixture(t, redisEnabled)
				seed, _, _ := f.request("switch")
				f.complete(seed, 56)
				ctx, _, _ := f.request("switch")
				f.complete(ctx, 19)
				initial, found := f.state(ctx)
				require.True(t, found, "switching from another channel creates a limited residence")
				late, _, _ := f.request("switch")
				setting := operation_setting.GetChannelAffinitySetting()
				setting.Rules[0].ChannelLifetimes = nil
				f.advance(100 * time.Second)
				ctx, _, _ = f.request("switch")
				f.complete(ctx, 19)
				renewed, _ := f.state(ctx)
				assert.Equal(t, initial.AbsoluteExpiresAt, renewed.AbsoluteExpiresAt)
				assert.Equal(t, f.now.Add(480*time.Second).UnixMilli(), renewed.IdleExpiresAt)
				setting.Rules[0].SessionMode = "off"
				off, _, found := f.request("switch")
				assert.False(t, found)
				f.complete(off, 19)
				state, _ := f.state(ctx)
				assert.Equal(t, renewed, state)
				setting.Rules[0].SessionMode = "prefer"
				ctx, _, _ = f.request("switch")
				f.complete(ctx, 40)
				tombstone, found := f.state(ctx)
				require.True(t, found)
				assert.Equal(t, 40, tombstone.ChannelID)
				assert.False(t, tombstone.limited())
				assert.NotEqual(t, renewed.Epoch, tombstone.Epoch)
				f.complete(late, 19)
				_, id, found := f.request("switch")
				assert.True(t, found)
				assert.Equal(t, 40, id)
				state, _ = f.state(ctx)
				assert.Equal(t, tombstone, state)

				noIdentity, _, _ := f.request("")
				f.complete(noIdentity, 19)
				_, ok := getChannelAffinityMeta(noIdentity)
				assert.False(t, ok)
				failed, _, _ := f.request("failed")
				RequestPolicy(failed).OutcomeRecorded = true
				f.complete(failed, 19)
				_, _, found = f.request("failed")
				assert.False(t, found, "a recorded failure never creates a binding")
			})

			t.Run("clear_metadata_precisely_and_count_only_v1", func(t *testing.T) {
				f := newAffinityLifetimeFixture(t, redisEnabled)
				setting := operation_setting.GetChannelAffinitySetting()
				rule := setting.Rules[0]
				rule.Name = "clear*"
				neighbor := rule
				neighbor.Name = "clear-other"
				setting.Rules = []operation_setting.ChannelAffinityRule{rule}
				current, _, _ := f.request("current")
				orphan, _, _ := f.request("orphan")
				f.complete(current, 19)
				f.complete(orphan, 19)
				orphanMeta, _ := getChannelAffinityMeta(orphan)
				_, err := getChannelAffinityCache().DeleteMany([]string{orphanMeta.CacheKey})
				require.NoError(t, err)
				setting.Rules = []operation_setting.ChannelAffinityRule{neighbor}
				other, _, _ := f.request("neighbor")
				f.complete(other, 19)
				setting.Rules = []operation_setting.ChannelAffinityRule{rule, neighbor}
				stats := GetChannelAffinityCacheStats()
				assert.Equal(t, 2, stats.Total)
				assert.Equal(t, 1, stats.ByRuleName[rule.Name])
				deleted, err := ClearChannelAffinityCacheByRuleName(rule.Name)
				require.NoError(t, err)
				assert.Equal(t, 1, deleted)
				for _, ctx := range []*gin.Context{current, orphan} {
					_, found := f.state(ctx)
					assert.False(t, found)
				}
				_, found := f.state(other)
				assert.True(t, found, "glob characters in a rule name must not broaden deletion")
				assert.True(t, ClearCurrentChannelAffinityCache(other))
				_, found = f.state(other)
				assert.False(t, found)
				current, _, _ = f.request("all")
				orphan, _, _ = f.request("all-orphan")
				f.complete(current, 19)
				f.complete(orphan, 19)
				orphanMeta, _ = getChannelAffinityMeta(orphan)
				_, err = getChannelAffinityCache().DeleteMany([]string{orphanMeta.CacheKey})
				require.NoError(t, err)
				assert.False(t, ClearCurrentChannelAffinityCache(orphan), "return value counts the main binding only")
				_, found = f.state(orphan)
				assert.False(t, found)
				// All also clears an orphan tombstone left by a switch to another channel.
				orphan, _, _ = f.request("all-orphan")
				f.complete(orphan, 19)
				orphan, _, _ = f.request("all-orphan")
				f.complete(orphan, 40)
				tombstone, found := f.state(orphan)
				require.True(t, found)
				assert.False(t, tombstone.limited())
				_, err = getChannelAffinityCache().DeleteMany([]string{orphanMeta.CacheKey})
				require.NoError(t, err)
				assert.Equal(t, 1, ClearChannelAffinityCacheAll())
				keys, err := getChannelAffinityLifetimeCache().Keys()
				require.NoError(t, err)
				assert.Empty(t, keys)
				assert.Zero(t, GetChannelAffinityCacheStats().Total)
				if f.server != nil {
					assert.Empty(t, common.RDB.Keys(context.Background(), channelAffinityCacheNamespace+":*").Val())
				}
			})
		})
	}
}

func TestChannelAffinityLifetimePolicyValidation(t *testing.T) {
	for _, tc := range []struct {
		name, lifetimes string
		invalid         bool
	}{
		{name: "omitted"},
		{name: "empty", lifetimes: `[]`},
		{name: "positive", lifetimes: `[{"channel_id":19,"idle_ttl_seconds":480,"max_residence_seconds":900}]`},
		{name: "zero_channel", lifetimes: `[{"channel_id":0,"idle_ttl_seconds":480,"max_residence_seconds":900}]`, invalid: true},
		{name: "negative_channel", lifetimes: `[{"channel_id":-19,"idle_ttl_seconds":480,"max_residence_seconds":900}]`, invalid: true},
		{name: "zero_idle", lifetimes: `[{"channel_id":19,"idle_ttl_seconds":0,"max_residence_seconds":900}]`, invalid: true},
		{name: "negative_idle", lifetimes: `[{"channel_id":19,"idle_ttl_seconds":-1,"max_residence_seconds":900}]`, invalid: true},
		{name: "zero_residence", lifetimes: `[{"channel_id":19,"idle_ttl_seconds":480,"max_residence_seconds":0}]`, invalid: true},
		{name: "negative_residence", lifetimes: `[{"channel_id":19,"idle_ttl_seconds":480,"max_residence_seconds":-1}]`, invalid: true},
		{name: "duplicate", lifetimes: `[{"channel_id":19,"idle_ttl_seconds":480,"max_residence_seconds":900},{"channel_id":19,"idle_ttl_seconds":60,"max_residence_seconds":120}]`, invalid: true},
		{name: "overflow_duration", lifetimes: `[{"channel_id":19,"idle_ttl_seconds":9223372037,"max_residence_seconds":900}]`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := `[{"name":"lifetime-policy","model_regex":[".*"],"fallback_channel_ids":[56,40,19]`
			if tc.lifetimes != "" {
				rules += `,"channel_lifetimes":` + tc.lifetimes
			}
			rules += `}]`
			snapshot, err := model.BuildRequestPolicy(map[string]string{"channel_affinity_setting.rules": rules})
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, snapshot.Affinity.Rules, 1)
			encoded, err := common.Marshal(snapshot.Affinity.Rules)
			require.NoError(t, err)
			roundtrip, err := model.BuildRequestPolicy(map[string]string{"channel_affinity_setting.rules": string(encoded)})
			require.NoError(t, err)
			if len(snapshot.Affinity.Rules[0].ChannelLifetimes) == 0 {
				snapshot.Affinity.Rules[0].ChannelLifetimes = nil // omitempty canonicalizes an explicit empty list.
			}
			assert.Equal(t, snapshot.Affinity.Rules, roundtrip.Affinity.Rules)
			if tc.name == "positive" {
				assert.Equal(t, []operation_setting.ChannelAffinityLifetime{{ChannelID: 19, IdleTTLSeconds: 480, MaxResidenceSeconds: 900}}, roundtrip.Affinity.Rules[0].ChannelLifetimes)
			} else {
				assert.Empty(t, roundtrip.Affinity.Rules[0].ChannelLifetimes)
				assert.NotContains(t, string(encoded), "channel_lifetimes")
			}
		})
	}
}
