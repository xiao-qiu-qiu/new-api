package volcengine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTTSWebSocketRequestCancellationClosesUpstream(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct {
		auth    string
		message *Message
		err     error
	}, 1)
	closed := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			closed <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		message, err := ReceiveMessage(conn)
		started <- struct {
			auth    string
			message *Message
			err     error
		}{r.Header.Get("Authorization"), message, err}
		if err != nil {
			closed <- err
			return
		}
		// Keep the upstream silent until the request owner closes the socket.
		_, _, err = conn.ReadMessage()
		closed <- err
	}))
	t.Cleanup(upstream.Close)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/speech", nil).WithContext(ctx)
	request := VolcengineTTSRequest{Request: VolcengineTTSReqInfo{Text: "hello", Operation: "submit"}}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ApiKey: "test-app|test-token"}}
	var usage any
	var apiErr *types.NewAPIError
	done := make(chan struct{})
	go func() {
		defer close(done)
		usage, apiErr = handleTTSWebSocketResponse(c, "ws"+strings.TrimPrefix(upstream.URL, "http"), request, info, "mp3")
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Error("TTS helper did not finish")
		}
	})
	select {
	case observed := <-started:
		require.NoError(t, observed.err)
		assert.Equal(t, "Bearer;test-token", observed.auth)
		require.NotNil(t, observed.message)
		assert.Equal(t, MsgTypeFullClientRequest, observed.message.MsgType)
		var received VolcengineTTSRequest
		require.NoError(t, common.Unmarshal(observed.message.Payload, &received))
		assert.Equal(t, request, received)
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not receive the TTS request")
	}
	select {
	case <-done:
		t.Fatal("TTS helper returned before cancellation or an upstream response")
	default:
	}
	cancel()
	select {
	case err := <-closed:
		require.Error(t, err, "request cancellation must close the upstream connection")
		assert.True(t, websocket.IsCloseError(err, websocket.CloseAbnormalClosure), "upstream must observe socket closure before its read deadline: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("upstream remained connected after request cancellation")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("TTS helper remained blocked waiting for an upstream response")
	}
	assert.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeBadResponse, apiErr.GetErrorCode())
}
