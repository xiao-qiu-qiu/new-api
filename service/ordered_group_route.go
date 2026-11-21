package service

import (
	"errors"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

const orderedGroupRouteKey = "ordered_token_group_route"
const Sub2APIWSAccountUnavailable = "sub2api_ws_account_unavailable"

type orderedGroupRoute struct {
	groups         []string
	index          int
	advance        bool
	preferred      int
	priorityOffset int
	current        int
	empty          []int
	lastError      *types.NewAPIError
}

func requestOrderedGroupRoute(c *gin.Context) *orderedGroupRoute {
	if c == nil {
		return nil
	}
	value, _ := c.Get(orderedGroupRouteKey)
	route, _ := value.(*orderedGroupRoute)
	return route
}

func (p *RetryParam) HasOrderedGroupRoute() bool {
	return requestOrderedGroupRoute(p.Ctx) != nil
}

func (p *RetryParam) orderedGroupCandidates() []string {
	c := p.Ctx
	if p.TokenGroup != "auto" || !common.GetContextKeyBool(c, constant.ContextKeyTokenCrossGroupRetry) || c.Request == nil || c.Request.URL == nil {
		return nil
	}
	switch c.Request.URL.Path {
	case "/v1/responses", "/v1/responses/compact", "/v1/chat/completions", "/pg/chat/completions":
	default:
		return nil
	}
	groups := GetRequestAutoGroups(c, common.GetContextKeyString(c, constant.ContextKeyUserGroup))
	if len(groups) < 2 {
		return nil
	}
	return groups
}

func (p *RetryParam) prepareOrderedGroupRoute(groups []string, preferred int) bool {
	if groups == nil || p.PreserveConnection || HasChannelAffinityFallback(p.Ctx) {
		return false
	}
	if preferred > 0 && ShouldSkipRetryAfterChannelAffinityFailure(p.Ctx) {
		bound, err := model.CacheGetChannel(preferred)
		if err != nil || bound == nil {
			return false
		}
		memberships := bound.GetGroups()
		priority := slices.IndexFunc(groups, func(group string) bool { return slices.Contains(memberships, group) })
		if priority < 0 || priority == 0 && eligibleFailoverChannel(p.Ctx, preferred, groups[0], p.ModelName) == nil {
			return false
		}
	}
	p.Ctx.Set(orderedGroupRouteKey, &orderedGroupRoute{groups: groups, preferred: preferred})
	return true
}

func (p *RetryParam) MayRerouteConnection(channelID int) bool {
	c := p.Ctx
	if groups := p.orderedGroupCandidates(); groups != nil {
		if _, pinned, _ := GetChannelConstraints(c).ResolvedPin(); pinned || p.PreserveConnection {
			return false
		}
		preferred, bound := GetPreferredChannelByAffinity(c, p.ModelName, p.TokenGroup)
		if !bound {
			preferred = channelID
		}
		if p.prepareOrderedGroupRoute(groups, preferred) {
			return true
		}
	}
	return ChannelFailoverMayReroute(c, p.ModelName, p.TokenGroup, channelID)
}

func (route *orderedGroupRoute) selectChannel(p *RetryParam) (*model.Channel, string, error) {
	c := p.Ctx
	filters := GetChannelConstraints(c).Filters
	var limited bool
	for route.index < len(route.groups) {
		if route.advance {
			route.advance = false
			route.index++
			route.current = 0
			route.priorityOffset = 0
			c.Set(channelFailoverContextKey, nil)
			p.SetRetry(0)
			continue
		}
		group := route.groups[route.index]
		if err := c.Request.Context().Err(); err != nil {
			return nil, group, err
		}
		prepareChannelMaintenanceScope(c, p.ModelName, group)
		state := requestChannelFailover(c)
		var preferred *model.Channel
		if route.current == 0 {
			if route.preferred > 0 && !slices.Contains(route.empty, route.preferred) {
				preferred = eligibleFailoverChannel(c, route.preferred, group, p.ModelName)
			}
			preferredID := 0
			if preferred != nil {
				preferredID = preferred.Id
			}
			state = prepareChannelFailover(c, p.ModelName, group, preferredID, 0)
			if state != nil {
				state.hasNextGroup = route.index+1 < len(route.groups)
			}
		}
		var channel *model.Channel
		var err error
		if state != nil {
			for _, channelID := range route.empty {
				state.visited[channelID] = true
			}
			channel, _, err = selectChannelFailover(c, state)
			if err != nil && (state.state == "rotation_wait" || state.state == "auth_recovery_wait") {
				return nil, group, err
			}
		} else {
			if preferred != nil {
				err = acquireChannelConcurrency(c, preferred)
				if err == nil {
					channel = preferred
					MarkChannelAffinityUsed(c, group, preferred.Id)
					route.priorityOffset = 1
				} else if ShouldSkipRetryAfterChannelAffinityFailure(c) {
					return nil, group, err
				}
			}
			if channel == nil && (err == nil || errors.Is(err, errChannelConcurrencyLimited)) {
				priority := max(p.GetRetry()-route.priorityOffset, 0)
				channel, err = selectChannelWithConcurrency(c, group, p.ModelName, priority, filters, route.empty...)
			}
		}
		if channel != nil {
			route.current = channel.Id
			common.SetContextKey(c, constant.ContextKeyAutoGroup, group)
			return channel, group, nil
		}
		if errors.Is(err, errChannelConcurrencyLimited) {
			limited = true
		} else if err != nil && !errors.Is(err, errChannelFailoverExhausted) {
			return nil, group, err
		}
		route.advance = true
	}
	group := route.groups[len(route.groups)-1]
	if limited {
		return nil, group, errChannelConcurrencyLimited
	}
	if route.lastError != nil {
		return nil, group, route.lastError
	}
	return nil, group, nil
}

func (p *RetryParam) DecideRelayRetry(err *types.NewAPIError) PolicyDecision {
	c := p.Ctx
	remaining := ChannelFailoverRetryBudget(c) - p.GetRetry()
	route := requestOrderedGroupRoute(c)
	if route == nil || err == nil {
		return DecideRelayRetry(c, err, remaining)
	}
	if c.Request.Context().Err() != nil {
		return PolicyDecision{Action: "stop", Reason: "request_cancelled", Source: "system"}
	}
	if c.Writer != nil && c.Writer.Written() {
		return PolicyDecision{Action: "stop", Reason: "response_started", Source: "system"}
	}
	hasNextGroup := route.index+1 < len(route.groups)
	capacity := channelCapacityExhausted(err) || err.GetErrorCode() == Sub2APIWSAccountUnavailable
	state := requestChannelFailover(c)
	budget := remaining
	if state == nil && hasNextGroup {
		budget = max(1, budget)
	}
	decision := DecideRelayRetry(c, err, budget)
	if decision.Action == "stop" {
		switch decision.Reason {
		case "primary_backup_exhausted", "attempt_budget_exhausted", "status_not_retryable", "non_retryable_error", "strict_session":
		default:
			return decision
		}
	}
	if operation_setting.IsAlwaysSkipRetryCode(err.GetErrorCode()) {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	if channelRotationWaiting(err) {
		return PolicyDecision{Action: "stop", Reason: "primary_rotation_wait", Source: "channel_failover"}
	}
	route.lastError = err
	if capacity {
		route.empty = append(route.empty, route.current)
	}
	if state != nil {
		if decision.Action == "retry" || decision.Reason != "primary_backup_exhausted" && decision.Reason != "attempt_budget_exhausted" {
			return decision
		}
		if !capacity && (ShouldSkipRetryAfterChannelAffinityFailure(c) || types.IsSkipRetryError(err) || !operation_setting.ShouldRetryByStatusCode(err.StatusCode)) {
			return decision
		}
	} else if !capacity && (decision.Action != "retry" || remaining > 0) {
		return decision
	}
	if capacity && state == nil {
		next, lookupErr := model.GetRandomSatisfiedChannel(route.groups[route.index], p.ModelName, 0, GetChannelConstraints(c).Filters, route.empty...)
		if lookupErr != nil {
			return PolicyDecision{Action: "stop", Reason: "channel_lookup_failed", Source: "token_groups"}
		}
		if next != nil {
			route.priorityOffset = 0
			p.SetRetry(0)
			p.ResetRetryNextTry()
			return PolicyDecision{Action: "retry", Reason: "channel_capacity_exhausted", Source: "token_groups"}
		}
	}
	if !hasNextGroup {
		return PolicyDecision{Action: "stop", Reason: "group_candidates_exhausted", Source: "token_groups"}
	}
	route.advance = true
	p.SetRetry(0)
	p.ResetRetryNextTry()
	return PolicyDecision{Action: "retry", Reason: "try_next_group", Source: "token_groups"}
}
