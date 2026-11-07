package controller

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/active_requests"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRelayActiveRequestLifecycle(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")
	require.NoError(t, i18n.Init())
	previousRetries, previousCount := common.RetryTimes, constant.CountToken
	previousLogs, previousErrors := common.LogConsumeEnabled, constant.ErrorLogEnabled
	previousDisable := common.AutomaticDisableChannelEnabled
	quotaSetting := operation_setting.GetQuotaSetting()
	previousQuota := *quotaSetting
	settings := operation_setting.GetGeneralSetting()
	previousPing := settings.PingIntervalEnabled
	previousUsableGroups := setting.UserUsableGroups2JSONString()
	common.RetryTimes, constant.CountToken = 1, false
	common.LogConsumeEnabled, constant.ErrorLogEnabled = false, false
	common.AutomaticDisableChannelEnabled = false
	quotaSetting.StreamDisconnectMode, quotaSetting.StreamDrainTimeoutSeconds = "drain", 10
	settings.PingIntervalEnabled = false
	t.Cleanup(func() {
		common.RetryTimes, constant.CountToken = previousRetries, previousCount
		common.LogConsumeEnabled, constant.ErrorLogEnabled = previousLogs, previousErrors
		common.AutomaticDisableChannelEnabled = previousDisable
		*quotaSetting = previousQuota
		settings.PingIntervalEnabled = previousPing
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousUsableGroups))
	})
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"premium":1}`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","premium":"Premium"}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"active-lifecycle":0}`))
	user := &model.User{Username: "active-lifecycle", Group: "default", Quota: 100000, Status: common.UserStatusEnabled, Setting: `{"billing_preference":"wallet_only"}`}
	require.NoError(t, db.Create(user).Error)
	service.InitHttpClient()

	for _, tc := range []struct {
		name                 string
		stream, retry, abort bool
		writeFailure         bool
		autoGroup            bool
	}{
		{name: "delayed HTTP and channel selection"},
		{name: "delayed SSE", stream: true},
		{name: "retry retains one request", retry: true},
		{name: "auto uses actual selected group", autoGroup: true},
		{name: "disconnect before headers drains", stream: true, abort: true},
		{name: "write failure without context cancellation drains", stream: true, writeFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestID := common.NewRequestId()
			query := func() active_requests.Result {
				result, err := active_requests.Query(t.Context(), active_requests.Filter{RequestID: requestID})
				require.NoError(t, err)
				return result
			}
			entered := make(chan int, 2)
			ready, end, secondChunk := make(chan struct{}), make(chan struct{}), make(chan struct{})
			releaseReady := sync.OnceFunc(func() { close(ready) })
			releaseEnd := sync.OnceFunc(func() { close(end) })
			releaseSecond := sync.OnceFunc(func() { close(secondChunk) })
			var attempts atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempt := int(attempts.Add(1))
				entered <- attempt
				<-ready
				if tc.retry && attempt == 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadGateway)
					_, _ = io.WriteString(w, `{"error":{"message":"try again","type":"upstream_error"}}`)
					return
				}
				if !tc.stream {
					<-end
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"chatcmpl-active","object":"chat.completion","model":"active-lifecycle","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-active\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n")
				_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-active\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
				w.(http.Flusher).Flush()
				if tc.writeFailure {
					<-secondChunk
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" again\"}}]}\n\n")
					w.(http.Flusher).Flush()
				}
				<-end
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			t.Cleanup(upstream.Close)
			group := "default"
			if tc.autoGroup {
				group = "premium"
			}
			channel := &model.Channel{Name: tc.name, Type: constant.ChannelTypeOpenAI, Key: "upstream-secret", Status: common.ChannelStatusEnabled, Group: group, Models: "active-lifecycle", BaseURL: &upstream.URL}
			require.NoError(t, db.Create(channel).Error)
			require.NoError(t, channel.AddAbilities(db))
			t.Cleanup(func() {
				require.NoError(t, db.Where("channel_id = ?", channel.Id).Delete(&model.Ability{}).Error)
				require.NoError(t, db.Delete(channel).Error)
			})

			selection, releaseSelection := make(chan struct{}), make(chan struct{})
			stopSelection := sync.OnceFunc(func() { close(releaseSelection) })
			if !tc.stream && !tc.retry {
				var blocked atomic.Bool
				require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:active-selection", func(tx *gorm.DB) {
					if tx.Statement.Table == "channels" && blocked.CompareAndSwap(false, true) {
						close(selection)
						<-releaseSelection
					}
				}))
				t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove("test:active-selection")) })
			}
			engine := gin.New()
			handles := make(chan *active_requests.Handle, 1)
			engine.POST("/v1/chat/completions", middleware.BodyStorageCleanup(), func(c *gin.Context) {
				c.Set(common.RequestIdKey, requestID)
				common.SetContextKey(c, constant.ContextKeyUserId, user.Id)
				user.ToBaseUser().WriteContext(c)
				common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
				common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
				if tc.autoGroup {
					common.SetContextKey(c, constant.ContextKeyUsingGroup, "auto")
					common.SetContextKey(c, constant.ContextKeyTokenGroup, "auto")
					common.SetContextKey(c, constant.ContextKeyTokenAutoGroups, []string{"premium"})
				}
				c.Set("token_name", "client-token")
			}, middleware.Distribute(), func(c *gin.Context) {
				handles <- active_requests.FromContext(c)
				Relay(c, types.RelayFormatOpenAI)
			})
			ctx, cancel := context.WithCancel(t.Context())
			body := fmt.Sprintf(`{"model":"active-lifecycle","stream":%t,"messages":[{"role":"user","content":"hello"}]}`, tc.stream)
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
			request.Header.Set("Content-Type", "application/json")
			writer := &activeRequestTestWriter{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{})}
			finishWrite := make(chan struct{})
			stopWrite := sync.OnceFunc(func() { close(finishWrite) })
			if !tc.stream && !tc.retry {
				writer.hold = finishWrite
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				engine.ServeHTTP(writer, request)
			}()
			t.Cleanup(func() {
				cancel()
				stopSelection()
				stopWrite()
				releaseReady()
				releaseSecond()
				releaseEnd()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("relay owner did not finish")
				}
			})
			if !tc.stream && !tc.retry {
				select {
				case <-selection:
				case <-time.After(5 * time.Second):
					t.Fatal("channel selection did not start")
				}
				result := query()
				require.Len(t, result.Items, 1)
				assert.Equal(t, "processing", result.Items[0].Status)
				assert.Zero(t, result.Items[0].Channel)
				stopSelection()
			}
			select {
			case attempt := <-entered:
				require.Equal(t, 1, attempt)
			case <-time.After(5 * time.Second):
				t.Fatal("upstream request did not start")
			}
			result := query()
			require.Len(t, result.Items, 1)
			assert.Equal(t, "waiting", result.Items[0].Status)
			assert.Equal(t, tc.stream, result.Items[0].IsStream)
			assert.Equal(t, channel.Id, result.Items[0].Channel)
			assert.Equal(t, group, result.Items[0].Group)
			if tc.autoGroup {
				filtered, err := active_requests.Query(t.Context(), active_requests.Filter{RequestID: requestID, Group: "premium"})
				require.NoError(t, err)
				assert.Len(t, filtered.Items, 1)
			}
			assert.Equal(t, user.Username, result.Items[0].Username)
			assert.Equal(t, "client-token", result.Items[0].TokenName)
			started := result.Items[0].StartedAt
			if tc.abort {
				cancel()
				require.Eventually(t, func() bool {
					result := query()
					return len(result.Items) == 1 && result.Items[0].ClientGone && result.Items[0].Status == "draining"
				}, 3*time.Second, time.Millisecond)
			}
			releaseReady()
			if tc.retry {
				select {
				case attempt := <-entered:
					require.Equal(t, 2, attempt)
				case <-time.After(5 * time.Second):
					t.Fatal("second upstream attempt did not start")
				}
				result := query()
				require.Len(t, result.Items, 1)
				assert.Equal(t, 1, result.Total)
				assert.Equal(t, 2, result.Items[0].Attempt)
				assert.Equal(t, started, result.Items[0].StartedAt)
			}
			if tc.stream && !tc.abort {
				select {
				case <-writer.wrote:
				case <-time.After(5 * time.Second):
					t.Fatal("first stream response did not arrive")
				}
				result := query()
				require.Len(t, result.Items, 1)
				assert.Equal(t, "streaming", result.Items[0].Status)
				if tc.writeFailure {
					writer.fail.Store(true)
					releaseSecond()
					require.Eventually(t, func() bool {
						result := query()
						return len(result.Items) == 1 && result.Items[0].ClientGone && result.Items[0].Status == "draining"
					}, 3*time.Second, time.Millisecond)
					assert.NoError(t, ctx.Err(), "write failures must be observed before net/http cancellation")
				}
			}
			releaseEnd()
			if writer.hold != nil {
				select {
				case <-writer.wrote:
				case <-time.After(5 * time.Second):
					t.Fatal("HTTP response did not arrive")
				}
				result := query()
				require.Len(t, result.Items, 1)
				assert.Equal(t, "waiting", result.Items[0].Status, "non-streaming responses must not enter streaming")
				stopWrite()
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("relay owner did not finish")
			}
			assert.Empty(t, query().Items)
			handle := <-handles
			handle.SetState("streaming")
			handle.MarkClientGone()
			assert.Empty(t, query().Items, "late callbacks must not recreate a finished request")
		})
	}
}

type activeRequestTestWriter struct {
	*httptest.ResponseRecorder
	wrote chan struct{}
	hold  <-chan struct{}
	once  sync.Once
	fail  atomic.Bool
}

func (w *activeRequestTestWriter) Write(data []byte) (int, error) {
	if w.fail.Load() {
		return 0, io.ErrClosedPipe
	}
	n, err := w.ResponseRecorder.Write(data)
	w.once.Do(func() { close(w.wrote) })
	if w.hold != nil {
		<-w.hold
	}
	return n, err
}

func (w *activeRequestTestWriter) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

func TestRelayKeepAliveDuringImagePreparation(t *testing.T) {
	requestID := common.NewRequestId()
	settings := operation_setting.GetGeneralSetting()
	previousEnabled, previousInterval := settings.PingIntervalEnabled, settings.PingIntervalSeconds
	previousCount, previousMedia := constant.CountToken, constant.GetMediaToken
	fetchSettings := system_setting.GetFetchSetting()
	previousFetchSettings := *fetchSettings
	fetchSettings.AllowPrivateIp = true
	fetchSettings.AllowedPorts = []string{"1-65535"}
	settings.PingIntervalEnabled, settings.PingIntervalSeconds = true, 1
	constant.CountToken, constant.GetMediaToken = true, true
	t.Cleanup(func() {
		settings.PingIntervalEnabled, settings.PingIntervalSeconds = previousEnabled, previousInterval
		constant.CountToken, constant.GetMediaToken = previousCount, previousMedia
		*fetchSettings = previousFetchSettings
	})
	service.InitHttpClient()

	imageRequested := make(chan struct{})
	releaseImage := make(chan struct{})
	var releaseOnce sync.Once
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(imageRequested)
		<-releaseImage
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer imageServer.Close()
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		c.Set(common.RequestIdKey, requestID)
		common.SetContextKey(c, constant.ContextKeyUserId, 9876543)
		common.SetContextKey(c, constant.ContextKeyOriginalModel, "claude-sonnet-4-20250514")
		Relay(c, types.RelayFormatClaude)
	})
	gateway := httptest.NewServer(engine)
	defer gateway.Close()
	defer releaseOnce.Do(func() { close(releaseImage) })

	body := `{"model":"claude-sonnet-4-20250514","max_tokens":16,"stream":true,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"` + imageServer.URL + `"}}]}]}`
	client := &http.Client{Timeout: 4 * time.Second}
	response, err := client.Post(gateway.URL+"/v1/messages", "application/json", strings.NewReader(body))
	require.NoError(t, err, "the gateway must send keepalive while its image download is blocked")
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "text/event-stream", response.Header.Get("Content-Type"))
	reader := bufio.NewReader(response.Body)
	for range 2 {
		line, readErr := reader.ReadString('\n')
		require.NoError(t, readErr)
		require.Equal(t, ": PING\n", line)
		line, readErr = reader.ReadString('\n')
		require.NoError(t, readErr)
		require.Equal(t, "\n", line)
	}
	select {
	case <-imageRequested:
	default:
		t.Fatal("the request never reached image preparation")
	}
	active, queryErr := active_requests.Query(t.Context(), active_requests.Filter{RequestID: requestID})
	require.NoError(t, queryErr)
	require.Len(t, active.Items, 1)
	assert.True(t, active.Items[0].IsStream)
	assert.Equal(t, "processing", active.Items[0].Status, "keepalive during local preparation is not an upstream response")
	releaseOnce.Do(func() { close(releaseImage) })
	remaining, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Contains(t, string(remaining), "event: error\n")
	require.Contains(t, string(remaining), `"type":"error"`)
	require.Contains(t, string(remaining), "data: ")
}

func TestRelayKeepAliveAcrossUpstreamRetries(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previousEnabled, previousInterval := settings.PingIntervalEnabled, settings.PingIntervalSeconds
	settings.PingIntervalEnabled, settings.PingIntervalSeconds = true, 1
	t.Cleanup(func() {
		settings.PingIntervalEnabled, settings.PingIntervalSeconds = previousEnabled, previousInterval
	})
	service.InitHttpClient()
	var attempts atomic.Int32
	retryStarted, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		close(retryStarted)
		select {
		case <-release:
			_, _ = io.WriteString(w, "data: ok\n\n")
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		stop := helper.StartStreamKeepAlive(c, 10*time.Millisecond)
		defer stop()
		if !assert.NoError(t, http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(5*time.Second))) {
			return
		}
		info := &relaycommon.RelayInfo{IsStream: true, ChannelMeta: &relaycommon.ChannelMeta{}}
		for range 2 {
			request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, upstream.URL, strings.NewReader("{}"))
			if !assert.NoError(t, err) {
				return
			}
			response, err := channel.DoRequest(c, request, info)
			if !assert.NoError(t, err) {
				return
			}
			if response.StatusCode == http.StatusTooManyRequests {
				response.Body.Close()
				continue
			}
			_, _ = io.Copy(c.Writer, response.Body)
			response.Body.Close()
		}
	})
	gateway := httptest.NewServer(engine)
	defer gateway.Close()
	defer releaseOnce.Do(func() { close(release) })
	client := &http.Client{Timeout: 4 * time.Second}
	response, err := client.Post(gateway.URL+"/v1/messages", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer response.Body.Close()
	select {
	case <-retryStarted:
	case <-time.After(time.Second):
		t.Fatal("the upstream retry did not start")
	}
	reader := bufio.NewReader(response.Body)
	for range 2 {
		line, readErr := reader.ReadString('\n')
		require.NoError(t, readErr)
		require.Equal(t, ": PING\n", line)
		line, readErr = reader.ReadString('\n')
		require.NoError(t, readErr)
		require.Equal(t, "\n", line)
	}
	releaseOnce.Do(func() { close(release) })
	body, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Contains(t, string(body), "data: ok\n\n")
	require.Equal(t, int32(2), attempts.Load())
}

func TestRelayKeepAlivePreservesValidationErrors(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previousEnabled := settings.PingIntervalEnabled
	settings.PingIntervalEnabled = true
	t.Cleanup(func() { settings.PingIntervalEnabled = previousEnabled })
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-20250514","stream":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	Relay(c, types.RelayFormatClaude)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
	require.NotContains(t, recorder.Body.String(), ": PING")
}

func TestStreamKeepAlivePreservesFastHTTPFailures(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previousEnabled, previousInterval := settings.PingIntervalEnabled, settings.PingIntervalSeconds
	settings.PingIntervalEnabled, settings.PingIntervalSeconds = true, 60
	t.Cleanup(func() {
		settings.PingIntervalEnabled, settings.PingIntervalSeconds = previousEnabled, previousInterval
	})
	service.InitHttpClient()
	for _, status := range []int{http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":"request rejected"}`)
			}))
			defer upstream.Close()
			engine := gin.New()
			engine.POST("/v1/messages", func(c *gin.Context) {
				stop := helper.StartStreamKeepAlive(c, time.Minute)
				defer stop()
				request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, upstream.URL, strings.NewReader("{}"))
				if !assert.NoError(t, err) {
					return
				}
				info := &relaycommon.RelayInfo{IsStream: true, ChannelMeta: &relaycommon.ChannelMeta{}}
				response, err := channel.DoRequest(c, request, info)
				if !assert.NoError(t, err) {
					return
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if assert.NoError(t, err) {
					c.Header("Content-Type", "application/json")
					c.Data(response.StatusCode, "application/json", body)
				}
			})
			gateway := httptest.NewServer(engine)
			defer gateway.Close()
			client := &http.Client{Timeout: 4 * time.Second}
			response, err := client.Post(gateway.URL+"/v1/messages", "application/json", strings.NewReader("{}"))
			require.NoError(t, err)
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, status, response.StatusCode)
			require.Equal(t, "application/json", response.Header.Get("Content-Type"))
			require.JSONEq(t, `{"error":"request rejected"}`, string(body))
		})
	}
}

func TestRelayKeepAliveHonorsStreamSettings(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previousEnabled := settings.PingIntervalEnabled
	previousCount, previousMedia, previousNonStreamMedia := constant.CountToken, constant.GetMediaToken, constant.GetMediaTokenNotStream
	constant.CountToken, constant.GetMediaToken, constant.GetMediaTokenNotStream = true, true, true
	t.Cleanup(func() {
		settings.PingIntervalEnabled = previousEnabled
		constant.CountToken, constant.GetMediaToken, constant.GetMediaTokenNotStream = previousCount, previousMedia, previousNonStreamMedia
	})
	for _, test := range []struct {
		name    string
		enabled bool
		stream  bool
	}{
		{name: "ping disabled", enabled: false, stream: true},
		{name: "non-streaming", enabled: true, stream: false},
		{name: "fast streaming failure", enabled: true, stream: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings.PingIntervalEnabled = test.enabled
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			body := `{"model":"claude-sonnet-4-20250514","max_tokens":16,"stream":` + strconv.FormatBool(test.stream) + `,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"!"}}]}]}`
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			common.SetContextKey(c, constant.ContextKeyOriginalModel, "claude-sonnet-4-20250514")
			Relay(c, types.RelayFormatClaude)
			require.Equal(t, http.StatusInternalServerError, recorder.Code)
			require.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
			require.NotContains(t, recorder.Body.String(), ": PING")
			var response map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			require.Equal(t, "error", response["type"])
		})
	}
}

func TestRelayStreamErrorAfterKeepAlive(t *testing.T) {
	for _, format := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(format), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/stream", nil)
			helper.SetEventStreamHeaders(c)
			require.NoError(t, helper.PingData(c))
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, "https://provider.example")
			common.SetContextKey(c, constant.ContextKeyUpstreamRequestOrigin, "https://provider.example")
			apiError := types.NewErrorWithStatusCode(errors.New("upstream rejected input at https://provider.example/v1/messages"), types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest)
			writeRelayStreamError(c, format, apiError)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
			require.Contains(t, recorder.Body.String(), "upstream rejected input")
			require.NotContains(t, recorder.Body.String(), "provider.example")
			status, ok := common.GetContextKeyType[*relaycommon.StreamStatus](c, constant.ContextKeyResponseStreamStatus)
			require.True(t, ok)
			require.True(t, status.ResponseFailed(), "stream errors must not consume the successful-request rate limit")
			require.Equal(t, http.StatusBadRequest, status.OutcomeSnapshot().ErrorStatus)
			if format == types.RelayFormatClaude {
				require.Contains(t, recorder.Body.String(), "event: error\n")
			} else {
				require.Contains(t, recorder.Body.String(), "data: [DONE]")
			}
		})
	}
}

func TestStreamKeepAliveCancellation(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/stream", nil).WithContext(requestContext)
	stop := helper.StartStreamKeepAlive(c, time.Hour)
	cancel()
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("keepalive did not stop after client cancellation")
	}
	_, err := c.Writer.WriteString("data: done\n\n")
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(recorder.Body.String(), "data: done\n\n"))
}
