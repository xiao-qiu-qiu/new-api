package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// Integration contract (the router owns access control):
// GET/POST /api/channel/probes -> ListChannelProbes / CreateChannelProbe
// PUT/DELETE /api/channel/probes/:id -> UpdateChannelProbe / DeleteChannelProbe
// POST /api/channel/probes/:id/run -> RunChannelProbe
// GET /api/channel/probes/status -> GetPublicChannelProbeStatus (public)
// JSON is always {success, message, data}. PUT replaces all editable fields.
// Timestamps are Unix seconds; TTFT/latency/thresholds are milliseconds.
// Raw history is newest first (60/model); timeline contains minute-based sample counts.

func ListChannelProbes(c *gin.Context) {
	probes, err := model.ListChannelProbeConfigs()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "failed to load probes"})
		return
	}
	views := make([]service.ChannelProbeView, 0, len(probes))
	for _, probe := range probes {
		view, err := service.BuildChannelProbeView(probe, time.Now())
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "failed to load probe history"})
			return
		}
		views = append(views, view)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": views})
}

func GetPublicChannelProbeStatus(c *gin.Context) {
	hours, err := strconv.Atoi(c.DefaultQuery("hours", "1"))
	if err != nil || !slices.Contains([]int{1, 6, 12, 24}, hours) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "hours must be 1, 6, 12 or 24"})
		return
	}
	now := time.Now()
	c.Header("Cache-Control", "no-store")
	probes, err := model.ListChannelProbeConfigs()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "status temporarily unavailable"})
		return
	}
	views := make([]service.PublicChannelProbeView, 0)
	for _, probe := range probes {
		if !probe.Enabled || !probe.Public || probe.PublicName == "" {
			continue
		}
		for _, name := range probe.Models {
			view, err := service.BuildPublicChannelProbeModel(probe, name, now, hours)
			if err != nil {
				c.JSON(http.StatusOK, gin.H{"success": false, "message": "status temporarily unavailable"})
				return
			}
			views = append(views, view)
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": views})
}

func CreateChannelProbe(c *gin.Context) {
	saveChannelProbe(c, true)
}

func UpdateChannelProbe(c *gin.Context) {
	saveChannelProbe(c, false)
}

func saveChannelProbe(c *gin.Context, create bool) {
	var probe model.ChannelProbe
	if err := c.ShouldBindJSON(&probe); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid probe configuration"})
		return
	}
	probe.ID = 0
	if !create {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil || id <= 0 {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid probe id"})
			return
		}
		probe.ID = id
	}
	if err := service.ValidateChannelProbe(&probe); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if probe.Scope == "upstream" {
		var channel model.Channel
		if err := model.DB.Select("id").First(&channel, probe.ChannelID).Error; err != nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "channel not found"})
			return
		}
	} else {
		groups, err := model.ListChannelProbeGroups()
		valid := false
		for _, group := range groups {
			if group.Name != probe.GroupName {
				continue
			}
			valid = true
			for _, name := range probe.Models {
				if !slices.Contains(group.Models, name) {
					valid = false
					break
				}
			}
		}
		if err != nil || !valid {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "group models must have enabled channels"})
			return
		}
	}
	if create {
		var count int64
		if err := model.DB.Model(&model.ChannelProbe{}).Count(&count).Error; err != nil || count >= 256 {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "probe limit reached or database unavailable"})
			return
		}
	}
	if err := model.SaveChannelProbe(&probe, create); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "failed to save probe; this target and model set may already exist"})
		return
	}
	_ = service.RefreshChannelProbeCache()
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": probe})
}

func DeleteChannelProbe(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid probe id"})
		return
	}
	if err := model.DeleteChannelProbe(id); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "failed to delete probe"})
		return
	}
	_ = service.RefreshChannelProbeCache()
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": nil})
}

func RunChannelProbe(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid probe id"})
		return
	}
	probe, err := model.GetChannelProbe(id)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "probe not found"})
		return
	}
	if err := service.QueueChannelProbe(probe, true); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"queued": true}})
}

type channelProbeContextKey struct{}

// RunActiveChannelProbe uses the native adapter test path directly, with no
// public HTTP relay request, root token, user quota, or channel auto-disable.
func RunActiveChannelProbe(ctx context.Context, probe model.ChannelProbe) model.ChannelProbeResult {
	if probe.Scope != "group" {
		channel, err := model.GetChannelById(probe.ChannelID, true)
		if err != nil {
			return model.ChannelProbeResult{ErrorCode: "channel_missing"}
		}
		observation, _ := runChannelProbeAttempt(ctx, channel, probe.Model)
		return observation
	}
	started := time.Now()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", nil)
	retry := &service.RetryParam{Ctx: c, TokenGroup: probe.GroupName, ModelName: probe.Model, RequestPath: c.Request.URL.Path, Retry: common.GetPointer(0)}
	observation := model.ChannelProbeResult{ErrorCode: "channel_missing"}
	for ; retry.GetRetry() <= common.RetryTimes; retry.IncreaseRetry() {
		if ctx.Err() != nil {
			observation.Success, observation.ErrorCode = false, "timeout"
			return observation
		}
		channel, _, err := service.CacheGetRandomSatisfiedChannel(retry)
		if err != nil || channel == nil {
			return model.ChannelProbeResult{ErrorCode: "channel_missing"}
		}
		service.RequestPolicy(c).BeginAttempt(channel, probe.GroupName)
		elapsed := time.Since(started).Milliseconds()
		var attempt testResult
		observation, attempt = runChannelProbeAttempt(ctx, channel, probe.Model)
		if observation.TTFTMS != nil {
			ms := *observation.TTFTMS + elapsed
			observation.TTFTMS = &ms
		}
		if observation.Success || ctx.Err() != nil {
			return observation
		}
		retryError := attempt.newAPIError
		if retryError == nil {
			retryError = types.NewErrorWithStatusCode(errors.New("probe generation failed"), types.ErrorCodeBadResponse, http.StatusBadGateway)
		}
		if service.DecideRelayRetry(c, retryError, common.RetryTimes-retry.GetRetry()).Action != "retry" {
			break
		}
	}
	return observation
}

func runChannelProbeAttempt(ctx context.Context, channel *model.Channel, modelName string) (model.ChannelProbeResult, testResult) {
	result := testChannel(context.WithValue(ctx, channelProbeContextKey{}, true), channel, 0, modelName, "", true)
	if result.probeObserver == nil {
		return model.ChannelProbeResult{ErrorCode: "unsupported_channel"}, result
	}
	observation := result.probeObserver.Result(ctx, result.relayInfo, result.newAPIError)
	if result.localErr != nil && observation.Success {
		observation.Success, observation.ErrorCode = false, "probe_failed"
	}
	noContent := observation.Success
	if observation.ErrorCode == "incomplete" && result.localErr == nil && result.newAPIError == nil && result.relayInfo != nil {
		outcome := result.relayInfo.StreamStatus.OutcomeSnapshot()
		noContent = outcome.EndReason == relaycommon.StreamEndReasonEOF && outcome.Response == relaycommon.ResponseOutcomeUnknown
	}
	if noContent && observation.TTFTMS == nil {
		observation.Success, observation.ErrorCode = false, "no_content"
	}
	return observation, result
}

func GetChannelProbeGroups(c *gin.Context) {
	groups, err := model.ListChannelProbeGroups()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "failed to load enabled groups"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": groups})
}

func OrderChannelProbes(c *gin.Context) {
	var body struct {
		IDs []int `json:"ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid order"})
		return
	}
	if err := model.OrderChannelProbes(body.IDs); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	_ = service.RefreshChannelProbeCache()
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": nil})
}
