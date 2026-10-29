package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The next upstream read stays blocked until the scanner closes its body.
// The channels let the downstream writer force cancellation during the final
// callback without depending on sleeps or goroutine scheduling.
type responsesLifecycleBody struct {
	data      *strings.Reader
	waiting   chan struct{}
	closed    chan struct{}
	readOnce  sync.Once
	closeOnce sync.Once
	readErr   error
	closeErr  error
}

func (b *responsesLifecycleBody) Read(p []byte) (int, error) {
	if b.data.Len() > 0 {
		return b.data.Read(p)
	}
	b.readOnce.Do(func() { close(b.waiting) })
	if b.readErr != nil {
		_ = b.Close()
		return 0, b.readErr
	}
	<-b.closed
	return 0, b.closeErr
}

func (b *responsesLifecycleBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

type responsesLifecycleWriter struct {
	*httptest.ResponseRecorder
	body              *responsesLifecycleBody
	cancel            context.CancelFunc
	cancelBeforeFlush bool
	waitForCleanup    bool
	failWrite         int
	writeErr          error
	flushErr          error
	writes            int
	needle            string
}

func (w *responsesLifecycleWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failWrite {
		return 0, w.writeErr
	}
	n, err := w.ResponseRecorder.Write(p)
	if w.cancelBeforeFlush && strings.Contains(w.Body.String(), w.needle) {
		<-w.body.waiting
		w.cancel()
		<-w.body.closed
	}
	return n, err
}

func (w *responsesLifecycleWriter) Flush() {
	w.ResponseRecorder.Flush()
	if !strings.Contains(w.Body.String(), w.needle) || w.body == nil {
		return
	}
	<-w.body.waiting
	if w.cancel != nil {
		w.cancel()
	}
	if w.waitForCleanup {
		<-w.body.closed
	}
}

func (w *responsesLifecycleWriter) FlushError() error {
	w.Flush()
	return w.flushErr
}

func TestOaiResponsesStreamHandlerCompletionLifecycle(t *testing.T) {
	oldMode, oldTimeout := gin.Mode(), constant.StreamingTimeout
	gin.SetMode(gin.TestMode)
	constant.StreamingTimeout = 1
	t.Cleanup(func() {
		gin.SetMode(oldMode)
		constant.StreamingTimeout = oldTimeout
	})

	for _, tc := range []struct {
		name              string
		event             string
		responseStatus    string
		cancel            bool
		cancelBeforeFlush bool
		closeEOF          bool
		eof               bool
		scannerError      bool
		priorError        bool
		failWrite         int
		failFlush         bool
		wantEnd           relaycommon.StreamEndReason
		wantError         bool
	}{
		{name: "completed without upstream EOF", event: "response.completed", responseStatus: "completed", wantEnd: relaycommon.StreamEndReasonDone},
		{name: "done alias without upstream EOF", event: "response.done", responseStatus: "completed", wantEnd: relaycommon.StreamEndReasonDone},
		{name: "disconnect during terminal flush", event: "response.completed", responseStatus: "completed", cancel: true, wantEnd: relaycommon.StreamEndReasonDone},
		{name: "completion then cancellation and cleanup EOF", event: "response.completed", responseStatus: "completed", cancel: true, closeEOF: true, wantEnd: relaycommon.StreamEndReasonDone},
		{name: "midstream disconnect", event: "response.output_text.delta", cancel: true, wantEnd: relaycommon.StreamEndReasonClientGone, wantError: true},
		{name: "midstream disconnect followed by cleanup EOF", event: "response.output_text.delta", cancel: true, closeEOF: true, wantEnd: relaycommon.StreamEndReasonClientGone, wantError: true},
		{name: "cancellation before flush", event: "response.completed", responseStatus: "completed", cancel: true, cancelBeforeFlush: true, wantEnd: relaycommon.StreamEndReasonClientGone, wantError: true},
		{name: "event write failure", event: "response.completed", responseStatus: "completed", failWrite: 1, wantEnd: relaycommon.StreamEndReasonClientGone, wantError: true},
		{name: "data write failure", event: "response.completed", responseStatus: "completed", failWrite: 2, wantEnd: relaycommon.StreamEndReasonClientGone, wantError: true},
		{name: "delimiter write failure", event: "response.completed", responseStatus: "completed", failWrite: 3, wantEnd: relaycommon.StreamEndReasonClientGone, wantError: true},
		{name: "flush failure", event: "response.completed", responseStatus: "completed", failFlush: true, wantEnd: relaycommon.StreamEndReasonClientGone, wantError: true},
		{name: "protocol error before completion", event: "response.completed", responseStatus: "completed", priorError: true, cancel: true, wantEnd: relaycommon.StreamEndReasonClientGone, wantError: true},
		// FIFO delivery preserves a terminal already queued before a read error.
		{name: "upstream read failure after queued completion", event: "response.completed", responseStatus: "completed", scannerError: true, wantEnd: relaycommon.StreamEndReasonDone},
		{name: "done with failed response", event: "response.done", responseStatus: "failed", eof: true, wantEnd: relaycommon.StreamEndReasonDone, wantError: true},
		{name: "done with incomplete response", event: "response.done", responseStatus: "incomplete", eof: true, wantEnd: relaycommon.StreamEndReasonDone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := fmt.Sprintf(`{"type":%q,"response":{"status":%q,"usage":{"input_tokens":7,"output_tokens":5,"total_tokens":12},"output":[{"id":"img_1","type":"image_generation_call","status":"completed","result":"image-data"}]}}`, tc.event, tc.responseStatus)
			if tc.responseStatus == "" {
				payload = `{"type":"response.output_text.delta","delta":"hello"}`
			}
			sse := "data: " + payload + "\n\n"
			if tc.priorError {
				sse = "data: invalid JSON\n\n" + sse
			}
			body := &responsesLifecycleBody{data: strings.NewReader(sse), waiting: make(chan struct{}), closed: make(chan struct{}), closeErr: io.ErrClosedPipe}
			if tc.closeEOF {
				body.closeErr = io.EOF
			}
			if tc.scannerError {
				body.readErr = io.ErrUnexpectedEOF
			}
			t.Cleanup(func() { require.NoError(t, body.Close()) })
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			writer := &responsesLifecycleWriter{
				ResponseRecorder: httptest.NewRecorder(), body: body, needle: payload + "\n\n",
				cancelBeforeFlush: tc.cancelBeforeFlush, waitForCleanup: tc.cancel || tc.scannerError,
				failWrite: tc.failWrite, writeErr: errors.New("downstream write failed"),
			}
			if tc.cancel {
				writer.cancel = cancel
			}
			if tc.failFlush {
				writer.flushErr = writer.writeErr
			}
			var upstream io.ReadCloser = body
			if tc.eof {
				upstream = io.NopCloser(strings.NewReader(sse))
				writer.body = nil
			}
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			info := &relaycommon.RelayInfo{
				OriginModelName: "gpt-4o", IsStream: true, DisablePing: true, StartTime: time.Now(),
				RelayFormat: types.RelayFormatOpenAIResponses,
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-4o"},
			}
			info.SetEstimatePromptTokens(7)
			resp := &http.Response{StatusCode: http.StatusOK, Body: upstream, Header: http.Header{"Content-Type": {"text/event-stream"}}}
			usage, apiErr := OaiResponsesStreamHandler(c, info, resp)
			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			require.NotNil(t, info.StreamStatus)
			assert.Equal(t, tc.wantEnd, info.StreamStatus.EndReason)
			assert.Equal(t, tc.responseStatus, info.StreamStatus.ResponseOutcome())
			log := service.GenerateTextOtherInfo(c, info, 1, 1, 1, 0, 1, 0, 1).Snapshot()["stream_status"].(map[string]any)
			assert.Equal(t, tc.wantError, log["status"] == "error")
			if tc.responseStatus != "" {
				assert.Equal(t, 7, usage.PromptTokens)
				assert.Equal(t, 5, usage.CompletionTokens)
				assert.Equal(t, 12, usage.TotalTokens)
				require.NotNil(t, info.ResponsesUsageInfo)
				require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, dto.BuildInToolImageGeneration)
				assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
			}
			if tc.failWrite == 0 {
				assert.Contains(t, writer.Body.String(), payload+"\n\n")
			} else {
				assert.ErrorIs(t, info.StreamStatus.EndError, writer.writeErr)
			}
			if tc.failFlush {
				assert.ErrorIs(t, info.StreamStatus.EndError, writer.flushErr)
			}
			if !tc.eof {
				select {
				case <-body.closed:
				default:
					t.Fatal("upstream body was not closed")
				}
			}
		})
	}
}

func TestOaiResponsesHandlerCountsOutputCallsNotDeclarations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	operation_setting.SetToolPriceForTest("priced_fn", 5.0)
	t.Cleanup(func() {
		operation_setting.DeleteToolPriceForTest("priced_fn")
	})

	body, err := common.Marshal(dto.OpenAIResponsesResponse{
		Tools: []map[string]any{
			{"type": "web_search_preview"},
			{"type": "file_search"},
		},
		Output: []dto.ResponsesOutput{
			{Type: dto.BuildInCallWebSearchCall},
			{Type: dto.BuildInCallWebSearchCall},
			{Type: dto.BuildInCallFunctionCall, Name: "priced_fn"},
			{Type: dto.BuildInCallFunctionCall, Name: "unpriced_fn"},
		},
		Usage: &dto.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1",
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				dto.BuildInToolWebSearchPreview: {ToolName: dto.BuildInToolWebSearchPreview, CallCount: 0},
				dto.BuildInToolFileSearch:       {ToolName: dto.BuildInToolFileSearch, CallCount: 0},
			},
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	usage, apiErr := OaiResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 2, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearchPreview].CallCount)
	assert.Equal(t, 0, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolFileSearch].CallCount)
	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, "priced_fn")
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools["priced_fn"].CallCount)
	assert.NotContains(t, info.ResponsesUsageInfo.BuiltInTools, "unpriced_fn")
}

func TestOaiResponsesHandlerDeclaredToolsWithoutOutputCountZero(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body, err := common.Marshal(dto.OpenAIResponsesResponse{
		Tools: []map[string]any{
			{"type": "web_search_preview"},
			{"type": "file_search"},
		},
		Output: []dto.ResponsesOutput{
			{Type: "message", Role: "assistant"},
		},
		Usage: &dto.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1",
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				dto.BuildInToolWebSearchPreview: {ToolName: dto.BuildInToolWebSearchPreview, CallCount: 0},
				dto.BuildInToolFileSearch:       {ToolName: dto.BuildInToolFileSearch, CallCount: 0},
			},
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	_, apiErr := OaiResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	assert.Equal(t, 0, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearchPreview].CallCount)
	assert.Equal(t, 0, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolFileSearch].CallCount)
}

func TestOaiResponsesHandlerCountsCompletedImageGenerationOutputs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body, err := common.Marshal(dto.OpenAIResponsesResponse{
		Status: []byte(`"completed"`),
		Output: []dto.ResponsesOutput{
			{
				Type:   dto.ResponsesOutputTypeImageGenerationCall,
				ID:     "img_1",
				Status: "completed",
				Result: "base64-a",
			},
			{
				Type:   dto.ResponsesOutputTypeImageGenerationCall,
				ID:     "img_2",
				Status: "completed",
				Result: "base64-b",
			},
			{
				Type:   dto.ResponsesOutputTypeImageGenerationCall,
				ID:     "img_empty",
				Status: "completed",
				Result: "",
			},
		},
		Usage: &dto.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{OriginModelName: "gpt-5.1"}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	_, apiErr := OaiResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, dto.BuildInToolImageGeneration)
	assert.Equal(t, 2, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
	assert.False(t, c.GetBool("image_generation_call"))
}

func TestOaiResponsesHandlerCountsImageGenerationOnIncompleteStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body, err := common.Marshal(dto.OpenAIResponsesResponse{
		Status: []byte(`"incomplete"`),
		Output: []dto.ResponsesOutput{
			{
				Type:   dto.ResponsesOutputTypeImageGenerationCall,
				ID:     "img_1",
				Status: "completed",
				Result: "base64-a",
			},
		},
		Usage: &dto.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1",
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				dto.BuildInToolImageGeneration: {ToolName: dto.BuildInToolImageGeneration, CallCount: 0},
			},
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	_, apiErr := OaiResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
}

func runResponsesImageBillingStream(t *testing.T, events ...string) *relaycommon.RelayInfo {
	t.Helper()
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		constant.StreamingTimeout = oldTimeout
	})

	var body strings.Builder
	for _, event := range events {
		body.WriteString("data: ")
		body.WriteString(event)
		body.WriteString("\n\n")
	}
	body.WriteString("data: [DONE]\n\n")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "responses-image-billing-test")
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1",
		DisablePing:     true,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-5.1",
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body.String())),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	_, apiErr := OaiResponsesStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, info.ResponsesUsageInfo)
	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, dto.BuildInToolImageGeneration)
	return info
}

func TestOaiResponsesStreamHandlerDeduplicatesCompletedImageOutput(t *testing.T) {
	item := `{"type":"image_generation_call","id":"img_1","call_id":"call_1","status":"completed","result":"base64-a"}`
	info := runResponsesImageBillingStream(
		t,
		`{"type":"response.output_item.done","output_index":0,"item":`+item+`}`,
		`{"type":"response.completed","response":{"status":"completed","output":[`+item+`],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
	)

	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
}

func TestOaiResponsesStreamHandlerCountsImageOutputOnIncomplete(t *testing.T) {
	info := runResponsesImageBillingStream(
		t,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"image_generation_call","id":"img_1","status":"completed","result":"base64-a"}}`,
		`{"type":"response.incomplete","response":{"status":"incomplete"}}`,
	)

	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
}

func TestOaiResponsesStreamHandlerDoesNotCountPartialImageEvent(t *testing.T) {
	info := runResponsesImageBillingStream(
		t,
		`{"type":"response.image_generation_call.partial_image","output_index":0,"partial_image_b64":"partial-bytes"}`,
		`{"type":"response.completed","response":{"status":"completed","output":[]}}`,
	)

	assert.Equal(t, 0, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
}

// The 12h cache-write split reaches billing from JSON, stream and compaction
// responses. The usage is the OpenAI prompt caching guide example.
func TestOaiResponsesHandlersKeepCacheWriteTTLSplit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		constant.StreamingTimeout = oldTimeout
	})

	const usage = `"usage":{"input_tokens":12000,"output_tokens":0,"total_tokens":12000,"input_tokens_details":{"cached_tokens":4000,"cache_write_tokens":7000,"cache_write_tokens_details":{"ttl_30m_tokens":3000,"ttl_12h_tokens":4000}}}`
	const response = `{"id":"resp_1","object":"response","model":"gpt-6-astra","status":"completed","output":[],` + usage + `}`
	for _, tt := range []struct {
		name, contentType, body string
	}{
		{"json", "application/json", response},
		{"stream", "text/event-stream", `data: {"type":"response.completed","response":` + response + "}\n\ndata: [DONE]\n\n"},
		{"compaction", "application/json", `{"id":"cmp_1","object":"response.compaction","output":[],` + usage + `}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			info := &relaycommon.RelayInfo{
				RelayFormat:     types.RelayFormatOpenAIResponses,
				OriginModelName: "gpt-6-astra",
				IsStream:        tt.name == "stream",
				DisablePing:     true,
				ChannelMeta:     &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI, UpstreamModelName: "gpt-6-astra"},
			}
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(tt.body)),
				Header:     http.Header{"Content-Type": []string{tt.contentType}},
			}
			var usage *dto.Usage
			var apiErr *types.NewAPIError
			switch tt.name {
			case "json":
				usage, apiErr = OaiResponsesHandler(c, info, resp)
			case "stream":
				usage, apiErr = OaiResponsesStreamHandler(c, info, resp)
			default:
				usage, apiErr = OaiResponsesCompactionHandler(c, resp, info)
			}
			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			assert.Equal(t, 12000, usage.PromptTokens)
			assert.Equal(t, 4000, usage.PromptTokensDetails.CachedTokens)
			assert.Equal(t, 7000, usage.PromptTokensDetails.CacheCreationTokensTotal())
			assert.Equal(t, 4000, usage.PromptTokensDetails.CacheWrite12hTokens())
		})
	}
}

func TestOaiResponsesHandlerRewritesSGLangCreatedAtToInt(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeSGLang},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_1","object":"response","created_at":1786588600.0,"status":"completed","output":[]}`)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	_, apiErr := OaiResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	assert.Contains(t, w.Body.String(), `"created_at":1786588600`)
	assert.NotContains(t, w.Body.String(), `1786588600.0`)
}

func TestOaiResponsesStreamHandlerRewritesSGLangCreatedAtToInt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		constant.StreamingTimeout = oldTimeout
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "sglang-created-at-test")

	info := &relaycommon.RelayInfo{
		DisablePing: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeSGLang,
			UpstreamModelName: "served-model",
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"created_at\":1.7865886E9,\"status\":\"completed\",\"output\":[]}}\n\n" +
				"data: [DONE]\n\n",
		)),
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	_, apiErr := OaiResponsesStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	assert.Contains(t, w.Body.String(), `"created_at":1786588600`)
	assert.NotContains(t, w.Body.String(), `1.7865886E9`)
	assert.NotContains(t, w.Body.String(), `1786588600.0`)
}

func TestOaiResponsesStreamHandlerKeepsNonSGLangCreatedAt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		constant.StreamingTimeout = oldTimeout
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "openai-created-at-test")

	info := &relaycommon.RelayInfo{
		DisablePing: true,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI, UpstreamModelName: "gpt-test"},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"created_at\":1786588600.0,\"status\":\"completed\",\"output\":[]}}\n\n" +
				"data: [DONE]\n\n",
		)),
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	_, apiErr := OaiResponsesStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	assert.Contains(t, w.Body.String(), `1786588600.0`)
}

// A converted hosted web search reaches Azure and Xinference Chat Completions
// as web_search_options, like OpenAI; the upstream decides whether it applies.
// Azure Responses bills web search by tool_usage.web_search.num_requests (Bing
// transactions) under bing_web_search, and the explicit zeros silence the
// web_search_call items, which leaves one surcharge row.
func TestAzureHostedWebSearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		constant.StreamingTimeout = oldTimeout
	})

	t.Run("Claude to Chat", func(t *testing.T) {
		const body = `{"model":"gpt-5.1","max_tokens":64,"messages":[{"role":"user","content":"Weather?"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`
		for _, channelType := range []int{constant.ChannelTypeAzure, constant.ChannelTypeXinference, constant.ChannelTypeOpenAI} {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			info := &relaycommon.RelayInfo{
				RelayFormat: types.RelayFormatClaude,
				ChannelMeta: &relaycommon.ChannelMeta{ChannelType: channelType, UpstreamModelName: "gpt-5.1"},
			}
			adaptor := &Adaptor{}
			adaptor.Init(info)
			var request dto.ClaudeRequest
			require.NoError(t, common.UnmarshalJsonStr(body, &request))
			converted, err := adaptor.ConvertClaudeRequest(c, info, &request)
			require.NoError(t, err)
			wire, err := common.Marshal(converted)
			require.NoError(t, err)
			assert.True(t, strings.Contains(string(wire), `"web_search_options"`), channelType)
			var codes []string
			for _, diagnostic := range info.ConversionDiagnostics() {
				codes = append(codes, diagnostic.Code)
			}
			assert.False(t, slices.Contains(codes, "hosted_web_search_omitted"), channelType)
		}
	})

	const searchCall = `{"type":"web_search_call","status":"completed","action":{"type":"search","query":"go"}}`
	const usage = `"usage":{"input_tokens":10,"output_tokens":3,"total_tokens":13},"tool_usage":{"web_search":{"num_requests":5}}`
	output := strings.Repeat(searchCall+",", 3) + `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Go 1.25"}]}`
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{
			name:        "json",
			contentType: "application/json",
			body:        `{"id":"resp_1","object":"response","model":"gpt-5.1","status":"completed","output":[` + output + `],` + usage + `}`,
		},
		{
			name:        "stream",
			contentType: "text/event-stream",
			body: strings.Repeat(`data: {"type":"response.output_item.done","output_index":0,"item":`+searchCall+"}\n\n", 3) +
				`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5.1","status":"completed","output":[` + output + `],` + usage + "}}\n\ndata: [DONE]\n\n",
		},
	}
	for _, tt := range tests {
		t.Run("Responses "+tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			info := &relaycommon.RelayInfo{
				RelayMode:       relayconstant.RelayModeResponses,
				RelayFormat:     types.RelayFormatOpenAIResponses,
				OriginModelName: "gpt-5.1",
				IsStream:        tt.name == "stream",
				DisablePing:     true,
				ChannelMeta:     &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeAzure, UpstreamModelName: "gpt-5.1"},
			}
			adaptor := &Adaptor{}
			adaptor.Init(info)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(tt.body)),
				Header:     http.Header{"Content-Type": []string{tt.contentType}},
			}
			var apiErr *types.NewAPIError
			if info.IsStream {
				_, apiErr = OaiResponsesStreamHandler(c, info, resp)
			} else {
				_, apiErr = OaiResponsesHandler(c, info, resp)
			}
			require.Nil(t, apiErr)
			billed := map[string]int{}
			for name, tool := range info.ResponsesUsageInfo.BuiltInTools {
				if tool.CallCount > 0 {
					billed[name] = tool.CallCount
				}
			}
			assert.Equal(t, map[string]int{"bing_web_search": 5}, billed)
		})
	}
}

// These cases use the production HTTP transport and the native Responses
// accumulator, so detaching only the scanner context cannot make them pass.
func TestResponsesStreamDisconnectTransport(t *testing.T) {
	service.InitHttpClient()
	for _, tc := range []struct {
		name, mode           string
		writeFail, flushFail bool
	}{
		{name: "cancel", mode: "cancel"},
		{name: "drain client cancellation", mode: "drain"},
		{name: "drain write failure", mode: "drain", writeFail: true},
		{name: "drain flush failure", mode: "drain", flushFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, cancel := context.WithCancel(context.Background())
			defer cancel()
			release := make(chan struct{})
			var releaseOnce sync.Once
			disconnect := func() {
				if !tc.writeFail && !tc.flushFail {
					cancel()
				}
				releaseOnce.Do(func() { close(release) })
			}
			upstreamDone := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(upstreamDone)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":100,\"output_tokens\":7,\"total_tokens\":107,\"input_tokens_details\":{\"cached_tokens\":80}}}}\n\n")
				w.(http.Flusher).Flush()
				// No EOF: the handler must close upstream on the terminal itself.
				<-r.Context().Done()
			}))
			defer server.Close()
			writer := &disconnectTestWriter{ResponseRecorder: httptest.NewRecorder(), disconnect: disconnect, writeFail: tc.writeFail, flushFail: tc.flushFail}
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(client)
			info := &relaycommon.RelayInfo{IsStream: true, DisablePing: true, StreamDisconnectMode: tc.mode, StreamDrainTimeoutSeconds: 3,
				OriginModelName: "gpt-4o", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-4o"}}
			info.SetEstimatePromptTokens(9)
			req, err := http.NewRequestWithContext(client, http.MethodPost, server.URL, strings.NewReader("{}"))
			require.NoError(t, err)
			resp, err := channel.DoRequest(c, req, info)
			require.NoError(t, err)
			usage, apiErr := OaiResponsesStreamHandler(c, info, resp)
			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			select {
			case <-upstreamDone:
			case <-time.After(2 * time.Second):
				t.Fatal("HTTP transport still owns the upstream request")
			}
			outcome := info.StreamStatus.OutcomeSnapshot()
			assert.Equal(t, relaycommon.StreamEndReasonClientGone, outcome.EndReason)
			assert.True(t, outcome.ClientGone)
			if tc.mode == "drain" {
				assert.Equal(t, 100, usage.PromptTokens)
				assert.Equal(t, 7, usage.CompletionTokens)
				assert.Equal(t, 80, usage.PromptTokensDetails.CachedTokens)
				assert.Equal(t, relaycommon.StreamEndReasonDone, outcome.DrainEndReason)
				assert.Equal(t, relaycommon.ResponseOutcomeCompleted, outcome.Response)
				if tc.writeFail || tc.flushFail {
					assert.NoError(t, client.Err(), "write failure alone must trigger drain")
				}
			} else {
				assert.NotEqual(t, 100, usage.PromptTokens)
				assert.Zero(t, usage.PromptTokensDetails.CachedTokens)
				assert.Equal(t, 1, info.ReceivedResponseCount, "cancel must stop before the terminal")
			}
			assert.NotContains(t, writer.Body.String(), "response.completed", "no terminal write after downstream loss")
			assert.Equal(t, writer.writesAtDisconnect, writer.writes, "drain must suppress every later write")
			other := service.GenerateTextOtherInfo(c, info, 1, 1, 1, 0, 1, 0, 1)
			streamStatus, ok := other.Snapshot()["stream_status"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "client_gone", streamStatus["end_reason"])
			assert.Equal(t, true, streamStatus["client_gone"])
			assert.Equal(t, tc.mode, streamStatus["disconnect_mode"])
		})
	}
}

type disconnectTestWriter struct {
	*httptest.ResponseRecorder
	disconnect           func()
	writeFail, flushFail bool
	writes               int
	writesAtDisconnect   int
}

func (w *disconnectTestWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.writeFail {
		w.writesAtDisconnect = w.writes
		w.disconnect()
		return 0, errors.New("downstream write failed")
	}
	n, err := w.ResponseRecorder.Write(data)
	if !w.flushFail {
		w.writesAtDisconnect = w.writes
		w.disconnect()
	}
	return n, err
}

func (w *disconnectTestWriter) FlushError() error {
	if w.flushFail {
		w.writesAtDisconnect = w.writes
		w.disconnect()
		return errors.New("downstream flush failed")
	}
	w.ResponseRecorder.Flush()
	return nil
}

func TestResponsesStreamDrainLimits(t *testing.T) {
	service.InitHttpClient()
	previousIdle := constant.StreamingTimeout
	constant.StreamingTimeout = 1
	t.Cleanup(func() { constant.StreamingTimeout = previousIdle })
	for _, tc := range []struct {
		name    string
		seconds int
		active  bool
		want    relaycommon.StreamEndReason
	}{
		{"total limit despite active upstream", 1, true, relaycommon.StreamEndReasonDrainTimeout},
		{"existing idle limit", 300, false, relaycommon.StreamEndReasonTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, cancel := context.WithCancel(context.Background())
			defer cancel()
			serverDone := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(serverDone)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
				w.(http.Flusher).Flush()
				if !tc.active {
					<-r.Context().Done()
					return
				}
				ticker := time.NewTicker(100 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-r.Context().Done():
						return
					case <-ticker.C:
						_, _ = fmt.Fprint(w, ": heartbeat\n\n")
						w.(http.Flusher).Flush()
					}
				}
			}))
			defer server.Close()
			writer := &disconnectTestWriter{ResponseRecorder: httptest.NewRecorder(), disconnect: cancel}
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(client)
			info := &relaycommon.RelayInfo{IsStream: true, DisablePing: true, StreamDisconnectMode: "drain", StreamDrainTimeoutSeconds: tc.seconds,
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-4o"}}
			req, err := http.NewRequestWithContext(client, http.MethodPost, server.URL, strings.NewReader("{}"))
			require.NoError(t, err)
			resp, err := channel.DoRequest(c, req, info)
			require.NoError(t, err)
			_, apiErr := OaiResponsesStreamHandler(c, info, resp)
			require.Nil(t, apiErr)
			assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)
			assert.Equal(t, tc.want, info.StreamStatus.DrainEndReason)
			select {
			case <-serverDone:
			case <-time.After(time.Second):
				t.Fatal("drain limit did not cancel transport")
			}
		})
	}
}

func TestStreamDrainBufferedTailAndDoneWriteFailure(t *testing.T) {
	for _, ending := range []string{"", "data: [DONE]\n"} {
		t.Run(fmt.Sprintf("ending %q", ending), func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			client, cancel := context.WithCancel(context.Background())
			cancel()
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(client)
			info := &relaycommon.RelayInfo{StreamDisconnectMode: "drain", StreamDrainTimeoutSeconds: 3, DisablePing: true}
			const prefix = "data: chunk\n"
			resp := &http.Response{Body: io.NopCloser(strings.NewReader(strings.Repeat(prefix, 25) + "data: final_usage\n" + ending))}
			var got []string
			helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) { got = append(got, data) })
			require.Len(t, got, 26)
			assert.Equal(t, "final_usage", got[25])
			assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)
			want := relaycommon.StreamEndReasonEOF
			if ending != "" {
				want = relaycommon.StreamEndReasonDone
			}
			assert.Equal(t, want, info.StreamStatus.DrainEndReason)
		})
	}
	t.Run("Done then failed write in same callback", func(t *testing.T) {
		writer := &disconnectTestWriter{ResponseRecorder: httptest.NewRecorder(), disconnect: func() {}, writeFail: true}
		c, _ := gin.CreateTestContext(writer)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		info := &relaycommon.RelayInfo{StreamDisconnectMode: "drain", StreamDrainTimeoutSeconds: 3, DisablePing: true}
		resp := &http.Response{Body: io.NopCloser(strings.NewReader("data: terminal\n"))}
		helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
			sr.Done()
			require.NoError(t, helper.StringData(c, data))
		})
		assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)
		assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.DrainEndReason)
	})
}

func TestResponsesStreamErrorBeforeTerminalKeepsUsage(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-4o"}}
	body := "data: {\"type\":\"error\",\"code\":\"upstream_error\"}\n\n" +
		"data: {\"type\":\"response.error\"}\n\n" +
		"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"usage\":{\"input_tokens\":100,\"output_tokens\":7,\"total_tokens\":107,\"input_tokens_details\":{\"cached_tokens\":80}}}}\n\n"
	usage, apiErr := OaiResponsesStreamHandler(c, info, &http.Response{Body: io.NopCloser(strings.NewReader(body))})
	require.Nil(t, apiErr)
	assert.Equal(t, 100, usage.PromptTokens)
	assert.Equal(t, 7, usage.CompletionTokens)
	assert.Equal(t, 80, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, relaycommon.ResponseOutcomeFailed, info.StreamStatus.OutcomeSnapshot().Response)
}

func TestStreamDisconnectOptionValidationAndSnapshot(t *testing.T) {
	for key, values := range map[string][]string{
		"quota_setting.stream_disconnect_mode":       {"cancel", "drain"},
		"quota_setting.stream_drain_timeout_seconds": {"1", "300", "3600"},
	} {
		for _, value := range values {
			require.NoError(t, operation_setting.ValidateQuotaOption(key, value))
		}
	}
	for key, values := range map[string][]string{
		"quota_setting.stream_disconnect_mode":       {"", "foo", "DRAIN"},
		"quota_setting.stream_drain_timeout_seconds": {"0", "-1", "3601", "1.5", "NaN", "Inf", ""},
	} {
		for _, value := range values {
			require.Error(t, operation_setting.ValidateQuotaOption(key, value))
		}
	}
	setting := operation_setting.GetQuotaSetting()
	previous := *setting
	t.Cleanup(func() { *setting = previous })
	setting.StreamDisconnectMode, setting.StreamDrainTimeoutSeconds = "drain", 25
	info := &relaycommon.RelayInfo{}
	info.SnapshotStreamDisconnectSettings()
	setting.StreamDisconnectMode, setting.StreamDrainTimeoutSeconds = "cancel", 30
	info.SnapshotStreamDisconnectSettings()
	assert.Equal(t, "drain", info.StreamDisconnectMode)
	assert.Equal(t, 25, info.StreamDrainTimeoutSeconds)
}

func TestStreamDisconnectDoesNotStartAnotherHTTPAttempt(t *testing.T) {
	service.InitHttpClient()
	for _, mode := range []string{"cancel", "drain"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			client, cancel := context.WithCancel(context.Background())
			defer cancel()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(client)
			info := &relaycommon.RelayInfo{IsStream: true, DisablePing: true, StreamDisconnectMode: mode, StreamDrainTimeoutSeconds: 3, ChannelMeta: &relaycommon.ChannelMeta{}}
			first, err := http.NewRequestWithContext(client, http.MethodPost, server.URL, strings.NewReader("{}"))
			require.NoError(t, err)
			resp, err := channel.DoRequest(c, first, info)
			require.NoError(t, err)
			require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
			require.NoError(t, resp.Body.Close())
			cancel()
			retry, err := http.NewRequestWithContext(client, http.MethodPost, server.URL, strings.NewReader("{}"))
			require.NoError(t, err)
			resp, err = channel.DoRequest(c, retry, info)
			require.Error(t, err)
			assert.Nil(t, resp)
			var apiErr *types.NewAPIError
			require.ErrorAs(t, err, &apiErr)
			assert.True(t, types.IsSkipRetryError(apiErr))
			assert.EqualValues(t, 1, requests.Load(), "client cancellation must not create a fresh upstream request")
		})
	}
	t.Run("header failure after disconnect is not retryable", func(t *testing.T) {
		client, cancel := context.WithCancel(context.Background())
		defer cancel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cancel()
			// Simulate upstream closing before headers, after client cancellation.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}))
		defer server.Close()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(client)
		info := &relaycommon.RelayInfo{IsStream: true, DisablePing: true, StreamDisconnectMode: "drain", StreamDrainTimeoutSeconds: 3, ChannelMeta: &relaycommon.ChannelMeta{}}
		req, err := http.NewRequestWithContext(client, http.MethodPost, server.URL, strings.NewReader("{}"))
		require.NoError(t, err)
		resp, err := channel.DoRequest(c, req, info)
		require.Error(t, err)
		assert.Nil(t, resp)
		var apiErr *types.NewAPIError
		require.ErrorAs(t, err, &apiErr)
		assert.True(t, types.IsSkipRetryError(apiErr))
	})
}

func TestStreamDrainIdleDoesNotDropQueuedUsage(t *testing.T) {
	previousIdle := constant.StreamingTimeout
	constant.StreamingTimeout = 1
	t.Cleanup(func() { constant.StreamingTimeout = previousIdle })
	client, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(client)
	info := &relaycommon.RelayInfo{DisablePing: true, StreamDisconnectMode: "drain", StreamDrainTimeoutSeconds: 5}
	readEOF := make(chan struct{})
	body := &streamEOFReader{Reader: strings.NewReader("data: first\ndata: final_usage\n"), eof: readEOF}
	resp := &http.Response{Body: io.NopCloser(body)}
	var received []string
	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		received = append(received, data)
		if data == "first" {
			// EOF proves final_usage is already queued. Callback work crosses
			// the real idle deadline while the handler owns this first event.
			<-readEOF
			timer := time.NewTimer(1100 * time.Millisecond)
			defer timer.Stop()
			<-timer.C
		}
	})
	assert.Equal(t, []string{"first", "final_usage"}, received)
	assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)
	assert.Equal(t, relaycommon.StreamEndReasonEOF, info.StreamStatus.DrainEndReason)
}

type streamEOFReader struct {
	io.Reader
	eof  chan struct{}
	once sync.Once
}

func (r *streamEOFReader) Read(data []byte) (int, error) {
	n, err := r.Reader.Read(data)
	if err == io.EOF {
		r.once.Do(func() { close(r.eof) })
	}
	return n, err
}

func TestStreamWriterObservesDisconnectAfterScannerCleanup(t *testing.T) {
	for _, mode := range []string{"cancel", "drain"} {
		t.Run(mode, func(t *testing.T) {
			client, cancel := context.WithCancel(context.Background())
			defer cancel()
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(client)
			info := &relaycommon.RelayInfo{DisablePing: true, StreamDisconnectMode: mode, StreamDrainTimeoutSeconds: 3}
			resp := &http.Response{Body: io.NopCloser(strings.NewReader("data: [DONE]\n"))}
			helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {})
			require.ErrorIs(t, info.StreamLifecycle.Context.Err(), context.Canceled, "scanner has already cleaned up")
			cancel()
			require.NoError(t, helper.StringData(c, "adapter_final_frame"))
			assert.Empty(t, recorder.Body.String(), "a late cancellation must suppress adaptor final writes")
			require.ErrorIs(t, info.StreamLifecycle.ClientError(), context.Canceled)
			select {
			case <-info.StreamLifecycle.Gone:
			default:
				t.Fatal("late downstream loss was not recorded")
			}
		})
	}
}

func TestResponsesStreamLogDisconnectAfterCompletion(t *testing.T) {
	for _, mode := range []string{"cancel", "drain"} {
		for _, tc := range []struct {
			name                 string
			writeFail, flushFail bool
		}{
			{name: "client closes during settlement"},
			{name: "adaptor final write fails", writeFail: true},
			{name: "adaptor final flush fails", flushFail: true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				client, cancel := context.WithCancel(context.Background())
				defer cancel()
				writer := &disconnectTestWriter{ResponseRecorder: httptest.NewRecorder(), disconnect: func() {}}
				c, _ := gin.CreateTestContext(writer)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(client)
				info := &relaycommon.RelayInfo{
					IsStream: true, DisablePing: true, StreamDisconnectMode: mode, StreamDrainTimeoutSeconds: 3,
					OriginModelName: "gpt-4o", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-4o"},
				}
				body := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":100,\"output_tokens\":7,\"total_tokens\":107}}}\n\n"
				usage, apiErr := OaiResponsesStreamHandler(c, info, &http.Response{Body: io.NopCloser(strings.NewReader(body))})
				require.Nil(t, apiErr)
				require.NotNil(t, usage)
				require.Contains(t, writer.Body.String(), "response.completed")
				require.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
				require.NoError(t, info.StreamLifecycle.ClientError())

				if tc.writeFail || tc.flushFail {
					writer.writeFail, writer.flushFail = tc.writeFail, tc.flushFail
					_ = helper.StringData(c, "adaptor_final_frame")
					require.Error(t, info.StreamLifecycle.ClientError())
					require.NoError(t, client.Err(), "the failure comes from the final write or flush")
				} else {
					// The final event was delivered. Settlement runs before log
					// generation, and the client may close during that interval.
					cancel()
				}

				other := service.GenerateTextOtherInfo(c, info, 1, 1, 1, 0, 1, 0, 1)
				streamStatus, ok := other.Snapshot()["stream_status"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "completed", streamStatus["response_status"])
				if tc.writeFail || tc.flushFail {
					assert.Equal(t, "error", streamStatus["status"])
					assert.Equal(t, "client_gone", streamStatus["end_reason"])
					assert.Equal(t, true, streamStatus["client_gone"])
					assert.Equal(t, mode, streamStatus["disconnect_mode"])
					assert.Equal(t, "done", streamStatus["drain_end_reason"])
				} else {
					assert.Equal(t, map[string]any{"status": "ok", "end_reason": "done", "response_status": "completed"}, streamStatus)
				}
			})
		}
	}
}
