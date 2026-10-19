package plugins_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	jspluginadaptor "github.com/QuantumNous/new-api/relay/channel/task/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadLaoliVideoPlugin(t *testing.T) *jsplugin.LoadedPlugin {
	t.Helper()
	source, err := builtinplugins.Source("laoli-video")
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: "laoli-video"})
	require.NoError(t, err)
	return plugin
}

func laoliObject(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, common.Unmarshal(encoded, &result))
	return result
}

func TestLaoliVideoProtocolRequests(t *testing.T) {
	plugin := loadLaoliVideoPlugin(t)
	for _, tc := range []struct {
		name string
		body map[string]any
		want map[string]any
	}{
		{
			name: "canvas metadata defaults become H3 parameters",
			body: map[string]any{"kind": "json", "value": map[string]any{
				"prompt": "  keep this prompt  ", "seconds": "6", "size": "1280x720", "n": 1,
				"images":   []string{"https://media.example/one.png", "https://media.example/two.png"},
				"metadata": map[string]any{"resolution": "720p", "aspect_ratio": "16:9", "first_frame": "https://media.example/one.png", "audios": []string{"https://media.example/voice.wav"}},
			}},
			want: map[string]any{"model": "H3-KS", "prompt": "  keep this prompt  ", "seconds": float64(6), "resolution": "1080p", "aspect_ratio": "16:9", "references": []any{
				map[string]any{"type": "image", "role": "reference", "source": "https://media.example/one.png"},
				map[string]any{"type": "image", "role": "reference", "source": "https://media.example/two.png"},
				map[string]any{"type": "audio", "role": "reference", "source": "https://media.example/voice.wav"},
			}},
		},
		{
			name: "explicit references retain duplicates and ignore mirror lists",
			body: map[string]any{"kind": "json", "value": map[string]any{
				"prompt": "p", "references": []any{
					map[string]any{"type": "image", "source": "https://media.example/one.png"},
					map[string]any{"type": "audio", "source": "https://media.example/voice.wav"},
					map[string]any{"type": "image", "source": "https://media.example/one.png"},
				}, "images": []string{"https://media.example/mirror.png"}, "metadata": map[string]any{"first_frame": "https://media.example/one.png", "audios": []string{"https://media.example/voice.wav"}},
			}},
			want: map[string]any{"model": "H3-KS", "prompt": "p", "seconds": float64(6), "resolution": "1080p", "aspect_ratio": "16:9", "references": []any{
				map[string]any{"type": "image", "role": "reference", "source": "https://media.example/one.png"},
				map[string]any{"type": "audio", "role": "reference", "source": "https://media.example/voice.wav"},
				map[string]any{"type": "image", "role": "reference", "source": "https://media.example/one.png"},
			}},
		},
		{
			name: "portrait size infers ratio and duration defaults",
			body: map[string]any{"kind": "json", "value": map[string]any{"prompt": "portrait", "size": "1080x1920"}},
			want: map[string]any{"model": "H3-KS", "prompt": "portrait", "seconds": float64(6), "resolution": "1080p", "aspect_ratio": "9:16"},
		},
		{
			name: "top level ratio overrides metadata",
			body: map[string]any{"kind": "json", "value": map[string]any{"prompt": "p", "seconds": 15, "ratio": "9:16", "metadata": map[string]any{"aspect_ratio": "1:1"}}},
			want: map[string]any{"model": "H3-KS", "prompt": "p", "seconds": float64(15), "resolution": "1080p", "aspect_ratio": "9:16"},
		},
		{
			name: "multipart JSON references",
			body: map[string]any{"kind": "multipart", "fields": map[string][]string{"prompt": {"p"}, "duration": {"5"}, "ratio": {"16:9"}, "references": {`[{"type":"image","source":"https://media.example/image.png"}]`}}, "files": []any{}},
			want: map[string]any{"model": "H3-KS", "prompt": "p", "seconds": float64(5), "resolution": "1080p", "aspect_ratio": "16:9", "references": []any{map[string]any{"type": "image", "role": "reference", "source": "https://media.example/image.png"}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"model": "H3-KS", "body": tc.body})
			require.NoError(t, err)
			decoded := laoliObject(t, value)
			assert.Equal(t, tc.want, decoded["requestBody"])
			value, err = plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{"model": "H3-KS", "upstreamModel": "H3-KS", "requestBody": decoded["requestBody"], "publicTaskId": "task_stable", "apiKey": "test-key", "baseUrl": "https://upstream.example"})
			require.NoError(t, err)
			descriptor := laoliObject(t, value)
			assert.Equal(t, tc.want, descriptor["body"])
			assert.Equal(t, "https://upstream.example/v1/videos", descriptor["url"])
			assert.Equal(t, map[string]any{"Authorization": "Bearer test-key", "Content-Type": "application/json", "Idempotency-Key": "task_stable"}, descriptor["headers"])
		})
	}
}

func TestLaoliVideoRejectsUnsupportedRequests(t *testing.T) {
	plugin := loadLaoliVideoPlugin(t)
	for _, tc := range []struct {
		name   string
		fields map[string]any
		error  string
	}{
		{"duration below minimum", map[string]any{"seconds": 4}, "integer from 5 to 15"},
		{"duration above maximum", map[string]any{"seconds": 16}, "integer from 5 to 15"},
		{"fractional duration", map[string]any{"seconds": 6.5}, "integer from 5 to 15"},
		{"boolean duration", map[string]any{"seconds": true}, "integer from 5 to 15"},
		{"multiple videos", map[string]any{"n": 2}, "n must be 1"},
		{"conflicting durations", map[string]any{"seconds": 6, "duration": 8}, "seconds and duration must match"},
		{"explicit resolution", map[string]any{"resolution": "720p"}, "only resolution 1080p"},
		{"unsupported ratio", map[string]any{"ratio": "1:1"}, "only aspect_ratio 16:9 or 9:16"},
		{"conflicting ratios", map[string]any{"ratio": "16:9", "aspect_ratio": "9:16"}, "aspect_ratio and ratio must match"},
		{"video reference", map[string]any{"references": []any{map[string]any{"type": "video", "source": "https://media.example/v.mp4"}}}, "only image and audio"},
		{"frame role", map[string]any{"references": []any{map[string]any{"type": "image", "role": "first_frame", "source": "https://media.example/i.png"}}}, "without first/last-frame mode"},
		{"frame mode", map[string]any{"metadata": map[string]any{"mode": "first_last_frame"}}, "does not support first/last-frame mode"},
		{"data URL", map[string]any{"images": []string{"data:image/png;base64,AA=="}}, "data and blob URLs"},
		{"blob URL", map[string]any{"images": []string{"blob:https://canvas.example/image"}}, "data and blob URLs"},
		{"local URL", map[string]any{"images": []string{"http://127.0.0.1/image"}}, "public HTTP or HTTPS"},
		{"image limit", map[string]any{"images": []string{"https://m.example/1", "https://m.example/2", "https://m.example/3", "https://m.example/4", "https://m.example/5", "https://m.example/6", "https://m.example/7"}}, "at most 6 image references"},
		{"audio limit", map[string]any{"audios": []string{"https://m.example/1", "https://m.example/2", "https://m.example/3"}}, "at most 6 image references and 2 audio"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := map[string]any{"prompt": "p"}
			for key, value := range tc.fields {
				request[key] = value
			}
			_, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"model": "H3-KS", "body": map[string]any{"kind": "json", "value": request}})
			require.ErrorContains(t, err, tc.error)
		})
	}
	_, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"model": "H3-KS", "body": map[string]any{"kind": "multipart", "fields": map[string][]string{"prompt": {"p"}}, "files": []any{map[string]any{"ref": "request_file:input_reference", "field": "input_reference"}}}})
	require.ErrorContains(t, err, "requires public URLs")
}

func TestLaoliVideoTaskLifecycleAndFixedBilling(t *testing.T) {
	plugin := loadLaoliVideoPlugin(t)
	const expression = `tier("request", u("video_count") * 4)`
	require.NoError(t, billing_setting.SmokeTestTaskExpr(expression, plugin.Meta.UsageSchema))
	require.ErrorContains(t, billing_setting.SmokeTestTaskExpr(`tier("request", fixed(4))`, plugin.Meta.UsageSchema), "fixed pricing is not supported")
	httpCost, _, err := billingexpr.RunExprWithRequest(`tier("request", fixed(4))`, billingexpr.TokenParams{}, billingexpr.RequestInput{})
	require.NoError(t, err)
	for _, seconds := range []int{5, 15} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader("{}"))
		c.Set("task_request", map[string]any{"model": "H3-KS", "prompt": "p", "seconds": seconds})
		info := &relaycommon.RelayInfo{OriginModelName: "H3-KS", TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_stable"}, ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeTaskPlugin, ChannelBaseUrl: "https://upstream.example", ApiKey: "test-key", UpstreamModelName: "H3-KS"}}
		adaptor := jspluginadaptor.New(plugin)
		adaptor.Init(info)
		require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
		ratios, err := adaptor.EstimateBillingValidated(c, info)
		require.NoError(t, err)
		assert.Equal(t, map[string]float64{"video_count": 1}, ratios, "each request creates one video, independently of duration")
		facts, err := adaptor.ExtractUsageFactsValidated(c, info)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"video_count": float64(1)}, facts)
		cost, _, err := billingexpr.RunExprWithRequest(expression, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: facts})
		require.NoError(t, err)
		assert.Equal(t, httpCost/1_000_000, cost)
		value, err := plugin.Engine.Call(t.Context(), "extractUsageOnComplete", map[string]any{"model": "H3-KS"}, map[string]any{"status": "SUCCESS"}, map[string]any{"seconds": seconds})
		require.NoError(t, err)
		completionFacts := laoliObject(t, value)
		assert.Equal(t, map[string]any{"video_count": float64(1)}, completionFacts)
		settled, _, err := service.EvaluateTaskCompletionUsage(&billingexpr.BillingSnapshot{BillingMode: billing_setting.BillingModeTieredExpr, ModelName: "H3-KS", ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000, ExprVersion: billingexpr.ExprVersion(expression), TaskUsageBilling: true, UsageFacts: facts}, completionFacts)
		require.NoError(t, err)
		assert.Equal(t, 2000000, settled.ActualQuotaAfterGroup, "5s and 15s tasks must both cost 4 before the group ratio")
	}
	value, err := plugin.Engine.Call(t.Context(), "parseSubmitResponse", map[string]any{}, map[string]any{"statusCode": 202, "body": map[string]any{"task_id": "task/upstream", "status": "queued"}})
	require.NoError(t, err)
	assert.Equal(t, "task/upstream", laoliObject(t, value)["taskId"])
	value, err = plugin.Engine.Call(t.Context(), "buildQueryRequest", map[string]any{"baseUrl": "https://upstream.example", "apiKey": "test-key", "taskId": "task/upstream"})
	require.NoError(t, err)
	assert.Equal(t, "https://upstream.example/v1/videos/task%2Fupstream", laoliObject(t, value)["url"])
	for status, want := range map[string]string{"queued": "QUEUED", "in_progress": "IN_PROGRESS", "completed": "SUCCESS", "failed": "FAILURE", "new_status": "UNKNOWN"} {
		value, err = plugin.Engine.Call(t.Context(), "parseTaskResult", nil, map[string]any{"status": status, "progress": 42, "error": map[string]any{"message": "vendor failure"}})
		require.NoError(t, err)
		result := laoliObject(t, value)
		assert.Equal(t, want, result["status"])
		assert.Equal(t, "42%", result["progress"])
		if status == "failed" {
			assert.Equal(t, "vendor failure", result["reason"])
		}
	}
	value, err = plugin.Engine.Call(t.Context(), "buildContentRequest", map[string]any{"baseUrl": "https://upstream.example", "apiKey": "test-key", "upstreamTaskId": "task/upstream", "artifactKey": "video", "clientRequest": map[string]any{"method": "HEAD", "headers": map[string]any{"Range": "bytes=0-99"}}})
	require.NoError(t, err)
	descriptor := laoliObject(t, value)
	assert.Equal(t, "https://upstream.example/v1/videos/task%2Fupstream/content?download=1", descriptor["url"])
	assert.Equal(t, "HEAD", descriptor["method"])
	assert.Equal(t, map[string]any{"Authorization": "Bearer test-key"}, descriptor["headers"])
}

func TestLaoliVideoCatalogProfilesAndPrices(t *testing.T) {
	plugin := loadLaoliVideoPlugin(t)
	data, err := os.ReadFile("tasks/laoli-video/catalog.json")
	require.NoError(t, err)
	var catalog struct {
		Markup map[string]string `json:"markup_rmb"`
		Models []struct {
			Name               string            `json:"name"`
			Unit               string            `json:"unit"`
			Prices             map[string]string `json:"prices_rmb"`
			ProviderExpression string            `json:"provider_expression"`
			Source             struct {
				Price        float64            `json:"model_price"`
				SecondPrices map[string]float64 `json:"video_prices_per_second"`
				Capabilities struct {
					Durations []int `json:"durations"`
				} `json:"full_video_capabilities"`
				VideoCapabilities struct {
					Durations []int `json:"durations"`
				} `json:"video_capabilities"`
			} `json:"source_entry"`
		} `json:"models"`
	}
	require.NoError(t, common.Unmarshal(data, &catalog))
	require.Len(t, catalog.Models, 7)
	names := make([]string, 0, len(catalog.Models))
	for _, item := range catalog.Models {
		names = append(names, item.Name)
		schema, _ := plugin.Meta.UsageForModel(item.Name)
		require.NoError(t, billing_setting.SmokeTestTaskExpr(item.ProviderExpression, schema), item.Name)
		markup, err := strconv.ParseFloat(catalog.Markup[item.Unit], 64)
		require.NoError(t, err)
		durations := item.Source.Capabilities.Durations
		if len(durations) == 0 {
			durations = item.Source.VideoCapabilities.Durations
		}
		if len(durations) == 0 {
			if item.Name == "特价2.0-需要过人脸技术-可参考过人脸素材库" {
				durations = []int{5, 10, 15}
			} else {
				durations = []int{16, 30}
			}
		}
		resolutions := make([]string, 0, len(item.Prices))
		for resolution := range item.Prices {
			resolutions = append(resolutions, resolution)
		}
		sort.Strings(resolutions)
		for _, resolution := range resolutions {
			t.Run(item.Name+"/"+resolution, func(t *testing.T) {
				price, err := strconv.ParseFloat(item.Prices[resolution], 64)
				require.NoError(t, err)
				sourcePrice := item.Source.Price
				if item.Unit == "second" {
					sourcePrice = item.Source.SecondPrices[resolution]
					assert.Contains(t, schema["resolution"].Enum, resolution)
				}
				assert.InDelta(t, sourcePrice+markup, price, 1e-12)
				for _, seconds := range []int{durations[0], durations[len(durations)-1]} {
					value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{
						"model": item.Name, "body": map[string]any{"kind": "json", "value": map[string]any{"prompt": "p", "seconds": seconds, "resolution": resolution, "n": 1}},
					})
					require.NoError(t, err)
					request := laoliObject(t, value)["requestBody"]
					ctx := map[string]any{"model": item.Name, "upstreamModel": item.Name, "requestBody": request, "baseUrl": "https://upstream.example", "apiKey": "test-key", "publicTaskId": "task_stable"}
					_, err = plugin.Engine.Call(t.Context(), "buildSubmitRequest", ctx)
					require.NoError(t, err)
					value, err = plugin.Engine.Call(t.Context(), "extractUsage", ctx)
					require.NoError(t, err)
					facts := laoliObject(t, value)
					wantCost := price
					if item.Unit == "second" {
						assert.Equal(t, map[string]any{"seconds": float64(seconds), "resolution": resolution}, facts)
						wantCost *= float64(seconds)
					} else {
						assert.Equal(t, map[string]any{"video_count": float64(1)}, facts)
					}
					cost, _, err := billingexpr.RunExprWithRequest(item.ProviderExpression, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: facts})
					require.NoError(t, err)
					assert.InDelta(t, wantCost, cost, 1e-12)
					snapshot := &billingexpr.BillingSnapshot{BillingMode: billing_setting.BillingModeTieredExpr, ModelName: item.Name, ExprString: item.ProviderExpression, ExprHash: billingexpr.ExprHashString(item.ProviderExpression), GroupRatio: 1, QuotaPerUnit: 500000, ExprVersion: billingexpr.ExprVersion(item.ProviderExpression), TaskUsageBilling: true, UsageFacts: facts}
					value, err = plugin.Engine.Call(t.Context(), "extractUsageOnComplete", ctx, map[string]any{"status": "SUCCESS"}, map[string]any{"seconds": strconv.Itoa(durations[0]), "resolution": "unexpected-provider-tier"})
					require.NoError(t, err)
					completion := laoliObject(t, value)
					if item.Unit == "second" {
						assert.Equal(t, map[string]any{"seconds": float64(durations[0])}, completion)
						wantCost = price * float64(durations[0])
					} else {
						assert.Equal(t, map[string]any{"video_count": float64(1)}, completion)
					}
					settled, _, err := service.EvaluateTaskCompletionUsage(snapshot, completion)
					require.NoError(t, err)
					assert.Equal(t, common.QuotaRound(wantCost*500000), settled.ActualQuotaAfterGroup)
					if item.Unit == "second" {
						for _, invalid := range []any{nil, true, -1, 0, 4.5, durations[len(durations)-1] + 1, 1e30} {
							value, err = plugin.Engine.Call(t.Context(), "extractUsageOnComplete", ctx, map[string]any{"status": "SUCCESS"}, map[string]any{"seconds": invalid})
							require.NoError(t, err)
							completion = laoliObject(t, value)
							assert.Empty(t, completion)
							settled, _, err = service.EvaluateTaskCompletionUsage(snapshot, completion)
							require.NoError(t, err)
							assert.Equal(t, common.QuotaRound(price*float64(seconds)*500000), settled.ActualQuotaAfterGroup)
						}
					}
				}
				for _, seconds := range []int{durations[0] - 1, durations[len(durations)-1] + 1} {
					_, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"model": item.Name, "body": map[string]any{"kind": "json", "value": map[string]any{"prompt": "p", "seconds": seconds, "resolution": resolution}}})
					require.ErrorContains(t, err, "seconds must be")
				}
			})
		}
	}
	assert.ElementsMatch(t, names, plugin.Meta.Models)
}

func TestLaoliVideoModelSpecificRequests(t *testing.T) {
	plugin := loadLaoliVideoPlugin(t)
	for _, tc := range []struct {
		name  string
		model string
		body  map[string]any
		want  map[string]any
		error string
	}{
		{
			name: "classic inline images remain native and ordered", model: "特价2.0-需要过人脸技术-可参考过人脸素材库",
			body: map[string]any{"prompt": "p", "duration": 10, "images": []string{"data:image/png;base64,AA==", "AA=="}, "face_direct": false},
			want: map[string]any{"prompt": "p", "duration": float64(10), "resolution": "720p", "ratio": "16:9", "images": []any{"data:image/png;base64,AA==", "AA=="}, "face_direct": false},
		},
		{name: "classic discrete duration", model: "特价2.0-需要过人脸技术-可参考过人脸素材库", body: map[string]any{"prompt": "p", "seconds": 6}, error: "one of 5, 10, 15"},
		{name: "classic face feature", model: "特价2.5需要过人脸技术-可参考过人脸素材库", body: map[string]any{"prompt": "p", "face_direct": true}, error: "face_direct is disabled"},
		{
			name: "full model canonical metadata and params", model: "原生不卡人脸-全参2.5",
			body: map[string]any{"prompt": "p", "metadata": map[string]any{"resolution": "720p", "aspect_ratio": "1:1"}, "params": map[string]any{"resolution": "480p", "seconds": 16}, "references": []any{map[string]any{"type": "video", "source": "https://media.example/v.mp4", "duration_seconds": 2}, map[string]any{"type": "audio", "source": "https://media.example/a.wav", "duration_seconds": 3}}},
			want: map[string]any{"prompt": "p", "seconds": float64(16), "resolution": "480p", "aspect_ratio": "1:1", "references": []any{map[string]any{"type": "video", "role": "reference", "source": "https://media.example/v.mp4", "duration_seconds": float64(2)}, map[string]any{"type": "audio", "role": "reference", "source": "https://media.example/a.wav", "duration_seconds": float64(3)}}},
		},
		{
			name: "full 20 explicit frame references", model: "原生不卡人脸-全参2.0",
			body: map[string]any{"prompt": "p", "task_mode": "first_last_frame", "references": []any{map[string]any{"type": "image", "role": "first_frame", "source": "https://media.example/first.png"}, map[string]any{"type": "image", "role": "last_frame", "source": "https://media.example/last.png"}}},
			want: map[string]any{"prompt": "p", "seconds": float64(6), "resolution": "720p", "aspect_ratio": "16:9", "task_mode": "first_last_frame", "references": []any{map[string]any{"type": "image", "role": "first_frame", "source": "https://media.example/first.png"}, map[string]any{"type": "image", "role": "last_frame", "source": "https://media.example/last.png"}}},
		},
		{name: "full 25 rejects frame mode", model: "原生不卡人脸-全参2.5", body: map[string]any{"prompt": "p", "task_mode": "first_frame"}, error: "does not support first/last-frame"},
		{name: "full 25 rejects 1080p", model: "原生不卡人脸-全参2.5", body: map[string]any{"prompt": "p", "resolution": "1080p"}, error: "only resolution 480p, 720p"},
		{name: "full media duration required", model: "原生不卡人脸-全参2.0", body: map[string]any{"prompt": "p", "references": []any{map[string]any{"type": "video", "source": "https://media.example/v.mp4"}}}, error: "duration_seconds must match"},
		{name: "full prompt limit", model: "原生不卡人脸-全参2.0", body: map[string]any{"prompt": strings.Repeat("字", 4001)}, error: "at most 4000 characters"},
		{name: "WAN video forbidden", model: "WAN3.0-不可参考视频", body: map[string]any{"prompt": "p", "videos": []string{"https://media.example/v.mp4"}}, error: "does not support reference videos"},
		{name: "WAN compatibility payload is not silently discarded", model: "WAN3.0-可参考视频", body: map[string]any{"prompt": "p", "metadata": map[string]any{"payload": `{"imageUrls":["https://media.example/image.png"]}`}}, error: "canonical references instead of WAN metadata.payload"},
		{
			name: "WAN video duration is optional", model: "WAN3.0-可参考视频",
			body: map[string]any{"prompt": "p", "references": []any{map[string]any{"type": "video", "source": "https://media.example/v.mp4"}}},
			want: map[string]any{"prompt": "p", "seconds": float64(6), "resolution": "720p", "aspect_ratio": "16:9", "references": []any{map[string]any{"type": "video", "role": "reference", "source": "https://media.example/v.mp4"}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"model": tc.model, "body": map[string]any{"kind": "json", "value": tc.body}})
			if tc.error != "" {
				require.ErrorContains(t, err, tc.error)
				return
			}
			require.NoError(t, err)
			tc.want["model"] = tc.model
			assert.Equal(t, tc.want, laoliObject(t, value)["requestBody"])
		})
	}
	refs := make([]any, 0, 11)
	for range 10 {
		refs = append(refs, map[string]any{"type": "image", "source": "https://media.example/image.png"})
	}
	refs = append(refs, map[string]any{"type": "audio", "source": "https://media.example/audio.wav"})
	_, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"model": "WAN3.0-不可参考视频", "body": map[string]any{"kind": "json", "value": map[string]any{"prompt": "p", "references": refs}}})
	require.ErrorContains(t, err, "10 materials in total")
}
