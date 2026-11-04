package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newResponsesChatTestContext(t *testing.T, body string, isStream bool) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set(common.RequestIdKey, "responses-test")

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
		IsStream:           isStream,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
	}
	return c, recorder, resp, info
}

func TestCyberPolicyBlocksChatResponsesConversions(t *testing.T) {
	require.NoError(t, i18n.Init())
	t.Setenv("CYBER_SESSION_BLOCK_ENABLED", "true")
	t.Setenv("CYBER_SESSION_BLOCK_TTL_SECONDS", "3600")
	previousRedis := common.RedisEnabled
	previousRetryStatuses := operation_setting.AutomaticRetryStatusCodeRanges
	common.RedisEnabled = false
	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 403, End: 403}, {Start: 502, End: 502}}
	t.Cleanup(func() {
		common.RedisEnabled = previousRedis
		operation_setting.AutomaticRetryStatusCodeRanges = previousRetryStatuses
	})

	for index, tc := range []struct {
		name, payload            string
		sse, stream, toResponses bool
		handler                  func(*gin.Context, *relaycommon.RelayInfo, *http.Response) (*dto.Usage, *types.NewAPIError)
	}{
		{name: "Responses to Chat JSON without error type", payload: `{"error":{"code":"cyber_policy","message":"policy rejected"}}`, handler: OaiResponsesToChatHandler},
		{name: "Chat to Responses JSON without error type", payload: `{"error":{"code":"cyber_policy","message":"policy rejected"}}`, toResponses: true, handler: OaiChatToResponsesHandler},
		{name: "buffered SSE error", sse: true, payload: `{"type":"error","code":"cyber_policy","message":"policy rejected"}`, handler: OaiResponsesToChatBufferedStreamHandler},
		{name: "buffered SSE response.error", sse: true, payload: `{"type":"response.error","response":{"error":{"code":"cyber_policy","message":"policy rejected"}}}`, handler: OaiResponsesToChatBufferedStreamHandler},
		{name: "buffered SSE response.failed", sse: true, payload: `{"type":"response.failed","response":{"status":"failed","error":{"code":"cyber_policy","message":"policy rejected"}}}`, handler: OaiResponsesToChatBufferedStreamHandler},
		{name: "Responses to Chat stream before output", sse: true, stream: true, payload: `{"type":"response.failed","response":{"status":"failed","error":{"code":"cyber_policy","message":"policy rejected"}}}`, handler: OaiResponsesToChatStreamHandler},
		{name: "Chat to Responses stream emits response.failed", sse: true, stream: true, toResponses: true, payload: `{"error":{"code":"cyber_policy","message":"policy rejected"}}`, handler: OaiChatToResponsesStreamHandler},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstreamBody := tc.payload
			contentType := "application/json"
			if tc.sse {
				upstreamBody = "data: " + tc.payload + "\n\ndata: [DONE]\n\n"
				contentType = "text/event-stream"
			}
			c, recorder, resp, info := newResponsesChatTestContext(t, upstreamBody, tc.stream)
			resp.Header.Set("Content-Type", contentType)
			path := "/v1/chat/completions"
			requestBody := `{"model":"gpt-test","messages":[{"role":"user","content":"first turn"}]}`
			followupBody := `{"model":"gpt-test","messages":[{"role":"user","content":"next turn"}]}`
			if tc.toResponses {
				path = "/v1/responses"
				requestBody = `{"model":"gpt-test","input":"first turn"}`
				followupBody = `{"model":"gpt-test","input":"next turn"}`
				info.RelayFormat = types.RelayFormatOpenAIResponses
			}
			if tc.stream {
				requestBody = strings.TrimSuffix(requestBody, "}") + `,"stream":true}`
			}
			c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(requestBody))
			followup, _ := gin.CreateTestContext(httptest.NewRecorder())
			followup.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(followupBody))
			sessionID := common.GetUUID()
			for _, context := range []*gin.Context{c, followup} {
				context.Request.Header.Set("Content-Type", "application/json")
				context.Request.Header.Set("session_id", sessionID)
				common.SetContextKey(context, constant.ContextKeyUserId, 8100+index)
				common.SetContextKey(context, constant.ContextKeyTokenId, 8200+index)
				t.Cleanup(func() { common.CleanupBodyStorage(context) })
			}
			require.Nil(t, service.CheckCyberSessionBlock(c), "the first request must be admitted")

			usage, apiErr := tc.handler(c, info, resp)

			require.NotNil(t, apiErr)
			assert.Nil(t, usage, "a rejected upstream response must not reach usage settlement")
			assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
			assert.Equal(t, types.ErrorCode("cyber_policy"), apiErr.GetErrorCode())
			assert.Equal(t, "policy rejected", apiErr.ToOpenAIError().Message)
			assert.NotEmpty(t, apiErr.ToOpenAIError().Type, "missing upstream error types must be filled")
			if tc.stream && tc.toResponses {
				assert.Contains(t, recorder.Body.String(), "event: response.failed", "the converted stream must terminate with a failure")
				assert.NotContains(t, recorder.Body.String(), "event: response.completed")
				assert.True(t, c.Writer.Written())
			} else {
				assert.Empty(t, recorder.Body.String(), "no converted success payload may be emitted")
				assert.False(t, c.Writer.Written())
			}
			assert.False(t, service.ShouldRetryRelayError(c, apiErr, 2), "configured 403 retries must not resubmit cyber requests")
			service.ResetStatusCode(apiErr, `{"403":502}`)
			assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
			assert.False(t, service.ShouldRetryRelayError(c, apiErr, 2), "status remapping to a retryable 502 must preserve the cyber stop")

			blocked := service.CheckCyberSessionBlock(followup)
			require.NotNil(t, blocked, "the handler must cache the signal before discarding the raw payload")
			assert.Equal(t, http.StatusForbidden, blocked.StatusCode)
			assert.Equal(t, types.ErrorCode("session_blocked_by_cyber_policy"), blocked.GetErrorCode())
		})
	}
}

func TestOaiResponsesToChatStreamHandlerConvertsSSEOrderAndUsage(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-test","created_at":1710000000}}`,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup"}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"q\":\"x\"}"}`,
		`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)

	usage, err := OaiResponsesToChatStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 2, usage.PromptTokens)
	require.Equal(t, 3, usage.CompletionTokens)
	require.Equal(t, 5, usage.TotalTokens)

	got := recorder.Body.String()
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Contains(t, got, `"role":"assistant"`)
	require.Contains(t, got, `"content":"hello"`)
	require.Contains(t, got, `"name":"lookup"`)
	require.Contains(t, got, `"arguments":"{\"q\":\"x\"}"`)
	require.Contains(t, got, `"finish_reason":"tool_calls"`)
	require.Contains(t, got, `"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5`)
	require.Contains(t, got, `data: [DONE]`)
	requireOrderedSubstrings(t, got,
		`"role":"assistant"`,
		`"content":"hello"`,
		`"name":"lookup"`,
		`"arguments":"{\"q\":\"x\"}"`,
		`"finish_reason":"tool_calls"`,
		`"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5`,
		`data: [DONE]`,
	)
}

func TestOaiResponsesToChatStreamHandlerConvertsClaudeSSETerminalsAndUsage(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-test","created_at":1710000000}}`,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	info.RelayFormat = types.RelayFormatClaude

	usage, err := OaiResponsesToChatStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 2, usage.PromptTokens)
	assert.Equal(t, 3, usage.CompletionTokens)
	assert.Equal(t, 5, usage.TotalTokens)

	got := recorder.Body.String()
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	assert.Equal(t, 1, strings.Count(got, "event: message_start\n"))
	assert.Equal(t, 1, strings.Count(got, "event: content_block_stop\n"))
	assert.Equal(t, 1, strings.Count(got, "event: message_delta\n"))
	assert.Equal(t, 1, strings.Count(got, "event: message_stop\n"))

	messageDeltaFrame := ""
	for frame := range strings.SplitSeq(got, "\n\n") {
		if strings.HasPrefix(frame, "event: message_delta\n") {
			messageDeltaFrame = frame
			break
		}
	}
	require.NotEmpty(t, messageDeltaFrame)
	assert.Contains(t, messageDeltaFrame, `"type":"message_delta"`)
	assert.Contains(t, messageDeltaFrame, `"stop_reason":"end_turn"`)
	assert.Contains(t, messageDeltaFrame, `"input_tokens":2`)
	assert.Contains(t, messageDeltaFrame, `"output_tokens":3`)
	requireOrderedSubstrings(t, got,
		"event: message_start\n",
		"event: content_block_stop\n",
		"event: message_delta\n",
		"event: message_stop\n",
	)
}

func TestOaiResponsesToChatBufferedStreamHandlerReturnsJSONFromSSE(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"buffered text"}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup"}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"q\":\"x\"}"}`,
		`data: {"type":"response.done","response":{"model":"gpt-test","status":"completed","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, false)

	usage, err := OaiResponsesToChatBufferedStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 3, usage.TotalTokens)

	got := recorder.Body.String()
	require.NotContains(t, got, `data:`)
	require.Contains(t, got, `"object":"chat.completion"`)
	require.Contains(t, got, `"content":"buffered text"`)
	require.Contains(t, got, `"name":"lookup"`)
	require.Contains(t, got, `"arguments":"{\"q\":\"x\"}"`)
	require.Contains(t, got, `"finish_reason":"tool_calls"`)
}

func TestOaiResponsesToChatBufferedStreamHandlerPreservesInterleavedClaudeContent(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	body := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`,
		`data: {"type":"response.reasoning_summary_text.delta","output_index":0,"item_id":"rs_1","delta":"**Planning file inspection**"}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}`,
		`data: {"type":"response.output_text.delta","output_index":1,"item_id":"msg_1","delta":"I’ll inspect the starter repository."}`,
		`data: {"type":"response.output_item.added","output_index":2,"item":{"type":"reasoning","id":"rs_2","summary":[]}}`,
		`data: {"type":"response.reasoning_summary_text.delta","output_index":2,"item_id":"rs_2","delta":"**Clarifying environment task requirements**"}`,
		`data: {"type":"response.output_item.added","output_index":3,"item":{"type":"message","id":"msg_2","role":"assistant","content":[]}}`,
		`data: {"type":"response.output_text.delta","output_index":3,"item_id":"msg_2","delta":"What would you like me to build?"}`,
		`data: {"type":"response.done","response":{"id":"resp_1","model":"gpt-test","status":"completed","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, false)
	info.RelayFormat = types.RelayFormatClaude

	usage, apiErr := OaiResponsesToChatBufferedStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 3, usage.TotalTokens)

	var claudeResponse dto.ClaudeResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &claudeResponse))
	require.Len(t, claudeResponse.Content, 4)
	assert.Equal(t, []string{"thinking", "text", "thinking", "text"}, []string{
		claudeResponse.Content[0].Type,
		claudeResponse.Content[1].Type,
		claudeResponse.Content[2].Type,
		claudeResponse.Content[3].Type,
	})
	require.NotNil(t, claudeResponse.Content[0].Thinking)
	require.NotNil(t, claudeResponse.Content[2].Thinking)
	assert.Equal(t, "**Planning file inspection**", *claudeResponse.Content[0].Thinking)
	assert.Equal(t, "I’ll inspect the starter repository.", claudeResponse.Content[1].GetText())
	assert.Equal(t, "**Clarifying environment task requirements**", *claudeResponse.Content[2].Thinking)
	assert.Equal(t, "What would you like me to build?", claudeResponse.Content[3].GetText())
}

func TestOaiChatToResponsesStreamHandlerConvertsSSEOrderAndUsage(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":\"x\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	usage, err := OaiChatToResponsesStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 2, usage.PromptTokens)
	require.Equal(t, 3, usage.CompletionTokens)
	require.Equal(t, 5, usage.TotalTokens)

	got := recorder.Body.String()
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Contains(t, got, `event: response.created`)
	require.Contains(t, got, `event: response.output_text.delta`)
	require.Contains(t, got, `"delta":"hello"`)
	require.Contains(t, got, `event: response.function_call_arguments.delta`)
	require.Contains(t, got, `"delta":"{\"q\":\"x\"}"`)
	require.Contains(t, got, `event: response.completed`)
	require.Contains(t, got, `"input_tokens":2`)
	require.Contains(t, got, `"output_tokens":3`)
	requireOrderedSubstrings(t, got,
		`event: response.created`,
		`event: response.output_item.added`,
		`event: response.output_text.delta`,
		`event: response.output_item.added`,
		`event: response.function_call_arguments.delta`,
		`event: response.output_text.done`,
		`event: response.function_call_arguments.done`,
		`event: response.completed`,
	)
}

func TestOaiChatToResponsesStreamHandlerRestoresCodexCustomToolCall(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_exec","type":"function","function":{"name":"exec","arguments":"{\"input\":"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls -la\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	tools, marshalErr := common.Marshal([]map[string]any{
		{"type": "custom", "name": "exec", "description": "Run a shell command", "format": map[string]any{"type": "text"}},
	})
	require.NoError(t, marshalErr)
	converted, convertErr := service.ConvertRequestByID(c, info, relayconvert.ConverterOpenAIResponsesToOpenAIChat, &dto.OpenAIResponsesRequest{
		Model: "gpt-test",
		Input: []byte(`"list files"`),
		Tools: tools,
	})
	require.NoError(t, convertErr)
	chatRequest, ok := converted.Value.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Len(t, chatRequest.Tools, 1)
	assert.Equal(t, "function", chatRequest.Tools[0].Type)
	assert.Equal(t, "exec", chatRequest.Tools[0].Function.Name)

	_, err := OaiChatToResponsesStreamHandler(c, info, resp)
	require.Nil(t, err)

	got := recorder.Body.String()
	assert.NotContains(t, got, `event: response.function_call_arguments`)
	assert.Contains(t, got, `"delta":"ls -la"`)
	assert.Contains(t, got, `"input":"ls -la"`)
	requireOrderedSubstrings(t, got,
		`event: response.output_item.added`,
		`"type":"custom_tool_call"`,
		`event: response.custom_tool_call_input.delta`,
		`event: response.custom_tool_call_input.done`,
		`event: response.output_item.done`,
		`event: response.completed`,
		`"type":"custom_tool_call"`,
	)
}

func requireOrderedSubstrings(t *testing.T, s string, parts ...string) {
	t.Helper()

	offset := 0
	for _, part := range parts {
		idx := strings.Index(s[offset:], part)
		require.NotEqualf(t, -1, idx, "missing %q after byte offset %d", part, offset)
		offset += idx + len(part)
	}
}
