package sub2api

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/newapi"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetRequestURLAlphaSearch(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeSub2API,
			ChannelBaseUrl: "https://sub2api.example",
		},
		RequestURLPath: "/v1/alpha/search",
		RelayMode:      relayconstant.RelayModeAlphaSearch,
	}

	url, err := adaptor.GetRequestURL(info)
	require.NoError(t, err)
	assert.Equal(t, "https://sub2api.example/v1/alpha/search", url)
}

func TestAdaptorInheritsNewAPIResponsesCompactSupport(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeSub2API,
			ChannelBaseUrl: "https://sub2api.example",
		},
		RequestURLPath: "/v1/responses/compact",
		RelayMode:      relayconstant.RelayModeResponsesCompact,
	}

	url, err := adaptor.GetRequestURL(info)

	require.NoError(t, err)
	assert.Equal(t, "https://sub2api.example/v1/responses/compact", url)
	assert.Equal(t, "sub2api", adaptor.GetChannelName())
	assert.Empty(t, adaptor.GetModelList())
}

func TestConvertClaudeRequestPreservesAdaptiveThinkingForCompatibleModel(t *testing.T) {
	adaptor := &Adaptor{}
	maxTokens := uint(8192)
	temperature := 0.2
	topP := 0.99
	request := &dto.ClaudeRequest{
		Model:        "gpt-5.6-sol",
		MaxTokens:    &maxTokens,
		Temperature:  &temperature,
		TopP:         &topP,
		Thinking:     &dto.Thinking{Type: "adaptive", Display: "summarized"},
		OutputConfig: json.RawMessage(`{"effort":"xhigh","provider_option":true}`),
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
	}
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.6-sol",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeSub2API,
		},
	}

	converted, err := adaptor.ConvertClaudeRequest(nil, info, request)

	require.NoError(t, err)
	assert.Same(t, request, converted)
	require.NotNil(t, request.Thinking)
	assert.Equal(t, "adaptive", request.Thinking.Type)
	assert.Equal(t, "summarized", request.Thinking.Display)
	assert.JSONEq(t, `{"effort":"xhigh","provider_option":true}`, string(request.OutputConfig))
	assert.Same(t, &temperature, request.Temperature)
	assert.Same(t, &topP, request.TopP)
	assert.Equal(t, "xhigh", info.ReasoningEffort)
	assert.Equal(t, "gpt-5.6-sol", info.UpstreamModelName)
}

const gatewayChatRequestBody = `{"model":"fixture-model","messages":[{"role":"user","content":"hello"}],"prompt_cache_key":"cache/Original:AbC_42","user":"user-Original@fixture"}`

type observedGatewayChatRequest struct {
	headers http.Header
	path    string
	body    []byte
	err     error
}

func newGatewayChatFixture(t *testing.T) (string, map[string]string, <-chan observedGatewayChatRequest) {
	t.Helper()
	service.InitHttpClient()
	t.Cleanup(service.GetHttpClient().CloseIdleConnections)

	sessionHeaders := map[string]string{
		"Session-Id":   "session/Original:AbC_01+=",
		"Session_id":   "session_Original:DeF_02+=",
		"X-Session-Id": "x-session/Original:GhI_03+=",
		"Thread-Id":    "thread/Original:JkL_04+=",
		"Thread_id":    "thread_Original:MnO_05+=",
		"X-Thread-Id":  "x-thread/Original:PqR_06+=",
	}
	observed := make(chan observedGatewayChatRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		observed <- observedGatewayChatRequest{headers: r.Header.Clone(), path: r.URL.Path, body: body, err: err}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	return upstream.URL, sessionHeaders, observed
}

func TestGatewayChatSessionHeadersOutbound(t *testing.T) {
	upstreamURL, sessionHeaders, observed := newGatewayChatFixture(t)
	partialHeaders := map[string]string{"Session-Id": sessionHeaders["Session-Id"]}
	deletedHeaders := maps.Clone(sessionHeaders)
	delete(deletedHeaders, "Session-Id")
	overriddenHeaders := map[string]string{
		"Session-Id": "channel-session", "Session_id": "", "X-Session-Id": "channel-x-session",
		"Thread-Id": "", "Thread_id": "channel-thread", "X-Thread-Id": "",
	}
	overrides := make(map[string]any, len(overriddenHeaders))
	for name, value := range overriddenHeaders {
		overrides[strings.ToUpper(name)] = value
	}
	blockedHeaders := map[string]string{
		"Cookie": "fixture_session=client-cookie", "X-Api-Key": "client-api-key",
		"X-Goog-Api-Key": "client-google-key", "Proxy-Authorization": "Basic client-proxy-key",
		"X-Unrelated": "private-client-metadata", "Session-Id-Extra": "not-allowlisted",
		"Thread-Id-Extra": "also-not-allowlisted",
	}
	cases := []struct {
		name, clientPath, upstreamPath string
		format                         types.RelayFormat
		clientHeaders, wantHeaders     map[string]string
		overrides, paramOverride       map[string]any
		channelTest                    bool
	}{
		{name: "chat", clientHeaders: sessionHeaders, wantHeaders: sessionHeaders},
		{name: "chat converted to Responses", upstreamPath: "/v1/responses", clientHeaders: sessionHeaders, wantHeaders: sessionHeaders},
		{name: "missing headers"},
		{name: "partial headers do not create aliases", clientHeaders: partialHeaders, wantHeaders: partialHeaders},
		{name: "channel overrides and clears win", clientHeaders: sessionHeaders, wantHeaders: overriddenHeaders, overrides: overrides},
		{name: "native Responses", clientPath: "/v1/responses", format: types.RelayFormatOpenAIResponses, clientHeaders: sessionHeaders},
		{name: "delete_header removes a default session header", clientHeaders: sessionHeaders, wantHeaders: deletedHeaders,
			paramOverride: map[string]any{"operations": []any{map[string]any{"mode": "delete_header", "path": "Session-Id"}}}},
		{name: "non-Chat OpenAI", clientPath: "/v1/embeddings", clientHeaders: sessionHeaders},
		{name: "non-OpenAI format", format: types.RelayFormatClaude, clientHeaders: sessionHeaders},
		{name: "channel test", clientHeaders: sessionHeaders, channelTest: true},
	}

	for _, provider := range []struct {
		name        string
		channelType int
		adaptor     channel.Adaptor
	}{
		{name: "NewAPI", channelType: constant.ChannelTypeNewAPI, adaptor: &newapi.Adaptor{}},
		{name: "Sub2API", channelType: constant.ChannelTypeSub2API, adaptor: &Adaptor{}},
		{name: "ordinary OpenAI", channelType: constant.ChannelTypeOpenAI, adaptor: &openai.Adaptor{}},
	} {
		providerCases := cases
		if provider.channelType == constant.ChannelTypeOpenAI {
			providerCases = cases[:1]
		}
		for _, tc := range providerCases {
			t.Run(provider.name+"/"+tc.name, func(t *testing.T) {
				clientPath := tc.clientPath
				if clientPath == "" {
					clientPath = "/v1/chat/completions"
				}
				upstreamPath := tc.upstreamPath
				if upstreamPath == "" {
					upstreamPath = clientPath
				}
				format := tc.format
				if format == "" {
					format = types.RelayFormatOpenAI
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, clientPath, strings.NewReader(gatewayChatRequestBody))
				c.Request.Header.Set("Content-Type", "application/json")
				c.Request.Header.Set("Authorization", "Bearer client-token")
				for name, value := range blockedHeaders {
					c.Request.Header.Set(name, value)
				}
				for name, value := range tc.clientHeaders {
					c.Request.Header.Set(name, value)
				}
				sourceHeaders := map[string]any{}
				maps.Copy(sourceHeaders, tc.overrides)
				originalHeaders := maps.Clone(sourceHeaders)
				info := &relaycommon.RelayInfo{
					ChannelMeta: &relaycommon.ChannelMeta{
						ChannelType: provider.channelType, ChannelBaseUrl: upstreamURL,
						ApiKey: "channel-key", HeadersOverride: sourceHeaders, ParamOverride: tc.paramOverride,
					},
					RelayFormat: format, RelayMode: relayconstant.Path2RelayMode(clientPath),
					RequestURLPath: clientPath, IsChannelTest: tc.channelTest,
					RequestHeaders: map[string]string{},
				}
				for name := range c.Request.Header {
					info.RequestHeaders[name] = c.Request.Header.Get(name)
				}
				provider.adaptor.Init(info)
				// TextHelper initializes the adaptor before Chat can switch to Responses.
				info.RelayMode = relayconstant.Path2RelayMode(upstreamPath)
				info.RequestURLPath = upstreamPath
				outboundBody, err := relaycommon.ApplyParamOverrideWithRelayInfo([]byte(gatewayChatRequestBody), info)
				require.NoError(t, err)
				// No GetBody or tier policy: exercise HTTP transport without billing preparation.
				resp, err := channel.DoApiRequest(provider.adaptor, c, info, io.NopCloser(strings.NewReader(string(outboundBody))))
				require.NoError(t, err)
				require.NotNil(t, resp)
				require.NoError(t, resp.Body.Close())
				assert.Equal(t, http.StatusNoContent, resp.StatusCode)
				got := <-observed
				require.NoError(t, got.err)
				assert.Equal(t, upstreamPath, got.path)
				assert.Equal(t, gatewayChatRequestBody, string(got.body), "prompt_cache_key, user and the rest of the body stay unchanged")
				assert.Equal(t, originalHeaders, sourceHeaders, "the channel's original header configuration must stay unchanged")
				assert.Equal(t, []string{"Bearer channel-key"}, got.headers.Values("Authorization"))
				for name := range blockedHeaders {
					if name == "X-Api-Key" && format == types.RelayFormatClaude {
						assert.Equal(t, []string{"channel-key"}, got.headers.Values(name))
					} else {
						assert.NotContains(t, got.headers, http.CanonicalHeaderKey(name))
					}
				}
				wantHeaders := tc.wantHeaders
				if provider.channelType == constant.ChannelTypeOpenAI {
					wantHeaders = nil
				}
				for name := range sessionHeaders {
					value, present := wantHeaders[name]
					if !present {
						assert.NotContains(t, got.headers, http.CanonicalHeaderKey(name))
					} else {
						assert.Equal(t, value, got.headers.Get(name), name)
					}
				}
			})
		}
	}
}

func TestGatewayChatSessionHeadersAcrossChannelRetries(t *testing.T) {
	upstreamURL, sessionHeaders, observed := newGatewayChatFixture(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(gatewayChatRequestBody))
	c.Request.Header.Set("Content-Type", "application/json")
	for name, value := range sessionHeaders {
		c.Request.Header.Set(name, value)
	}
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions,
		RequestURLPath: "/v1/chat/completions", OriginModelName: "fixture-model",
		RequestHeaders: maps.Clone(sessionHeaders),
	}
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstreamURL)
	common.SetContextKey(c, constant.ContextKeyChannelKey, "channel-key")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "fixture-model")
	gatewayHeaders := maps.Clone(sessionHeaders)
	gatewayHeaders["Session_id"] = "gateway-config-session"

	for _, attempt := range []struct {
		name                           string
		channelType                    int
		adaptor                        channel.Adaptor
		headerOverrides, paramOverride map[string]any
		wantHeaders                    map[string]string
		wantPreviousHeader             string
	}{
		{name: "OpenAI with runtime headers", channelType: constant.ChannelTypeOpenAI, adaptor: &openai.Adaptor{},
			paramOverride: map[string]any{"operations": []any{
				map[string]any{"mode": "set_header", "path": "Session-Id", "value": "previous-runtime-session"},
				map[string]any{"mode": "set_header", "path": "X-Previous-Attempt", "value": "previous-channel"},
			}},
			wantHeaders: map[string]string{"Session-Id": "previous-runtime-session"}, wantPreviousHeader: "previous-channel"},
		{name: "Sub2API restores current headers", channelType: constant.ChannelTypeSub2API, adaptor: &Adaptor{},
			headerOverrides: map[string]any{"SESSION_ID": "gateway-config-session"}, wantHeaders: gatewayHeaders},
		{name: "OpenAI receives no inherited headers", channelType: constant.ChannelTypeOpenAI, adaptor: &openai.Adaptor{}},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			common.SetContextKey(c, constant.ContextKeyChannelType, attempt.channelType)
			common.SetContextKey(c, constant.ContextKeyChannelHeaderOverride, attempt.headerOverrides)
			common.SetContextKey(c, constant.ContextKeyChannelParamOverride, attempt.paramOverride)
			info.InitChannelMeta(c)
			attempt.adaptor.Init(info)
			outboundBody, err := relaycommon.ApplyParamOverrideWithRelayInfo([]byte(gatewayChatRequestBody), info)
			require.NoError(t, err)
			resp, err := channel.DoApiRequest(attempt.adaptor, c, info, io.NopCloser(strings.NewReader(string(outboundBody))))
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.NoError(t, resp.Body.Close())
			assert.Equal(t, http.StatusNoContent, resp.StatusCode)
			got := <-observed
			require.NoError(t, got.err)
			assert.Equal(t, gatewayChatRequestBody, string(got.body))
			assert.Equal(t, []string{"Bearer channel-key"}, got.headers.Values("Authorization"))
			assert.Equal(t, attempt.wantPreviousHeader, got.headers.Get("X-Previous-Attempt"))
			for name := range sessionHeaders {
				if value, present := attempt.wantHeaders[name]; present {
					assert.Equal(t, []string{value}, got.headers.Values(name), name)
				} else {
					assert.NotContains(t, got.headers, http.CanonicalHeaderKey(name))
				}
			}
		})
	}
}
