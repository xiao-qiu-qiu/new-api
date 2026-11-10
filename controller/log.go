package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// logFilterFromQuery reads the log search shared by the log list and the log
// export. Username and channel are read only for administrators.
func logFilterFromQuery(c *gin.Context, admin bool) model.LogFilter {
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	filter := model.LogFilter{
		Type:              logType,
		StartTimestamp:    startTimestamp,
		EndTimestamp:      endTimestamp,
		ModelName:         c.Query("model_name"),
		TokenName:         c.Query("token_name"),
		Group:             c.Query("group"),
		RequestId:         c.Query("request_id"),
		UpstreamRequestId: c.Query("upstream_request_id"),
		ErrorKeyword:      c.Query("error_keyword"),
		ErrorCode:         c.Query("error_code"),
		RequestResult:     c.Query("request_result"),
	}
	if admin {
		filter.Username = c.Query("username")
		filter.Channel, _ = strconv.Atoi(c.Query("channel"))
	}
	return filter
}

// limitLogTimeRange returns the start of a log or dashboard search kept within
// the longest log time range. A missing start reaches back the whole range, and
// time after now does not count toward it.
func limitLogTimeRange(startTimestamp int64, endTimestamp int64) (int64, error) {
	maxRange := operation_setting.GetLogSetting().MaxRangeSeconds()
	end := common.GetTimestamp()
	if endTimestamp != 0 {
		end = min(end, endTimestamp)
	}
	if startTimestamp == 0 {
		return end - maxRange, nil
	}
	if end-startTimestamp > maxRange {
		return 0, common.NewMessage("The time range cannot exceed {{days}} days", map[string]any{"days": maxRange / (24 * 60 * 60)})
	}
	return startTimestamp, nil
}

func GetAllLogs(c *gin.Context) {
	getLogs(c, true)
}

func GetUserLogs(c *gin.Context) {
	getLogs(c, false)
}

func getLogs(c *gin.Context, all bool) {
	filter, ok := validatedLogFilter(c, all, false)
	if !ok {
		return
	}
	page, ok := validatedLogPage(c)
	if !ok {
		return
	}
	ctx, finish, ok := beginLogQuery(c, filter.NeedsScanBudget(common.GetTimestamp()), false)
	if !ok {
		return
	}
	defer finish()
	result, err := model.GetLogPage(ctx, c.GetInt("id"), all, filter, page.Page, page.PageSize, c.Query("cursor"))
	if err != nil {
		logQueryError(c, err)
		return
	}
	if all {
		if c.GetInt("role") < common.RoleRootUser {
			model.FormatAdminLogs(result.Items)
		} else {
			model.FormatRootLogs(result.Items)
		}
	}
	common.ApiSuccess(c, result)
}

// ExportAllLogs streams every user's logs matching the search.
func ExportAllLogs(c *gin.Context) {
	exportLogs(c, true)
}

// ExportUserLogs streams the caller's own logs matching the search.
func ExportUserLogs(c *gin.Context) {
	exportLogs(c, false)
}

// exportLogs writes one JSON log per line, newest first, and ends with an API
// response object: success with the log count, or the failure. Each user runs
// one export at a time, and every export stays within the longest log time
// range.
func exportLogs(c *gin.Context, all bool) {
	filter, valid := validatedLogFilter(c, all, true)
	if !valid {
		return
	}
	if !all && c.GetInt("role") < common.RoleAdminUser && !operation_setting.GetLogSetting().UserExportEnabled {
		common.ApiErrorT(c, "Log export is disabled")
		return
	}
	if filter.IsHeavy() {
		var err error
		filter, err = filter.LimitErrorSearchRange(common.GetTimestamp())
		if err != nil {
			common.ApiErrorStatus(c, http.StatusBadRequest, err)
			return
		}
	}

	userId := c.GetInt("id")
	release, ok, err := service.AcquireLogExportLock(userId)
	if err != nil {
		common.SysError(common.LogText("failed to lock log export: %s", err.Error()))
		common.ApiErrorT(c, "Failed to export logs")
		return
	}
	if !ok {
		c.Header("Retry-After", "1")
		common.ApiErrorStatus(c, http.StatusTooManyRequests, common.NewMessage("Another log export is running. Try again after it finishes."))
		return
	}
	defer release()
	ctx, finish, ok := beginLogQuery(c, filter.NeedsScanBudget(common.GetTimestamp()), true)
	if !ok {
		return
	}
	defer finish()
	// Bound slow-client writes as well as database execution. Gin unwraps its
	// writer to the server connection; httptest may not support deadlines.
	responseControl := http.NewResponseController(c.Writer)
	deadline, _ := ctx.Deadline()
	if err := responseControl.SetWriteDeadline(deadline); err == nil {
		defer responseControl.SetWriteDeadline(time.Time{})
	}

	c.Header("Content-Type", "application/x-ndjson; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	count := 0
	writeLog := func(log *model.Log) error {
		line, err := common.Marshal(log)
		if err != nil {
			return err
		}
		if _, err := c.Writer.Write(append(line, '\n')); err != nil {
			return err
		}
		count++
		return nil
	}
	if all {
		err = model.StreamAllLogs(ctx, filter, c.GetInt("role"), writeLog)
	} else {
		err = model.StreamUserLogs(ctx, userId, filter, writeLog)
	}
	if err != nil {
		// The response may already be 200, including socket write failures.
		c.Set("log_query_failed", true)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	// The status is already sent, so the result object becomes the last line.
	if err != nil {
		common.SysError(common.LogText("failed to export logs: %s", err.Error()))
		if errors.Is(err, context.DeadlineExceeded) {
			common.ApiErrorT(c, "Log query timed out. Narrow the time range and try again.")
		} else {
			common.ApiErrorT(c, "Failed to export logs")
		}
		return
	}
	common.ApiSuccess(c, gin.H{"count": count})
}

// Deprecated: SearchAllLogs 已废弃，前端未使用该接口。
func SearchAllLogs(c *gin.Context) {
	common.ApiErrorT(c, "This API is deprecated")
}

// Deprecated: SearchUserLogs 已废弃，前端未使用该接口。
func SearchUserLogs(c *gin.Context) {
	common.ApiErrorT(c, "This API is deprecated")
}

func GetLogByKey(c *gin.Context) {
	tokenId := c.GetInt("token_id")
	if tokenId == 0 {
		common.ApiErrorI18n(c, i18n.MsgTokenInvalid)
		return
	}
	logs, err := model.GetLogByTokenId(tokenId)
	if err != nil {
		c.JSON(200, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	c.JSON(200, gin.H{
		"success": true,
		"message": "",
		"data":    logs,
	})
}

func GetLogsStat(c *gin.Context) {
	getLogsStat(c, true)
}

func GetLogsSelfStat(c *gin.Context) {
	getLogsStat(c, false)
}

func getLogsStat(c *gin.Context, all bool) {
	filter, ok := validatedLogFilter(c, all, false)
	if !ok {
		return
	}
	if filter.IsHeavy() {
		var err error
		filter, err = filter.LimitErrorSearchRange(common.GetTimestamp())
		if err != nil {
			common.ApiErrorStatus(c, http.StatusBadRequest, err)
			return
		}
	}
	ctx, finish, ok := beginLogQuery(c, filter.NeedsScanBudget(common.GetTimestamp()), false)
	if !ok {
		return
	}
	defer finish()
	var stat model.Stat
	var err error
	if all {
		stat, err = model.SumUsedQuotaWithContext(ctx, filter)
	} else {
		stat, err = model.SumUserUsedQuotaWithContext(ctx, c.GetInt("id"), filter)
	}
	if err != nil {
		logQueryError(c, err)
		return
	}
	common.ApiSuccess(c, stat)
}
