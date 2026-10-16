package helper

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	InitialScannerBufferSize    = 64 << 10  // 64KB (64*1024)
	DefaultMaxScannerBufferSize = 128 << 20 // 64MB (64*1024*1024) default SSE buffer size
	DefaultPingInterval         = 10 * time.Second
	// Bound a synchronous write to a slow client (full TCP buffer and no
	// server WriteTimeout), so it cannot keep the handler from cleaning up.
	streamWriteTimeout = 30 * time.Second
)

func getScannerBufferSize() int {
	if constant.StreamScannerMaxBufferMB > 0 {
		return constant.StreamScannerMaxBufferMB << 20
	}
	return DefaultMaxScannerBufferSize
}

// NewStreamScanner shares relay scanner configuration. Callers buffering bounded
// task state may additionally cap a line without increasing the configured limit.
func NewStreamScanner(reader io.Reader, maxBytes ...int) *bufio.Scanner {
	limit := getScannerBufferSize()
	if len(maxBytes) > 0 && maxBytes[0] > 0 {
		limit = min(limit, maxBytes[0])
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, min(InitialScannerBufferSize, limit)), limit)
	return scanner
}

func copyCodexSSEHeaders(c *gin.Context, resp *http.Response) {
	if c == nil || c.Writer == nil || resp == nil {
		return
	}
	// codex
	for _, name := range []string{"X-Reasoning-Included", "X-Codex-Turn-State"} {
		values := resp.Header.Values(name)
		if !service.ShouldCopyUpstreamHeader(c, name, values) {
			continue
		}
		for _, value := range values {
			if value != "" {
				c.Writer.Header().Add(name, value)
			}
		}
	}
}

// ExtendWriteDeadline pushes the connection write deadline forward before each
// stream write. Best-effort: writers that don't support deadlines (e.g.
// httptest recorders) are silently ignored.
func ExtendWriteDeadline(c *gin.Context) {
	if c == nil || c.Writer == nil {
		return
	}
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(streamWriteTimeout))
}

func StreamScannerHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, dataHandler func(data string, sr *StreamResult)) {
	if resp == nil || resp.Body == nil || dataHandler == nil {
		return
	}
	info.SnapshotStreamDisconnectSettings()
	info.StreamStatus = relaycommon.NewStreamStatus()
	lifecycle := info.StreamLifecycle
	if lifecycle == nil {
		lifecycle = relaycommon.NewStreamLifecycle(c.Request.Context(), info.StreamDisconnectMode == "drain" && !info.IsChannelTest, time.Duration(info.StreamDrainTimeoutSeconds)*time.Second)
		info.StreamLifecycle = lifecycle
	}
	InstallStreamWriter(c, lifecycle)

	// Only this reader runs concurrently. It never touches Gin, adaptor state,
	// or stream status. EOF travels through the same FIFO as the final usage.
	type scanEvent struct {
		line string
		end  bool
		err  error
	}
	events := make(chan scanEvent, 10)
	readerCtx, stopReader := context.WithCancel(context.Background())
	readerDone := make(chan struct{})
	readStarted := time.Now()
	var lastRead atomic.Int64
	streamingTimeout := time.Duration(constant.StreamingTimeout) * time.Second
	if streamingTimeout <= 0 {
		streamingTimeout = 300 * time.Second
	}
	idle := time.NewTimer(streamingTimeout)
	general := operation_setting.GetGeneralSetting()
	var ping *time.Ticker
	var pingC <-chan time.Time
	if general.PingIntervalEnabled && !info.DisablePing {
		interval := time.Duration(general.PingIntervalSeconds) * time.Second
		if interval <= 0 {
			interval = DefaultPingInterval
		}
		ping = time.NewTicker(interval)
		pingC = ping.C
	}
	defer func() {
		if r := recover(); r != nil {
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("stream panic: %v", r))
		}
		lifecycle.ObserveClient()
		if err := lifecycle.ClientError(); err != nil {
			info.StreamStatus.MarkClientGone(err)
		}
		stopReader()
		lifecycle.Close()
		_ = resp.Body.Close()
		idle.Stop()
		if ping != nil {
			ping.Stop()
		}
		<-readerDone
		if info.StreamStatus.IsNormalEnd() && !info.StreamStatus.HasErrors() {
			logger.LogInfo(c, common.LogText("stream ended: %s", info.StreamStatus.Summary()))
		} else {
			logger.LogError(c, common.LogText("stream ended: %s, received=%d", info.StreamStatus.Summary(), info.ReceivedResponseCount))
		}
	}()

	gopool.Go(func() {
		defer close(readerDone)
		defer close(events)
		defer func() {
			if r := recover(); r != nil {
				select {
				case events <- scanEvent{end: true, err: fmt.Errorf("scanner panic: %v", r)}:
				case <-readerCtx.Done():
				}
			}
		}()
		scanner := NewStreamScanner(&streamActivityReader{Reader: resp.Body, started: readStarted, lastRead: &lastRead})
		for scanner.Scan() {
			select {
			case events <- scanEvent{line: scanner.Text()}:
			case <-readerCtx.Done():
				return
			}
		}
		select {
		case events <- scanEvent{end: true, err: scanner.Err()}:
		case <-readerCtx.Done():
		}
	})

	copyCodexSSEHeaders(c, resp)
	SetEventStreamHeaders(c)
	gone := lifecycle.Gone
	sr := newStreamResult(info.StreamStatus)
	sr.lifecycle = lifecycle
	for {
		// Observe cancellation before processing already buffered data, too.
		lifecycle.ObserveClient()
		if err := lifecycle.ClientError(); err != nil {
			info.StreamStatus.MarkClientGone(err)
			gone = nil
			pingC = nil
			if ping != nil {
				ping.Stop()
			}
			if !lifecycle.Drain {
				return
			}
		}
		if lifecycle.Context.Err() != nil {
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDrainTimeout, context.Cause(lifecycle.Context))
			return
		}
		var event scanEvent
		var ok bool
		// Callback work may take longer than the idle interval while the reader
		// has already queued the final usage. Always consume that FIFO first.
		select {
		case event, ok = <-events:
		default:
			select {
			case <-gone:
				continue
			case <-lifecycle.Context.Done():
				continue
			case <-idle.C:
				if len(events) > 0 {
					// Keep the timer armed when a queued event wins this race.
					idle.Reset(time.Nanosecond)
					continue
				}
				remaining := streamingTimeout - (time.Since(readStarted) - time.Duration(lastRead.Load()))
				if remaining > 0 {
					idle.Reset(remaining)
					continue
				}
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonTimeout, nil)
				return
			case <-pingC:
				ExtendWriteDeadline(c)
				if err := PingData(c); err != nil {
					lifecycle.Disconnect(err)
				}
				continue
			case event, ok = <-events:
			}
		}
		// Cancellation may have happened while waiting for a queued event.
		lifecycle.ObserveClient()
		if err := lifecycle.ClientError(); err != nil {
			info.StreamStatus.MarkClientGone(err)
			if !lifecycle.Drain {
				return
			}
		}
		if lifecycle.Context.Err() != nil {
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDrainTimeout, context.Cause(lifecycle.Context))
			return
		}
		if !ok || event.end {
			if event.err != nil && event.err != io.EOF {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, event.err)
			} else {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
			}
			return
		}
		data := strings.TrimSpace(event.line)
		if data == "[DONE]" {
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
			return
		}
		payload, ok := strings.CutPrefix(data, "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" {
			continue
		}
		if strings.HasPrefix(payload, "[DONE]") {
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
			return
		}
		info.SetFirstResponseTime()
		info.ReceivedResponseCount++
		sr.reset()
		ExtendWriteDeadline(c)
		dataHandler(payload, sr)
		if err := lifecycle.ClientError(); err != nil {
			info.StreamStatus.MarkClientGone(err)
		}
		// These protocol terminals carry the last accounting facts. Do not
		// wait for EOF on providers/conversions that leave the socket open.
		if !sr.IsStopped() {
			switch gjson.Get(payload, "type").Str {
			case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled", "message_stop", "message_end":
				sr.Done()
			}
		}
		if sr.IsStopped() {
			return
		}
	}
}

// Count transport read activity, including partial SSE lines and comments;
// downstream callback time must not manufacture an upstream idle timeout.
type streamActivityReader struct {
	io.Reader
	started  time.Time
	lastRead *atomic.Int64
}

func (r *streamActivityReader) Read(data []byte) (int, error) {
	n, err := r.Reader.Read(data)
	if n > 0 {
		r.lastRead.Store(int64(time.Since(r.started)))
	}
	return n, err
}
