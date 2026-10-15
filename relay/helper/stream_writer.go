package helper

import (
	"net/http"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

// InstallStreamWriter keeps suppression in place for adaptor final frames too.
// Call only while the handler owns c.Writer (before starting any pinger).
func InstallStreamWriter(c *gin.Context, lifecycle *relaycommon.StreamLifecycle) {
	if writer, ok := c.Writer.(*streamWriter); ok {
		writer.lifecycle = lifecycle
		return
	}
	c.Writer = &streamWriter{ResponseWriter: c.Writer, lifecycle: lifecycle}
}

type streamWriter struct {
	gin.ResponseWriter
	lifecycle *relaycommon.StreamLifecycle
}

func (w *streamWriter) disconnected() bool {
	w.lifecycle.ObserveClient()
	return w.lifecycle.ClientError() != nil
}

func (w *streamWriter) Write(data []byte) (int, error) {
	if w.disconnected() {
		return len(data), nil
	}
	n, err := w.ResponseWriter.Write(data)
	if err != nil {
		w.lifecycle.Disconnect(err)
		if w.lifecycle.Drain {
			return len(data), nil
		}
	}
	return n, err
}

func (w *streamWriter) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

func (w *streamWriter) WriteHeader(code int) {
	if !w.disconnected() {
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *streamWriter) WriteHeaderNow() {
	if !w.disconnected() {
		w.ResponseWriter.WriteHeaderNow()
	}
}

func (w *streamWriter) Flush() { _ = w.FlushError() }

func (w *streamWriter) FlushError() error {
	if w.disconnected() {
		return nil
	}
	w.ResponseWriter.WriteHeaderNow()
	var target http.ResponseWriter = w.ResponseWriter
	// Gin's Flush discards flush errors. Unwrap it so net/http can report a
	// failed flush even when the preceding buffered Write succeeded.
	if unwrapper, ok := target.(interface{ Unwrap() http.ResponseWriter }); ok {
		target = unwrapper.Unwrap()
	}
	err := http.NewResponseController(target).Flush()
	if err != nil {
		w.lifecycle.Disconnect(err)
		if w.lifecycle.Drain {
			return nil
		}
	}
	return err
}

func (w *streamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
