package controller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/ali"
	"github.com/QuantumNous/new-api/relay/channel/cloudflare"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestChannelTestCustomPromptRequests(t *testing.T) {
	const prompt = "请回答：\"你好\"\n第二行 🐱"
	for _, tc := range []struct {
		endpoint        constant.EndpointType
		path, limitPath string
	}{
		{constant.EndpointTypeOpenAI, "messages.0.content", "max_tokens"},
		{constant.EndpointTypeOpenAIResponse, "input.0.content", "max_output_tokens"},
		{constant.EndpointTypeOpenAIResponseCompact, "input.0.content", ""},
		{constant.EndpointTypeAnthropic, "messages.0.content", "max_tokens"},
		{constant.EndpointTypeGemini, "contents.0.parts.0.text", "generationConfig.maxOutputTokens"},
		{constant.EndpointTypeEmbeddings, "input.0", ""},
		{constant.EndpointTypeImageGeneration, "prompt", ""},
		{constant.EndpointTypeJinaRerank, "query", ""},
	} {
		t.Run(string(tc.endpoint), func(t *testing.T) {
			request := buildTestRequest("fixture-model", string(tc.endpoint), &model.Channel{}, true)
			before, err := common.Marshal(request)
			require.NoError(t, err)
			require.NoError(t, applyChannelTestPrompt(request, " \n\t"))
			after, err := common.Marshal(request)
			require.NoError(t, err)
			assert.JSONEq(t, string(before), string(after), "empty prompts preserve health-check defaults")
			require.NoError(t, applyChannelTestPrompt(request, prompt))
			encoded, err := common.Marshal(request)
			require.NoError(t, err)
			assert.Equal(t, prompt, gjson.GetBytes(encoded, tc.path).String())
			if tc.limitPath != "" {
				assert.EqualValues(t, 4096, gjson.GetBytes(encoded, tc.limitPath).Int())
			}
		})
	}
	request := buildTestRequest("gpt-6-astra", "", &model.Channel{}, false)
	require.NoError(t, applyChannelTestPrompt(request, prompt))
	encoded := convertChatCompatibilityRequest(t, request.(*dto.GeneralOpenAIRequest), constant.ChannelTypeOpenAI, nil)
	assert.Equal(t, prompt, gjson.GetBytes(encoded, "messages.0.content").String())
	assert.EqualValues(t, 4096, gjson.GetBytes(encoded, "max_completion_tokens").Int())
}

func TestChannelTestPromptValidation(t *testing.T) {
	request := buildTestRequest("gpt-4.1", "", &model.Channel{}, false)
	require.NoError(t, applyChannelTestPrompt(request, strings.Repeat("🐱", 16000)))
	require.Error(t, applyChannelTestPrompt(request, strings.Repeat("🐱", 16001)))
	for _, body := range []string{`{"stream":"invalid"}`, `{"prompt":` + strings.Repeat("x", 128<<10), `{"prompt":"` + strings.Repeat("猫", 16001) + `"}`} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/test/1", strings.NewReader(body))
		TestChannel(c)
		assert.Equal(t, http.StatusBadRequest, recorder.Code, "invalid input is rejected before database access")
	}
}

func TestChannelTestResponsePreviews(t *testing.T) {
	for _, tc := range []struct {
		name, body, text, model string
		truncated               bool
	}{
		{"chat JSON", `{"model":"actual-model","choices":[{"message":{"content":"你好"}}]}`, "你好", "actual-model", false},
		{"responses JSON", `{"model":"codex-model","output":[{"type":"reasoning","summary":[{"text":"private"}]},{"type":"message","content":[{"type":"output_text","text":"answer"}]}]}`, "answer", "codex-model", false},
		{"Claude JSON", `{"model":"claude-fixture","content":[{"type":"thinking","thinking":"private"},{"type":"text","text":"answer"}],"stop_reason":"max_tokens"}`, "answer", "claude-fixture", true},
		{"Gemini JSON", `{"modelVersion":"gemini-fixture","candidates":[{"content":{"parts":[{"thought":true,"text":"private"},{"text":"answer"}]}}]}`, "answer", "gemini-fixture", false},
		{"chat SSE", "data: {\"model\":\"actual-model\",\"choices\":[{\"delta\":{\"content\":\"hello \"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"world\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n", "hello world", "actual-model", true},
		{"responses SSE without duplicate final", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\ndata: {\"type\":\"response.output_text.done\",\"text\":\"answer\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"codex-model\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}]}]}}\n\n", "answer", "codex-model", false},
		{"responses complete snapshot restores missing deltas", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ans\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"codex-model\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}]}]}}\n\n", "answer", "codex-model", false},
		{"responses done parts are ordered and replace deltas", "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"a\"}\n\ndata: {\"type\":\"response.output_text.done\",\"output_index\":1,\"content_index\":0,\"text\":\"second\"}\n\ndata: {\"type\":\"response.output_text.done\",\"output_index\":0,\"content_index\":0,\"text\":\"first\"}\n\n", "firstsecond", "", false},
		{"Claude SSE", "event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-fixture\"}}\r\n\r\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"answer\"}}\n\n", "answer", "claude-fixture", false},
		{"Gemini SSE", "data: {\"modelVersion\":\"gemini-fixture\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"a\"}]}}]}\n\ndata: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"b\"}]}}]}\n\n", "ab", "gemini-fixture", false},
		{"partial stream with error", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\ndata: {\"error\":{\"message\":\"interrupted\"}}\n\n", "partial", "", false},
		{"hidden reasoning alone", `{"output":[{"type":"reasoning","encrypted_content":"private"}]}`, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, model, truncated := extractChannelTestResponse([]byte(tc.body), false)
			assert.Equal(t, tc.text, text)
			assert.Equal(t, tc.model, model)
			assert.Equal(t, tc.truncated, truncated)
		})
	}
	text, _, _ := extractChannelTestResponse([]byte(`{"data":[{"embedding":[0.1,0.2]}]}`), false)
	assert.JSONEq(t, `{"data":[{"embedding":[0.1,0.2]}]}`, text)
	recorder := httptest.NewRecorder()
	recorder.Body = nil
	c, _ := gin.CreateTestContext(recorder)
	writer := &channelTestResponseWriter{ResponseWriter: c.Writer}
	_, err := writer.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"retained\"}}]}\n\n")
	require.NoError(t, err)
	_, err = writer.Write([]byte(strings.Repeat(" ", channelTestResponseLimit+1)))
	require.NoError(t, err)
	text, _, truncated := extractChannelTestResponse(writer.body.Bytes(), writer.truncated)
	assert.Equal(t, "retained", text)
	assert.True(t, truncated)
	assert.LessOrEqual(t, writer.body.Len(), channelTestResponseLimit)
	body, err := common.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": strings.Repeat("猫", channelTestTextLimit)}}}})
	require.NoError(t, err)
	text, _, truncated = extractChannelTestResponse(body, false)
	assert.True(t, truncated)
	assert.True(t, utf8.ValidString(text))
	assert.LessOrEqual(t, len(text), channelTestTextLimit)
}

func TestChannelTestPromptReachesUpstream(t *testing.T) {
	oldDB, oldLogDB, oldLogEnabled := model.DB, model.LOG_DB, common.LogConsumeEnabled
	t.Cleanup(func() { model.DB, model.LOG_DB, common.LogConsumeEnabled = oldDB, oldLogDB, oldLogEnabled })
	db := setupModelListControllerTestDB(t)
	common.LogConsumeEnabled = false
	oldRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatios)) })
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gpt-4.1":1}`))
	user := model.User{Id: 88001, Username: "channel-prompt-test", Group: "default"}
	require.NoError(t, db.Create(&user).Error)
	service.InitHttpClient()
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat-fixture","model":"upstream-model","choices":[{"index":0,"message":{"role":"assistant","content":"fixture answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`))
	}))
	defer server.Close()
	channel := &model.Channel{Id: 88001, Type: constant.ChannelTypeOpenAI, Key: "fixture-key", BaseURL: &server.URL, Models: "gpt-4.1", Group: "default"}
	result := testChannelWithPrompt(context.Background(), channel, user.Id, "gpt-4.1", "", false, "用户的自定义问题", true)
	require.NoError(t, result.localErr)
	assert.Equal(t, "用户的自定义问题", gjson.GetBytes(received, "messages.0.content").String())
	assert.EqualValues(t, 4096, gjson.GetBytes(received, "max_tokens").Int())
	assert.Equal(t, "fixture answer", result.responseText)
	assert.Equal(t, "upstream-model", result.responseModel)
}

func convertChatCompatibilityRequest(t *testing.T, request *dto.GeneralOpenAIRequest, channelType int, mapping map[string]string) []byte {
	t.Helper()
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	settings := model_setting.GetGlobalSettings()
	oldPassThrough, oldBlacklist := settings.PassThroughRequestEnabled, settings.ThinkingModelBlacklist
	oldEffortTailModels := settings.EffortTailModelIDs
	settings.PassThroughRequestEnabled = false
	settings.ThinkingModelBlacklist = nil
	settings.EffortTailModelIDs = []string{"gpt-5.1-codex-max"}
	t.Cleanup(func() {
		settings.PassThroughRequestEnabled = oldPassThrough
		settings.ThinkingModelBlacklist = oldBlacklist
		settings.EffortTailModelIDs = oldEffortTailModels
	})

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	if mapping != nil {
		encoded, err := common.Marshal(mapping)
		require.NoError(t, err)
		c.Set("model_mapping", string(encoded))
	}
	info := &relaycommon.RelayInfo{
		OriginModelName: request.Model,
		Request:         request,
		RelayFormat:     types.RelayFormatOpenAI,
		IsStream:        request.IsStream(nil),
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:          channelType,
			UpstreamModelName:    request.Model,
			SupportStreamOptions: true,
		},
	}
	require.NoError(t, helper.ModelMappedHelper(c, info, request))
	require.NoError(t, helper.ApplyReasoningModelSuffix(c, info, request))
	var converted any
	var err error
	if channelType == constant.ChannelTypeAli {
		converted, err = (&ali.Adaptor{}).ConvertOpenAIRequest(c, info, request)
	} else {
		converted, err = (&openai.Adaptor{}).ConvertOpenAIRequest(c, info, request)
	}
	require.NoError(t, err)
	encoded, err := common.Marshal(converted)
	require.NoError(t, err)
	return encoded
}

func TestChannelTestOpenAIChatCompatibility(t *testing.T) {
	for _, tt := range []struct {
		name        string
		model       string
		upstream    string
		endpoint    string
		channelType int
		stream      bool
		wantLimit   string
	}{
		{name: "GPT6 automatic", model: "gpt-6-astra", upstream: "gpt-6-astra", channelType: constant.ChannelTypeOpenAI, wantLimit: "max_completion_tokens"},
		{name: "GPT6 explicit Azure stream", model: "gpt-6-astra", upstream: "gpt-6-astra", endpoint: string(constant.EndpointTypeOpenAI), channelType: constant.ChannelTypeAzure, stream: true, wantLimit: "max_completion_tokens"},
		{name: "GPT6 sol", model: "gpt-6-sol", upstream: "gpt-6-sol", channelType: constant.ChannelTypeOpenAI, wantLimit: "max_completion_tokens"},
		{name: "GPT6 luna", model: "gpt-6-luna", upstream: "gpt-6-luna", channelType: constant.ChannelTypeOpenAI, wantLimit: "max_completion_tokens"},
		{name: "GPT6.1 sol", model: "gpt-6.1-sol", upstream: "gpt-6.1-sol", channelType: constant.ChannelTypeOpenAI, wantLimit: "max_completion_tokens"},
		{name: "GPT6.1 terra", model: "gpt-6.1-terra", upstream: "gpt-6.1-terra", channelType: constant.ChannelTypeOpenAI, wantLimit: "max_completion_tokens"},
		{name: "alias maps to GPT6", model: "customer-model", upstream: "gpt-6-astra", channelType: constant.ChannelTypeOpenAI, wantLimit: "max_completion_tokens"},
		{name: "GPT5 alias maps to Qwen", model: "gpt-5.6-luna", upstream: "qwen-turbo", channelType: constant.ChannelTypeAli, wantLimit: "max_tokens"},
		{name: "GPT5 stream", model: "gpt-5.6-luna", upstream: "gpt-5.6-luna", channelType: constant.ChannelTypeOpenAI, stream: true, wantLimit: "max_completion_tokens"},
		{name: "GPT4 explicit", model: "gpt-4.1", upstream: "gpt-4.1", endpoint: string(constant.EndpointTypeOpenAI), channelType: constant.ChannelTypeOpenAI, wantLimit: "max_tokens"},
		{name: "o series", model: "o3-mini", upstream: "o3-mini", channelType: constant.ChannelTypeAzure, wantLimit: "max_completion_tokens"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request, ok := buildTestRequest(tt.model, tt.endpoint, &model.Channel{}, tt.stream).(*dto.GeneralOpenAIRequest)
			require.True(t, ok)
			encoded := convertChatCompatibilityRequest(t, request, tt.channelType, map[string]string{tt.model: tt.upstream})
			want := map[string]any{
				"model":      tt.upstream,
				"messages":   []dto.Message{{Role: "user", Content: "hi"}},
				"stream":     tt.stream,
				tt.wantLimit: 16,
			}
			if tt.stream {
				want["stream_options"] = map[string]any{"include_usage": true}
			}
			wantJSON, err := common.Marshal(want)
			require.NoError(t, err)
			assert.JSONEq(t, string(wantJSON), string(encoded))
		})
	}
}

func TestOpenAIChatSamplingCompatibility(t *testing.T) {
	const sampling = `{"temperature":0.2,"top_p":0.8,"logprobs":true,"top_logprobs":5}`
	for _, tt := range []struct {
		name       string
		model      string
		effort     string
		reasoning  string
		mapping    map[string]string
		zeroValues bool
		wantModel  string
		wantEffort string
		wantRole   string
		wantParams string
	}{
		{name: "GPT5.1 explicit none", model: "gpt-5.1", effort: "none", wantEffort: "none", wantRole: "developer", wantParams: sampling},
		{name: "GPT5.2 default none", model: "gpt-5.2", wantRole: "developer", wantParams: sampling},
		{name: "GPT5.2 dated snapshot", model: "gpt-5.2-2025-12-11", wantRole: "developer", wantParams: sampling},
		{name: "GPT5.4 reasoning", model: "gpt-5.4", effort: "high", wantEffort: "high", wantRole: "developer", wantParams: `{}`},
		{name: "GPT5.4 snapshot none", model: "gpt-5.4-2026-03-05", effort: "none", wantEffort: "none", wantRole: "developer", wantParams: sampling},
		{name: "explicit zero values", model: "gpt-5.4", effort: "none", zeroValues: true, wantEffort: "none", wantRole: "developer", wantParams: `{"temperature":0,"top_p":0,"logprobs":false}`},
		{name: "GPT5 original", model: "gpt-5", wantRole: "developer", wantParams: `{}`},
		{name: "GPT5.6 existing policy", model: "gpt-5.6-luna", wantRole: "developer", wantParams: `{}`},
		{name: "pro variant", model: "gpt-5.2-pro-2025-12-11", wantRole: "developer", wantParams: `{}`},
		{name: "chat variant", model: "gpt-5.2-chat-latest", wantRole: "developer", wantParams: `{}`},
		{name: "codex model name keeps max", model: "gpt-5.1-codex-max", wantRole: "developer", wantParams: `{}`},
		{name: "GPT6", model: "gpt-6-astra", wantRole: "developer", wantParams: `{}`},
		{name: "GPT6 snapshot", model: "gpt-6-astra-2026-09-03", wantRole: "developer", wantParams: `{}`},
		{name: "GPT6 effort suffix", model: "gpt-6-astra-high", wantModel: "gpt-6-astra", wantEffort: "high", wantRole: "developer", wantParams: `{}`},
		{name: "GPT6 sol none keeps sampling", model: "gpt-6-sol", wantRole: "developer", wantParams: sampling},
		{name: "GPT6 luna effort drops sampling", model: "gpt-6-luna-high", wantModel: "gpt-6-luna", wantEffort: "high", wantRole: "developer", wantParams: `{}`},
		{name: "GPT6.1 sol drops sampling", model: "gpt-6.1-sol", wantRole: "developer", wantParams: `{}`},
		{name: "GPT6.1 terra default drops sampling", model: "gpt-6.1-terra", wantRole: "developer", wantParams: `{}`},
		{name: "GPT6.1 terra none keeps sampling", model: "gpt-6.1-terra", effort: "none", wantEffort: "none", wantRole: "developer", wantParams: sampling},
		{name: "none effort suffix", model: "gpt-5.2-none", wantModel: "gpt-5.2", wantEffort: "none", wantRole: "developer", wantParams: sampling},
		{name: "modifier overrides explicit effort", model: "gpt-5.2@thinking:off", effort: "high", wantModel: "gpt-5.2", wantEffort: "none", wantRole: "developer", wantParams: sampling},
		{name: "mapped modifier wins", model: "customer-model@thinking:off", mapping: map[string]string{"customer-model": "gpt-5.2@effort:high"}, wantModel: "gpt-5.2", wantEffort: "high", wantRole: "developer", wantParams: `{}`},
		{name: "nested reasoning disabled", model: "gpt-5.2", reasoning: `{"enabled":false}`, wantEffort: "none", wantRole: "developer", wantParams: sampling},
		{name: "o1 mini role exception", model: "o1-mini", wantRole: "system", wantParams: `{"top_p":0.8,"logprobs":true,"top_logprobs":5}`},
		{name: "GPT4 unchanged", model: "gpt-4.1", wantRole: "system", wantParams: sampling},
		{name: "future model unchanged", model: "gpt-7", wantRole: "system", wantParams: sampling},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := &dto.GeneralOpenAIRequest{
				Model: tt.model,
				Messages: []dto.Message{
					{Role: "system", Content: "first instruction"},
					{Role: "system", Content: "second instruction"},
					{Role: "user", Content: "hi"},
				},
				ReasoningEffort: tt.effort,
			}
			require.NoError(t, common.UnmarshalJsonStr(sampling, request))
			if tt.reasoning != "" {
				request.Reasoning = []byte(tt.reasoning)
			}
			if tt.zeroValues {
				request.Temperature = lo.ToPtr(0.0)
				request.TopP = lo.ToPtr(0.0)
				request.LogProbs = lo.ToPtr(false)
				request.TopLogProbs = nil
			}
			encoded := convertChatCompatibilityRequest(t, request, constant.ChannelTypeOpenAI, tt.mapping)
			var want map[string]any
			require.NoError(t, common.UnmarshalJsonStr(tt.wantParams, &want))
			want["model"] = tt.model
			if tt.wantModel != "" {
				want["model"] = tt.wantModel
			}
			want["messages"] = []dto.Message{
				{Role: tt.wantRole, Content: "first instruction"},
				{Role: "system", Content: "second instruction"},
				{Role: "user", Content: "hi"},
			}
			if tt.wantEffort != "" {
				want["reasoning_effort"] = tt.wantEffort
			}
			wantJSON, err := common.Marshal(want)
			require.NoError(t, err)
			assert.JSONEq(t, string(wantJSON), string(encoded))
		})
	}
}

func TestOpenAIChatTokenLimitCompatibility(t *testing.T) {
	for _, modelName := range []string{"gpt-5", "o3-mini", "gpt-6-astra"} {
		for _, tt := range []struct {
			name  string
			input string
			want  string
		}{
			{name: "omitted", input: `{}`, want: `{}`},
			{name: "legacy only", input: `{"max_tokens":100}`, want: `{"max_completion_tokens":100}`},
			{name: "completion only", input: `{"max_completion_tokens":50}`, want: `{"max_completion_tokens":50}`},
			{name: "both positive stay present", input: `{"max_tokens":100,"max_completion_tokens":50}`, want: `{"max_tokens":100,"max_completion_tokens":50}`},
			{name: "zero completion falls back", input: `{"max_tokens":100,"max_completion_tokens":0}`, want: `{"max_completion_tokens":100}`},
			{name: "legacy zero stays present", input: `{"max_tokens":0}`, want: `{"max_tokens":0}`},
			{name: "completion zero stays present", input: `{"max_completion_tokens":0}`, want: `{"max_completion_tokens":0}`},
			{name: "both zero stay present", input: `{"max_tokens":0,"max_completion_tokens":0}`, want: `{"max_tokens":0,"max_completion_tokens":0}`},
		} {
			t.Run(modelName+"/"+tt.name, func(t *testing.T) {
				request := &dto.GeneralOpenAIRequest{Model: modelName, Messages: []dto.Message{{Role: "user", Content: "hi"}}}
				require.NoError(t, common.UnmarshalJsonStr(tt.input, request))
				encoded := convertChatCompatibilityRequest(t, request, constant.ChannelTypeOpenAI, nil)
				want := dto.GeneralOpenAIRequest{Model: modelName, Messages: []dto.Message{{Role: "user", Content: "hi"}}}
				require.NoError(t, common.UnmarshalJsonStr(tt.want, &want))
				wantJSON, err := common.Marshal(want)
				require.NoError(t, err)
				assert.JSONEq(t, string(wantJSON), string(encoded))
			})
		}
	}
}

func TestDirectOpenAIResponsesKeepsExistingParameters(t *testing.T) {
	const body = `{"model":"gpt-6-astra","input":"hi","max_output_tokens":100,"temperature":0.2,"top_p":0.8,"top_logprobs":5,"include":["message.output_text.logprobs"],"reasoning":{"effort":"high"}}`
	var request dto.OpenAIResponsesRequest
	require.NoError(t, common.UnmarshalJsonStr(body, &request))
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-6-astra",
		RelayFormat:     types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
			UpstreamModelName: "gpt-6-astra",
		},
	}
	converted, err := (&openai.Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)
	require.NoError(t, err)
	encoded, err := common.Marshal(converted)
	require.NoError(t, err)
	assert.JSONEq(t, body, string(encoded))
}

func TestCloudflareEmbeddingResponseCompatibility(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		forceFormat bool
		wantTokens  int
	}{
		{
			name:       "single embedding preserves upstream usage",
			body:       `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,-0.2]}],"model":"@cf/baai/bge-large-en-v1.5","usage":{"prompt_tokens":8,"total_tokens":8}}`,
			wantTokens: 8,
		},
		{
			name:        "batch embeddings survive force format",
			body:        `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,-0.2]},{"object":"embedding","index":1,"embedding":[0.3,0.4]}],"model":"@cf/baai/bge-large-en-v1.5","usage":{"prompt_tokens":16,"total_tokens":16},"extra":"preserved"}`,
			forceFormat: true,
			wantTokens:  16,
		},
		{
			name:       "base64 embedding is forwarded unchanged",
			body:       `{"object":"list","data":[{"object":"embedding","index":0,"embedding":"zczMPc3MTL4="}],"model":"@cf/baai/bge-large-en-v1.5","usage":{"prompt_tokens":5,"total_tokens":5}}`,
			wantTokens: 5,
		},
		{
			name:        "missing usage falls back to input estimate",
			body:        `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,-0.2]}],"model":"@cf/baai/bge-large-en-v1.5"}`,
			forceFormat: true,
			wantTokens:  99,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/embeddings", nil)
			info := &relaycommon.RelayInfo{
				RelayMode:   relayconstant.RelayModeEmbeddings,
				RelayFormat: types.RelayFormatEmbedding,
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelType:       constant.ChannelCloudflare,
					UpstreamModelName: "@cf/baai/bge-large-en-v1.5",
					ChannelSetting:    dto.ChannelSettings{ForceFormat: tt.forceFormat},
				},
			}
			info.SetEstimatePromptTokens(99)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}

			usage, apiErr := (&cloudflare.Adaptor{}).DoResponse(c, resp, info)
			require.Nil(t, apiErr)
			require.IsType(t, &dto.Usage{}, usage)
			assert.Equal(t, &dto.Usage{PromptTokens: tt.wantTokens, TotalTokens: tt.wantTokens}, usage)
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
			assert.JSONEq(t, tt.body, w.Body.String())
		})
	}
}
