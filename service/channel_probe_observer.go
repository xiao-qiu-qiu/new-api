package service

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// ChannelProbeObserver inspects only bounded SSE lines, without saving content.
// A response header, heartbeat, role delta, or usage event is not a first token.
// It is independent of billing's FirstResponseTime (which can be metadata).
type ChannelProbeObserver struct {
	gin.ResponseWriter
	mu      sync.Mutex
	started time.Time
	first   *int64
	pending []byte
	discard bool
	failed  bool
}

func NewChannelProbeObserver(writer gin.ResponseWriter) *ChannelProbeObserver {
	return &ChannelProbeObserver{ResponseWriter: writer, started: time.Now()}
}

// NewChannelProbeObserverAt includes selection and retries in group TTFT.
func NewChannelProbeObserverAt(writer gin.ResponseWriter, started time.Time) *ChannelProbeObserver {
	return &ChannelProbeObserver{ResponseWriter: writer, started: started}
}

func (w *ChannelProbeObserver) Write(data []byte) (int, error) {
	w.observe(data)
	return w.ResponseWriter.Write(data)
}

func (w *ChannelProbeObserver) WriteString(data string) (int, error) {
	w.observe([]byte(data))
	return w.ResponseWriter.WriteString(data)
}

func (w *ChannelProbeObserver) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *ChannelProbeObserver) observe(data []byte) {
	if !strings.Contains(strings.ToLower(w.Header().Get("Content-Type")), "text/event-stream") {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for len(data) > 0 {
		line, rest, found := bytes.Cut(data, []byte{'\n'})
		if !w.discard && len(w.pending)+len(line) <= 128<<10 {
			w.pending = append(w.pending, line...)
		} else {
			w.pending = nil
			w.discard = true
		}
		if !found {
			return
		}
		if !w.discard {
			payload, ok := bytes.CutPrefix(bytes.TrimSpace(w.pending), []byte("data:"))
			if ok {
				w.observeEvent(bytes.TrimSpace(payload))
			}
		}
		w.pending = w.pending[:0]
		w.discard = false
		data = rest
	}
}

// ObserveEvent is used by native WebSocket relays, whose frames never pass
// through gin's HTTP response writer. Call only for the current request.
func (w *ChannelProbeObserver) ObserveEvent(payload []byte) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.observeEvent(payload)
}

func (w *ChannelProbeObserver) observeEvent(payload []byte) {
	event := gjson.ParseBytes(payload)
	typ := event.Get("type").String()
	if event.Get("error").Exists() && event.Get("error").Type != gjson.Null || typ == "error" || typ == "response.failed" {
		w.failed = true
	}
	if w.first == nil && channelProbeContentDelta(event) {
		ms := time.Since(w.started).Milliseconds()
		w.first = &ms
	}
}

func channelProbeContentDelta(event gjson.Result) bool {
	for _, choice := range event.Get("choices").Array() {
		for _, path := range []string{"delta.content", "delta.reasoning_content", "delta.reasoning", "text"} {
			if value := choice.Get(path); value.Type == gjson.String && value.String() != "" {
				return true
			}
		}
		for _, call := range choice.Get("delta.tool_calls").Array() {
			if call.Get("function.arguments").String() != "" || call.Get("function.name").String() != "" {
				return true
			}
		}
	}
	switch event.Get("type").String() {
	case "content_block_delta":
		for _, field := range []string{"delta.text", "delta.thinking", "delta.partial_json"} {
			if event.Get(field).String() != "" {
				return true
			}
		}
	case "response.output_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta", "response.function_call_arguments.delta":
		return event.Get("delta").String() != ""
	}
	for _, candidate := range event.Get("candidates").Array() {
		for _, part := range candidate.Get("content.parts").Array() {
			if part.Get("text").String() != "" || part.Get("functionCall.name").String() != "" {
				return true
			}
		}
	}
	return false
}

func (w *ChannelProbeObserver) Result(ctx context.Context, info *relaycommon.RelayInfo, apiErr *types.NewAPIError) model.ChannelProbeResult {
	w.mu.Lock()
	defer w.mu.Unlock()
	result := model.ChannelProbeResult{CheckedAt: time.Now().Unix(), LatencyMS: time.Since(w.started).Milliseconds(), Success: true}
	if w.first != nil {
		value := *w.first
		result.TTFTMS = &value
	}
	var networkError net.Error
	switch {
	case apiErr != nil && (errors.Is(apiErr, context.DeadlineExceeded) || (errors.As(apiErr, &networkError) && networkError.Timeout())):
		result.ErrorCode = "timeout"
	case ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.ErrorCode = "timeout"
	case ctx != nil && errors.Is(ctx.Err(), context.Canceled):
		result.ErrorCode = "cancelled"
	case apiErr != nil:
		result.ErrorCode = "request_failed"
	case w.failed:
		result.ErrorCode = "stream_error"
	}
	if info != nil && info.StreamStatus != nil && result.ErrorCode == "" {
		outcome := info.StreamStatus.OutcomeSnapshot()
		switch {
		case outcome.EndReason == relaycommon.StreamEndReasonTimeout:
			result.ErrorCode = "timeout"
		case outcome.HasErrors || outcome.Response == relaycommon.ResponseOutcomeFailed:
			result.ErrorCode = "stream_error"
		case outcome.Response == relaycommon.ResponseOutcomeCancelled || outcome.EndReason == relaycommon.StreamEndReasonClientGone || outcome.EndReason == relaycommon.StreamEndReasonPingFail:
			result.ErrorCode = "cancelled"
		case outcome.EndReason == relaycommon.StreamEndReasonScannerErr || outcome.EndReason == relaycommon.StreamEndReasonPanic:
			result.ErrorCode = "stream_error"
		case outcome.Response == relaycommon.ResponseOutcomeIncomplete && outcome.IncompleteReason != "max_tokens" && outcome.IncompleteReason != "max_output_tokens":
			result.ErrorCode = "incomplete"
		case outcome.ExpectsTerminal && outcome.Response == relaycommon.ResponseOutcomeUnknown && outcome.EndReason != relaycommon.StreamEndReasonDone:
			result.ErrorCode = "incomplete"
		}
	}
	result.Success = result.ErrorCode == ""
	return result
}

// Each retry calls this afresh. Recording only the outer Relay return would
// erase failures whenever another channel succeeds later in the same request.
func BeginPassiveChannelProbe(c *gin.Context, info *relaycommon.RelayInfo, channelID int) func(*types.NewAPIError) {
	if outer, ok := c.Writer.(*ChannelProbeObserver); ok {
		outer.mu.Lock()
		outer.first, outer.pending, outer.failed, outer.discard = nil, nil, false, false
		outer.mu.Unlock()
	}
	probes := PassiveChannelProbes(channelID, info.OriginModelName)
	if len(probes) == 0 || info.IsChannelTest {
		return func(*types.NewAPIError) {}
	}
	previous := c.Writer
	observer := NewChannelProbeObserver(previous)
	c.Writer = observer
	return func(apiErr *types.NewAPIError) {
		c.Writer = previous
		result := observer.Result(c.Request.Context(), info, apiErr)
		RecordPassiveChannelProbeResult(probes, result)
	}
}

func RecordPassiveChannelProbeResult(probes []model.ChannelProbe, result model.ChannelProbeResult) {
	result.Source = "passive"
	for _, probe := range probes {
		result.Model = probe.Model
		result.Status = ChannelProbeResultStatus(result, probe)
		if err := model.StoreChannelProbeResult(probe, result); err != nil {
			common.SysError("passive channel probe persistence failed")
		}
	}
}

func RunWithPassiveChannelProbe(c *gin.Context, info *relaycommon.RelayInfo, channelID int, run func() *types.NewAPIError) (apiErr *types.NewAPIError) {
	finish := BeginPassiveChannelProbe(c, info, channelID)
	defer func() {
		if recovered := recover(); recovered != nil {
			finish(types.NewError(errors.New("relay attempt panic"), types.ErrorCodeBadResponse))
			panic(recovered)
		}
		finish(apiErr)
	}()
	return run()
}

// The group observer spans the retry loop. Attempt errors only reach the
// upstream observer; the final return determines group availability.
func BeginPassiveGroupProbe(c *gin.Context, info *relaycommon.RelayInfo) func(*types.NewAPIError) {
	if info.IsChannelTest {
		return func(*types.NewAPIError) {}
	}
	// Snapshot all candidate groups before any attempt. Auto-group retries are
	// attributed only to their final concrete group, with the original revision.
	probes := PassiveGroupProbes("", info.OriginModelName)
	if len(probes) == 0 {
		return func(*types.NewAPIError) {}
	}
	previous := c.Writer
	observer := NewChannelProbeObserver(previous)
	if started := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime); !started.IsZero() {
		observer.started = started
	}
	if started, ok := c.Get(channelProbeRequestStartKey); ok {
		observer.started = started.(time.Time)
	}
	c.Writer = observer
	return func(apiErr *types.NewAPIError) {
		c.Writer = previous
		result := observer.Result(c.Request.Context(), info, apiErr)
		group := info.UsingGroup
		if group == "" {
			group = info.TokenGroup
		}
		RecordPassiveGroupProbeResult(probes, group, result)
	}
}

const channelProbeRequestStartKey = "channel_probe_request_start"

func MarkChannelProbeRequestStart(c *gin.Context) { c.Set(channelProbeRequestStartKey, time.Now()) }

// A failed initial selection never reaches Relay's final-request observer.
// Count it once here, after model/group access validation, with no upstream row.
func RecordGroupProbeSelectionFailure(c *gin.Context, group, modelName string) {
	probes := PassiveGroupProbes(group, modelName)
	result := model.ChannelProbeResult{CheckedAt: time.Now().Unix(), ErrorCode: "channel_missing"}
	if started, ok := c.Get(channelProbeRequestStartKey); ok {
		result.LatencyMS = time.Since(started.(time.Time)).Milliseconds()
	}
	if errors.Is(c.Request.Context().Err(), context.DeadlineExceeded) {
		result.ErrorCode = "timeout"
	}
	RecordPassiveChannelProbeResult(probes, result)
}

func RecordPassiveGroupProbeResult(probes []model.ChannelProbe, group string, result model.ChannelProbeResult) {
	for _, probe := range probes {
		if probe.GroupName == group {
			RecordPassiveChannelProbeResult([]model.ChannelProbe{probe}, result)
		}
	}
}
