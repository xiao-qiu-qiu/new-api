package service

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/tokenkit"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDistillationUsageRequiresReportedOutputAndCache(t *testing.T) {
	const responsesWithZero = `{"input_tokens":8000,"output_tokens":4200,"input_tokens_details":{"cached_tokens":0}}`
	const responsesWithoutCache = `{"input_tokens":8000,"output_tokens":4200,"input_tokens_details":{"text_tokens":8000}}`
	for _, tc := range []struct {
		name            string
		raw             string
		billing         *dto.BillingUsage
		nativeZeroCache bool
		local           bool
		wantOutput      int
		wantCache       int
	}{
		{name: "explicit zero in Responses", raw: responsesWithZero, wantOutput: 4200},
		{name: "cache missing from Responses details", raw: responsesWithoutCache},
		{name: "explicit zero in Chat", raw: `{"prompt_tokens":8000,"completion_tokens":4200,"prompt_tokens_details":{"cached_tokens":0}}`, wantOutput: 4200},
		{name: "nonzero legacy cache", raw: `{"prompt_tokens":8000,"completion_tokens":4200,"prompt_cache_hit_tokens":1024}`, wantOutput: 4200, wantCache: 1024},
		{name: "locally estimated usage cannot hit", raw: responsesWithZero, local: true},
		{name: "cache not an output measurement", raw: `{"input_tokens":8000,"input_tokens_details":{"cached_tokens":0}}`},
		{name: "malformed negative cache", raw: `{"input_tokens":8000,"output_tokens":4200,"input_tokens_details":{"cached_tokens":-1}}`},
		{name: "unmeasured output is not recovered from the total", billing: dto.NewGeminiChatBillingUsage(&dto.GeminiUsageMetadata{TotalTokenCount: 9000, PromptTokenCount: 2000}), nativeZeroCache: true},
		{name: "Gemini candidates plus thoughts", billing: dto.NewGeminiChatBillingUsage(&dto.GeminiUsageMetadata{CandidatesTokenCount: 3200, ThoughtsTokenCount: 1000}), nativeZeroCache: true, wantOutput: 4200},
		{name: "Gemini cache omitted", billing: dto.NewGeminiChatBillingUsage(&dto.GeminiUsageMetadata{CandidatesTokenCount: 4200})},
		{name: "Gemini token arithmetic overflow", billing: dto.NewGeminiChatBillingUsage(&dto.GeminiUsageMetadata{CandidatesTokenCount: math.MaxInt, ThoughtsTokenCount: 100}), nativeZeroCache: true},
		{name: "Claude explicit zero", billing: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{OutputTokens: 4200}), nativeZeroCache: true, wantOutput: 4200},
		{name: "Claude cache read excludes creation", billing: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{OutputTokens: 4200, CacheCreationInputTokens: 500, CacheReadInputTokens: 20}), wantOutput: 4200, wantCache: 20},
		{name: "Claude cache omitted", billing: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{OutputTokens: 4200})},
		{name: "provider sidecar marks usage estimated", raw: responsesWithZero, billing: dto.NewEstimatedGeminiChatBillingUsage(&dto.Usage{CompletionTokens: 4200}), nativeZeroCache: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var usage dto.Usage
			if tc.raw != "" {
				require.NoError(t, common.UnmarshalJsonStr(tc.raw, &usage))
			}
			usage.BillingUsage = tc.billing
			output, cached, known := measuredGenerationUsage(&usage, tc.nativeZeroCache, tc.local)
			require.Equal(t, tc.wantOutput > 0, known)
			if known {
				assert.Equal(t, tc.wantOutput, output)
				assert.Equal(t, tc.wantCache, cached)
			}
		})
	}
}

func TestDistillationStreamRequiresSuccessfulCompletion(t *testing.T) {
	for _, tc := range []struct {
		name            string
		end             relaycommon.StreamEndReason
		terminal        bool
		policyCompleted bool
		failure         bool
		want            bool
	}{
		{name: "delivered protocol terminal", end: relaycommon.StreamEndReasonDone, terminal: true, want: true},
		{name: "buffered Responses terminal", end: relaycommon.StreamEndReasonNone, terminal: true, want: true},
		{name: "converted Responses terminal tracked privately", end: relaycommon.StreamEndReasonEOF, policyCompleted: true, want: true},
		{name: "truncated expected terminal", end: relaycommon.StreamEndReasonEOF},
		{name: "disconnected after upstream completion", end: relaycommon.StreamEndReasonClientGone, terminal: true},
		{name: "forwarded upstream failure", end: relaycommon.StreamEndReasonDone, failure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := relaycommon.NewStreamStatus()
			status.RequireTerminal()
			if tc.terminal {
				status.MarkCompleted()
			}
			if tc.failure {
				status.MarkFailed("upstream_error", "server_error", 502)
			}
			status.SetEndReason(tc.end, nil)
			assert.Equal(t, tc.want, generationStreamCompleted(status, tc.policyCompleted))
		})
	}
}

func TestDistillationCompletedTextAdvancesOrClearsConsecutiveHits(t *testing.T) {
	previous, redisEnabled := operation_setting.GetDistillationGuard(), common.RedisEnabled
	common.RedisEnabled = false
	settings := operation_setting.DefaultDistillationGuard()
	settings.Enabled = true
	settings.MinOutputTokens, settings.MaxCachedTokens, settings.ConsecutiveRequests = 3000, 0, 2
	operation_setting.SetDistillationGuard(settings)
	t.Cleanup(func() {
		operation_setting.SetDistillationGuard(previous)
		common.RedisEnabled = redisEnabled
	})
	reportedZero := &dto.Usage{PromptTokens: 8000, CompletionTokens: 3000}
	reportedZero.PromptTokensDetails.SetReportedCachedTokens(0)
	lowOutput := &dto.Usage{PromptTokens: 8000, CompletionTokens: 2999}
	lowOutput.PromptTokensDetails.SetReportedCachedTokens(0)
	for index, tc := range []struct {
		name                                 string
		path                                 string
		status                               int
		clientStream, upstreamStream, failed bool
		usage                                *dto.Usage
		preservesFirstHit                    bool
	}{
		{name: "client stream resets even when upstream is not streaming", clientStream: true, usage: reportedZero},
		{name: "buffered upstream stream is still client non-stream", upstreamStream: true, usage: reportedZero, preservesFirstHit: true},
		{name: "unknown usage resets"},
		{name: "insufficient output resets", usage: lowOutput},
		{name: "high cache resets", usage: &dto.Usage{PromptTokens: 8000, CompletionTokens: 3000, PromptCacheHitTokens: 200}},
		{name: "relayed failure does not participate", status: http.StatusBadGateway, preservesFirstHit: true},
		{name: "aborted stream does not participate", clientStream: true, upstreamStream: true, failed: true, preservesFirstHit: true},
		{name: "compression does not participate", path: "/v1/responses/compact", preservesFirstHit: true},
		{name: "embedding does not participate", path: "/v1/embeddings", preservesFirstHit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			userID := 900_000 + index
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			path := tc.path
			if path == "" {
				path = "/v1/chat/completions"
			}
			c.Request = httptest.NewRequest(http.MethodPost, path, nil)
			status := tc.status
			if status == 0 {
				status = http.StatusOK
			}
			c.String(status, "generation terminal")
			info := &relaycommon.RelayInfo{UserId: userID, ClientIsStream: tc.clientStream, IsStream: tc.upstreamStream}
			if tc.upstreamStream {
				info.StreamStatus = relaycommon.NewStreamStatus()
				if tc.failed {
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, nil)
				} else {
					info.StreamStatus.MarkCompleted()
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
				}
			}
			ObserveNonStreamGeneration(c.Request.Context(), userID, true, settings)
			captureDistillationUsage(c, info, tc.usage)
			ObserveCompletedTextGeneration(c, info)
			ObserveNonStreamGeneration(c.Request.Context(), userID, true, settings)
			assert.Equal(t, tc.preservesFirstHit, !NonStreamBlockedUntil(c.Request.Context(), userID).IsZero())
		})
	}
}

func TestResponsesUsageAccumulatorTerminalAccounting(t *testing.T) {
	operation_setting.SetToolPriceForTest("responses_priced_fn", 5)
	t.Cleanup(func() { operation_setting.DeleteToolPriceForTest("responses_priced_fn") })
	for _, tc := range []struct {
		eventType  string
		wantImages int
	}{
		{eventType: "response.completed", wantImages: 1},
		{eventType: "response.done", wantImages: 1},
		{eventType: "response.incomplete", wantImages: 1},
		{eventType: "response.failed", wantImages: 1},
		{eventType: "response.cancelled", wantImages: 1},
		{eventType: "response.canceled", wantImages: 1},
	} {
		t.Run(tc.eventType, func(t *testing.T) {
			info := &relaycommon.RelayInfo{OriginModelName: "gpt-5.1", StreamStatus: relaycommon.NewStreamStatus()}
			accumulator := NewResponsesUsageAccumulator(info)
			for _, item := range []dto.ResponsesOutput{
				{Type: dto.BuildInCallWebSearchCall},
				{Type: dto.BuildInCallFileSearchCall},
				{Type: dto.BuildInCallFunctionCall, Name: "responses_priced_fn"},
				{Type: dto.BuildInCallFunctionCall, Name: "responses_unpriced_fn"},
			} {
				accumulator.Observe(&dto.ResponsesStreamResponse{Type: dto.ResponsesOutputTypeItemDone, Item: &item}, nil)
			}
			image := dto.ResponsesOutput{ID: "image-1", Type: dto.ResponsesOutputTypeImageGenerationCall, Status: "completed", Result: "image-data"}
			accumulator.Observe(&dto.ResponsesStreamResponse{Type: dto.ResponsesOutputTypeItemDone, Item: &image}, nil)
			upstream := &dto.Usage{InputTokens: 20, OutputTokens: 5, TotalTokens: 25, InputTokensDetails: &dto.InputTokenDetails{CachedTokens: 4}}
			upstream.BillingUsage = dto.NewOpenAIResponsesBillingUsage(upstream)
			terminal := &dto.ResponsesStreamResponse{
				Type: tc.eventType,
				Response: &dto.OpenAIResponsesResponse{
					Usage: upstream, Output: []dto.ResponsesOutput{image},
				},
			}
			accumulator.Observe(terminal, nil)
			accumulator.Observe(terminal, nil)
			usage := accumulator.Finish()
			assert.Equal(t, tc.eventType == "response.failed", info.StreamStatus.ResponseFailed())
			assert.NotEmpty(t, info.StreamStatus.ResponseOutcome())

			assert.Equal(t, 20, usage.PromptTokens)
			assert.Equal(t, 5, usage.CompletionTokens)
			assert.Equal(t, 25, usage.TotalTokens)
			assert.Equal(t, 4, usage.PromptTokensDetails.CachedTokens)
			require.NotNil(t, usage.BillingUsage)
			assert.Equal(t, upstream.BillingUsage, usage.BillingUsage)
			assert.NotSame(t, upstream.BillingUsage, usage.BillingUsage)
			tools := info.ResponsesUsageInfo.BuiltInTools
			for _, name := range []string{dto.BuildInToolWebSearchPreview, dto.BuildInToolFileSearch, "responses_priced_fn"} {
				require.Contains(t, tools, name)
				assert.Equal(t, 1, tools[name].CallCount)
			}
			assert.NotContains(t, tools, "responses_unpriced_fn")
			require.Contains(t, tools, dto.BuildInToolImageGeneration)
			assert.Equal(t, tc.wantImages, tools[dto.BuildInToolImageGeneration].CallCount)
		})
	}
}

func TestResponsesUsageAccumulatorInterruptedTextFallback(t *testing.T) {
	for _, withUsage := range []bool{false, true} {
		name := "disconnect without terminal usage"
		if withUsage {
			name = "failed response preserves native billing usage"
		}
		t.Run(name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-4o"}}
			info.SetEstimatePromptTokens(100)
			accumulator := NewResponsesUsageAccumulator(info)
			accumulator.Observe(&dto.ResponsesStreamResponse{Type: "response.output_text.delta", Delta: "hello"}, nil)
			if withUsage {
				upstream := &dto.Usage{InputTokens: 20, InputTokensDetails: &dto.InputTokenDetails{CachedTokens: 4}}
				upstream.BillingUsage = dto.NewOpenAIResponsesBillingUsage(upstream)
				accumulator.Observe(&dto.ResponsesStreamResponse{Type: "response.failed", Response: &dto.OpenAIResponsesResponse{Usage: upstream}}, nil)
			}
			usage := accumulator.Finish()
			assert.Equal(t, 1, usage.CompletionTokens)
			if withUsage {
				assert.Equal(t, 20, usage.PromptTokens)
				assert.Equal(t, 21, usage.TotalTokens)
				require.NotNil(t, usage.BillingUsage)
				assert.Equal(t, dto.BillingUsageSourceOAIResponses, usage.BillingUsage.Source)
				assert.True(t, usage.BillingUsage.Estimated)
				canonical, ok := usage.BillingUsage.CanonicalUsage()
				require.True(t, ok)
				assert.Equal(t, 4, canonical.PromptTokensDetails.CachedTokens)
				assert.Equal(t, 1, canonical.CompletionTokens)
			} else {
				assert.Equal(t, 100, usage.PromptTokens)
				assert.Equal(t, 101, usage.TotalTokens)
			}
			accumulator.Observe(&dto.ResponsesStreamResponse{Type: "response.output_text.delta", Delta: " late output"}, nil)
			assert.Equal(t, usage, accumulator.Finish())
			assert.Equal(t, 1, usage.CompletionTokens)
		})
	}
}

func TestObserveResponsesOutcomeRecordsProtocolFacts(t *testing.T) {
	for _, tc := range []struct {
		name        string
		event       string
		wantOutcome relaycommon.ResponseOutcome
		wantCode    string
		wantType    string
		wantIncompl string
	}{
		{"flat sse error", `{"type":"error","code":"context_length_exceeded","message":"too long"}`, relaycommon.ResponseOutcomeFailed, "context_length_exceeded", "", ""},
		{"done with failed status", `{"type":"response.done","response":{"status":"failed","error":{"code":"invalid_api_key","type":"invalid_request_error","message":"bad key"}}}`, relaycommon.ResponseOutcomeFailed, "invalid_api_key", "invalid_request_error", ""},
		{"incomplete keeps reason", `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, relaycommon.ResponseOutcomeIncomplete, "", "", "max_output_tokens"},
		{"in progress is not terminal", `{"type":"response.created","response":{"status":"in_progress"}}`, relaycommon.ResponseOutcomeUnknown, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var event dto.ResponsesStreamResponse
			require.NoError(t, common.UnmarshalJsonStr(tc.event, &event))
			info := &relaycommon.RelayInfo{StreamStatus: relaycommon.NewStreamStatus()}
			ObserveResponsesOutcome(info, &event)
			outcome := info.StreamStatus.OutcomeSnapshot()
			assert.Equal(t, tc.wantOutcome, outcome.Response)
			assert.Equal(t, tc.wantCode, outcome.ErrorCode)
			assert.Equal(t, tc.wantType, outcome.ErrorType)
			assert.Equal(t, tc.wantIncompl, outcome.IncompleteReason)
		})
	}
}

func TestResponsesUsageAccumulatorDisconnectBillsCompletedImage(t *testing.T) {
	info := &relaycommon.RelayInfo{OriginModelName: "gpt-5.1", StreamStatus: relaycommon.NewStreamStatus()}
	accumulator := NewResponsesUsageAccumulator(info)
	accumulator.Observe(&dto.ResponsesStreamResponse{
		Type: dto.ResponsesOutputTypeItemDone,
		Item: &dto.ResponsesOutput{ID: "complete-image", Type: dto.ResponsesOutputTypeImageGenerationCall, Status: "completed", Result: "final-image-data"},
	}, nil)
	accumulator.Observe(&dto.ResponsesStreamResponse{
		Type: dto.ResponsesOutputTypeItemDone,
		Item: &dto.ResponsesOutput{ID: "partial-image", Type: dto.ResponsesOutputTypeImageGenerationCall, Status: "partial", Result: "partial-image-data"},
	}, nil)
	usage := accumulator.Finish()
	assert.Zero(t, usage.TotalTokens)
	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, dto.BuildInToolImageGeneration)
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
	accumulator.Finish()
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
}

func TestApplyResponsesUsageCopiesTokenDetails(t *testing.T) {
	dst := &dto.Usage{}
	src := &dto.Usage{
		InputTokens:  11,
		OutputTokens: 7,
		TotalTokens:  18,
		InputTokensDetails: &dto.InputTokenDetails{
			CachedTokens:         3,
			CachedCreationTokens: 2,
			TextTokens:           6,
			AudioTokens:          4,
			ImageTokens:          5,
		},
		OutputTokensDetails: &dto.OutputTokenDetails{
			TextTokens:      1,
			AudioTokens:     2,
			ImageTokens:     3,
			ReasoningTokens: 4,
		},
		PromptCacheHitTokens: 3,
		UsageSemantic:        "openai",
		UsageSource:          "upstream",
	}

	ApplyResponsesUsage(dst, src)

	assert.Equal(t, 11, dst.PromptTokens)
	assert.Equal(t, 7, dst.CompletionTokens)
	assert.Equal(t, 18, dst.TotalTokens)
	require.NotNil(t, dst.InputTokensDetails)
	assert.Equal(t, *src.InputTokensDetails, dst.PromptTokensDetails)
	assert.Equal(t, src.InputTokensDetails, dst.InputTokensDetails)
	assert.NotSame(t, src.InputTokensDetails, dst.InputTokensDetails)
	assert.Equal(t, *src.OutputTokensDetails, dst.CompletionTokenDetails)
	require.NotNil(t, dst.OutputTokensDetails)
	assert.Equal(t, *src.OutputTokensDetails, *dst.OutputTokensDetails)
	assert.NotSame(t, src.OutputTokensDetails, dst.OutputTokensDetails)
	assert.Equal(t, 3, dst.PromptCacheHitTokens)
	assert.Equal(t, "openai", dst.UsageSemantic)
	assert.Equal(t, "upstream", dst.UsageSource)
}

func TestApplyResponsesUsageFallsBackToCompletionTokenDetails(t *testing.T) {
	dst := &dto.Usage{}
	src := &dto.Usage{
		CompletionTokenDetails: dto.OutputTokenDetails{
			ReasoningTokens: 9,
		},
	}

	ApplyResponsesUsage(dst, src)

	assert.Equal(t, 9, dst.CompletionTokenDetails.ReasoningTokens)
	require.NotNil(t, dst.OutputTokensDetails)
	assert.Equal(t, 9, dst.OutputTokensDetails.ReasoningTokens)
}

func TestApplyResponsesUsagePreservesBillingSnapshotAcrossPartialUpdates(t *testing.T) {
	dst := &dto.Usage{}
	src := &dto.Usage{
		InputTokens:  20,
		OutputTokens: 10,
		TotalTokens:  30,
		InputTokensDetails: &dto.InputTokenDetails{
			CachedTokens: 3,
			AudioTokens:  2,
		},
		CompletionTokenDetails: dto.OutputTokenDetails{AudioTokens: 4},
		UsageSemantic:          "openai",
		UsageSource:            "upstream",
	}
	src.BillingUsage = dto.NewOpenAIResponsesBillingUsage(src)
	ApplyResponsesUsage(dst, src)
	ApplyResponsesUsage(dst, &dto.Usage{
		OutputTokens:        12,
		InputTokensDetails:  &dto.InputTokenDetails{},
		OutputTokensDetails: &dto.OutputTokenDetails{ReasoningTokens: 5},
	})

	assert.Equal(t, 20, dst.PromptTokens)
	assert.Equal(t, 12, dst.CompletionTokens)
	assert.Equal(t, 32, dst.TotalTokens)
	assert.Equal(t, 3, dst.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 2, dst.PromptTokensDetails.AudioTokens)
	assert.Equal(t, 4, dst.CompletionTokenDetails.AudioTokens)
	assert.Equal(t, 5, dst.CompletionTokenDetails.ReasoningTokens)
	require.NotNil(t, dst.OutputTokensDetails)
	assert.Equal(t, dst.CompletionTokenDetails, *dst.OutputTokensDetails)
	assert.Equal(t, "openai", dst.UsageSemantic)
	assert.Equal(t, "upstream", dst.UsageSource)
	require.NotNil(t, dst.BillingUsage)
	assert.Equal(t, src.BillingUsage, dst.BillingUsage)
	assert.NotSame(t, src.BillingUsage, dst.BillingUsage)
}

func TestResponsesUsageAccumulatorMissingUsageEstimation(t *testing.T) {
	const model = "gpt-4o"
	const summary = "Inspect the repository before editing."
	const arguments = `{"command":["bash","-lc","ls"]}`
	inProgress := &dto.OpenAIResponsesResponse{Status: []byte(`"in_progress"`)}
	for _, tc := range []struct {
		name           string
		events         []dto.ResponsesStreamResponse
		wantPrompt     int
		wantCompletion int
	}{
		{
			name: "tool call stream cut before terminal usage",
			events: []dto.ResponsesStreamResponse{
				{Type: "response.created", Response: inProgress},
				{Type: "response.reasoning_summary_text.delta", Delta: summary},
				{Type: "response.function_call_arguments.delta", Delta: arguments},
				{Type: dto.ResponsesOutputTypeItemDone, Item: &dto.ResponsesOutput{Type: dto.BuildInCallFunctionCall, Name: "shell"}},
			},
			wantPrompt:     100,
			wantCompletion: tokenkit.Count(model, summary+arguments),
		},
		{
			name: "created only then disconnect bills the prompt",
			events: []dto.ResponsesStreamResponse{
				{Type: "response.created", Response: inProgress},
			},
			wantPrompt: 100,
		},
		{
			name: "incomplete without usage bills the prompt",
			events: []dto.ResponsesStreamResponse{
				{Type: "response.created", Response: inProgress},
				{Type: "response.incomplete", Response: &dto.OpenAIResponsesResponse{Status: []byte(`"incomplete"`)}},
			},
			wantPrompt: 100,
		},
		{
			name: "completed without usage estimates from terminal output",
			events: []dto.ResponsesStreamResponse{
				{Type: "response.completed", Response: &dto.OpenAIResponsesResponse{
					Status: []byte(`"completed"`),
					Output: []dto.ResponsesOutput{{
						Type:    "message",
						Role:    "assistant",
						Content: []dto.ResponsesOutputContent{{Type: "output_text", Text: "final answer"}},
					}},
				}},
			},
			wantPrompt:     100,
			wantCompletion: tokenkit.Count(model, "final answer"),
		},
		{
			name: "explicit failure without usage bills nothing",
			events: []dto.ResponsesStreamResponse{
				{Type: "response.created", Response: inProgress},
				{Type: "response.failed", Response: &dto.OpenAIResponsesResponse{Status: []byte(`"failed"`)}},
			},
		},
		{
			name: "flat error event bills nothing",
			events: []dto.ResponsesStreamResponse{
				{Type: "response.created", Response: inProgress},
				{Type: "error", Code: "server_error"},
			},
		},
		{
			name: "no upstream events bills nothing",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				ChannelMeta:  &relaycommon.ChannelMeta{UpstreamModelName: model},
				StreamStatus: relaycommon.NewStreamStatus(),
			}
			info.SetEstimatePromptTokens(100)
			accumulator := NewResponsesUsageAccumulator(info)
			for i := range tc.events {
				accumulator.Observe(&tc.events[i], nil)
			}
			usage := accumulator.Finish()
			assert.Equal(t, tc.wantPrompt, usage.PromptTokens)
			assert.Equal(t, tc.wantCompletion, usage.CompletionTokens)
			assert.Equal(t, tc.wantPrompt+tc.wantCompletion, usage.TotalTokens)
		})
	}
}

func TestResponsesUsageAccumulatorBillingCompleteness(t *testing.T) {
	for _, tc := range []struct {
		name           string
		upstream       []string
		wantPrompt     int
		wantCompletion int
		wantCache      int
		wantCacheKnown bool
		wantEstimated  bool
		wantPath       string
	}{
		{
			name: "no upstream usage", wantPrompt: 100, wantCompletion: 1,
			wantEstimated: true, wantPath: usageBillingPathLocal,
		},
		{
			name: "empty upstream usage", upstream: []string{`{}`},
			wantPrompt: 100, wantCompletion: 1, wantEstimated: true, wantPath: usageBillingPathLocal,
		},
		{
			name:       "complete usage with known zero cache",
			upstream:   []string{`{"input_tokens":20,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}`},
			wantPrompt: 20, wantCompletion: 5, wantCacheKnown: true, wantPath: usageBillingPathOpenAI,
		},
		{
			name:       "known zero cache arrives after output usage",
			upstream:   []string{`{"output_tokens":5}`, `{"input_tokens":20,"input_tokens_details":{"cached_tokens":0}}`},
			wantPrompt: 20, wantCompletion: 5, wantCacheKnown: true, wantPath: usageBillingPathOpenAI,
		},
		{
			name:       "missing cache facts",
			upstream:   []string{`{"input_tokens":20,"output_tokens":5}`},
			wantPrompt: 20, wantCompletion: 5, wantPath: usageBillingPathOpenAI,
		},
		{
			name:       "empty input details leave cache unknown",
			upstream:   []string{`{"input_tokens":20,"output_tokens":5,"input_tokens_details":{}}`},
			wantPrompt: 20, wantCompletion: 5, wantPath: usageBillingPathOpenAI,
		},
		{
			name:       "partial text details leave cache unknown",
			upstream:   []string{`{"output_tokens":5}`, `{"input_tokens":20,"input_tokens_details":{"text_tokens":20}}`},
			wantPrompt: 20, wantCompletion: 5, wantPath: usageBillingPathOpenAI,
		},
		{
			name:       "null cache leaves cache unknown",
			upstream:   []string{`{"input_tokens":20,"output_tokens":5,"input_tokens_details":{"cached_tokens":null}}`},
			wantPrompt: 20, wantCompletion: 5, wantPath: usageBillingPathOpenAI,
		},
		{
			name:       "known zero arrives after empty details",
			upstream:   []string{`{"input_tokens":20,"output_tokens":5,"input_tokens_details":{}}`, `{"input_tokens":20,"input_tokens_details":{"cached_tokens":0}}`},
			wantPrompt: 20, wantCompletion: 5, wantCacheKnown: true, wantPath: usageBillingPathOpenAI,
		},
		{
			name:       "DeepSeek compatible cache hits",
			upstream:   []string{`{"input_tokens":20,"output_tokens":5,"prompt_cache_hit_tokens":4}`},
			wantPrompt: 20, wantCompletion: 5, wantCache: 4, wantCacheKnown: true, wantPath: usageBillingPathOpenAI,
		},
		{
			name:       "estimated output keeps known zero cache",
			upstream:   []string{`{"input_tokens":20,"input_tokens_details":{"cached_tokens":0}}`},
			wantPrompt: 20, wantCompletion: 1, wantCacheKnown: true, wantEstimated: true, wantPath: usageBillingPathOpenAIEstimated,
		},
		{
			name:       "estimated output keeps upstream cache across partial updates",
			upstream:   []string{`{"input_tokens":20,"input_tokens_details":{"cached_tokens":4}}`, `{}`},
			wantPrompt: 20, wantCompletion: 1, wantCache: 4, wantCacheKnown: true, wantEstimated: true, wantPath: usageBillingPathOpenAIEstimated,
		},
		{
			name:       "output only usage still settles estimated input",
			upstream:   []string{`{"output_tokens":5}`},
			wantPrompt: 100, wantCompletion: 5, wantEstimated: true, wantPath: usageBillingPathOpenAIEstimated,
		},
		{
			name:       "missing input leaves cache rate unknown while preserving cache count",
			upstream:   []string{`{"output_tokens":5,"input_tokens_details":{"cached_tokens":4}}`},
			wantPrompt: 100, wantCompletion: 5, wantCache: 4, wantEstimated: true, wantPath: usageBillingPathOpenAIEstimated,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-4o"}, StreamStatus: relaycommon.NewStreamStatus()}
			info.SetEstimatePromptTokens(100)
			accumulator := NewResponsesUsageAccumulator(info)
			accumulator.Observe(&dto.ResponsesStreamResponse{Type: "response.output_text.delta", Delta: "hello"}, nil)
			for _, raw := range tc.upstream {
				var upstream dto.Usage
				require.NoError(t, common.UnmarshalJsonStr(raw, &upstream))
				encoded, err := common.Marshal(upstream)
				require.NoError(t, err)
				require.NoError(t, common.Unmarshal(encoded, &upstream))
				accumulator.Observe(&dto.ResponsesStreamResponse{Type: "response.completed", Response: &dto.OpenAIResponsesResponse{Usage: &upstream}}, nil)
			}
			usage := accumulator.Finish()
			require.NotNil(t, usage.BillingUsage)
			assert.Equal(t, tc.wantEstimated, usage.BillingUsage.Estimated)
			assert.Same(t, usage, accumulator.Finish())

			settled := effectiveBillingUsage(usage)
			assert.Equal(t, tc.wantPrompt, settled.PromptTokens)
			assert.Equal(t, tc.wantCompletion, settled.CompletionTokens)
			assert.Equal(t, tc.wantPrompt+tc.wantCompletion, settled.TotalTokens)
			assert.Equal(t, tc.wantCache, settled.PromptTokensDetails.CachedTokens)

			other := model.NewLogOther()
			other.SetPublic("cache_tokens", settled.PromptTokensDetails.CachedTokens)
			appendUsageBillingPathForLog(other, false, usage)
			var logged map[string]any
			require.NoError(t, common.UnmarshalJsonStr(other.JSONString(), &logged))
			assert.Equal(t, tc.wantEstimated, logged["usage_estimated"])
			assert.Equal(t, tc.wantCacheKnown, logged["cache_usage_known"])
			assert.Equal(t, float64(tc.wantCache), logged["cache_tokens"])
			admin, ok := logged["admin_info"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, tc.wantPath, admin["usage_billing_path"])
		})
	}
}

func TestResponsesUsageAccumulatorDisconnectLogKeepsFinalUsage(t *testing.T) {
	info := &relaycommon.RelayInfo{
		IsStream: true, StreamDisconnectMode: "drain", StreamStatus: relaycommon.NewStreamStatus(),
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-4o"},
	}
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, nil)
	accumulator := NewResponsesUsageAccumulator(info)
	var upstream dto.Usage
	require.NoError(t, common.UnmarshalJsonStr(`{"input_tokens":100,"output_tokens":10,"input_tokens_details":{"cached_tokens":80}}`, &upstream))
	accumulator.Observe(&dto.ResponsesStreamResponse{Type: "response.completed", Response: &dto.OpenAIResponsesResponse{Usage: &upstream}}, nil)
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
	usage := accumulator.Finish()
	other := model.NewLogOther()
	appendStreamStatus(info, other)
	appendUsageBillingPathForLog(other, false, usage)
	var logged map[string]any
	require.NoError(t, common.UnmarshalJsonStr(other.JSONString(), &logged))
	assert.Equal(t, false, logged["usage_estimated"])
	assert.Equal(t, true, logged["cache_usage_known"])
	status, ok := logged["stream_status"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "client_gone", status["end_reason"])
	assert.Equal(t, "done", status["drain_end_reason"])
	assert.Equal(t, "completed", status["response_status"])
	assert.Equal(t, "drain", status["disconnect_mode"])
}

func TestPostTextConsumeQuotaUsageMetadata(t *testing.T) {
	for _, tc := range []struct {
		name       string
		format     types.RelayFormat
		billing    *dto.BillingUsage
		raw        string
		chatSource bool
		fallback   bool
		normalName bool
		wantInput  float64
		wantCache  float64
		wantWrites float64
		cacheKnown *bool
	}{
		{
			name: "DeepSeek chat cache hit count", format: types.RelayFormatOpenAI,
			billing:   dto.NewOpenAIChatBillingUsage(&dto.Usage{PromptTokens: 100, CompletionTokens: 10, PromptCacheHitTokens: 80}),
			wantInput: 100, wantCache: 80, normalName: true, cacheKnown: common.GetPointer(true),
		},
		{
			name: "Claude split writes define the complete input", format: types.RelayFormatClaude,
			billing: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{InputTokens: 10, OutputTokens: 10, CacheReadInputTokens: 60, CacheCreationInputTokens: 20,
				CacheCreation: &dto.ClaudeCacheCreationUsage{Ephemeral5mInputTokens: 10, Ephemeral1hInputTokens: 20}}),
			wantInput: 100, wantCache: 60, wantWrites: 30,
		},
		{
			name: "Claude usage converted to OpenAI keeps separate cache", format: types.RelayFormatOpenAI,
			billing:   dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{InputTokens: 10, OutputTokens: 10, CacheReadInputTokens: 60, CacheCreationInputTokens: 30}),
			wantInput: 100, wantCache: 60, wantWrites: 30,
		},
		{
			name: "OpenAI usage over Claude retains inclusive input", format: types.RelayFormatClaude,
			billing:   dto.NewOpenAIChatBillingUsage(&dto.Usage{PromptTokens: 100, CompletionTokens: 10, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 80, CacheWriteTokens: 70}}),
			wantInput: 100, wantCache: 80, wantWrites: 70, cacheKnown: common.GetPointer(true),
		},
		{
			name: "failed expression marks retained reservation", format: types.RelayFormatOpenAI,
			billing:  dto.NewOpenAIChatBillingUsage(&dto.Usage{PromptTokens: 100, CompletionTokens: 10}),
			fallback: true, wantInput: 100, cacheKnown: common.GetPointer(false),
		},
		{
			name: "Chat without sidecar leaves absent cache unknown", format: types.RelayFormatOpenAI,
			raw:       `{"prompt_tokens":100,"completion_tokens":10,"input_tokens":100,"usage_source":"upstream"}`,
			wantInput: 100, cacheKnown: common.GetPointer(false),
		},
		{
			name: "Chat without sidecar keeps measured hits", format: types.RelayFormatOpenAI,
			raw:       `{"prompt_tokens":100,"completion_tokens":10,"input_tokens":100,"usage_source":"upstream","prompt_tokens_details":{"cached_tokens":80}}`,
			wantInput: 100, wantCache: 80, cacheKnown: common.GetPointer(true),
		},
		{
			name: "Chat without sidecar keeps a measured miss", format: types.RelayFormatOpenAI,
			raw:       `{"prompt_tokens":100,"completion_tokens":10,"input_tokens":100,"usage_source":"upstream","prompt_tokens_details":{"cached_tokens":0}}`,
			wantInput: 100, cacheKnown: common.GetPointer(true),
		},
		{
			name: "Chat sidecar keeps a measured miss", format: types.RelayFormatOpenAI,
			raw:       `{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":0}}`,
			wantInput: 100, chatSource: true, cacheKnown: common.GetPointer(true),
		},
		{
			name: "Chat sidecar does not treat other prompt details as a miss", format: types.RelayFormatOpenAI,
			raw:       `{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"text_tokens":100}}`,
			wantInput: 100, chatSource: true, cacheKnown: common.GetPointer(false),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			const userID, channelID = 73, 73
			seedUser(t, userID, 1_000_000)
			seedChannel(t, channelID)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{
				UserId: userID, OriginModelName: "requested-model", BillingModelName: "shared-price", UsingGroup: "default", StartTime: time.Now(), RelayFormat: tc.format,
				ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID},
				PriceData: hosttypes.PriceData{ModelRatio: 1, CompletionRatio: 1, CacheRatio: 0.1, CacheCreationRatio: 1.25,
					CacheCreation5mRatio: 1.25, CacheCreation1hRatio: 2, GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1}},
			}
			if tc.fallback {
				info.TieredBillingSnapshot = makeSnapshot(`tier("base", p * param("missing"))`, 1, 100, 0)
				info.TieredBillingSnapshot.EstimatedBillingUnit = billingexpr.BillingUnitToken
				info.TieredBillingSnapshot.EstimatedQuotaAfterGroup = 123
			}
			if tc.normalName {
				info.BillingModelName = info.OriginModelName
			}
			usage := &dto.Usage{BillingUsage: tc.billing}
			if tc.raw != "" {
				require.NoError(t, common.UnmarshalJsonStr(tc.raw, usage))
			}
			if tc.chatSource {
				usage.BillingUsage = dto.NewOpenAIChatBillingUsage(usage)
			}
			PostTextConsumeQuota(ctx, info, usage, nil)
			log := getLastLog(t)
			require.NotNil(t, log)
			assert.Equal(t, info.BillingModelName, log.ModelName)
			var other map[string]any
			require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
			assert.NotContains(t, other, "original_model_name")
			assert.Equal(t, tc.wantInput, other["input_tokens_total"])
			assert.Equal(t, tc.wantCache, other["cache_tokens"])
			assert.Equal(t, false, other["usage_estimated"])
			if tc.cacheKnown != nil {
				assert.Equal(t, *tc.cacheKnown, other["cache_usage_known"])
			}
			if tc.wantWrites > 0 {
				assert.Equal(t, tc.wantWrites, other["cache_write_tokens"])
			}
			admin, ok := other["admin_info"].(map[string]any)
			require.True(t, ok)
			if tc.normalName {
				assert.NotContains(t, admin, "original_model_name")
			} else {
				assert.Equal(t, "requested-model", admin["original_model_name"])
			}
			if tc.fallback {
				assert.Equal(t, true, admin["billing_settlement_fallback"])
				assert.Equal(t, 123, log.Quota)
			} else {
				assert.NotContains(t, admin, "billing_settlement_fallback")
			}
		})
	}
}
