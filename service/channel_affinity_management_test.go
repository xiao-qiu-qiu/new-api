package service

import (
	"context"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newAffinityManagementFixture(t *testing.T, redisEnabled bool) *affinityLifetimeFixture {
	t.Helper()
	f := newAffinityLifetimeFixture(t, redisEnabled)
	db := setupChannelSelectAutoGroupsTest(t)
	for _, id := range []int{56, 40, 19} {
		createChannelSelectAutoGroupsChannel(t, db, id, "default", "lifetime-model")
	}
	model.InitChannelCache()
	gin.SetMode(gin.TestMode)
	return f
}

func affinityManagementRequest(f *affinityLifetimeFixture, session string, userID, tokenID int, modelName, group, path string, filters ...dto.ChannelFilter) (*gin.Context, int, bool) {
	c := f.context(session)
	c.Request = httptest.NewRequest("POST", path, nil)
	c.Request.Header.Set("X-Session", session)
	c.Set("id", userID)
	c.Set("token_id", tokenID)
	c.Set("username", fmt.Sprintf("user-%d", userID))
	c.Set("token_name", fmt.Sprintf("token-%d", tokenID))
	c.Set("token_key", "never-store-this-api-key")
	GetChannelConstraints(c).Filters = filters
	id, found := GetPreferredChannelByAffinity(c, modelName, group)
	return c, id, found
}

func affinityManagementBinding(t *testing.T, c *gin.Context) ChannelAffinityBinding {
	t.Helper()
	meta, ok := getChannelAffinityMeta(c)
	require.True(t, ok)
	result, err := ListChannelAffinityBindings(ChannelAffinityBindingFilter{BindingID: channelAffinityBindingID(meta.CacheKey)})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	return result.Items[0]
}

func affinityManagementRef(b ChannelAffinityBinding) ChannelAffinityBindingRef {
	return ChannelAffinityBindingRef{ID: b.ID, Version: b.Version, ChannelID: b.ChannelID}
}

func TestChannelAffinityManagement(t *testing.T) {
	for _, redisEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("redis=%t", redisEnabled), func(t *testing.T) {
			t.Run("ownership_metadata_privacy_and_filters", func(t *testing.T) {
				f := newAffinityManagementFixture(t, redisEnabled)
				first, _, _ := affinityManagementRequest(f, "secret-session-A", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
				f.complete(first, 56)
				second, _, _ := affinityManagementRequest(f, "secret-session-B", 2, 22, "lifetime-model", "default", "/v1/chat/completions")
				f.complete(second, 40)
				b := affinityManagementBinding(t, first)
				assert.Equal(t, 1, b.UserID)
				assert.Equal(t, 11, b.TokenID)
				assert.Equal(t, "user-1", b.UserName)
				assert.Equal(t, "token-11", b.TokenName)
				assert.True(t, b.OwnershipKnown)
				assert.False(t, b.Shared)
				assert.Equal(t, "prefer", b.SessionMode)
				assert.Equal(t, f.now.Add(time.Hour).Unix(), b.ExpiresAt)
				for _, filter := range []ChannelAffinityBindingFilter{
					{UserID: 1}, {TokenID: 11}, {ChannelID: 56}, {Model: "lifetime-model", RuleName: "cpa-lifetime", UsingGroup: "default", KeyFingerprint: b.KeyFingerprint},
				} {
					result, err := ListChannelAffinityBindings(filter)
					require.NoError(t, err)
					require.Len(t, result.Items, 1)
					assert.Equal(t, b.ID, result.Items[0].ID)
				}
				result, err := ListChannelAffinityBindings(ChannelAffinityBindingFilter{UserID: 9})
				require.NoError(t, err)
				assert.Zero(t, result.Total)
				result, err = ListChannelAffinityBindings(ChannelAffinityBindingFilter{Page: 2, PageSize: 1})
				require.NoError(t, err)
				assert.Equal(t, 2, result.Total)
				assert.Len(t, result.Items, 1)
				result, err = ListChannelAffinityBindings(ChannelAffinityBindingFilter{Page: int(^uint(0) >> 1)})
				require.NoError(t, err)
				assert.Empty(t, result.Items)
				encoded, err := common.Marshal(b)
				require.NoError(t, err)
				assert.NotContains(t, string(encoded), "secret-session-A")
				assert.NotContains(t, string(encoded), "never-store-this-api-key")
				assert.NotContains(t, string(encoded), "cache_key")
				other := model.NewLogOther()
				AppendChannelAffinityAdminInfo(first, other)
				logJSON := other.JSONString()
				assert.Contains(t, logJSON, b.ID)
				assert.NotContains(t, logJSON, "secret-session-A")
				assert.NotContains(t, logJSON, "key_hint")
				meta, _ := getChannelAffinityMeta(first)
				private, found, err := getChannelAffinityManagementCache().Get(b.ID)
				require.NoError(t, err)
				require.True(t, found)
				privateJSON, err := common.Marshal(private)
				require.NoError(t, err)
				assert.NotContains(t, string(privateJSON), "never-store-this-api-key")
				assert.Equal(t, channelAffinityBindingID(channelAffinityLifetimeSuffix(meta.CacheKey)), b.ID)
				stats := GetChannelAffinityCacheStats()
				assert.Equal(t, 2, stats.Total)
				assert.Equal(t, 2, stats.ByRuleName["cpa-lifetime"])

				// A different user or token sharing one underlying cache key is never
				// assigned to the last caller and disappears from person filters.
				_, id, found := affinityManagementRequest(f, "secret-session-A", 2, 22, "lifetime-model", "default", "/v1/chat/completions")
				require.True(t, found)
				assert.Equal(t, 56, id)
				shared := affinityManagementBinding(t, first)
				assert.True(t, shared.Shared)
				assert.Zero(t, shared.UserID)
				assert.Zero(t, shared.TokenID)
				assert.Empty(t, shared.UserName)
				assert.NotEqual(t, b.Version, shared.Version)
				result, err = ListChannelAffinityBindings(ChannelAffinityBindingFilter{UserID: 1})
				require.NoError(t, err)
				assert.Zero(t, result.Total)
				_, _, _ = affinityManagementRequest(f, "secret-session-B", 2, 23, "lifetime-model", "default", "/v1/chat/completions")
				assert.True(t, affinityManagementBinding(t, second).Shared, "a second token of the same user also makes a shared binding")
				_, _, _ = affinityManagementRequest(f, "secret-session-A", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
				_, _, _ = affinityManagementRequest(f, "secret-session-B", 2, 22, "lifetime-model", "default", "/v1/chat/completions")
				sharedRefs := []ChannelAffinityBindingRef{
					affinityManagementRef(affinityManagementBinding(t, first)),
					affinityManagementRef(affinityManagementBinding(t, second)),
				}
				migration, err := MutateChannelAffinityBindings("migrate", sharedRefs, 19)
				require.NoError(t, err)
				assert.Equal(t, 2, migration.Succeeded)
				for _, c := range []*gin.Context{first, second} {
					shared := affinityManagementBinding(t, c)
					assert.True(t, shared.Shared, "returning original owners and migration must not erase a conflict")
					assert.Zero(t, shared.UserID)
					assert.Zero(t, shared.TokenID)
					assert.Empty(t, shared.UserName)
					assert.Empty(t, shared.TokenName)
				}
				for _, filter := range []ChannelAffinityBindingFilter{{UserID: 1}, {UserID: 2}, {TokenID: 11}, {TokenID: 22}, {TokenID: 23}} {
					result, err := ListChannelAffinityBindings(filter)
					require.NoError(t, err)
					assert.Zero(t, result.Total)
				}

				// Legacy bindings acquire the owner observed on an authenticated
				// request without resetting the existing channel or extending its TTL.
				legacyKey := buildChannelAffinityCacheKeySuffix(operation_setting.GetChannelAffinitySetting().Rules[0], "lifetime-model", "default", "old-secret")
				require.NoError(t, getChannelAffinityCache().SetWithTTL(legacyKey, 19, time.Minute))
				legacy, _, found := affinityManagementRequest(f, "old-secret", 3, 33, "lifetime-model", "default", "/v1/chat/completions")
				require.True(t, found)
				owned := affinityManagementBinding(t, legacy)
				assert.True(t, owned.OwnershipKnown)
				assert.Equal(t, 3, owned.UserID)
				assert.Equal(t, 33, owned.TokenID)
				assert.Equal(t, "user-3", owned.UserName)
				assert.Equal(t, "token-33", owned.TokenName)
				assert.Equal(t, 19, owned.ChannelID)
				for _, filter := range []ChannelAffinityBindingFilter{{UserID: 3}, {TokenID: 33}} {
					result, err = ListChannelAffinityBindings(filter)
					require.NoError(t, err)
					require.Len(t, result.Items, 1)
					assert.Equal(t, owned.ID, result.Items[0].ID)
				}
				if f.server != nil {
					assert.Equal(t, time.Minute, f.server.TTL(getChannelAffinityCache().FullKey(legacyKey)))
				}
			})

			t.Run("stored_unknown_owner_recovers_on_request_or_migration", func(t *testing.T) {
				for _, operation := range []string{"request", "migrate"} {
					t.Run(operation, func(t *testing.T) {
						f := newAffinityManagementFixture(t, redisEnabled)
						c, _, _ := affinityManagementRequest(f, "stored-unknown", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
						f.complete(c, 56)
						b := affinityManagementBinding(t, c)
						stored, found, err := getChannelAffinityManagementCache().Get(b.ID)
						require.NoError(t, err)
						require.True(t, found)
						require.True(t, stored.OwnerObserved)
						require.Equal(t, 1, stored.ObservedUserID)
						require.Equal(t, 11, stored.ObservedTokenID)
						// Reproduce persisted metadata from the affected release: the
						// observation is valid but the public ownership fields are blank.
						stored.Binding.OwnershipKnown = false
						stored.Binding.UserID, stored.Binding.TokenID = 0, 0
						stored.Binding.UserName, stored.Binding.TokenName = "", ""
						require.NoError(t, getChannelAffinityManagementCache().SetWithTTL(b.ID, stored, 24*time.Hour))
						stale := affinityManagementRef(stored.Binding)
						listed, err := ListChannelAffinityBindings(ChannelAffinityBindingFilter{BindingID: b.ID})
						require.NoError(t, err)
						require.Len(t, listed.Items, 1)
						assert.False(t, listed.Items[0].OwnershipKnown, "listing alone does not promote ownership")
						assert.Equal(t, stale.Version, listed.Items[0].Version)
						wantChannel := 56
						if operation == "request" {
							_, id, found := affinityManagementRequest(f, "stored-unknown", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
							require.True(t, found)
							assert.Equal(t, wantChannel, id)
						} else {
							result, err := MutateChannelAffinityBindings("migrate", []ChannelAffinityBindingRef{stale}, 40)
							require.NoError(t, err)
							require.Equal(t, 1, result.Succeeded)
							wantChannel = 40
						}
						repaired, found, err := getChannelAffinityManagementCache().Get(b.ID)
						require.NoError(t, err)
						require.True(t, found)
						assert.True(t, repaired.Binding.OwnershipKnown)
						assert.False(t, repaired.Binding.Shared)
						assert.Equal(t, 1, repaired.Binding.UserID)
						assert.Equal(t, 11, repaired.Binding.TokenID)
						assert.Equal(t, wantChannel, repaired.Binding.ChannelID)
						assert.NotEqual(t, stale.Version, repaired.Binding.Version)
						if operation == "request" {
							assert.Equal(t, "user-1", repaired.Binding.UserName)
							assert.Equal(t, "token-11", repaired.Binding.TokenName)
							assert.Equal(t, stored.ExpiresAtMillis, repaired.ExpiresAtMillis, "owner repair preserves the live binding deadline")
						} else {
							assert.Empty(t, repaired.Binding.UserName)
							assert.Empty(t, repaired.Binding.TokenName)
						}
						for _, filter := range []ChannelAffinityBindingFilter{{UserID: 1}, {TokenID: 11}} {
							result, err := ListChannelAffinityBindings(filter)
							require.NoError(t, err)
							if assert.Len(t, result.Items, 1) {
								assert.Equal(t, b.ID, result.Items[0].ID)
							}
						}
						result, err := MutateChannelAffinityBindings("reset", []ChannelAffinityBindingRef{stale}, 0)
						require.NoError(t, err)
						assert.Equal(t, 1, result.Conflicted, "ownership repair invalidates the stale selection")
						if operation == "migrate" {
							_, id, found := affinityManagementRequest(f, "stored-unknown", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
							require.True(t, found)
							assert.Equal(t, 40, id)
							owned := affinityManagementBinding(t, c)
							assert.Equal(t, "user-1", owned.UserName)
							assert.Equal(t, "token-11", owned.TokenName)
						}
					})
				}
			})

			t.Run("stored_unknown_conflicting_owner_stays_shared", func(t *testing.T) {
				for _, other := range []struct {
					name    string
					userID  int
					tokenID int
				}{{"different_user", 2, 22}, {"different_token", 1, 12}} {
					t.Run(other.name, func(t *testing.T) {
						f := newAffinityManagementFixture(t, redisEnabled)
						c, _, _ := affinityManagementRequest(f, "stored-conflict", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
						f.complete(c, 56)
						b := affinityManagementBinding(t, c)
						stored, found, err := getChannelAffinityManagementCache().Get(b.ID)
						require.NoError(t, err)
						require.True(t, found)
						stored.Binding.OwnershipKnown = false
						stored.Binding.UserID, stored.Binding.TokenID = 0, 0
						stored.Binding.UserName, stored.Binding.TokenName = "", ""
						require.NoError(t, getChannelAffinityManagementCache().SetWithTTL(b.ID, stored, 24*time.Hour))
						stale := affinityManagementRef(stored.Binding)
						_, _, found = affinityManagementRequest(f, "stored-conflict", other.userID, other.tokenID, "lifetime-model", "default", "/v1/chat/completions")
						require.True(t, found)
						conflicted := affinityManagementBinding(t, c)
						assert.True(t, conflicted.Shared)
						assert.NotEqual(t, stale.Version, conflicted.Version)
						result, err := MutateChannelAffinityBindings("reset", []ChannelAffinityBindingRef{stale}, 0)
						require.NoError(t, err)
						assert.Equal(t, 1, result.Conflicted)
						_, _, found = affinityManagementRequest(f, "stored-conflict", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
						require.True(t, found)
						shared := affinityManagementBinding(t, c)
						result, err = MutateChannelAffinityBindings("migrate", []ChannelAffinityBindingRef{affinityManagementRef(shared)}, 40)
						require.NoError(t, err)
						assert.Equal(t, 1, result.Succeeded)
						shared = affinityManagementBinding(t, c)
						assert.True(t, shared.Shared)
						assert.Zero(t, shared.UserID)
						assert.Zero(t, shared.TokenID)
						assert.Empty(t, shared.UserName)
						assert.Empty(t, shared.TokenName)
						for _, filter := range []ChannelAffinityBindingFilter{{UserID: 1}, {UserID: other.userID}, {TokenID: 11}, {TokenID: other.tokenID}} {
							result, err := ListChannelAffinityBindings(filter)
							require.NoError(t, err)
							assert.Zero(t, result.Total)
						}
					})
				}
			})

			t.Run("single_reset_migration_and_late_completions", func(t *testing.T) {
				for _, action := range []string{"reset", "migrate"} {
					t.Run(action, func(t *testing.T) {
						f := newAffinityManagementFixture(t, redisEnabled)
						setting := operation_setting.GetChannelAffinitySetting()
						setting.Rules[0].SessionMode = "strict"
						// This request observed no binding before another request created it.
						unbound, _, found := affinityManagementRequest(f, "manual", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
						require.False(t, found)
						seed, _, _ := affinityManagementRequest(f, "manual", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
						f.complete(seed, 56)
						late, _, found := affinityManagementRequest(f, "manual", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
						require.True(t, found)
						b := affinityManagementBinding(t, seed)
						targetID := 0
						if action == "migrate" {
							targetID = 19
						}
						result, err := MutateChannelAffinityBindings(action, []ChannelAffinityBindingRef{affinityManagementRef(b)}, targetID)
						require.NoError(t, err)
						assert.Equal(t, 1, result.Succeeded)
						assert.Equal(t, "succeeded", result.Items[0].Status)
						f.complete(unbound, 56)
						f.complete(late, 56)
						assert.False(t, ClearCurrentChannelAffinityCache(late), "a stale automatic clear must not erase a manual migration")
						fresh, id, found := affinityManagementRequest(f, "manual", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
						if action == "reset" {
							assert.False(t, found)
							result, err := ListChannelAffinityBindings(ChannelAffinityBindingFilter{})
							require.NoError(t, err)
							assert.Zero(t, result.Total)
							f.complete(fresh, 40)
							_, id, found = f.request("manual")
							require.True(t, found)
							assert.Equal(t, 40, id)
						} else {
							require.True(t, found)
							assert.Equal(t, 19, id)
							assert.Equal(t, "strict", RequestPolicy(fresh).SessionMode)
							migrated := affinityManagementBinding(t, fresh)
							assert.True(t, migrated.OwnershipKnown)
							assert.Equal(t, 1, migrated.UserID)
							assert.Equal(t, 11, migrated.TokenID)
							assert.Equal(t, "user-1", migrated.UserName)
							assert.Equal(t, "token-11", migrated.TokenName)
							assert.NotEqual(t, b.Version, migrated.Version)
							assert.Equal(t, f.now.Add(480*time.Second).Unix(), migrated.ExpiresAt)
							state, found := f.state(fresh)
							require.True(t, found)
							assert.Equal(t, f.now.UnixMilli(), state.BoundAt)
							assert.Equal(t, f.now.Add(900*time.Second).UnixMilli(), state.AbsoluteExpiresAt)
							f.advance(400 * time.Second)
							f.complete(fresh, 19)
							state, _ = f.state(fresh)
							assert.Equal(t, f.now.Add(480*time.Second).UnixMilli(), state.IdleExpiresAt)
							f.advance(400 * time.Second)
							fresh, _, _ = affinityManagementRequest(f, "manual", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
							f.complete(fresh, 19)
							migrated = affinityManagementBinding(t, fresh)
							assert.Equal(t, f.now.Add(100*time.Second).Unix(), migrated.ExpiresAt)
							f.advance(100 * time.Second)
							fresh, _, found = f.request("manual")
							assert.False(t, found)
							f.complete(fresh, 40)
						}
						// The manual fence survives later automatic residence switches.
						f.complete(unbound, 56)
						f.complete(late, 56)
						_, id, found = affinityManagementRequest(f, "manual", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
						require.True(t, found)
						assert.Equal(t, 40, id)
					})
				}
			})

			t.Run("batch_results_targets_and_stale_metadata", func(t *testing.T) {
				f := newAffinityManagementFixture(t, redisEnabled)
				refs := make([]ChannelAffinityBindingRef, 0, 3)
				for _, session := range []string{"batch-a", "batch-b", "batch-c"} {
					c, _, _ := affinityManagementRequest(f, session, 1, 11, "lifetime-model", "default", "/v1/chat/completions")
					f.complete(c, 56)
					refs = append(refs, affinityManagementRef(affinityManagementBinding(t, c)))
				}
				targets, err := GetChannelAffinityTargets(refs)
				require.NoError(t, err)
				assert.Len(t, targets, 3)
				result, err := MutateChannelAffinityBindings("migrate", refs, 19)
				require.NoError(t, err)
				assert.Equal(t, 3, result.Succeeded)
				result, err = MutateChannelAffinityBindings("reset", refs, 0)
				require.NoError(t, err)
				assert.Equal(t, 3, result.Conflicted)
				_, err = GetChannelAffinityTargets(refs)
				require.Error(t, err)
				for i, session := range []string{"batch-a", "batch-b", "batch-c"} {
					c, _, _ := affinityManagementRequest(f, session, 1, 11, "lifetime-model", "default", "/v1/chat/completions")
					refs[i] = affinityManagementRef(affinityManagementBinding(t, c))
				}
				result, err = MutateChannelAffinityBindings("reset", refs, 0)
				require.NoError(t, err)
				assert.Equal(t, 3, result.Succeeded)
				result, err = MutateChannelAffinityBindings("reset", refs, 0)
				require.NoError(t, err)
				assert.Equal(t, 3, result.Expired)
				result, err = affinityManagementMixedResult(t, f)
				require.NoError(t, err)
				assert.Equal(t, 1, result.Succeeded)
				assert.Equal(t, 1, result.Expired)
				assert.Equal(t, 1, result.Conflicted)
			})

			t.Run("target_checks_all_observed_models_groups_paths_and_filters", func(t *testing.T) {
				f := newAffinityManagementFixture(t, redisEnabled)
				setting := operation_setting.GetChannelAffinitySetting()
				setting.Rules[0].ModelRegex = []string{".*"}
				// Only channel 40 supports both observed models and both observed groups.
				require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 40).Updates(map[string]any{"models": "lifetime-model,other-model", "group": "default,vip"}).Error)
				for _, pair := range [][2]string{{"default", "other-model"}, {"vip", "lifetime-model"}, {"vip", "other-model"}} {
					require.NoError(t, model.DB.Create(&model.Ability{Group: pair[0], Model: pair[1], ChannelId: 40, Enabled: true}).Error)
				}
				// A second candidate supports every model/group but only one path.
				custom := model.Channel{Id: 58, Name: "responses-only", Type: constant.ChannelTypeAdvancedCustom, Status: common.ChannelStatusEnabled, Group: "default,vip", Models: "lifetime-model,other-model"}
				custom.SetOtherSettings(kitdto.ChannelOtherSettings{AdvancedCustom: &kitdto.AdvancedCustomConfig{Routes: []kitdto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", Models: []string{"lifetime-model", "other-model"}}}}})
				require.NoError(t, model.DB.Create(&custom).Error)
				require.NoError(t, custom.AddAbilities(model.DB))
				model.InitChannelCache()
				first, _, _ := affinityManagementRequest(f, "diverse", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
				f.complete(first, 40)
				stale := affinityManagementRef(affinityManagementBinding(t, first))
				_, _, _ = affinityManagementRequest(f, "diverse", 1, 11, "other-model", "vip", "/v1/responses")
				b := affinityManagementBinding(t, first)
				assert.Equal(t, []string{"lifetime-model", "other-model"}, b.Models)
				assert.ElementsMatch(t, []string{"/v1/chat/completions", "/v1/responses"}, b.RequestPaths)
				targets, err := GetChannelAffinityTargets([]ChannelAffinityBindingRef{affinityManagementRef(b)})
				require.NoError(t, err)
				assert.Equal(t, []ChannelAffinityTarget{{ID: 40, Name: "channel-40"}}, targets)

				// A provider with the model/group but no alpha/search protocol must
				// not be offered, even though the ordinary path filter is permissive.
				search, _, _ := affinityManagementRequest(f, "search", 1, 11, "lifetime-model", "default", "/v1/alpha/search")
				f.complete(search, 56)
				searchBinding := affinityManagementBinding(t, search)
				targets, err = GetChannelAffinityTargets([]ChannelAffinityBindingRef{affinityManagementRef(searchBinding)})
				require.NoError(t, err)
				assert.Empty(t, targets)
				searchTarget, err := model.CacheGetChannel(40)
				require.NoError(t, err)
				searchChannel := *searchTarget
				searchChannel.Type = constant.ChannelTypeSub2API
				model.CacheUpdateChannel(&searchChannel)
				targets, err = GetChannelAffinityTargets([]ChannelAffinityBindingRef{affinityManagementRef(searchBinding)})
				require.NoError(t, err)
				assert.Equal(t, []ChannelAffinityTarget{{ID: 40, Name: "channel-40"}}, targets)
				result, err := MutateChannelAffinityBindings("migrate", []ChannelAffinityBindingRef{stale}, 19)
				require.NoError(t, err)
				assert.Equal(t, 1, result.Conflicted)
				for _, target := range []int{19, 56, 58, 99999} {
					result, err := MutateChannelAffinityBindings("migrate", []ChannelAffinityBindingRef{affinityManagementRef(b)}, target)
					require.NoError(t, err)
					assert.Equal(t, 1, result.Failed)
				}
				model.CacheUpdateChannelStatus(40, common.ChannelStatusManuallyDisabled)
				targets, err = GetChannelAffinityTargets([]ChannelAffinityBindingRef{affinityManagementRef(b)})
				require.NoError(t, err)
				assert.Empty(t, targets)
				result, err = MutateChannelAffinityBindings("migrate", []ChannelAffinityBindingRef{affinityManagementRef(b)}, 40)
				require.NoError(t, err)
				assert.Equal(t, 1, result.Failed)

				model.InitChannelCache()
				websocket, _, _ := affinityManagementRequest(f, "websocket", 1, 11, "lifetime-model", "default", "/v1/responses", dto.ChannelFilter{Kind: dto.FilterResponsesWebSocket})
				f.complete(websocket, 56)
				wsBinding := affinityManagementBinding(t, websocket)
				targets, err = GetChannelAffinityTargets([]ChannelAffinityBindingRef{affinityManagementRef(wsBinding)})
				require.NoError(t, err)
				assert.Empty(t, targets)
				channel, err := model.CacheGetChannel(40)
				require.NoError(t, err)
				cloned := *channel
				settings := cloned.GetSetting()
				settings.ResponsesWebSocketEnabled = true
				cloned.SetSetting(settings)
				model.CacheUpdateChannel(&cloned)
				targets, err = GetChannelAffinityTargets([]ChannelAffinityBindingRef{affinityManagementRef(wsBinding)})
				require.NoError(t, err)
				assert.Equal(t, []ChannelAffinityTarget{{ID: 40, Name: "channel-40"}}, targets)
			})

			t.Run("legacy_unbound_epoch_and_cross_process_cas", func(t *testing.T) {
				f := newAffinityManagementFixture(t, redisEnabled)
				unbound, _, _ := affinityManagementRequest(f, "legacy-fence", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
				meta, _ := getChannelAffinityMeta(unbound)
				require.NoError(t, getChannelAffinityCache().SetWithTTL(meta.CacheKey, 56, time.Hour))
				legacy, _, found := affinityManagementRequest(f, "legacy-fence", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
				require.True(t, found)
				b := affinityManagementBinding(t, legacy)
				before, err := loadChannelAffinityBinding(meta.CacheKey)
				require.NoError(t, err)
				result, err := MutateChannelAffinityBindings("migrate", []ChannelAffinityBindingRef{affinityManagementRef(b)}, 40)
				require.NoError(t, err)
				assert.Equal(t, 1, result.Succeeded)
				if f.server != nil {
					// Fresh cache instances simulate another process, reading only Redis.
					channelAffinityCacheOnce, channelAffinityLifetimeOnce, channelAffinityManagementOnce = sync.Once{}, sync.Once{}, sync.Once{}
					channelAffinityCache, channelAffinityLifetimeCache, channelAffinityManagementCache = nil, nil, nil
					value := before.management
					value.Binding.ChannelID = 56
					updated, err := writeChannelAffinityManagedBinding(meta.CacheKey, before, 56, time.Hour, before.lifetime, value, 24*time.Hour)
					require.NoError(t, err)
					assert.False(t, updated, "stale snapshots from another process cannot overwrite management operations")
				}
				f.complete(unbound, 56)
				f.complete(legacy, 56)
				_, id, found := affinityManagementRequest(f, "legacy-fence", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
				require.True(t, found)
				assert.Equal(t, 40, id)
			})

			t.Run("current_rule_TTL_expiry_and_concurrent_admins", func(t *testing.T) {
				f := newAffinityManagementFixture(t, redisEnabled)
				operation_setting.GetChannelAffinitySetting().Rules[0].TTLSeconds = 7200
				c, _, _ := affinityManagementRequest(f, "ttl", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
				f.complete(c, 56)
				ref := affinityManagementRef(affinityManagementBinding(t, c))
				result, err := MutateChannelAffinityBindings("migrate", []ChannelAffinityBindingRef{ref}, 40)
				require.NoError(t, err)
				require.Equal(t, 1, result.Succeeded)
				b := affinityManagementBinding(t, c)
				assert.Equal(t, f.now.Add(7200*time.Second).Unix(), b.ExpiresAt, "a rule TTL longer than the default must remain intact")
				f.advance(7200 * time.Second)
				result, err = MutateChannelAffinityBindings("migrate", []ChannelAffinityBindingRef{affinityManagementRef(b)}, 56)
				require.NoError(t, err)
				assert.Equal(t, 1, result.Expired)
				_, err = GetChannelAffinityTargets([]ChannelAffinityBindingRef{affinityManagementRef(b)})
				require.Error(t, err)

				c, _, _ = affinityManagementRequest(f, "two-admins", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
				f.complete(c, 56)
				ref = affinityManagementRef(affinityManagementBinding(t, c))
				var workers sync.WaitGroup
				results := make([]ChannelAffinityBindingMutationResult, 2)
				errs := make([]error, 2)
				for i, target := range []int{19, 40} {
					workers.Go(func() {
						results[i], errs[i] = MutateChannelAffinityBindings("migrate", []ChannelAffinityBindingRef{ref}, target)
					})
				}
				workers.Wait()
				for _, err := range errs {
					require.NoError(t, err)
				}
				assert.Equal(t, 1, results[0].Succeeded+results[1].Succeeded)
				assert.Equal(t, 1, results[0].Conflicted+results[1].Conflicted)
			})

			t.Run("expired_legacy_starts_new_ownership_and_route_history", func(t *testing.T) {
				f := newAffinityManagementFixture(t, redisEnabled)
				setting := operation_setting.GetChannelAffinitySetting()
				setting.Rules[0].ModelRegex = []string{".*"}
				key := buildChannelAffinityCacheKeySuffix(setting.Rules[0], "lifetime-model", "default", "expired-legacy")
				require.NoError(t, getChannelAffinityCache().SetWithTTL(key, 56, time.Minute))
				legacy, _, found := affinityManagementRequest(f, "expired-legacy", 1, 11, "old-model", "old-group", "/v1/responses")
				require.True(t, found)
				unknown := affinityManagementBinding(t, legacy)
				assert.True(t, unknown.OwnershipKnown)
				f.advance(time.Hour)
				fresh, _, found := affinityManagementRequest(f, "expired-legacy", 2, 22, "lifetime-model", "default", "/v1/chat/completions")
				require.False(t, found)
				f.complete(fresh, 40)
				owned := affinityManagementBinding(t, fresh)
				assert.True(t, owned.OwnershipKnown)
				assert.False(t, owned.Shared)
				assert.Equal(t, 2, owned.UserID)
				assert.Equal(t, 22, owned.TokenID)
				assert.Equal(t, []string{"lifetime-model"}, owned.Models)
				assert.Equal(t, []string{"default"}, owned.UsingGroups)
				assert.Equal(t, []string{"/v1/chat/completions"}, owned.RequestPaths)
				assert.NotEqual(t, unknown.Version, owned.Version)
				result, err := ListChannelAffinityBindings(ChannelAffinityBindingFilter{UserID: 2, TokenID: 22})
				require.NoError(t, err)
				assert.Equal(t, 1, result.Total)
				f.complete(legacy, 56)
				owned = affinityManagementBinding(t, fresh)
				assert.Equal(t, 2, owned.UserID)
				assert.Equal(t, 40, owned.ChannelID, "old lifetime's completion cannot taint the new binding")
			})

			t.Run("all_and_rule_clear_fence_bound_legacy_and_unbound_requests", func(t *testing.T) {
				for _, all := range []bool{false, true} {
					t.Run(fmt.Sprintf("all=%t", all), func(t *testing.T) {
						f := newAffinityManagementFixture(t, redisEnabled)
						setting := operation_setting.GetChannelAffinitySetting()
						rule := setting.Rules[0]
						rule.Name = "clear*"
						neighbor := rule
						neighbor.Name = "clear-other"
						setting.Rules = []operation_setting.ChannelAffinityRule{rule}
						bound, _, _ := affinityManagementRequest(f, "clear-bound", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
						f.complete(bound, 56)
						late, _, found := affinityManagementRequest(f, "clear-bound", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
						require.True(t, found)
						legacyKey := buildChannelAffinityCacheKeySuffix(rule, "lifetime-model", "default", "clear-legacy")
						require.NoError(t, getChannelAffinityCache().SetWithTTL(legacyKey, 56, time.Hour))
						legacy, _, found := affinityManagementRequest(f, "clear-legacy", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
						require.True(t, found)
						unbound, _, found := affinityManagementRequest(f, "clear-pending", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
						require.False(t, found)
						setting.Rules = []operation_setting.ChannelAffinityRule{neighbor}
						other, _, _ := affinityManagementRequest(f, "neighbor", 2, 22, "lifetime-model", "default", "/v1/chat/completions")
						f.complete(other, 40)
						otherLate, _, _ := affinityManagementRequest(f, "neighbor", 2, 22, "lifetime-model", "default", "/v1/chat/completions")
						setting.Rules = []operation_setting.ChannelAffinityRule{rule, neighbor}
						if all {
							assert.Equal(t, 3, ClearChannelAffinityCacheAll())
						} else {
							deleted, err := ClearChannelAffinityCacheByRuleName(rule.Name)
							require.NoError(t, err)
							assert.Equal(t, 2, deleted)
						}
						for _, c := range []*gin.Context{late, legacy, unbound} {
							f.complete(c, 56)
						}
						f.complete(otherLate, 40)
						for _, session := range []string{"clear-bound", "clear-legacy", "clear-pending"} {
							_, _, found := f.request(session)
							assert.False(t, found, session)
						}
						setting.Rules = []operation_setting.ChannelAffinityRule{neighbor}
						_, id, found := f.request("neighbor")
						assert.Equal(t, !all, found)
						if !all {
							assert.Equal(t, 40, id)
						}
						setting.Rules = []operation_setting.ChannelAffinityRule{rule, neighbor}
						fresh, _, _ := affinityManagementRequest(f, "clear-bound", 2, 22, "lifetime-model", "default", "/v1/chat/completions")
						f.complete(fresh, 40)
						f.complete(late, 56)
						_, id, found = f.request("clear-bound")
						require.True(t, found)
						assert.Equal(t, 40, id)
					})
				}
			})
		})
	}
}

type affinityManagementRedisCommandCounter struct{ get, mget, pttl atomic.Int64 }

func (h *affinityManagementRedisCommandCounter) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	switch cmd.Name() {
	case "get":
		h.get.Add(1)
	case "mget":
		h.mget.Add(1)
	case "pttl":
		h.pttl.Add(1)
	}
	return ctx, nil
}
func (*affinityManagementRedisCommandCounter) AfterProcess(context.Context, redis.Cmder) error {
	return nil
}
func (h *affinityManagementRedisCommandCounter) BeforeProcessPipeline(ctx context.Context, cmds []redis.Cmder) (context.Context, error) {
	for _, cmd := range cmds {
		_, _ = h.BeforeProcess(ctx, cmd)
	}
	return ctx, nil
}
func (*affinityManagementRedisCommandCounter) AfterProcessPipeline(context.Context, []redis.Cmder) error {
	return nil
}

func TestChannelAffinityManagementListBatchesRedisReads(t *testing.T) {
	f := newAffinityManagementFixture(t, true)
	operation_setting.GetChannelAffinitySetting().MaxEntries = 512
	// Cross one batch boundary. This protects the administrative list against
	// returning to one Redis/DB lookup per binding as its cache grows.
	for i := range 257 {
		key := getChannelAffinityCache().FullKey(fmt.Sprintf("list-batch:session-%d", i))
		value := channelAffinityManagedBinding{
			CacheKey: key, Active: true, ExpiresAtMillis: f.now.Add(time.Hour).UnixMilli(),
			Binding: ChannelAffinityBinding{ID: channelAffinityBindingID(key), Version: fmt.Sprintf("%032x", i+1), ChannelID: 56, UserID: 1, TokenID: 11, OwnershipKnown: true, RuleName: "list-batch", RuleNames: []string{"list-batch"}, Models: []string{"lifetime-model"}, LastSeenAt: f.now.Unix()},
		}
		require.NoError(t, getChannelAffinityCache().SetWithTTL(key, 56, time.Hour))
		require.NoError(t, getChannelAffinityManagementCache().SetWithTTL(value.Binding.ID, value, 24*time.Hour))
	}
	hook := &affinityManagementRedisCommandCounter{}
	common.RDB.AddHook(hook)
	result, err := ListChannelAffinityBindings(ChannelAffinityBindingFilter{UserID: 1, Page: 2, PageSize: 20})
	require.NoError(t, err)
	assert.Equal(t, 257, result.Total)
	assert.Len(t, result.Items, 20)
	assert.Equal(t, "channel-56", result.Items[0].ChannelName)
	assert.Zero(t, hook.get.Load())
	assert.Zero(t, hook.pttl.Load())
	assert.LessOrEqual(t, hook.mget.Load(), int64(4))
}

func affinityManagementMixedResult(t *testing.T, f *affinityLifetimeFixture) (ChannelAffinityBindingMutationResult, error) {
	t.Helper()
	refs := make([]ChannelAffinityBindingRef, 0, 3)
	for _, session := range []string{"mixed-success", "mixed-expired", "mixed-conflict"} {
		c, _, _ := affinityManagementRequest(f, session, 1, 11, "lifetime-model", "default", "/v1/chat/completions")
		f.complete(c, 56)
		refs = append(refs, affinityManagementRef(affinityManagementBinding(t, c)))
		if strings.HasSuffix(session, "expired") {
			meta, _ := getChannelAffinityMeta(c)
			_, err := getChannelAffinityCache().DeleteMany([]string{meta.CacheKey})
			require.NoError(t, err)
		}
	}
	refs[2].Version = common.GetUUID()
	return MutateChannelAffinityBindings("reset", refs, 0)
}

func TestChannelAffinityManagementValidation(t *testing.T) {
	valid := ChannelAffinityBindingRef{ID: strings.Repeat("a", 64), Version: strings.Repeat("b", 32), ChannelID: 1}
	for _, tc := range []struct {
		name string
		refs []ChannelAffinityBindingRef
	}{
		{"empty", nil}, {"over_limit", slices.Repeat([]ChannelAffinityBindingRef{valid}, 201)},
		{"duplicate", []ChannelAffinityBindingRef{valid, valid}},
		{"bad_id", []ChannelAffinityBindingRef{{ID: "raw-session-key", Version: valid.Version, ChannelID: 1}}},
		{"bad_version", []ChannelAffinityBindingRef{{ID: valid.ID, Version: "invalid", ChannelID: 1}}},
		{"bad_channel", []ChannelAffinityBindingRef{{ID: valid.ID, Version: valid.Version, ChannelID: 0}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := MutateChannelAffinityBindings("reset", tc.refs, 0)
			require.Error(t, err)
			_, err = GetChannelAffinityTargets(tc.refs)
			require.Error(t, err)
		})
	}
	_, err := MutateChannelAffinityBindings("unknown", []ChannelAffinityBindingRef{valid}, 0)
	require.Error(t, err)
	_, err = MutateChannelAffinityBindings("migrate", []ChannelAffinityBindingRef{valid}, 0)
	require.Error(t, err)
	_, err = MutateChannelAffinityBindings("reset", []ChannelAffinityBindingRef{valid}, 19)
	require.Error(t, err)
}

func TestChannelAffinityManagementHotReloadSnapshot(t *testing.T) {
	for _, change := range []string{"delete", "rename", "ttl", "limited"} {
		t.Run(change, func(t *testing.T) {
			f := newAffinityManagementFixture(t, true)
			c, _, _ := f.request("hot-reload")
			f.complete(c, 56)
			ref := affinityManagementRef(affinityManagementBinding(t, c))
			rule := operation_setting.GetChannelAffinitySetting().Rules[0]
			rules := []operation_setting.ChannelAffinityRule{rule}
			target := 40
			switch change {
			case "delete":
				rules = nil
			case "rename":
				rules[0].Name = "renamed"
			case "ttl":
				rules[0].TTLSeconds = 5
			case "limited":
				target = 19
				rules[0].ChannelLifetimes = []operation_setting.ChannelAffinityLifetime{{ChannelID: 19, IdleTTLSeconds: 5, MaxResidenceSeconds: 10}}
			}
			encoded, err := common.Marshal(rules)
			require.NoError(t, err)
			// Initialize dialect column names on the existing fixture without
			// migrating tables or opening another database.
			t.Setenv("LOG_SQL_DSN", "")
			previousLogDB, previousLogType := model.LOG_DB, common.LogDatabaseType()
			previousMaster := common.IsMasterNode
			common.IsMasterNode = false
			err = model.InitLogDB()
			common.IsMasterNode = previousMaster
			require.NoError(t, err)
			t.Cleanup(func() {
				model.LOG_DB = previousLogDB
				common.SetLogDatabaseType(previousLogType)
			})
			common.MemoryCacheEnabled = false
			var changed bool
			// A config update exactly during eligibility reproduces the old
			// index=-1 panic without scheduling races or sleeps.
			require.NoError(t, model.DB.Callback().Query().After("gorm:query").Register("affinity_config_reload", func(tx *gorm.DB) {
				if !changed && tx.Statement.Table == "abilities" {
					changed = true
					require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"channel_affinity_setting.rules": string(encoded)}))
				}
			}))
			var result ChannelAffinityBindingMutationResult
			require.NotPanics(t, func() {
				result, err = MutateChannelAffinityBindings("migrate", []ChannelAffinityBindingRef{ref}, target)
			})
			require.NoError(t, err)
			require.True(t, changed)
			require.Equal(t, 1, result.Succeeded)
			binding := affinityManagementBinding(t, c)
			if target == 19 {
				assert.Equal(t, f.now.Add(480*time.Second).Unix(), binding.ExpiresAt)
				state, found := f.state(c)
				require.True(t, found)
				assert.Equal(t, f.now.Add(900*time.Second).UnixMilli(), state.AbsoluteExpiresAt)
			} else {
				assert.Equal(t, f.now.Add(time.Hour).Unix(), binding.ExpiresAt)
			}
			if change == "delete" || change == "rename" {
				refs := []ChannelAffinityBindingRef{affinityManagementRef(binding)}
				targets, err := GetChannelAffinityTargets(refs)
				require.NoError(t, err)
				assert.Empty(t, targets)
				result, err = MutateChannelAffinityBindings("migrate", refs, 56)
				require.NoError(t, err)
				assert.Equal(t, 1, result.Failed)
			}
		})
	}
}

func TestChannelAffinityManagementSharedRuleTTL(t *testing.T) {
	for _, redisEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("redis=%t", redisEnabled), func(t *testing.T) {
			f := newAffinityManagementFixture(t, redisEnabled)
			setting := operation_setting.GetChannelAffinitySetting()
			short := setting.Rules[0]
			short.Name, short.TTLSeconds, short.IncludeRuleName = "short", 60, false
			short.PathRegex = []string{"^/v1/responses$"}
			long := short
			long.Name, long.TTLSeconds, long.PathRegex = "long", 3600, []string{"^/v1/chat/completions$"}
			newRule := short
			newRule.Name, newRule.TTLSeconds, newRule.PathRegex = "shortest", 30, []string{"^/v1/embeddings$"}
			setting.Rules = []operation_setting.ChannelAffinityRule{short, long, newRule}
			c, _, _ := affinityManagementRequest(f, "shared-ttl", 1, 11, "lifetime-model", "default", "/v1/responses")
			f.complete(c, 56)
			c, _, _ = affinityManagementRequest(f, "shared-ttl", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
			f.complete(c, 56)
			binding := affinityManagementBinding(t, c)
			assert.Equal(t, f.now.Add(time.Minute).Unix(), binding.ExpiresAt)
			result, err := MutateChannelAffinityBindings("migrate", []ChannelAffinityBindingRef{affinityManagementRef(binding)}, 40)
			require.NoError(t, err)
			require.Equal(t, 1, result.Succeeded)
			c, _, _ = affinityManagementRequest(f, "shared-ttl", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
			f.complete(c, 40)
			assert.Equal(t, f.now.Add(time.Minute).Unix(), affinityManagementBinding(t, c).ExpiresAt)
			c, _, _ = affinityManagementRequest(f, "shared-ttl", 1, 11, "lifetime-model", "default", "/v1/embeddings")
			f.complete(c, 40)
			c, _, _ = affinityManagementRequest(f, "shared-ttl", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
			f.complete(c, 40)
			binding = affinityManagementBinding(t, c)
			assert.Equal(t, f.now.Add(30*time.Second).Unix(), binding.ExpiresAt)
			f.advance(30 * time.Second)
			c, _, found := affinityManagementRequest(f, "shared-ttl", 1, 11, "lifetime-model", "default", "/v1/chat/completions")
			require.False(t, found)
			f.complete(c, 40)
			binding = affinityManagementBinding(t, c)
			assert.Equal(t, f.now.Add(time.Hour).Unix(), binding.ExpiresAt)
			assert.Equal(t, []string{"long"}, binding.RuleNames)
		})
	}
}

func TestChannelAffinityManagementClearExpiredUnlimited(t *testing.T) {
	for _, redisEnabled := range []bool{false, true} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("redis=%t/legacy=%t", redisEnabled, legacy), func(t *testing.T) {
				f := newAffinityManagementFixture(t, redisEnabled)
				c, _, _ := f.request("expired-clear")
				meta, _ := getChannelAffinityMeta(c)
				if legacy {
					require.NoError(t, getChannelAffinityCache().SetWithTTL(meta.CacheKey, 56, time.Hour))
				} else {
					f.complete(c, 56)
				}
				c, _, found := f.request("expired-clear")
				require.True(t, found)
				f.advance(time.Hour)
				if f.server == nil {
					// The fixture clock controls metadata; explicitly expire the
					// real-time memory main key to exercise the same boundary.
					_, err := getChannelAffinityCache().DeleteMany([]string{meta.CacheKey})
					require.NoError(t, err)
				}
				assert.False(t, ClearCurrentChannelAffinityCache(c))
				state, found := f.state(c)
				require.True(t, found)
				assert.Equal(t, 56, state.ChannelID)
				fresh, _, found := f.request("expired-clear")
				require.False(t, found)
				f.complete(fresh, 40)
				_, id, found := f.request("expired-clear")
				require.True(t, found)
				assert.Equal(t, 40, id)
			})
		}
	}
}

func TestChannelAffinityManagementEvictedManualFence(t *testing.T) {
	for _, backend := range []string{"memory", "redis", "redis_fence_evicted"} {
		t.Run(backend, func(t *testing.T) {
			f := newAffinityManagementFixture(t, backend != "memory")
			operation_setting.GetChannelAffinitySetting().MaxEntries = 1
			late, _, found := f.request("evicted-reset")
			require.False(t, found)
			seed, _, _ := f.request("evicted-reset")
			f.complete(seed, 56)
			ref := affinityManagementRef(affinityManagementBinding(t, seed))
			result, err := MutateChannelAffinityBindings("reset", []ChannelAffinityBindingRef{ref}, 0)
			require.NoError(t, err)
			require.Equal(t, 1, result.Succeeded)
			other, _, _ := f.request("evictor")
			f.complete(other, 19)
			meta, _ := getChannelAffinityMeta(late)
			if f.server != nil {
				// Model Redis eviction of both sidecars independently of the
				// stable generation, then eviction of the generation itself.
				require.NoError(t, common.RDB.Del(context.Background(), getChannelAffinityManagementCache().FullKey(ref.ID), getChannelAffinityLifetimeCache().FullKey(channelAffinityLifetimeSuffix(meta.CacheKey))).Err())
				if backend == "redis_fence_evicted" {
					require.NoError(t, channelAffinityClearFenceCache.Purge())
				}
			}
			f.complete(late, 56)
			_, found, err = getChannelAffinityCache().Get(meta.CacheKey)
			require.NoError(t, err)
			assert.False(t, found, "an old unbound request must not resurrect the reset after eviction")
			_, id, found := f.request("evictor")
			require.True(t, found, "unrelated existing bindings must survive fencing")
			assert.Equal(t, 19, id)
			fresh, _, found := f.request("evicted-reset")
			require.False(t, found)
			f.complete(fresh, 40)
			_, id, found = f.request("evicted-reset")
			require.True(t, found)
			assert.Equal(t, 40, id)
		})
	}
}

func TestChannelAffinityManagementProtocolTargets(t *testing.T) {
	for _, tc := range []struct {
		name, path  string
		channelType int
		eligible    bool
	}{
		{"anthropic_embeddings", "/v1/embeddings", constant.ChannelTypeAnthropic, false},
		{"gemini_speech", "/v1/audio/speech", constant.ChannelTypeGemini, false},
		{"sub2api_embeddings", "/v1/embeddings", constant.ChannelTypeSub2API, true},
		{"sub2api_responses", "/v1/responses", constant.ChannelTypeSub2API, true},
		{"dify_messages", "/v1/messages", constant.ChannelTypeDify, false},
		{"jina_chat", "/v1/chat/completions", constant.ChannelTypeJina, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAffinityManagementFixture(t, true)
			require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 40).Update("type", tc.channelType).Error)
			model.InitChannelCache()
			c, _, _ := affinityManagementRequest(f, tc.name, 1, 11, "lifetime-model", "default", tc.path)
			f.complete(c, 56)
			refs := []ChannelAffinityBindingRef{affinityManagementRef(affinityManagementBinding(t, c))}
			targets, err := GetChannelAffinityTargets(refs)
			require.NoError(t, err)
			assert.Equal(t, tc.eligible, slices.ContainsFunc(targets, func(target ChannelAffinityTarget) bool { return target.ID == 40 }))
			result, err := MutateChannelAffinityBindings("migrate", refs, 40)
			require.NoError(t, err)
			if tc.eligible {
				assert.Equal(t, 1, result.Succeeded)
				assert.Equal(t, 40, affinityManagementBinding(t, c).ChannelID)
			} else {
				assert.Equal(t, 1, result.Failed)
				assert.Equal(t, "failed", result.Items[0].Status)
				assert.Equal(t, 56, affinityManagementBinding(t, c).ChannelID)
			}
		})
	}
}
