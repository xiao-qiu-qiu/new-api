package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/active_requests"
	"github.com/gin-gonic/gin"
)

func GetActiveRequests(c *gin.Context) {
	getActiveRequests(c, true)
}

func GetUserActiveRequests(c *gin.Context) {
	getActiveRequests(c, false)
}

func getActiveRequests(c *gin.Context, admin bool) {
	c.Header("Cache-Control", "no-store")
	search := logFilterFromQuery(c, admin)
	filter := active_requests.Filter{
		Type: search.Type, StartTimestamp: search.StartTimestamp, EndTimestamp: search.EndTimestamp,
		ModelName: search.ModelName, Username: search.Username, TokenName: search.TokenName,
		Channel: search.Channel, Group: search.Group, RequestID: search.RequestId,
		UpstreamRequestID: search.UpstreamRequestId,
	}
	if !admin {
		filter.UserID = c.GetInt("id")
		if filter.UserID <= 0 {
			// A missing identity must never turn a self search into a global one.
			filter.UserID = -1
		}
		if c.GetInt("role") < common.RoleAdminUser {
			start, err := limitLogTimeRange(filter.StartTimestamp, filter.EndTimestamp)
			if err != nil {
				common.ApiError(c, err)
				return
			}
			filter.StartTimestamp = start
		}
	}
	result, err := active_requests.Query(c.Request.Context(), filter)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !admin {
		for i := range result.Items {
			result.Items[i].Username = ""
			result.Items[i].Channel = 0
			result.Items[i].ChannelName = ""
		}
	}
	common.ApiSuccess(c, result)
}
