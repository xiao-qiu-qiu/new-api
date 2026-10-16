package operation_setting

import (
	"fmt"
	"math"
	"strconv"

	"github.com/QuantumNous/new-api/setting/config"
)

type QuotaSetting struct {
	StreamDisconnectMode      string  `json:"stream_disconnect_mode"`
	StreamDrainTimeoutSeconds int     `json:"stream_drain_timeout_seconds"`
	EnableFreeModelPreConsume bool    `json:"enable_free_model_pre_consume"` // 是否对免费模型启用预消耗
	TrustQuotaUSD             float64 `json:"trust_quota_usd"`               // 钱包免预扣门槛，0 表示禁用
	PreConsumeMultiplier      float64 `json:"pre_consume_multiplier"`        // 预计输入费用的预扣倍率，仅影响预留
}

// 默认配置
var quotaSetting = QuotaSetting{
	StreamDisconnectMode:      "cancel",
	StreamDrainTimeoutSeconds: 300,
	EnableFreeModelPreConsume: true,
	TrustQuotaUSD:             10,
	PreConsumeMultiplier:      1,
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("quota_setting", &quotaSetting)
}

func GetQuotaSetting() *QuotaSetting {
	return &quotaSetting
}

// ValidateQuotaOption validates reservation settings before they are persisted.
func ValidateQuotaOption(key, value string) error {
	switch key {
	case "quota_setting.stream_disconnect_mode":
		if value != "cancel" && value != "drain" {
			return fmt.Errorf("stream disconnect mode must be cancel or drain")
		}
		return nil
	case "quota_setting.stream_drain_timeout_seconds":
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds < 1 || seconds > 3600 {
			return fmt.Errorf("stream drain timeout must be an integer between 1 and 3600 seconds")
		}
		return nil
	}
	if key != "quota_setting.trust_quota_usd" && key != "quota_setting.pre_consume_multiplier" {
		return nil
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return fmt.Errorf("%s must be a finite non-negative number", key)
	}
	if key == "quota_setting.pre_consume_multiplier" && number == 0 {
		return fmt.Errorf("%s must be greater than zero", key)
	}
	return nil
}

// InputPreConsumeMultiplier rejects invalid runtime settings as well as invalid saves.
func InputPreConsumeMultiplier() (float64, error) {
	multiplier := quotaSetting.PreConsumeMultiplier
	if multiplier <= 0 || math.IsNaN(multiplier) || math.IsInf(multiplier, 0) {
		return 0, fmt.Errorf("pre-consume multiplier must be a finite number greater than zero")
	}
	return multiplier, nil
}
