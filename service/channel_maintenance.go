package service

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

const (
	channelMaintenanceScopeKey    = "channel_maintenance_scope"
	channelMaintenanceDeadlineKey = "channel_maintenance_deadline"
	channelMaintenanceWaitTimeout = 60 * time.Second
	channelMaintenanceMaxWaiters  = 512
)

var (
	errChannelMaintenanceBlocked     = errors.New("channel maintenance is in progress")
	errChannelMaintenanceTimeout     = errors.New("channel maintenance wait timed out")
	errChannelMaintenanceUnavailable = errors.New("channel maintenance state is unavailable")
	channelMaintenanceWaiters        atomic.Int64
)

// ChannelMaintenanceCapabilities is the deployment executor's versioned
// contract. Fences live in the same Redis database as channel admission leases.
func ChannelMaintenanceCapabilities() gin.H {
	return gin.H{
		"version": 1, "redis": common.RedisEnabled && common.RDB != nil,
		"wait_seconds":  int(channelMaintenanceWaitTimeout / time.Second),
		"lease_seconds": int(channelConcurrencyLeaseTTL / time.Second),
	}
}

// ChannelMaintenanceWaited tells a persistent transport to reconnect its
// upstream after a deployment, even when the selected channel stayed the same.
func ChannelMaintenanceWaited(c *gin.Context) bool {
	if c == nil {
		return false
	}
	_, waited := c.Get(channelMaintenanceDeadlineKey)
	return waited
}

// The primary fence also covers requests about to use its backup or an older
// affinity binding. Admission checks these keys atomically with the actual
// channel's fence, closing the race between selection and deployment start.
func prepareChannelMaintenanceScope(c *gin.Context, modelName, usingGroup string) {
	if c == nil {
		return
	}
	c.Set(channelMaintenanceScopeKey, []int(nil))
	if !common.RedisEnabled || modelName == "" {
		return
	}
	setting := model.CurrentRequestPolicy().Failover
	if channelFailoverBackup(setting, modelName) == 0 {
		return
	}
	if pin, pinned, _ := GetChannelConstraints(c).ResolvedPin(); pinned && !isChannelFailoverMember(setting, pin.ChannelId) {
		return
	}
	primary, err := model.CacheGetChannel(setting.PrimaryChannelID)
	if err != nil || primary == nil || primary.Status != common.ChannelStatusEnabled {
		return
	}
	if satisfied, _ := model.ChannelSatisfiesFilters(primary, modelName, GetChannelConstraints(c).Filters); !satisfied {
		return
	}
	groups := []string{usingGroup}
	if usingGroup == "auto" {
		groups = GetRequestAutoGroups(c, common.GetContextKeyString(c, constant.ContextKeyUserGroup))
	}
	for _, group := range groups {
		if model.IsChannelEnabledForGroupModel(group, modelName, primary.Id) {
			c.Set(channelMaintenanceScopeKey, []int{primary.Id})
			return
		}
	}
}

func channelMaintenanceFenceKeys(c *gin.Context, channelID int) []string {
	ids := []int{channelID}
	if c != nil {
		value, _ := c.Get(channelMaintenanceScopeKey)
		if scope, ok := value.([]int); ok {
			for _, id := range scope {
				if id > 0 && !slices.Contains(ids, id) {
					ids = append(ids, id)
				}
			}
		}
	}
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, "newapi:maintenance:channel:"+strconv.Itoa(id))
	}
	return keys
}

// A deployment fence deliberately outlives its owner's heartbeat. Losing an
// executor must leave requests failing closed, rather than send them to a
// different upstream. Only the owning executor removes it after readiness.
func waitForChannelMaintenance(c *gin.Context, channelID int) error {
	if c == nil || c.Request == nil || common.RDB == nil {
		return errChannelMaintenanceUnavailable
	}
	if channelMaintenanceWaiters.Add(1) > channelMaintenanceMaxWaiters {
		channelMaintenanceWaiters.Add(-1)
		return errChannelMaintenanceUnavailable
	}
	defer channelMaintenanceWaiters.Add(-1)
	deadline, found := c.Get(channelMaintenanceDeadlineKey)
	waitUntil, ok := deadline.(time.Time)
	if !found || !ok {
		waitUntil = time.Now().Add(channelMaintenanceWaitTimeout)
		c.Set(channelMaintenanceDeadlineKey, waitUntil)
		RequestPolicy(c).AddEvent(PolicyEvent{ChannelID: channelID, Decision: PolicyDecision{
			Action: "observe", Reason: "deployment_wait", Source: "channel_maintenance",
		}})
	}
	ctx, cancel := context.WithDeadline(c.Request.Context(), waitUntil)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	keys := channelMaintenanceFenceKeys(c, channelID)
	for {
		if err := ctx.Err(); err != nil {
			if c.Request.Context().Err() != nil {
				return context.Cause(c.Request.Context())
			}
			return errChannelMaintenanceTimeout
		}
		opCtx, cancelRead := context.WithTimeout(ctx, channelConcurrencyTimeout)
		blocked, err := common.RDB.Exists(opCtx, keys...).Result()
		cancelRead()
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			return errChannelMaintenanceUnavailable
		}
		if blocked == 0 {
			RequestPolicy(c).AddEvent(PolicyEvent{ChannelID: channelID, Decision: PolicyDecision{
				Action: "observe", Reason: "deployment_resumed", Source: "channel_maintenance",
			}})
			return nil
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

func IsChannelMaintenanceError(err *types.NewAPIError) bool {
	return err != nil && (err.GetErrorCode() == "channel_maintenance_timeout" || err.GetErrorCode() == "channel_maintenance_unavailable")
}

func channelMaintenanceSelectionError(err error) *ChannelSelectError {
	switch {
	case errors.Is(err, errChannelMaintenanceTimeout):
		return &ChannelSelectError{StatusCode: http.StatusServiceUnavailable, Code: "channel_maintenance_timeout", MessageID: i18n.MsgChannelMaintenanceTimeout}
	case errors.Is(err, errChannelMaintenanceUnavailable):
		return &ChannelSelectError{StatusCode: http.StatusServiceUnavailable, Code: "channel_maintenance_unavailable", MessageID: i18n.MsgChannelMaintenanceUnavailable}
	default:
		return nil
	}
}
