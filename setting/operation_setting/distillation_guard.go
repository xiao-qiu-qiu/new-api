package operation_setting

import (
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
)

const DistillationGuardOption = "DistillationGuardPolicy"

type DistillationGuardPolicy struct {
	Enabled             bool `json:"enabled"`
	MinOutputTokens     int  `json:"min_output_tokens"`
	MaxCachedTokens     int  `json:"max_cached_tokens"`
	WindowMinutes       int  `json:"window_minutes"`
	ConsecutiveRequests int  `json:"consecutive_requests"`
	BlockMinutes        int  `json:"block_minutes"`
}

var currentDistillationGuard atomic.Pointer[DistillationGuardPolicy]

func DefaultDistillationGuard() DistillationGuardPolicy {
	return DistillationGuardPolicy{
		Enabled: true, MinOutputTokens: 3000, MaxCachedTokens: 0,
		WindowMinutes: 5, ConsecutiveRequests: 10, BlockMinutes: 10,
	}
}

func GetDistillationGuard() DistillationGuardPolicy {
	if policy := currentDistillationGuard.Load(); policy != nil {
		return *policy
	}
	return DefaultDistillationGuard()
}

func SetDistillationGuard(policy DistillationGuardPolicy) {
	currentDistillationGuard.Store(&policy)
}

func ParseDistillationGuard(raw string) (DistillationGuardPolicy, error) {
	var policy DistillationGuardPolicy
	if err := common.UnmarshalJsonStr(raw, &policy); err != nil ||
		policy.MinOutputTokens < 1 || policy.MinOutputTokens > 10_000_000 ||
		policy.MaxCachedTokens < 0 || policy.MaxCachedTokens > 10_000_000 ||
		policy.WindowMinutes < 1 || policy.WindowMinutes > 1440 ||
		policy.ConsecutiveRequests < 1 || policy.ConsecutiveRequests > 1000 ||
		policy.BlockMinutes < 1 || policy.BlockMinutes > 1440 {
		return DistillationGuardPolicy{}, common.NewMessage("Invalid distillation protection settings")
	}
	return policy, nil
}
