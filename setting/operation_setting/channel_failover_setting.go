package operation_setting

import (
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

// ChannelFailoverSetting routes eligible requests through a primary and its backups.
// Availability is observed at runtime; it never changes a channel's saved status.
type ChannelFailoverSetting struct {
	Enabled             bool                          `json:"enabled"`
	PrimaryChannelID    int                           `json:"primary_channel_id"`
	FallbackChannelID   int                           `json:"fallback_channel_id"`
	FallbackRules       []ChannelFailoverFallbackRule `json:"fallback_rules"`
	fallbackChains      []ChannelFailoverChain
	ModelRegex          []string `json:"model_regex"`
	PollIntervalSeconds int      `json:"poll_interval_seconds"`
	RecoverySuccesses   int      `json:"recovery_successes"`
}

// The separate option key lets older versions ignore chains without rejecting
// the primary/backup options they already support.
const ChannelFailoverFallbackChainsOption = "ChannelFailoverFallbackChains"

type ChannelFailoverChain struct {
	Groups     []string `json:"groups"`
	ModelRegex []string `json:"model_regex"`
	ChannelIDs []int    `json:"channel_ids"`
}

// FallbackChains returns the snapshot's ordered backup chains. Callers must not mutate them.
func (s ChannelFailoverSetting) FallbackChains() []ChannelFailoverChain {
	return s.fallbackChains
}

func (s *ChannelFailoverSetting) LoadFallbackChains(value string) error {
	var chains []ChannelFailoverChain
	if err := common.UnmarshalJsonStr(value, &chains); err != nil {
		return common.NewMessage("Invalid primary and backup settings")
	}
	if len(chains) == 0 {
		chains = nil
	}
	s.fallbackChains = chains
	return nil
}

type ChannelFailoverFallbackRule struct {
	ModelRegex []string `json:"model_regex"`
	ChannelID  int      `json:"channel_id"`
}

var channelFailoverSetting = ChannelFailoverSetting{
	ModelRegex:          []string{},
	FallbackRules:       []ChannelFailoverFallbackRule{},
	PollIntervalSeconds: 3,
	RecoverySuccesses:   2,
}

func init() {
	config.GlobalConfig.Register("channel_failover_setting", &channelFailoverSetting)
}

func GetChannelFailoverSetting() *ChannelFailoverSetting {
	return &channelFailoverSetting
}

func (s ChannelFailoverSetting) Validate() error {
	if s.PrimaryChannelID < 0 || s.FallbackChannelID < 0 {
		return common.NewMessage("Select a valid channel ID")
	}
	if s.Enabled && (s.PrimaryChannelID == 0 || s.FallbackChannelID == 0 || s.PrimaryChannelID == s.FallbackChannelID) {
		return common.NewMessage("Choose two different channels")
	}
	if s.PollIntervalSeconds < 1 || s.PollIntervalSeconds > 60 {
		return common.NewMessage("Check interval must be between 1 and 60 seconds")
	}
	if s.RecoverySuccesses < 1 || s.RecoverySuccesses > 10 {
		return common.NewMessage("Recovery confirmations must be between 1 and 10")
	}
	if len(s.FallbackRules) > 32 {
		return common.NewMessage("Use up to 32 backup rules")
	}
	patterns := [][]string{s.ModelRegex}
	for _, rule := range s.FallbackRules {
		if rule.ChannelID <= 0 {
			return common.NewMessage("Select a valid channel ID")
		}
		if rule.ChannelID == s.PrimaryChannelID {
			return common.NewMessage("Choose two different channels")
		}
		if len(rule.ModelRegex) == 0 || len(rule.ModelRegex) > 32 {
			return common.NewMessage("Enter 1 to 32 model patterns, each at most 256 bytes")
		}
		patterns = append(patterns, rule.ModelRegex)
	}
	if len(s.fallbackChains) > 32 {
		return common.NewMessage("Use up to 32 backup rules")
	}
	for _, chain := range s.fallbackChains {
		if len(chain.Groups) == 0 || len(chain.Groups) > 32 || len(chain.ChannelIDs) < 2 || len(chain.ChannelIDs) > 8 || len(chain.ModelRegex) == 0 {
			return common.NewMessage("Invalid primary and backup settings")
		}
		groups := make(map[string]bool, len(chain.Groups))
		for _, group := range chain.Groups {
			if group == "" || group == "auto" || group != strings.TrimSpace(group) || len(group) > 64 || groups[group] {
				return common.NewMessage("Invalid primary and backup settings")
			}
			groups[group] = true
		}
		ids := map[int]bool{s.PrimaryChannelID: true}
		for _, id := range chain.ChannelIDs {
			if id <= 0 || ids[id] {
				return common.NewMessage("Invalid primary and backup settings")
			}
			ids[id] = true
		}
		patterns = append(patterns, chain.ModelRegex)
	}
	for _, modelRegex := range patterns {
		if len(modelRegex) > 32 {
			return common.NewMessage("Use up to 32 model patterns, each at most 256 bytes")
		}
		for _, pattern := range modelRegex {
			if len(pattern) == 0 || len(pattern) > 256 {
				return common.NewMessage("Use up to 32 model patterns, each at most 256 bytes")
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return common.NewMessage("Invalid model pattern: {{error}}", map[string]any{"error": err.Error()})
			}
		}
	}
	return nil
}
