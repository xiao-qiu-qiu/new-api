package helper

import (
	"errors"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// StreamResult is passed to each dataHandler invocation, providing methods
// to record soft errors, signal fatal stops, or mark normal completion.
// StreamScannerHandler checks IsStopped() after each callback invocation.
type StreamResult struct {
	status    *relaycommon.StreamStatus
	stopped   bool
	lifecycle *relaycommon.StreamLifecycle
}

func newStreamResult(status *relaycommon.StreamStatus) *StreamResult {
	return &StreamResult{status: status}
}

// Error records a soft error. The stream continues processing.
// Can be called multiple times per chunk.
func (r *StreamResult) Error(err error) {
	if err == nil {
		return
	}
	r.status.RecordError(err.Error())
	var apiErr *types.NewAPIError
	if errors.As(err, &apiErr) {
		r.status.MarkFailed(string(apiErr.GetErrorCode()), apiErr.ToOpenAIError().Type, apiErr.StatusCode)
	}
}

// Stop records a fatal error and marks the stream to stop after this chunk.
func (r *StreamResult) Stop(err error) {
	// A provider may explicitly stop on a downstream cancellation. Keep
	// draining in that case, while genuine protocol/parse errors still stop.
	if r.lifecycle != nil && r.lifecycle.Drain && r.lifecycle.ClientError() != nil && !r.status.ResponseFailed() &&
		(err == nil || errors.Is(err, r.lifecycle.ClientError())) {
		r.status.MarkClientGone(r.lifecycle.ClientError())
		return
	}
	r.Error(err)
	r.status.SetEndReason(relaycommon.StreamEndReasonHandlerStop, err)
	r.stopped = true
}

// Done signals that the handler has finished processing normally
// (e.g., Dify "message_end"). The stream stops after this chunk.
func (r *StreamResult) Done() {
	if r.lifecycle != nil && r.lifecycle.ClientError() != nil {
		r.status.MarkClientGone(r.lifecycle.ClientError())
	}
	r.status.SetEndReason(relaycommon.StreamEndReasonDone, nil)
	r.stopped = true
}

// IsStopped returns whether Stop() or Done() was called during this chunk.
func (r *StreamResult) IsStopped() bool {
	return r.stopped
}

// reset clears the per-chunk stopped flag so the object can be reused.
func (r *StreamResult) reset() {
	r.stopped = false
}
