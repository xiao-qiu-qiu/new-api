package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateConfigFromMapSliceReplacementClearsOmittedFields(t *testing.T) {
	type rule struct {
		Name       string   `json:"name"`
		Models     []string `json:"models"`
		Fallback   []int    `json:"fallback,omitempty"`
		UserAgents []string `json:"user_agents,omitempty"`
	}
	cfg := &struct {
		Rules []rule `json:"rules"`
	}{}
	updates := []string{
		`[{"name":"mock","models":["deepseek"],"fallback":[67,68,69],"user_agents":["mock-only"]},{"name":"deepseek","models":["deepseek"],"fallback":[56,40,19]},{"name":"gpt","models":["gpt"]}]`,
		`[{"name":"deepseek","models":["deepseek"],"fallback":[56,40,19]},{"name":"gpt","models":["gpt"]}]`,
	}
	for _, raw := range updates {
		require.NoError(t, UpdateConfigFromMap(cfg, map[string]string{"rules": raw}))
	}
	require.Len(t, cfg.Rules, 2)
	assert.Equal(t, "deepseek", cfg.Rules[0].Name)
	assert.Empty(t, cfg.Rules[0].UserAgents)
	assert.Empty(t, cfg.Rules[1].Fallback)
	require.NoError(t, UpdateConfigFromMap(cfg, map[string]string{"rules": "[]"}))
	assert.Empty(t, cfg.Rules)
}
