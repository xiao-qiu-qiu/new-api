package openai

import (
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOaiStreamHandlerRequestPolicyOutcome(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	const content = `{"id":"chat-test","model":"deepseek-chat","choices":[{"index":0,"delta":{"content":"hello"}}]}`
	const finish = `{"id":"chat-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	const nullErrorFinish = `{"id":"chat-test","error":null,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	const errorFrame = `{"error":{"message":"upstream unavailable","type":"server_error","code":"upstream_error"}}`

	for _, tc := range []struct {
		name   string
		chunks []string
		failed bool
	}{
		{"error_object_then_done", []string{errorFrame}, true},
		{"error_type_without_error_payload", []string{`{"type":"error","message":"upstream unavailable"}`}, true},
		{"upstream_error_type_with_null_payload", []string{`{"type":"upstream_error","error":null,"message":"upstream unavailable"}`}, true},
		{"content_finish_then_done", []string{content, finish}, false},
		{"null_error_is_success", []string{content, nullErrorFinish}, false},
		{"error_after_finish_overrides_completion", []string{content, finish, errorFrame}, true},
		{"finish_after_error_cannot_restore_success", []string{errorFrame, finish}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "data:" + strings.Join(tc.chunks, "\n\ndata:") + "\n\ndata:[DONE]\n\n"
			c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
			info.RelayMode = relayconstant.RelayModeChatCompletions
			info.UpstreamModelName = "deepseek-chat"
			info.ShouldIncludeUsage = false
			policy := service.RequestPolicy(c)

			_, apiErr := OaiStreamHandler(c, info, resp)
			// HTTP 200 stream errors remain forwarded; StreamStatus carries failure.
			require.Nil(t, apiErr)
			require.NotNil(t, info.StreamStatus)
			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
			assert.Equal(t, tc.failed, info.StreamStatus.HasErrors())
			assert.Equal(t, tc.failed, info.StreamStatus.ResponseFailed())
			wantOutcome, wantAction, wantReason := "completed", "success", "request_completed"
			if tc.failed {
				wantOutcome, wantAction, wantReason = "failed", "stop", "stream_not_successful"
			}
			assert.Equal(t, wantOutcome, info.StreamStatus.ResponseOutcome())

			// This is the success flag consulted before refreshing channel affinity.
			service.MarkRequestPolicySuccess(c, info.StreamStatus)
			assert.True(t, policy.OutcomeRecorded)
			assert.Equal(t, !tc.failed, policy.Successful)
			events := policy.Events()
			require.Len(t, events, 1)
			assert.Equal(t, wantAction, events[0].Decision.Action)
			assert.Equal(t, wantReason, events[0].Decision.Reason)

			for _, chunk := range tc.chunks {
				assert.Contains(t, recorder.Body.String(), chunk, "upstream frames must remain intact")
			}
			assert.Contains(t, recorder.Body.String(), "data: [DONE]")
		})
	}
}
