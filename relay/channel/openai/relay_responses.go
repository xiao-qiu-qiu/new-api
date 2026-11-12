package openai

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
)

func OaiResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// read response body
	var responsesResponse dto.OpenAIResponsesResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	if apiErr := service.CyberPolicyErrorFromPayload(responseBody, resp.StatusCode); apiErr != nil {
		service.ObserveCyberPolicyError(c, apiErr)
		return nil, apiErr
	}
	err = common.Unmarshal(responseBody, &responsesResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oaiError := responsesResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	info.ObserveResponseModel(responsesResponse.Model)
	info.ObserveResponseServiceTier(responseBody)
	responseBody = rewriteSGLangResponsesCreatedAt(info, responseBody, "created_at", responsesResponse.CreatedAt)

	// 写入新的 response body
	service.IOCopyBytesGracefully(c, resp, responseBody)

	// compute usage
	usage := &dto.Usage{}
	service.ApplyResponsesUsage(usage, responsesResponse.Usage)
	// Count actual tool invocations from Output (not tool declarations).
	for _, output := range responsesResponse.Output {
		switch output.Type {
		case dto.BuildInCallWebSearchCall:
			info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
		case dto.BuildInCallFileSearchCall:
			info.CountBillableToolCall(dto.BuildInCallFileSearchCall, "")
		case dto.BuildInCallFunctionCall:
			info.CountBillableToolCall(dto.BuildInCallFunctionCall, output.Name)
		}
	}
	info.ApplyVendorToolUsage(responseBody)

	imageCounter := &relaycommon.ImageGenerationCallCounter{}
	for i := range responsesResponse.Output {
		imageCounter.Observe(&responsesResponse.Output[i], &i)
	}
	imageCounter.Commit(info)

	return usage, nil
}

func OaiResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, common.LogText("invalid response or response body"))
		return nil, types.NewError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse)
	}

	defer service.CloseResponseBodyGracefully(resp)

	accumulator := service.NewResponsesUsageAccumulator(info)
	var streamError *types.NewAPIError
	accepted := false

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {

		// 检查当前数据是否包含 completed 状态和 usage 信息
		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
			logger.LogError(c, common.LogText("failed to unmarshal stream response: %s", err.Error()))
			sr.Error(err)
			return
		}
		if streamResponse.Response != nil {
			data = string(rewriteSGLangResponsesCreatedAt(info, []byte(data), "response.created_at", streamResponse.Response.CreatedAt))
		}
		accumulator.Observe(&streamResponse, common.StringToByteSlice(data))
		apiErr := openaiStreamError(data)
		if apiErr == nil || streamResponse.Response != nil &&
			(streamResponse.Response.Usage != nil || len(streamResponse.Response.Output) > 0) {
			accepted = true
		}
		noRetry := accepted || c.Writer.Written() || info.StreamLifecycle.ClientError() != nil
		common.SetContextKey(c, constant.ContextKeyResponseStreamNoRetry, noRetry)
		if apiErr != nil {
			if streamError == nil {
				streamError = apiErr
			}
			info.StreamStatus.MarkFailed(string(apiErr.GetErrorCode()), apiErr.ToOpenAIError().Type, apiErr.StatusCode)
			// Keep an initial rejection uncommitted so the shared retry policy
			// can select another channel before any response reaches the client.
			if !noRetry {
				sr.Done()
				return
			}
		}
		written := c.Writer.Size()
		if err := helper.ResponseChunkData(c, streamResponse, data); err != nil {
			sr.Stop(err)
			return
		}
		if apiErr != nil && c.Writer.Size() > written && info.StreamLifecycle.ClientError() == nil {
			common.SetContextKey(c, constant.ContextKeyResponseStreamErrorForwarded, true)
		}
		// Responses ends at its protocol terminal; providers may keep the HTTP
		// connection open indefinitely after sending the final usage.
		switch streamResponse.Type {
		case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
			sr.Done()
		}
	})

	common.SetContextKey(c, constant.ContextKeyResponseStreamStatus, info.StreamStatus)
	info.StreamStatus.RequireTerminal()
	if streamError != nil && common.GetContextKeyBool(c, constant.ContextKeyResponseStreamNoRetry) {
		// A prefix, heartbeat or error frame already committed this attempt.
		// Preserve its accounting facts, but never replay the request.
		types.ErrOptionWithSkipRetry()(streamError)
	}
	return accumulator.Finish(), streamError
}

func openaiStreamError(data string) *types.NewAPIError {
	eventType := strings.ToLower(strings.TrimSpace(ojson.Get(data, "type").Str))
	errorPayload := strings.TrimSpace(ojson.Get(data, "error").Raw)
	hasError := errorPayload != "" && errorPayload != "null" && errorPayload != "{}" && errorPayload != `""`
	if eventType != "error" && eventType != "upstream_error" && eventType != "response.error" &&
		eventType != "response.failed" && ojson.Get(data, "response.status").Str != "failed" && !hasError {
		return nil
	}
	if apiErr := service.CyberPolicyErrorFromPayload(common.StringToByteSlice(data), http.StatusOK); apiErr != nil {
		return apiErr
	}
	var event dto.ResponsesStreamResponse
	_ = common.UnmarshalJsonStr(data, &event)
	upstreamError := types.OpenAIError{Code: event.Code, Message: event.Message, Param: event.Param}
	if raw := ojson.Get(data, "error"); raw.Raw != "" {
		// Older Responses-compatible providers nest their error object.
		_ = common.UnmarshalJsonStr(raw.Raw, &upstreamError)
	}
	if event.Response != nil {
		if responseError := event.Response.GetOpenAIError(); responseError != nil {
			upstreamError = *responseError
		}
	}
	if upstreamError.Code == nil || upstreamError.Code == "" {
		upstreamError.Code = string(types.ErrorCodeBadResponse)
	}
	if upstreamError.Message == "" {
		upstreamError.Message = "upstream stream error: " + eventType
	}
	status := http.StatusBadGateway
	switch upstreamError.Type {
	case "invalid_request_error":
		status = http.StatusBadRequest
	case "authentication_error":
		status = http.StatusUnauthorized
	case "permission_error":
		status = http.StatusForbidden
	case "rate_limit_error":
		status = http.StatusTooManyRequests
	}
	// Standard error events carry a code but have no error-type field.
	// Preserve their client/rate-limit classification before retry decisions.
	switch fmt.Sprint(upstreamError.Code) {
	case "invalid_request", "invalid_request_error", "invalid_argument", "invalid_value",
		"unsupported_value", "invalid_prompt", "context_length_exceeded", "string_above_max_length",
		"previous_response_not_found", "invalid_encrypted_content":
		status = http.StatusBadRequest
	case "invalid_api_key", "authentication_error", "token_expired":
		status = http.StatusUnauthorized
	case "permission_error", "permission_denied", "access_denied", "basispoints_model_access_changed":
		status = http.StatusForbidden
	case "model_not_found", "model_not_found_error":
		status = http.StatusNotFound
	case "rate_limit_error", "rate_limit_exceeded", "insufficient_quota", "usage_limit_reached":
		status = http.StatusTooManyRequests
	case "overloaded_error", "service_unavailable":
		status = http.StatusServiceUnavailable
	}
	for _, path := range []string{"response.error.status", "error.status", "status"} {
		if code, err := strconv.Atoi(ojson.Get(data, path).Raw); err == nil && code >= 400 && code <= 599 {
			status = code
			break
		}
	}
	return types.WithOpenAIError(upstreamError, status)
}

func rewriteSGLangResponsesCreatedAt(info *relaycommon.RelayInfo, payload []byte, path string, createdAt dto.IntValue) []byte {
	if info.GetChannelType() != constant.ChannelTypeSGLang {
		return payload
	}
	if !ojson.GetBytes(payload, path).Exists() {
		return payload
	}
	patched, err := ojson.SetBytes(payload, path, int(createdAt))
	if err != nil {
		return payload
	}
	return patched
}
