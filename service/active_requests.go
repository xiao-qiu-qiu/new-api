package service

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/active_requests"
	"github.com/gin-gonic/gin"
)

// BeginActiveRequest gives cleanup ownership to the first ordinary HTTP relay
// entry point. Keep the handle on Gin after finishing so nested entries and late
// callbacks cannot recreate a completed request.
func BeginActiveRequest(c *gin.Context, modelName string) func() {
	if active_requests.FromContext(c) != nil {
		return func() {}
	}
	userID := common.GetContextKeyInt(c, constant.ContextKeyUserId)
	if userID <= 0 {
		return func() {}
	}
	requestID := c.GetString(common.RequestIdKey)
	if requestID == "" {
		requestID = common.NewRequestId()
		c.Set(common.RequestIdKey, requestID)
	}
	group := common.GetContextKeyString(c, constant.ContextKeyAutoGroup)
	if group == "" {
		group = common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	}
	handle := active_requests.Start(active_requests.Record{
		RequestID:   requestID,
		UserID:      userID,
		Username:    common.GetContextKeyString(c, constant.ContextKeyUserName),
		TokenID:     common.GetContextKeyInt(c, constant.ContextKeyTokenId),
		TokenName:   c.GetString("token_name"),
		ModelName:   modelName,
		Group:       group,
		Channel:     common.GetContextKeyInt(c, constant.ContextKeyChannelId),
		ChannelName: common.GetContextKeyString(c, constant.ContextKeyChannelName),
		StartedAt:   time.Now().UnixMilli(),
		Status:      active_requests.StatusProcessing,
		IsStream:    common.GetContextKeyBool(c, constant.ContextKeyIsStream),
	})
	active_requests.SetContext(c, handle)
	stop := context.AfterFunc(c.Request.Context(), handle.MarkClientGone)
	if c.Request.Context().Err() != nil {
		handle.MarkClientGone()
	}
	return func() {
		stop()
		handle.Finish()
	}
}

// MarkActiveRequestWaiting belongs immediately before sending an upstream HTTP
// attempt, after local conversion and billing preparation have succeeded.
func MarkActiveRequestWaiting(c *gin.Context) {
	active_requests.FromContext(c).SetState(active_requests.StatusWaiting)
}
