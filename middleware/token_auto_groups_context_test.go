package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTokenAutoGroupsContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	return ctx
}

func TestSetupContextForTokenOpenAITierPolicy(t *testing.T) {
	for _, policy := range []string{"", model.OpenAITierPolicyFollow, model.OpenAITierPolicyPriority, model.OpenAITierPolicyUltrafast} {
		t.Run("policy="+policy, func(t *testing.T) {
			ctx := newTokenAutoGroupsContext()
			token := &model.Token{Id: 1, UserId: 2, OpenAITierPolicy: policy, ModelLimitsEnabled: true, ModelLimits: "allowed-model"}
			require.NoError(t, SetupContextForToken(ctx, token))
			assert.Equal(t, token.GetOpenAITierPolicy(), common.GetContextKeyString(ctx, constant.ContextKeyTokenOpenAITierPolicy))
			assert.True(t, ctx.GetBool("token_model_limit_enabled"))
			assert.Equal(t, map[string]bool{"allowed-model": true}, ctx.MustGet("token_model_limit"))
		})
	}
}

func TestSetupContextForTokenPreservesCustomAutoGroupsOrder(t *testing.T) {
	ctx := newTokenAutoGroupsContext()
	token := &model.Token{Id: 1, UserId: 2, AutoGroups: `["vip","default"]`}

	require.NoError(t, SetupContextForToken(ctx, token))
	value, ok := common.GetContextKey(ctx, constant.ContextKeyTokenAutoGroups)
	require.True(t, ok)
	assert.Equal(t, []string{"vip", "default"}, value)
}

func TestSetupContextForTokenTreatsStoredEmptyArrayAsInheritance(t *testing.T) {
	ctx := newTokenAutoGroupsContext()
	token := &model.Token{Id: 1, UserId: 2, AutoGroups: `[]`}

	require.NoError(t, SetupContextForToken(ctx, token))
	_, ok := common.GetContextKey(ctx, constant.ContextKeyTokenAutoGroups)
	assert.False(t, ok)
}

func TestSetupContextForTokenMalformedAutoGroupsFailsClosed(t *testing.T) {
	ctx := newTokenAutoGroupsContext()
	token := &model.Token{Id: 1, UserId: 2, AutoGroups: `not-json`}

	require.NoError(t, SetupContextForToken(ctx, token))
	value, ok := common.GetContextKey(ctx, constant.ContextKeyTokenAutoGroups)
	require.True(t, ok)
	assert.Equal(t, []string{}, value)
}
