package operation_setting

import (
	"regexp"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

// ChannelFailoverSetting routes eligible requests through a primary/backup pair.
// Availability is observed at runtime; it never changes a channel's saved status.
type ChannelFailoverSetting struct {
	Enabled             bool                          `json:"enabled"`
	PrimaryChannelID    int                           `json:"primary_channel_id"`
	FallbackChannelID   int                           `json:"fallback_channel_id"`
	FallbackRules       []ChannelFailoverFallbackRule `json:"fallback_rules"`
	ModelRegex          []string                      `json:"model_regex"`
	PollIntervalSeconds int                           `json:"poll_interval_seconds"`
	RecoverySuccesses   int                           `json:"recovery_successes"`
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
