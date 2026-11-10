package router

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type playgroundMediaRoundTripper func(*http.Request) (*http.Response, error)

func (transport playgroundMediaRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func playgroundMediaMultipart(t *testing.T, fields map[string]string, fileField string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for key, value := range fields {
		require.NoError(t, writer.WriteField(key, value))
	}
	if fileField != "" {
		file, err := writer.CreateFormFile(fileField, "input.png")
		require.NoError(t, err)
		_, err = file.Write([]byte("image-fixture"))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return body, writer.FormDataContentType()
}

func TestPlaygroundMediaRelay(t *testing.T) {
	require.NoError(t, i18n.Init())
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldBatch, oldMemory, oldConsume := common.BatchUpdateEnabled, common.MemoryCacheEnabled, common.LogConsumeEnabled
	oldSecret := common.SessionSecret
	oldPrices, oldGroups := ratio_setting.ModelPrice2JSONString(), ratio_setting.GroupRatio2JSONString()
	oldUsable := setting.UserUsableGroups2JSONString()
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.BatchUpdateEnabled, common.MemoryCacheEnabled, common.LogConsumeEnabled = oldBatch, oldMemory, oldConsume
		common.SessionSecret = oldSecret
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrices))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(oldUsable))
	})
	setupRelayRouterTestDB(t)
	common.BatchUpdateEnabled, common.MemoryCacheEnabled, common.LogConsumeEnabled = false, false, false
	common.SessionSecret = "playground-media-test-secret"
	require.NoError(t, model.DB.AutoMigrate(&model.UserSession{}, &model.UserAccessToken{}, &model.Option{}, &model.Channel{}, &model.Task{}, &model.Log{}))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":2}`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default group","vip":"VIP group"}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"canvas-image":0.001,"canvas-image-newapi":0.001,"canvas-image-xai":0.001,"canvas-speech":0.001,"canvas-chat":0.001,"sora-2":0.001}`))
	user := &model.User{Username: "canvas-owner", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", Quota: 1000000, AuthVersion: 1, Setting: `{"billing_preference":"wallet_only"}`}
	require.NoError(t, model.DB.Create(user).Error)
	bundle, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "test")
	require.NoError(t, err)

	var upstreamCalls atomic.Int32
	type imageRequest struct {
		accept string
		fields map[string]any
	}
	upstreamImages := make(chan imageRequest, 1)
	speechPCM := make([]byte, 24000*2) // One second of 24 kHz, 16-bit mono PCM.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		switch r.URL.Path {
		case "/v1/images/generations", "/v1/images/edits":
			fields := make(map[string]any)
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				require.NoError(t, r.ParseMultipartForm(1024))
				defer r.MultipartForm.RemoveAll()
				for key, values := range r.MultipartForm.Value {
					require.Len(t, values, 1)
					fields[key] = values[0]
				}
				file, _, fileErr := r.FormFile("image")
				require.NoError(t, fileErr)
				defer file.Close()
				data, readErr := io.ReadAll(file)
				require.NoError(t, readErr)
				assert.Equal(t, "image-fixture", string(data))
			} else {
				require.NoError(t, common.DecodeJson(r.Body, &fields))
			}
			upstreamImages <- imageRequest{accept: r.Header.Get("Accept"), fields: fields}
			w.Header().Set("Content-Type", "application/json")
			switch fields["prompt"] {
			case "upstream rejection":
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"message":"image rejected","type":"invalid_request_error","code":"image_rejected"}}`)
				return
			case "malformed response":
				_, _ = io.WriteString(w, `{`)
				return
			}
			_, _ = io.WriteString(w, `{"created":1,"data":[{"b64_json":"aW1hZ2U=","revised_prompt":"`+r.Header.Get("Authorization")+`"}]}`)
		case "/v1/videos":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"id":"vendor-private-video","object":"video","status":"queued","progress":0}`)
		case "/v1/audio/speech":
			w.Header().Set("Content-Type", "audio/pcm")
			_, _ = w.Write(speechPCM)
		case "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"resp_chat","object":"response","model":"canvas-chat","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
		default:
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	service.InitHttpClient()
	for _, tc := range []struct {
		group, models string
		channelType   int
	}{
		{"default", "canvas-image,canvas-speech,canvas-chat,sora-2", constant.ChannelTypeOpenAI},
		{"vip", "canvas-image,canvas-speech,canvas-chat,sora-2", constant.ChannelTypeOpenAI},
		{"default", "canvas-image-newapi", constant.ChannelTypeNewAPI},
		{"default", "canvas-image-xai", constant.ChannelTypeXai},
	} {
		channel := &model.Channel{Type: tc.channelType, Key: "upstream-" + tc.group, Name: tc.models, Status: common.ChannelStatusEnabled, BaseURL: &upstream.URL, Group: tc.group, Models: tc.models}
		require.NoError(t, channel.Insert())
	}
	engine := gin.New()
	engine.Use(middleware.BodyStorageCleanup())
	SetPlaygroundMediaRouter(engine)
	SetTaskPluginProtocolRouter(engine)
	apiToken := &model.Token{UserId: user.Id, Key: "canvasapitoken", Group: "default", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}
	require.NoError(t, model.DB.Create(apiToken).Error)

	for _, tc := range []struct {
		name, path, json, group, file string
	}{
		{name: "image default group", path: "/images/generations", json: `{"model":"canvas-image","prompt":"cat","n":1}`},
		{name: "image JSON group", path: "/images/generations", json: `{"model":"canvas-image","prompt":"cat","n":1,"group":"vip"}`},
		{name: "image edit JSON", path: "/images/edits", json: `{"model":"canvas-image","prompt":"cat","n":1,"group":"vip","image":"https://example.com/input.png"}`},
		{name: "image edit multipart", path: "/images/edits", group: "vip", file: "image"},
		{name: "video JSON", path: "/videos", json: `{"model":"sora-2","prompt":"cat","seconds":4,"group":"vip"}`},
		{name: "video multipart", path: "/videos", group: "vip", file: "input_reference"},
		{name: "speech", path: "/audio/speech", json: `{"model":"canvas-speech","input":"hello","voice":"alloy","response_format":"pcm","group":"vip"}`},
		{name: "responses", path: "/responses", json: `{"model":"canvas-chat","input":"hello","group":"vip"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader = strings.NewReader(tc.json)
			contentType := "application/json"
			if tc.file != "" {
				modelName := "canvas-image"
				if tc.path == "/videos" {
					modelName = "sora-2"
				}
				body, contentType = playgroundMediaMultipart(t, map[string]string{"model": modelName, "prompt": "cat", "group": tc.group}, tc.file)
			}
			request := httptest.NewRequest(http.MethodPost, "/pg"+tc.path, body)
			request.Header.Set("Content-Type", contentType)
			request.Header.Set("Authorization", "Bearer "+bundle.AccessToken)
			before, quotaErr := model.GetUserQuota(user.Id, true)
			require.NoError(t, quotaErr)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Equal(t, "/pg"+tc.path, request.URL.Path)
			after, quotaErr := model.GetUserQuota(user.Id, true)
			require.NoError(t, quotaErr)
			assert.Less(t, after, before, "session calls must charge the user without an API token row")
			if strings.HasPrefix(tc.path, "/images/") {
				<-upstreamImages
				assert.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
				assert.Contains(t, recorder.Body.String(), "aW1hZ2U=")
				wantGroup := "vip"
				if tc.name == "image default group" {
					wantGroup = "default"
				}
				assert.Contains(t, recorder.Body.String(), "upstream-"+wantGroup)
			}
			if tc.path == "/videos" {
				var response struct {
					ID string `json:"id"`
				}
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
				task, exists, taskErr := model.GetByTaskId(user.Id, response.ID)
				require.NoError(t, taskErr)
				require.True(t, exists)
				assert.Equal(t, "vip", task.Group)
				assert.Zero(t, task.PrivateData.TokenId)
				assert.NotContains(t, recorder.Body.String(), "vendor-private-video")
			}
			if tc.path == "/audio/speech" {
				assert.Equal(t, "audio/pcm", recorder.Header().Get("Content-Type"))
				assert.Equal(t, speechPCM, recorder.Body.Bytes())
				assert.Equal(t, 1000, before-after, "speech must settle the configured fixed price and group ratio")
			}
		})
	}

	for _, multipartRequest := range []bool{false, true} {
		name := "JSON keepalive spans upstream headers and body"
		if multipartRequest {
			name = "multipart keepalive spans upstream headers and body"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// Prepared-statement channels must not escape a synctest bubble.
				oldDB, oldLogDB := model.DB, model.LOG_DB
				sqlDB, dbErr := oldDB.DB()
				require.NoError(t, dbErr)
				bubbleDB := oldDB.Session(&gorm.Session{NewDB: true, Initialized: true})
				bubbleDB.PrepareStmt = false
				bubbleDB.ConnPool, bubbleDB.Statement.ConnPool = sqlDB, sqlDB
				bubbleDB = bubbleDB.Session(&gorm.Session{NewDB: true})
				model.DB, model.LOG_DB = bubbleDB, bubbleDB
				defer func() { model.DB, model.LOG_DB = oldDB, oldLogDB }()
				// Issue the session against synctest's clock so JWT not-before checks remain valid.
				session, sessionErr := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "test")
				require.NoError(t, sessionErr)
				headersStarted, releaseHeaders := make(chan struct{}), make(chan struct{}, 1)
				defer close(releaseHeaders)
				bodyReader, bodyWriter := io.Pipe()
				defer bodyReader.Close()
				defer bodyWriter.Close()
				client := service.GetHttpClient()
				oldTransport := client.Transport
				defer func() { client.Transport = oldTransport }()
				client.Transport = playgroundMediaRoundTripper(func(request *http.Request) (*http.Response, error) {
					defer request.Body.Close()
					close(headersStarted)
					<-releaseHeaders
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: bodyReader, Request: request}, nil
				})
				path := "/pg/images/generations"
				var body io.Reader = strings.NewReader(`{"model":"canvas-image","prompt":"cat","n":2}`)
				contentType := "application/json"
				if multipartRequest {
					path = "/pg/images/edits"
					body, contentType = playgroundMediaMultipart(t, map[string]string{"model": "canvas-image", "prompt": "cat", "n": "2"}, "image")
				}
				request := httptest.NewRequest(http.MethodPost, path, body)
				request.Header.Set("Content-Type", contentType)
				request.Header.Set("Authorization", "Bearer "+session.AccessToken)
				request.Header.Set("X-Playground-Image-Stream", "true")
				recorder := httptest.NewRecorder()
				finished := make(chan struct{})
				go func() {
					defer close(finished)
					engine.ServeHTTP(recorder, request)
				}()
				synctest.Wait()
				select {
				case <-headersStarted:
				default:
					t.Fatalf("image request did not reach the upstream transport: status=%d body=%s", recorder.Code, recorder.Body.String())
				}
				assert.Empty(t, recorder.Body.String())
				<-time.After(20 * time.Second) // synctest advances virtual time; no wall-clock wait.
				synctest.Wait()
				assert.Equal(t, ": PING\n\n", recorder.Body.String(), "heartbeat while waiting for upstream headers")
				releaseHeaders <- struct{}{}
				synctest.Wait()
				<-time.After(20 * time.Second)
				synctest.Wait()
				assert.Equal(t, ": PING\n\n: PING\n\n", recorder.Body.String(), "heartbeat while reading the upstream body")
				_, writeErr := io.WriteString(bodyWriter, `{"created":1,"data":[{"b64_json":"aW1hZ2U="}]}`)
				require.NoError(t, writeErr)
				require.NoError(t, bodyWriter.Close())
				<-finished
				assert.Equal(t, http.StatusOK, recorder.Code)
				assert.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
				assert.Contains(t, recorder.Body.String(), "event: image_generation.completed\n")
				assert.Contains(t, recorder.Body.String(), `"b64_json":"aW1hZ2U="`)
				assert.True(t, strings.HasSuffix(recorder.Body.String(), "data: [DONE]\n\n"))
			})
		})
	}

	for _, tc := range []struct {
		name, path, model, header, accept, prompt string
		multipart, stream, explicitFalse          bool
		status                                    int
	}{
		{name: "OpenAI JSON negotiated SSE", stream: true},
		{name: "OpenAI multipart negotiated SSE", multipart: true, stream: true},
		{name: "NewAPI JSON negotiated SSE", model: "canvas-image-newapi", stream: true},
		{name: "NewAPI multipart negotiated SSE", model: "canvas-image-newapi", multipart: true, stream: true},
		{name: "explicit false keeps upstream synchronous", explicitFalse: true, accept: "application/json", stream: true},
		{name: "no negotiation stays JSON", header: "absent"},
		{name: "false negotiation stays JSON", header: "false"},
		{name: "v1 JSON ignores playground header", path: "/v1/images/generations"},
		{name: "v1 multipart ignores playground header", path: "/v1/images/edits", multipart: true},
		{name: "other API stays JSON", model: "canvas-image-xai"},
		{name: "upstream rejection stays JSON", prompt: "upstream rejection", status: http.StatusBadRequest},
		{name: "malformed upstream stays JSON", prompt: "malformed response", status: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.model == "" {
				tc.model = "canvas-image"
			}
			if tc.prompt == "" {
				tc.prompt = "cat"
			}
			if tc.path == "" {
				tc.path = "/pg/images/generations"
				if tc.multipart {
					tc.path = "/pg/images/edits"
				}
			}
			fields := map[string]any{"model": tc.model, "prompt": tc.prompt, "n": 2, "response_format": "b64_json"}
			if tc.model != "canvas-image-xai" {
				fields["size"], fields["quality"], fields["output_format"] = "1024x1024", "high", "png"
			}
			if tc.explicitFalse {
				fields["stream"] = false
			}
			jsonBody, marshalErr := common.Marshal(fields)
			require.NoError(t, marshalErr)
			var body io.Reader = bytes.NewReader(jsonBody)
			contentType := "application/json"
			if tc.multipart {
				form := make(map[string]string, len(fields))
				for key, value := range fields {
					form[key] = fmt.Sprint(value)
				}
				body, contentType = playgroundMediaMultipart(t, form, "image")
			}
			request := httptest.NewRequest(http.MethodPost, tc.path, body)
			request.Header.Set("Content-Type", contentType)
			request.Header.Set("Accept", tc.accept)
			if tc.header != "absent" {
				request.Header.Set("X-Playground-Image-Stream", common.GetStringIfEmpty(tc.header, "true"))
			}
			token := bundle.AccessToken
			if strings.HasPrefix(tc.path, "/v1/") {
				token = apiToken.Key
			}
			request.Header.Set("Authorization", "Bearer "+token)
			beforeCalls := upstreamCalls.Load()
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			assert.Equal(t, beforeCalls+1, upstreamCalls.Load(), "a synchronous image request must only be submitted once")
			select {
			case submitted := <-upstreamImages:
				assert.Equal(t, tc.accept, submitted.accept, "SSE negotiation must preserve the upstream Accept header")
				for key, value := range fields {
					assert.Equal(t, fmt.Sprint(value), fmt.Sprint(submitted.fields[key]), "upstream field %s", key)
				}
				if !tc.explicitFalse {
					assert.NotContains(t, submitted.fields, "stream", "SSE negotiation must not enable upstream streaming")
				}
			default:
				t.Fatal("image request did not reach the upstream")
			}
			wantStatus := tc.status
			if wantStatus == 0 {
				wantStatus = http.StatusOK
			}
			require.Equal(t, wantStatus, recorder.Code, recorder.Body.String())
			if tc.stream {
				assert.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
				frames := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n\n")
				require.Len(t, frames, 2, recorder.Body.String())
				payload, ok := strings.CutPrefix(frames[0], "event: image_generation.completed\ndata: ")
				require.True(t, ok, frames[0])
				var event map[string]any
				require.NoError(t, common.UnmarshalJsonStr(payload, &event))
				assert.Equal(t, "image_generation.completed", event["type"])
				assert.Equal(t, "aW1hZ2U=", event["b64_json"])
				assert.Equal(t, "Bearer upstream-default", event["revised_prompt"])
				assert.Equal(t, float64(1), event["created_at"])
				assert.Equal(t, "data: [DONE]", frames[1])
				return
			}
			assert.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
			assert.NotContains(t, recorder.Body.String(), "[DONE]")
			var response map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			if tc.status != 0 {
				assert.Contains(t, response, "error")
				assert.NotContains(t, response, "data")
				return
			}
			assert.Contains(t, response, "data")
			assert.Contains(t, recorder.Body.String(), "aW1hZ2U=")
			assert.NotContains(t, response, "error")
		})
	}

	for _, tc := range []struct{ name, path, body, group, file string }{
		{name: "JSON group denied", path: "/images/generations", body: `{"model":"canvas-image","prompt":"cat","group":"private"}`},
		{name: "multipart group denied", path: "/images/edits", group: "private", file: "image"},
		{name: "image count bounded", path: "/images/generations", body: `{"model":"canvas-image","prompt":"cat","n":1000000}`},
		{name: "video duration bounded", path: "/videos", body: `{"model":"sora-2","prompt":"cat","seconds":1000000}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader = strings.NewReader(tc.body)
			contentType := "application/json"
			if tc.file != "" {
				body, contentType = playgroundMediaMultipart(t, map[string]string{"model": "canvas-image", "prompt": "cat", "group": tc.group}, tc.file)
			}
			request := httptest.NewRequest(http.MethodPost, "/pg"+tc.path, body)
			request.Header.Set("Content-Type", contentType)
			request.Header.Set("Authorization", "Bearer "+bundle.AccessToken)
			request.Header.Set("X-Playground-Image-Stream", "true")
			before := upstreamCalls.Load()
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			wantStatus := http.StatusBadRequest
			if tc.group != "" || strings.Contains(tc.body, "private") {
				wantStatus = http.StatusForbidden
			}
			assert.Equal(t, wantStatus, recorder.Code, recorder.Body.String())
			assert.Equal(t, before, upstreamCalls.Load())
			assert.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
			assert.NotContains(t, recorder.Body.String(), "[DONE]")
		})
	}

	task := &model.Task{TaskID: "task_canvas_owned", Platform: constant.TaskPlatform("jimeng"), UserId: user.Id, Group: "vip", Status: model.TaskStatusSuccess, Progress: "100%", Properties: model.Properties{OriginModelName: "sora-2"}, PrivateData: model.TaskPrivateData{ResultURL: "data:video/mp4;base64,dmlkZW8="}}
	require.NoError(t, model.DB.Create(task).Error)
	admin := &model.User{Username: "canvas-admin", AffCode: "canvasadmin", Role: common.RoleRootUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1}
	require.NoError(t, model.DB.Create(admin).Error)
	adminBundle, err := service.CreateLoginSession(admin.Id, "password", "127.0.0.1", "test")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, method, path, token string
		status                    int
	}{
		{"owner polls despite different query group", "GET", "/pg/videos/task_canvas_owned?group=default", bundle.AccessToken, 200},
		{"owner downloads", "GET", "/pg/videos/task_canvas_owned/content", bundle.AccessToken, 200},
		{"owner HEAD", "HEAD", "/pg/videos/task_canvas_owned/content", bundle.AccessToken, 200},
		{"admin cross-owner poll denied", "GET", "/pg/videos/task_canvas_owned?group=vip", adminBundle.AccessToken, 400},
		{"admin cross-owner download denied", "GET", "/pg/videos/task_canvas_owned/content", adminBundle.AccessToken, 404},
		{"admin cross-owner HEAD denied", "HEAD", "/pg/videos/task_canvas_owned/content", adminBundle.AccessToken, 404},
		{"query group denied", "GET", "/pg/videos/task_canvas_owned?group=private", bundle.AccessToken, 403},
		{"anonymous denied", "GET", "/pg/videos/task_canvas_owned", "", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.token != "" {
				request.Header.Set("Authorization", "Bearer "+tc.token)
			}
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			assert.Equal(t, tc.status, recorder.Code, recorder.Body.String())
			if tc.status == 200 && tc.method == "GET" && strings.HasSuffix(tc.path, "/content") {
				assert.Equal(t, "video", recorder.Body.String())
			}
		})
	}

	t.Run("empty user wallet denied", func(t *testing.T) {
		remaining, quotaErr := model.GetUserQuota(user.Id, true)
		require.NoError(t, quotaErr)
		require.NoError(t, model.DecreaseUserQuota(user.Id, remaining, false))
		request := httptest.NewRequest(http.MethodPost, "/pg/images/generations", strings.NewReader(`{"model":"canvas-image","prompt":"cat"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+bundle.AccessToken)
		request.Header.Set("X-Playground-Image-Stream", "true")
		before := upstreamCalls.Load()
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
		assert.Equal(t, before, upstreamCalls.Load())
		assert.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
		assert.NotContains(t, recorder.Body.String(), "[DONE]")
	})

	request := httptest.NewRequest(http.MethodPost, "/pg/images/generations", strings.NewReader(`{"model":"canvas-image","prompt":"cat"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://untrusted.example")
	request.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: bundle.RefreshToken})
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, "a cookie-only cross-origin request must not authorize media generation")
	identity, _, err := service.ParseDashboardAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	revoked, err := model.RevokeUserSession(user.Id, identity.SessionID, "test")
	require.NoError(t, err)
	require.True(t, revoked)
	request = httptest.NewRequest(http.MethodGet, "/pg/videos/task_canvas_owned/content", nil)
	request.Header.Set("Authorization", "Bearer "+bundle.AccessToken)
	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, "revoked sessions must not read artifacts")
}
