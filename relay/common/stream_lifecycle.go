package common

import (
	"context"
	"sync"
	"time"

	hostcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func (info *RelayInfo) SnapshotStreamDisconnectSettings() {
	if info.StreamDisconnectMode != "" {
		return
	}
	hostcommon.OptionMapRWMutex.RLock()
	setting := *operation_setting.GetQuotaSetting()
	hostcommon.OptionMapRWMutex.RUnlock()
	info.StreamDisconnectMode = setting.StreamDisconnectMode
	if info.StreamDisconnectMode != "drain" {
		info.StreamDisconnectMode = "cancel"
	}
	info.StreamDrainTimeoutSeconds = setting.StreamDrainTimeoutSeconds
	if info.StreamDrainTimeoutSeconds < 1 || info.StreamDrainTimeoutSeconds > 3600 {
		info.StreamDrainTimeoutSeconds = 300
	}
}

// StreamLifecycle separates client cancellation from the HTTP transport only
// in drain mode. Its callbacks own no Gin state and Close releases all timers.
type StreamLifecycle struct {
	Context context.Context
	Gone    chan struct{}
	Drain   bool

	client     context.Context
	cancel     context.CancelCauseFunc
	stopClient func() bool
	timeout    time.Duration
	mu         sync.Mutex
	err        error
	timer      *time.Timer
	closed     bool
}

func NewStreamLifecycle(client context.Context, drain bool, timeout time.Duration) *StreamLifecycle {
	parent := client
	if drain {
		parent = context.WithoutCancel(client)
	}
	ctx, cancel := context.WithCancelCause(parent)
	l := &StreamLifecycle{Context: ctx, Gone: make(chan struct{}), Drain: drain, client: client, cancel: cancel, timeout: timeout}
	l.stopClient = context.AfterFunc(client, func() { l.Disconnect(client.Err()) })
	l.ObserveClient()
	return l
}

func (l *StreamLifecycle) ObserveClient() {
	if err := l.client.Err(); err != nil {
		l.Disconnect(err)
	}
}

// Disconnect also handles a failed downstream write when net/http has not yet
// cancelled the client context. Repeated failures never extend the total limit.
func (l *StreamLifecycle) Disconnect(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return
	}
	if err == nil {
		err = context.Canceled
	}
	l.err = err
	close(l.Gone)
	// Adaptors may emit final frames after the scanner closes the upstream.
	// Keep observing downstream loss through those writes, without restarting
	// any reading, watcher, or timer after Close.
	if l.closed {
		return
	}
	if !l.Drain {
		l.cancel(err)
		return
	}
	l.timer = time.AfterFunc(l.timeout, func() { l.cancel(context.DeadlineExceeded) })
}

func (l *StreamLifecycle) ClientError() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

func (l *StreamLifecycle) Close() {
	l.stopClient()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.timer != nil {
		l.timer.Stop()
	}
	l.cancel(context.Canceled)
}
