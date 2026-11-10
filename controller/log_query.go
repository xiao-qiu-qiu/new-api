package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// validatedLogFilter is shared by list/stat/export, including administrator
// callers of the self endpoints. The raw timestamps stay unchanged for cursor
// scope binding; the model freezes the effective bounds in a signed cursor.
func validatedLogFilter(c *gin.Context, all, export bool) (model.LogFilter, bool) {
	filter := logFilterFromQuery(c, all)
	if (all && c.GetInt("role") < common.RoleAdminUser) || (filter.IsHeavy() && c.GetInt("role") < common.RoleAdminUser) {
		common.ApiErrorStatus(c, http.StatusForbidden, common.NewMessage("Error log searches are only available to administrators"))
		return filter, false
	}
	query := c.Request.URL.Query()
	for _, name := range []string{"type", "channel", "start_timestamp", "end_timestamp", "error_keyword", "error_code", "request_result", "model_name", "username", "token_name", "group", "request_id", "upstream_request_id", "cursor"} {
		values := query[name]
		if len(values) > 1 {
			common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid log search parameters"))
			return filter, false
		}
	}
	for _, name := range []string{"type", "channel", "start_timestamp", "end_timestamp"} {
		if values, exists := query[name]; exists {
			value, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil || value < 0 || ((name == "type" || name == "channel") && uint64(value) > uint64(^uint(0)>>1)) {
				common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid log search parameters"))
				return filter, false
			}
		}
	}
	if err := filter.Validate(); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, err)
		return filter, false
	}
	end := common.GetTimestamp()
	if filter.EndTimestamp != 0 {
		end = min(end, filter.EndTimestamp)
	}
	if filter.StartTimestamp > end {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid log search parameters"))
		return filter, false
	}
	if len(c.Query("cursor")) > 2048 {
		common.ApiErrorStatus(c, http.StatusBadRequest, model.ErrInvalidLogCursor)
		return filter, false
	}
	if filter.IsHeavy() {
		if _, err := filter.LimitErrorSearchRange(common.GetTimestamp()); err != nil && c.Query("cursor") == "" {
			common.ApiErrorStatus(c, http.StatusBadRequest, err)
			return filter, false
		}
	} else if export || c.GetInt("role") < common.RoleAdminUser {
		start, err := limitLogTimeRange(filter.StartTimestamp, filter.EndTimestamp)
		if err != nil {
			common.ApiErrorStatus(c, http.StatusBadRequest, err)
			return filter, false
		}
		filter.StartTimestamp = start
	}
	return filter, true
}

func validatedLogPage(c *gin.Context) (*common.PageInfo, bool) {
	for _, name := range []string{"p", "page_size", "ps", "size"} {
		values, exists := c.Request.URL.Query()[name]
		if !exists {
			continue
		}
		if len(values) != 1 {
			common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid log pagination"))
			return nil, false
		}
		n, err := strconv.Atoi(values[0])
		if err != nil || n < 1 {
			common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid log pagination"))
			return nil, false
		}
	}
	page := common.GetPageQuery(c)
	if value := c.Query("p"); value != "" {
		n, _ := strconv.Atoi(value)
		if n > int(^uint(0)>>1)/page.PageSize {
			common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid log pagination"))
			return nil, false
		}
	}
	return page, true
}

func beginLogQuery(c *gin.Context, heavy, export bool) (context.Context, func(), bool) {
	budget := model.LogQueryTimeout
	if export {
		budget = model.LogExportQueryTimeout
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), budget)
	release, retry, err := service.AcquireLogQuery(ctx, c.GetInt("id"), heavy, export)
	if err != nil {
		cancel()
		logQueryError(c, err)
		return nil, nil, false
	}
	if retry > 0 {
		cancel()
		c.Header("Retry-After", strconv.Itoa(retry))
		common.ApiErrorStatus(c, http.StatusTooManyRequests, common.NewMessage("Too many log queries. Try again shortly."))
		return nil, nil, false
	}
	return ctx, func() {
		defer cancel()
		// A failed SQL connection or stream can return before the server stops.
		// Keep its lease until expiry; successful requests release before cancel.
		if c.Writer.Status() >= http.StatusInternalServerError || c.GetBool("log_query_failed") || len(c.Errors) > 0 {
			return
		}
		release()
	}, true
}

func logQueryError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		common.ApiErrorStatus(c, http.StatusGatewayTimeout, common.NewMessage("Log query timed out. Narrow the time range and try again."))
	case errors.Is(err, context.Canceled):
		common.ApiErrorStatus(c, http.StatusRequestTimeout, common.NewMessage("Log query was canceled"))
	case errors.Is(err, model.ErrInvalidLogCursor):
		common.ApiErrorStatus(c, http.StatusBadRequest, err)
	default:
		common.SysError(common.LogText("failed to query logs: %s", err.Error()))
		common.ApiErrorStatus(c, http.StatusServiceUnavailable, common.NewMessage("Failed to query logs"))
	}
}
