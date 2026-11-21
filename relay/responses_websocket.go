package relay

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	appconstant "github.com/QuantumNous/new-api/constant"
	appdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	appmodel "github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/pkg/wsmanager"
	relaychannel "github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relay/requestplugin"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const responsesWSEventTypeResponseCreate = "response.create"
const responsesWSWriteTimeout = 30 * time.Second

// ResponsesWSRequestRunner executes the existing authentication and rate-limit
// middleware around one complete request, without a second HTTP connection.
type ResponsesWSRequestRunner func(*http.Request, string, func(*gin.Context) *types.NewAPIError) *types.NewAPIError

type responsesWSCreateEvent struct {
	Type     string            `json:"type"`
	EventID  string            `json:"event_id,omitempty"`
	StreamID common.RawMessage `json:"stream_id,omitempty"`
	Generate common.RawMessage `json:"generate,omitempty"`
	Request  common.RawMessage `json:"response,omitempty"`
}

type responsesWSCreateRequest struct {
	Request  dto.OpenAIResponsesRequest
	Body     []byte
	Generate common.RawMessage
	StreamID string
	EventID  string
}

type responsesWSErrorEvent struct {
	Type       string             `json:"type"`
	Status     int                `json:"status"`
	EventID    string             `json:"event_id,omitempty"`
	StreamID   string             `json:"stream_id,omitempty"`
	ResponseID string             `json:"response_id,omitempty"`
	Error      *types.OpenAIError `json:"error"`
}

type responsesWSMessage struct {
	kind int
	body []byte
	err  error
}

// responsesWSControl is a client control event (response.cancel) whose
// envelope the read loop already parsed.
type responsesWSControl struct {
	body              []byte
	eventID, streamID string
}

// Only the request worker reads or changes billing state. Socket readers pass
// bounded messages to it; cancellation never performs an independent refund.
type responsesWSCallState struct {
	inbox       chan responsesWSMessage
	controls    chan responsesWSControl
	done        chan struct{}
	terminal    *responsesWSMessage
	publicError *types.NewAPIError
	// A nonzero closeCode ends the connection after this request.
	closeCode   int
	closeReason string
}

type responsesWSSession struct {
	ctx            context.Context
	cancel         context.CancelFunc
	client         *websocket.Conn
	runner         ResponsesWSRequestRunner
	request        *http.Request
	requestID      string
	nextEventIndex int
	workers        sync.WaitGroup
	closeOnce      sync.Once

	clientWriteMu sync.Mutex
	targetWriteMu sync.Mutex
	connectionMu  sync.Mutex
	target        *websocket.Conn
	targetCancel  context.CancelFunc
	targetDone    <-chan struct{}
	unregister    func()
	stateMu       sync.Mutex
	current       *responsesWSCallState

	// clientWriteBudget is guarded by clientWriteMu.
	clientWriteBudget helper.StreamWriteBudget

	// These fields belong to the serial request worker and describe the actual
	// established connection. Per-request token/user data is never stored here.
	lastResponseID  string
	lockedModel     string
	lockedChannelID int
	lockedGroup     string
	lockedKey       string
	lockedKeyIndex  int
	lockedContext   map[appconstant.ContextKey]any
	lockedRoute     dto.AdvancedCustomRoute
	cyberBlockUntil time.Time
}

func ResponsesWebSocketHelper(c *gin.Context, client *websocket.Conn, runner ResponsesWSRequestRunner) *types.NewAPIError {
	ctx, cancel := context.WithCancel(c.Request.Context())
	s := &responsesWSSession{ctx: ctx, cancel: cancel, client: client, runner: runner,
		request: c.Request.Clone(ctx), requestID: c.GetString(common.RequestIdKey)}
	if s.requestID == "" {
		s.requestID = common.NewRequestId()
	}
	maxMB := appconstant.MaxRequestBodyMB
	if maxMB <= 0 {
		maxMB = 128
	}
	client.SetReadLimit(int64(maxMB) << 20)
	defer func() {
		// Unless an earlier shutdown ended the read, the client closed or left.
		// Gorilla already answered a client close frame, so this tells the upstream.
		s.shutdown(websocket.CloseNormalClosure, "")
		s.workers.Wait()
	}()

	for {
		_, message, err := client.ReadMessage()
		if err != nil {
			return nil
		}
		envelope, streamID, err := parseResponsesWSEnvelope(message)
		eventType := envelope.Type
		if err != nil {
			s.sendError(envelope.EventID, streamID, newResponsesWSInvalidRequestError(err))
			continue
		}
		if eventType != responsesWSEventTypeResponseCreate {
			// Controls are owned by the active request too. In particular a cancel
			// arriving during authentication must not precede its upstream create.
			state := s.getCurrent()
			if eventType != "response.cancel" || state == nil {
				s.sendError(envelope.EventID, streamID, newResponsesWSInvalidRequestError(fmt.Errorf("unsupported websocket event %q", eventType)))
				continue
			}
			select {
			case state.controls <- responsesWSControl{body: message, eventID: envelope.EventID, streamID: streamID}:
			case <-state.done:
			case <-s.ctx.Done():
				return nil
			default:
				s.sendError(envelope.EventID, streamID, newResponsesWSInvalidRequestError(errors.New("a response control event is already pending")))
			}
			continue
		}
		state := &responsesWSCallState{inbox: make(chan responsesWSMessage), controls: make(chan responsesWSControl, 1), done: make(chan struct{})}
		if !s.tryReserveCurrent(state) {
			s.sendError(envelope.EventID, streamID, types.NewErrorWithStatusCode(errors.New("another response.create is already in progress on this websocket connection"), types.ErrorCodeInvalidRequest, http.StatusConflict, types.ErrOptionWithSkipRetry()))
			continue
		}
		requestID := fmt.Sprintf("%s-ws-%d", s.requestID, s.nextEventIndex)
		s.nextEventIndex++
		s.workers.Go(func() { s.runRequest(state, message, envelope, streamID, requestID) })
	}
}

func (s *responsesWSSession) runRequest(state *responsesWSCallState, message []byte, envelope responsesWSCreateEvent, streamID string, requestID string) {
	create, parseErr := normalizeResponsesWSCreateEvent(message, envelope, streamID)
	var apiErr *types.NewAPIError
	var upstreamURLs []string
	defer func() {
		if recovered := recover(); recovered != nil {
			apiErr = types.NewError(fmt.Errorf("responses websocket request panic: %v", recovered), types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry())
			state.closeCode = websocket.CloseInternalServerErr
		}
		// Finish the middleware count before publishing the terminal event. Hold
		// admission while writing it so an immediate next create cannot race
		// the current request's release. A failing request also shuts down
		// before that release, so a concurrent upstream close cannot report it
		// as a normal closure; shutdown itself takes neither lock.
		outgoing := state.terminal
		if clientErr := cmp.Or(state.publicError, apiErr); clientErr != nil {
			if clientErr.StatusCode == http.StatusUnauthorized || clientErr.StatusCode == http.StatusForbidden {
				state.closeCode = websocket.ClosePolicyViolation
			}
			if body, err := buildResponsesWSErrorPayload(envelope.EventID, streamID, clientErr, upstreamURLs...); err == nil {
				outgoing = &responsesWSMessage{kind: websocket.TextMessage, body: body}
			}
		}
		s.clientWriteMu.Lock()
		s.stateMu.Lock()
		if outgoing != nil {
			if err := s.client.SetWriteDeadline(s.clientWriteBudget.Deadline(time.Now(), len(outgoing.body))); err != nil {
				state.closeCode = websocket.CloseInternalServerErr
			} else if err := s.client.WriteMessage(outgoing.kind, outgoing.body); err != nil {
				state.closeCode = websocket.CloseInternalServerErr
			}
		}
		if state.closeCode != 0 {
			s.shutdown(state.closeCode, state.closeReason)
		}
		s.current = nil
		close(state.done)
		s.stateMu.Unlock()
		s.clientWriteMu.Unlock()
	}()
	requestCtx, cancelRequest := context.WithCancel(s.ctx)
	defer cancelRequest()
	request := s.request.Clone(requestCtx)
	request.Body = io.NopCloser(bytes.NewReader(message))
	request.ContentLength = int64(len(message))
	request.Header.Set("Content-Type", "application/json")
	apiErr = s.runner(request, requestID, func(c *gin.Context) *types.NewAPIError {
		if parseErr != nil {
			return newResponsesWSInvalidRequestError(parseErr)
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(create.Body))
		c.Request.ContentLength = int64(len(create.Body))
		callErr := s.runCall(c, state, create)
		upstreamURLs = common.UpstreamAddresses(c)
		if callErr != nil {
			state.publicError = service.PublicUpstreamBalanceError(c, callErr)
		}
		return callErr
	})
}

func (s *responsesWSSession) runCall(c *gin.Context, state *responsesWSCallState, create responsesWSCreateRequest) (apiErr *types.NewAPIError) {
	defer service.ReleaseChannelConcurrency(c)
	policy := service.RequestPolicy(c)
	modelName := create.Request.Model
	started := time.Now()
	var info *relaycommon.RelayInfo
	var probeObserver *service.ChannelProbeObserver
	var groupProbeObserver *service.ChannelProbeObserver
	var groupProbes []appmodel.ChannelProbe
	groupSelectionFailed := false
	var probes []appmodel.ChannelProbe
	billingPrepared := false
	var ingressSteps []relaycommon.RequestPluginStep
	var ingressRecord []relaycommon.RequestPluginRecord
	// stopErr is the error the client gets for a response a request plugin
	// stopped while checking its text.
	var stopErr *types.NewAPIError
	defer func() {
		if recovered := recover(); recovered != nil {
			apiErr = types.NewError(fmt.Errorf("responses websocket call panic: %v", recovered), types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry())
			state.closeCode = websocket.CloseInternalServerErr
		}
		if info == nil && modelName != "" {
			info = &relaycommon.RelayInfo{OriginModelName: modelName, UsingGroup: common.GetContextKeyString(c, appconstant.ContextKeyUsingGroup), StartTime: started}
			info.SetRequestPluginIngress(ingressSteps, ingressRecord)
		}
		perfmetrics.RecordRelayResult(c.Request.Context(), info, apiErr)
		if probeObserver != nil {
			service.RecordPassiveChannelProbeResult(probes, probeObserver.Result(c.Request.Context(), info, apiErr))
		}
		if groupProbeObserver != nil && (billingPrepared || groupSelectionFailed) {
			groupName := info.UsingGroup
			if groupName == "" {
				groupName = common.GetContextKeyString(c, appconstant.ContextKeyUsingGroup)
			}
			service.RecordPassiveGroupProbeResult(groupProbes, groupName, groupProbeObserver.Result(c.Request.Context(), info, apiErr))
		}
		// Settlement already marks the request policy successful, and nothing
		// reads a termination decision after this point on the WebSocket path,
		// so neither policy record belongs here.
		if info != nil && billingPrepared {
			apiErr = RefundFailedRequestBilling(c, info, apiErr)
		}
		// The request plugins' report hooks get the response once it settled.
		requestplugin.Report(c, types.RelayFormatOpenAIResponses, info, ingressRecord, cmp.Or(apiErr, stopErr))
	}()
	if modelName == "" {
		return newResponsesWSInvalidRequestError(errors.New("model is required"))
	}
	if s.lockedModel != "" && modelName != s.lockedModel {
		return newResponsesWSInvalidRequestError(fmt.Errorf("responses websocket connection is locked to model %q", s.lockedModel))
	}
	common.SetContextKey(c, appconstant.ContextKeyOriginalModel, modelName)
	common.SetContextKey(c, appconstant.ContextKeyRequestStartTime, time.Now())
	// As on HTTP, the request plugins' ingress reads the create before the
	// model access and channel checks; a changed body is decoded again.
	changed, ingressSteps, ingressRecord, ingressErr := requestplugin.SocketIngress(c, types.RelayFormatOpenAIResponses)
	if ingressErr != nil {
		ingress := &relaycommon.RelayInfo{}
		ingress.SetRequestPluginIngress(ingressSteps, ingressRecord)
		service.RecordRelayErrorLog(c, 0, ingressErr, ingress)
		return ingressErr
	}
	if changed {
		if apiErr = reloadResponsesWSCreate(c, &create); apiErr != nil {
			return apiErr
		}
	}
	if apiErr = checkResponsesWSModelAccess(c, modelName); apiErr != nil {
		return apiErr
	}
	// Explicit sessions use their own block keys; only unidentified turns share the connection window.
	if time.Now().Before(s.cyberBlockUntil) && service.CyberSessionBlockEnabled() && !service.HasCyberSessionIdentity(c) {
		return service.CyberSessionBlockedError(c)
	}
	if apiErr = service.CheckCyberSessionBlock(c); apiErr != nil {
		return apiErr
	}
	service.GetChannelConstraints(c).AddFilter(appdto.ChannelFilter{Kind: appdto.FilterRequestPath, RequestPath: c.Request.URL.Path})
	service.SetRequestServiceTierFilter(c, modelName, create.Request.ServiceTier)
	service.GetChannelConstraints(c).AddFilter(appdto.ChannelFilter{Kind: appdto.FilterResponsesWebSocket})
	retry := &service.RetryParam{
		Ctx: c, TokenGroup: common.GetContextKeyString(c, appconstant.ContextKeyUsingGroup), ModelName: modelName,
		RequestPath: c.Request.URL.Path, Retry: common.GetPointer(0), PreserveConnection: create.Request.PreviousResponseID != "",
	}
	var firstChannel *appmodel.Channel
	if s.lockedChannelID != 0 && retry.MayRerouteConnection(s.lockedChannelID) {
		firstChannel, apiErr = chooseResponsesWSChannel(c, modelName, retry)
		if apiErr != nil {
			groupSelectionFailed = true
			return apiErr
		}
		if firstChannel.Id == s.lockedChannelID && !service.ChannelMaintenanceWaited(c) {
			s.lockedGroup = cmp.Or(common.GetContextKeyString(c, appconstant.ContextKeyAutoGroup), retry.TokenGroup)
			firstChannel = nil
		} else {
			if apiErr = middleware.SetupContextForSelectedChannel(c, firstChannel, modelName); apiErr != nil {
				groupSelectionFailed = true
				return apiErr
			}
			s.detachTarget()
		}
	}

	groupProbes = service.PassiveGroupProbes("", modelName)
	if len(groupProbes) > 0 {
		groupProbeObserver = service.NewChannelProbeObserverAt(nil, started)
	}
routeAttempt:
	if s.lockedChannelID != 0 {
		if apiErr = s.restoreConnectionContext(c, modelName); apiErr != nil {
			groupSelectionFailed = true
			return apiErr
		}
		if service.ChannelMaintenanceWaited(c) {
			var err error
			firstChannel, err = appmodel.CacheGetChannel(s.lockedChannelID)
			if err != nil {
				return types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
			}
			if firstChannel == nil {
				return types.NewError(errors.New("the connection channel is unavailable"), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
			}
			s.detachTarget()
			goto routeAttempt
		}
		info = relaycommon.GenRelayInfoResponses(c, &create.Request)
		service.MarkMediaGenerationRequest(c, info)
		info.IsStream = true
		info.CaptureNativeCompactionV2(create.Request.Input)
		info.SetRequestPluginIngress(ingressSteps, ingressRecord)
		common.SetContextKey(c, appconstant.ContextKeyIsStream, true)
		if apiErr = PrepareRequestBilling(c, info); apiErr != nil {
			return apiErr
		}
		billingPrepared = true
		var payload []byte
		payload, apiErr = buildResponsesWSCreatePayload(c, info, create.Request, create.Generate, create.StreamID)
		if apiErr != nil {
			recordResponsesWSPluginError(c, info, apiErr)
			return apiErr
		}
		probes = service.PassiveChannelProbes(s.lockedChannelID, modelName)
		if len(probes) > 0 {
			probeObserver = service.NewChannelProbeObserver(nil)
		}
		if err := s.writeTarget(websocket.TextMessage, payload); err != nil {
			state.closeCode = websocket.CloseInternalServerErr
			return types.NewError(err, types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry())
		}
	} else {
		for ; retry.GetRetry() <= service.ChannelFailoverRetryBudget(c); retry.IncreaseRetry() {
			var channel *appmodel.Channel
			if firstChannel != nil {
				channel, firstChannel = firstChannel, nil
			} else {
				channel, apiErr = selectResponsesWSChannel(c, modelName, retry)
			}
			if apiErr != nil {
				groupSelectionFailed = true
				return apiErr
			}
			service.AppendUsedChannel(c, channel.Id)
			if info == nil {
				info = relaycommon.GenRelayInfoResponses(c, &create.Request)
				service.MarkMediaGenerationRequest(c, info)
				info.IsStream = true
				info.CaptureNativeCompactionV2(create.Request.Input)
				info.SetRequestPluginIngress(ingressSteps, ingressRecord)
				common.SetContextKey(c, appconstant.ContextKeyIsStream, true)
				if apiErr = PrepareRequestBilling(c, info); apiErr != nil {
					return apiErr
				}
				billingPrepared = true
			} else {
				info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)
				if apiErr = service.PrepareTieredBillingForSelectedGroup(c, info); apiErr != nil {
					return apiErr
				}
			}
			info.RetryIndex = retry.GetRetry()
			policy.BeginAttempt(channel, info.UsingGroup)
			var payload []byte
			payload, apiErr = buildResponsesWSCreatePayload(c, info, create.Request, create.Generate, create.StreamID)
			if apiErr != nil {
				recordResponsesWSPluginError(c, info, apiErr)
				return apiErr
			}
			adaptor := GetAdaptor(info.ApiType)
			adaptor.Init(info)
			probes = service.PassiveChannelProbes(channel.Id, modelName)
			if len(probes) > 0 {
				probeObserver = service.NewChannelProbeObserver(nil)
			}
			target, dialErr := relaychannel.DoWssRequest(adaptor, c, info, nil)
			if dialErr != nil {
				apiErr = service.NormalizeViolationFeeError(types.NewError(dialErr, types.ErrorCodeDoRequestFailed))
				if probeObserver != nil {
					service.RecordPassiveChannelProbeResult(probes, probeObserver.Result(c.Request.Context(), info, apiErr))
					probeObserver = nil
				}
				service.ResetStatusCode(apiErr, c.GetString("status_code_mapping"))
				info.LastError = apiErr
				decision := retry.DecideRelayRetry(apiErr)
				service.RecordPolicyFailure(c, channel.Id, apiErr, decision)
				service.ProcessChannelError(c, *types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey, info.ApiKey, channel.GetAutoBan()), apiErr, info)
				if decision.Action == "retry" {
					continue
				}
				return apiErr
			}
			if !s.setTarget(target) {
				return types.NewError(context.Canceled, types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry())
			}
			if err := s.writeTarget(websocket.TextMessage, payload); err != nil {
				state.closeCode = websocket.CloseInternalServerErr
				return types.NewError(err, types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry())
			}
			s.lockedModel, s.lockedChannelID, s.lockedGroup = modelName, channel.Id, info.UsingGroup
			s.lockedKey, s.lockedKeyIndex = info.ApiKey, info.ChannelMultiKeyIndex
			s.lockedRoute, _ = connectionOtherSettings(c, channel).AdvancedCustom.MatchPathForModel(c.Request.URL.Path, modelName)
			s.lockedContext = make(map[appconstant.ContextKey]any)
			for _, key := range []appconstant.ContextKey{
				appconstant.ContextKeyChannelId, appconstant.ContextKeyChannelName, appconstant.ContextKeyChannelType,
				appconstant.ContextKeyChannelCreateTime, appconstant.ContextKeyChannelSetting, appconstant.ContextKeyChannelOtherSetting,
				appconstant.ContextKeyChannelParamOverride, appconstant.ContextKeyChannelHeaderOverride, appconstant.ContextKeyChannelOrganization,
				appconstant.ContextKeyChannelAutoBan, appconstant.ContextKeyChannelModelMapping, appconstant.ContextKeyChannelStatusCodeMapping,
				appconstant.ContextKeyChannelIsMultiKey, appconstant.ContextKeyChannelMultiKeyIndex, appconstant.ContextKeyChannelKey, appconstant.ContextKeyChannelBaseUrl,
				appconstant.ContextKeyUpstreamRequestOrigin, appconstant.ContextKeyProviderPlugin,
			} {
				if value, ok := c.Get(string(key)); ok {
					s.lockedContext[key] = value
				}
			}
			s.registerChannelClose(channel.Id)
			s.startTargetReader(target)
			apiErr = nil
			break
		}
		if apiErr != nil {
			return apiErr
		}
	}

	// The request plugins' reply check reads the text of the response before
	// the client gets it; without a reply or report instance the events go
	// out untouched.
	forwarded := false
	socket := requestplugin.ResponsesSocket(c, info, func(message []byte) {
		forwarded = true
		if err := s.writeClient(websocket.TextMessage, message); err != nil {
			s.shutdown(websocket.CloseInternalServerErr, "")
		}
	})
	// terminalOf is the event that ends the response for the client: the
	// reply check reads the rest of the text first, and a response it
	// stopped ends with the error instead.
	var responseID string
	terminalOf := func(incoming responsesWSMessage, upstreamError *types.NewAPIError) *responsesWSMessage {
		if socket != nil && stopErr == nil && incoming.kind == websocket.TextMessage {
			if body := socket.Terminal(incoming.body); body != nil {
				incoming.body = body
			}
			stopErr = socket.Stopped()
		}
		if stopErr == nil {
			if publicError := service.PublicUpstreamBalanceError(c, upstreamError); publicError != nil {
				state.publicError = publicError
				return nil
			}
			return &incoming
		}
		openAIErr := stopErr.ToOpenAIError(common.UpstreamAddresses(c)...)
		body, _ := common.Marshal(&responsesWSErrorEvent{Type: "error", Status: stopErr.StatusCode, EventID: create.EventID, StreamID: create.StreamID, ResponseID: responseID, Error: &openAIErr})
		return &responsesWSMessage{kind: websocket.TextMessage, body: body}
	}
	// finishSocket ends the reply check of a response that ended without
	// its terminal event.
	finishSocket := func() {
		if socket != nil && stopErr == nil {
			socket.Finish()
			stopErr = socket.Stopped()
		}
	}

	accumulator := service.NewResponsesUsageAccumulator(info)
	info.StreamStatus = relaycommon.NewStreamStatus()
	info.StreamStatus.RequireTerminal()
	common.SetContextKey(c, appconstant.ContextKeyResponseStreamStatus, info.StreamStatus)
	timeout := time.Duration(appconstant.StreamingTimeout) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	idle := time.NewTimer(timeout)
	defer idle.Stop()
	accepted := false
	var pendingControl []byte
	var sentControl []byte
	// cancelled is set once the response.cancel of a stop went upstream.
	cancelled := false
	replayStopReason := func() string {
		switch {
		case accepted || forwarded:
			return "response_started"
		case stopErr != nil:
			return "local_rejection"
		case cancelled || pendingControl != nil || sentControl != nil || len(state.controls) > 0:
			return "request_cancelled"
		default:
			return ""
		}
	}
	retryBeforeAcceptance := func(rejected *types.NewAPIError) bool {
		decision := service.PolicyDecision{Action: "stop", Reason: replayStopReason(), Source: "system"}
		if decision.Reason == "" {
			decision = retry.DecideRelayRetry(rejected)
		}
		service.RecordPolicyFailure(c, s.lockedChannelID, rejected, decision)
		if decision.Action != "retry" {
			return false
		}
		if probeObserver != nil {
			service.RecordPassiveChannelProbeResult(probes, probeObserver.Result(c.Request.Context(), info, rejected))
			probeObserver = nil
		}
		idle.Stop()
		s.detachTarget()
		retry.IncreaseRetry()
		return true
	}
	for {
		select {
		case incoming := <-state.inbox:
			idle.Reset(timeout)
			if incoming.err != nil {
				if info.ChannelType == appconstant.ChannelTypeSub2API && retry.HasOrderedGroupRoute() && replayStopReason() == "" {
					if closed, ok := errors.AsType[*websocket.CloseError](incoming.err); ok && closed.Code == websocket.CloseTryAgainLater && closed.Text == "no available account" {
						rejected := types.WithOpenAIError(types.OpenAIError{Code: service.Sub2APIWSAccountUnavailable, Type: "api_error", Message: closed.Text}, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
						if retryBeforeAcceptance(rejected) {
							goto routeAttempt
						}
						s.detachTarget()
						return rejected
					}
				}
				finishSocket()
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, incoming.err)
				state.closeCode, state.closeReason = websocket.CloseInternalServerErr, "upstream connection closed"
				ConsumeResponsesQuota(c, info, accumulator.Finish())
				return nil
			}
			info.SetFirstResponseTime()
			var event struct {
				dto.ResponsesStreamResponse
				StreamID string `json:"stream_id"`
			}
			if err := common.Unmarshal(incoming.body, &event); err != nil {
				info.StreamStatus.RecordError("invalid upstream websocket event")
			} else {
				if event.Type != "error" && event.StreamID != "" && event.StreamID != create.StreamID {
					forwarded = true
					if err := s.writeClient(incoming.kind, incoming.body); err != nil {
						s.shutdown(websocket.CloseInternalServerErr, "")
					}
					continue
				}
				// A repeated terminal from the previous response must never finish
				// a subsequent request on this persistent connection.
				if event.Response != nil && event.Response.ID != "" && event.Response.ID == s.lastResponseID {
					continue
				}
				if event.Type == "error" {
					var rejection responsesWSErrorEvent
					_ = common.Unmarshal(incoming.body, &rejection)
					if rejection.ResponseID != "" && rejection.ResponseID == s.lastResponseID {
						continue
					}
					terminal, ambiguous, controlError := responsesWSErrorEndsRequest(rejection, create.StreamID, responseID, sentControl)
					if !terminal {
						// A stopped response sends nothing more before its error.
						if stopErr != nil {
							if controlError {
								sentControl = nil
							}
							continue
						}
						forwarded = true
						if err := s.writeClient(incoming.kind, incoming.body); err != nil {
							s.shutdown(websocket.CloseInternalServerErr, "")
						}
						// Only a control error in this stream resolves its pending control.
						if controlError {
							sentControl = nil
						}
						continue
					}
					if service.ObserveCyberPolicyPayload(c, incoming.body) {
						s.cyberBlockUntil = time.Now().Add(service.CyberSessionBlockTTL())
					}
					probeObserver.ObserveEvent(incoming.body)
					groupProbeObserver.ObserveEvent(incoming.body)
					upstreamError := rejection.Error
					if upstreamError == nil || upstreamError.Message == "" {
						if raw := ojson.GetBytes(incoming.body, "error"); raw.Type == ojson.String && service.IsUpstreamBalanceMessage(raw.Str) {
							upstreamError = &types.OpenAIError{Code: event.Code, Message: raw.Str}
						}
					}
					if upstreamError == nil {
						upstreamError = &types.OpenAIError{Type: "invalid_request_error", Message: event.Message, Code: event.Code}
					}
					rejected := types.WithOpenAIError(*upstreamError, rejection.Status, types.ErrOptionWithSkipRetry())
					if rejected.StatusCode < 400 || rejected.StatusCode > 599 {
						rejected.StatusCode = http.StatusBadRequest
					}
					if accepted {
						if rejection.Error != nil {
							code := ""
							if rejection.Error.Code != nil {
								code = fmt.Sprint(rejection.Error.Code)
							}
							info.StreamStatus.MarkFailed(code, rejection.Error.Type, rejection.Status)
						}
						accumulator.Observe(&event.ResponsesStreamResponse, incoming.body)
						info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
						s.lastResponseID = responseID
						state.terminal = terminalOf(incoming, rejected)
						if ambiguous {
							state.closeCode = websocket.CloseInternalServerErr
						}
						ConsumeResponsesQuota(c, info, accumulator.Finish())
						return nil
					}
					if !service.HasChannelFailover(c) && !retry.HasOrderedGroupRoute() {
						return rejected
					}
					if retryBeforeAcceptance(rejected) {
						goto routeAttempt
					}
					return rejected
				}
				if service.ObserveCyberPolicyPayload(c, incoming.body) {
					s.cyberBlockUntil = time.Now().Add(service.CyberSessionBlockTTL())
				}
				probeObserver.ObserveEvent(incoming.body)
				groupProbeObserver.ObserveEvent(incoming.body)
				if strings.HasPrefix(event.Type, "response.") {
					if !accepted {
						// Like HTTP, bind the session only once upstream accepted the request.
						service.RecordChannelAffinity(c, s.lockedChannelID)
					}
					accepted = true
					if event.Response != nil && event.Response.ID != "" {
						responseID = event.Response.ID
					}
				}
				accumulator.Observe(&event.ResponsesStreamResponse, incoming.body)
			}
			switch event.Type {
			case "response.completed", "response.done", "response.incomplete", "response.failed", "response.cancelled", "response.canceled":
				var upstreamError *types.OpenAIError
				if event.Response != nil {
					s.lastResponseID = event.Response.ID
					upstreamError = event.Response.GetOpenAIError()
				}
				if upstreamError == nil && event.Type == "response.failed" {
					var failure responsesWSErrorEvent
					_ = common.Unmarshal(incoming.body, &failure)
					upstreamError = failure.Error
					if upstreamError == nil && (event.Code != "" || event.Message != "") {
						upstreamError = &types.OpenAIError{Code: event.Code, Message: event.Message}
					}
				}
				var terminalError *types.NewAPIError
				if upstreamError != nil {
					terminalError = types.WithOpenAIError(*upstreamError, http.StatusBadGateway)
				}
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
				state.terminal = terminalOf(incoming, terminalError)
				ConsumeResponsesQuota(c, info, accumulator.Finish())
				return nil
			}
			if stopErr == nil {
				if socket != nil && incoming.kind == websocket.TextMessage {
					socket.Event(incoming.body)
					stopErr = socket.Stopped()
				} else {
					forwarded = true
					if err := s.writeClient(incoming.kind, incoming.body); err != nil {
						s.shutdown(websocket.CloseInternalServerErr, "")
					}
				}
			}
			if accepted && pendingControl != nil {
				if err := s.writeTarget(websocket.TextMessage, pendingControl); err != nil {
					s.shutdown(websocket.CloseInternalServerErr, "")
				}
				sentControl = pendingControl
				pendingControl = nil
			}
			if stopErr != nil && !cancelled && sentControl == nil {
				// A stop ends the response as a client cancel does; the
				// upstream terminal settles it and the client gets the error.
				cancelled = true
				cancel := map[string]string{"type": "response.cancel", "event_id": "evt_" + common.GetUUID()}
				if responseID != "" {
					cancel["response_id"] = responseID
				}
				if create.StreamID != "" {
					cancel["stream_id"] = create.StreamID
				}
				body, _ := common.Marshal(cancel)
				if err := s.writeTarget(websocket.TextMessage, body); err != nil {
					s.shutdown(websocket.CloseInternalServerErr, "")
				}
				sentControl = body
			}
		case control := <-state.controls:
			if stopErr != nil {
				// The response is already ending.
				continue
			}
			if pendingControl != nil || sentControl != nil {
				s.sendError(control.eventID, control.streamID, newResponsesWSInvalidRequestError(errors.New("a response control event is already pending")))
				continue
			}
			if !accepted {
				pendingControl = control.body
				continue
			}
			if err := s.writeTarget(websocket.TextMessage, control.body); err != nil {
				s.shutdown(websocket.CloseInternalServerErr, "")
			}
			sentControl = control.body
		case <-idle.C:
			finishSocket()
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonTimeout, context.DeadlineExceeded)
			state.closeCode, state.closeReason = websocket.CloseInternalServerErr, "upstream response timed out"
			ConsumeResponsesQuota(c, info, accumulator.Finish())
			return nil
		case <-c.Request.Context().Done():
			if s.ctx.Err() == nil {
				state.closeCode, state.closeReason = websocket.CloseInternalServerErr, "channel concurrency lease lost"
				s.shutdown(state.closeCode, state.closeReason)
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, context.Cause(c.Request.Context()))
			} else {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, s.ctx.Err())
			}
			ConsumeResponsesQuota(c, info, accumulator.Finish())
			return nil
		}
	}
}

// A single generation is active, but a cancel can fail independently. Correlate
// available IDs first; uncorrelated control/server errors require a bounded
// connection close because continuing could leave the generation stuck forever.
func responsesWSErrorEndsRequest(event responsesWSErrorEvent, streamID, responseID string, control []byte) (terminal, ambiguous, controlError bool) {
	if len(control) > 0 {
		var pending struct {
			EventID    string `json:"event_id"`
			StreamID   string `json:"stream_id"`
			ResponseID string `json:"response_id"`
		}
		_ = common.Unmarshal(control, &pending)
		if event.EventID != "" && event.EventID == pending.EventID {
			return false, false, true
		}
		if event.ResponseID != "" && event.ResponseID == pending.ResponseID && event.ResponseID != responseID {
			return false, false, true
		}
		if (event.StreamID == "" || event.StreamID == pending.StreamID) && event.Error != nil && event.Error.Type == "invalid_request_error" {
			switch event.Error.Code {
			case "response_not_found", "response_not_active", "response_already_completed":
				return false, false, true
			}
		}
	}
	if event.StreamID != "" && event.StreamID != streamID || responseID != "" && event.ResponseID != "" && event.ResponseID != responseID {
		return false, false, false
	}
	return true, len(control) > 0 && event.ResponseID == "", false
}

func (s *responsesWSSession) restoreConnectionContext(c *gin.Context, model string) *types.NewAPIError {
	// The channel type is not rechecked: a locked channel keeps its type, and
	// the FilterResponsesWebSocket rules were applied when it was selected.
	channel, err := appmodel.CacheGetChannel(s.lockedChannelID)
	if err != nil || channel == nil || channel.Status != common.ChannelStatusEnabled || !channel.GetSetting().ResponsesWebSocketEnabled {
		return types.NewErrorWithStatusCode(errors.New("Responses WebSocket is disabled for this channel"), types.ErrorCode(appdto.FilterResponsesWebSocket), http.StatusForbidden, types.ErrOptionWithSkipRetry())
	}
	if ok, kind := appmodel.ChannelSatisfiesFilters(channel, model, service.GetChannelConstraints(c).Filters); !ok {
		return types.NewErrorWithStatusCode(errors.New("the connection channel does not support this request; reconnect required"), types.ErrorCode(kind), http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	keyEnabled := channel.Key == s.lockedKey
	if channel.ChannelInfo.IsMultiKey {
		keys := channel.GetKeys()
		status := channel.ChannelInfo.MultiKeyStatusList[s.lockedKeyIndex]
		keyEnabled = s.lockedKeyIndex >= 0 && s.lockedKeyIndex < len(keys) && keys[s.lockedKeyIndex] == s.lockedKey && (status == 0 || status == common.ChannelStatusEnabled)
	}
	if !keyEnabled {
		return types.NewErrorWithStatusCode(errors.New("the upstream connection credential is no longer enabled"), types.ErrorCodeAccessDenied, http.StatusForbidden, types.ErrOptionWithSkipRetry())
	}
	// Changes that alter the physical upstream connection require a new
	// handshake. Request-level settings can be refreshed without rotating keys.
	if channel.GetBaseURL() != s.lockedContext[appconstant.ContextKeyChannelBaseUrl] ||
		!reflect.DeepEqual(channel.GetHeaderOverride(), s.lockedContext[appconstant.ContextKeyChannelHeaderOverride]) {
		return types.NewErrorWithStatusCode(errors.New("upstream connection settings changed; reconnect required"), types.ErrorCodeAccessDenied, http.StatusForbidden, types.ErrOptionWithSkipRetry())
	}
	if previous, ok := s.lockedContext[appconstant.ContextKeyChannelSetting].(dto.ChannelSettings); ok && previous.Proxy != channel.GetSetting().Proxy {
		return types.NewErrorWithStatusCode(errors.New("upstream proxy changed; reconnect required"), types.ErrorCodeAccessDenied, http.StatusForbidden, types.ErrOptionWithSkipRetry())
	}
	// An advanced custom connection was dialed through the route matched for
	// this model. Only the fields that shape the upstream handshake force a
	// reconnect: target path, protocol conversion and credential placement.
	// Other channel types match the zero route on both sides.
	if pinned, ok := s.lockedContext[appconstant.ContextKeyProviderPlugin]; ok {
		c.Set(string(appconstant.ContextKeyProviderPlugin), pinned)
	}
	otherSettings := connectionOtherSettings(c, channel)
	route, _ := otherSettings.AdvancedCustom.MatchPathForModel(c.Request.URL.Path, model)
	if strings.TrimSpace(route.UpstreamPath) != strings.TrimSpace(s.lockedRoute.UpstreamPath) ||
		route.IsNative() != s.lockedRoute.IsNative() ||
		!reflect.DeepEqual(route.Auth, s.lockedRoute.Auth) {
		return types.NewErrorWithStatusCode(errors.New("upstream route changed; reconnect required"), types.ErrorCodeAccessDenied, http.StatusForbidden, types.ErrOptionWithSkipRetry())
	}
	if pin, found, _ := service.GetChannelConstraints(c).ResolvedPin(); found {
		if pin.ChannelId != s.lockedChannelID {
			return types.NewErrorWithStatusCode(errors.New("channel pin changed; reconnect required"), types.ErrorCodeAccessDenied, http.StatusForbidden, types.ErrOptionWithSkipRetry())
		}
	} else {
		group := common.GetContextKeyString(c, appconstant.ContextKeyUsingGroup)
		if group == "auto" {
			if !slices.Contains(service.GetRequestAutoGroups(c, common.GetContextKeyString(c, appconstant.ContextKeyUserGroup)), s.lockedGroup) {
				return types.NewErrorWithStatusCode(errors.New("the connection group is no longer allowed"), types.ErrorCodeAccessDenied, http.StatusForbidden, types.ErrOptionWithSkipRetry())
			}
			group = s.lockedGroup
			common.SetContextKey(c, appconstant.ContextKeyAutoGroup, group)
		}
		if !appmodel.IsChannelEnabledForGroupModel(group, model, s.lockedChannelID) {
			return types.NewErrorWithStatusCode(errors.New("the connection channel is no longer allowed for this group and model"), types.ErrorCodeAccessDenied, http.StatusForbidden, types.ErrOptionWithSkipRetry())
		}
	}
	if capacityErr := service.AcquireChannelConcurrency(c, channel); capacityErr != nil {
		return capacityErr
	}
	for key, value := range s.lockedContext {
		c.Set(string(key), value)
	}
	common.SetContextKey(c, appconstant.ContextKeyChannelSetting, channel.GetSetting())
	if len(otherSettings.SupportedServiceTiers) > 0 && appmodel.IsGPTTextModel(channel.ResolveServiceTierModel(model)) {
		otherSettings.AllowServiceTier = true
	}
	common.SetContextKey(c, appconstant.ContextKeyChannelOtherSetting, otherSettings)
	common.SetContextKey(c, appconstant.ContextKeyChannelParamOverride, channel.GetParamOverride())
	common.SetContextKey(c, appconstant.ContextKeyChannelModelMapping, channel.GetModelMapping())
	common.SetContextKey(c, appconstant.ContextKeyChannelStatusCodeMapping, channel.GetStatusCodeMapping())
	// A waited turn opens a fresh socket before beginning its upstream attempt.
	if service.ChannelMaintenanceWaited(c) {
		return nil
	}
	policy := service.RequestPolicy(c)
	policy.BeginAttempt(channel, s.lockedGroup)
	policy.AddEvent(service.PolicyEvent{ChannelID: channel.Id, Decision: service.PolicyDecision{Action: "select", Reason: "pinned_channel", Source: "channel_constraint"}})
	return nil
}

// connectionOtherSettings is the channel's other settings as its requests use
// them: a task plugin channel runs on the relay routes of the plugin pinned
// for the connection, with the channel's plugin config.
func connectionOtherSettings(c *gin.Context, channel *appmodel.Channel) dto.ChannelOtherSettings {
	otherSettings := channel.GetOtherSettings()
	pinned, _ := common.GetContextKey(c, appconstant.ContextKeyProviderPlugin)
	if plugin, ok := pinned.(jsplugin.PinnedPlugin); ok && channel.Type == appconstant.ChannelTypeTaskPlugin {
		otherSettings.AdvancedCustom = plugin.Plugin.Meta.ChannelRelayConfig(plugin.Plugin.ChannelConfigFor(otherSettings.PluginConfig))
	}
	return otherSettings
}

// reloadResponsesWSCreate decodes a create body the request plugins' ingress
// changed, with the checks the first decode applied.
func reloadResponsesWSCreate(c *gin.Context, create *responsesWSCreateRequest) *types.NewAPIError {
	storage, err := common.GetBodyStorage(c)
	var body []byte
	if err == nil {
		body, err = storage.Bytes()
	}
	var request dto.OpenAIResponsesRequest
	if err == nil {
		err = common.Unmarshal(body, &request)
	}
	if err == nil && helper.ExceedsMaxTokensLimit(request.MaxOutputTokens) {
		err = errors.New("max_output_tokens is invalid")
	}
	if err != nil {
		return newResponsesWSInvalidRequestError(err)
	}
	request.Stream = nil
	request.StreamOptions = nil
	create.Request = request
	return nil
}

// recordResponsesWSPluginError writes the error log of a create a request
// plugin stopped; it shows what the plugins did.
func recordResponsesWSPluginError(c *gin.Context, info *relaycommon.RelayInfo, apiErr *types.NewAPIError) {
	switch apiErr.GetErrorCode() {
	case types.ErrorCodeRequestRejected, types.ErrorCodeRequestPluginFailed:
		service.RecordRelayErrorLog(c, info.ChannelId, apiErr, info)
	}
}

func buildResponsesWSCreatePayload(c *gin.Context, info *relaycommon.RelayInfo, req dto.OpenAIResponsesRequest, generate common.RawMessage, streamID string) ([]byte, *types.NewAPIError) {
	_, body, closer, apiErr := PrepareResponsesRequest(c, info, &req)
	if apiErr != nil {
		return nil, apiErr
	}
	defer closer.Close()
	jsonData, err := io.ReadAll(body)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeReadRequestBodyFailed, types.ErrOptionWithSkipRetry())
	}
	service.MarkMediaGenerationRequestBody(c, info, jsonData)
	jsonData, tier, err := info.ApplyOutboundServiceTierPolicy(c, jsonData)
	if err != nil {
		var apiErr *types.NewAPIError
		if errors.As(err, &apiErr) {
			return nil, apiErr
		}
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	if apiErr := service.PrepareServiceTierBilling(c, info, tier); apiErr != nil {
		return nil, apiErr
	}
	event, err := buildResponsesWSCreateEvent(jsonData, generate, streamID)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	return event, nil
}

func (s *responsesWSSession) startTargetReader(target *websocket.Conn) {
	limit := int64(helper.DefaultMaxScannerBufferSize)
	if appconstant.StreamScannerMaxBufferMB > 0 {
		limit = int64(appconstant.StreamScannerMaxBufferMB) << 20
	}
	target.SetReadLimit(limit)
	readerContext, cancelReader := context.WithCancel(s.ctx)
	done := make(chan struct{})
	s.connectionMu.Lock()
	s.targetCancel, s.targetDone = cancelReader, done
	s.connectionMu.Unlock()
	s.workers.Go(func() {
		defer close(done)
		defer cancelReader()
		for {
			kind, body, err := target.ReadMessage()
			if readerContext.Err() != nil || s.getTarget() != target {
				return
			}
			incoming := responsesWSMessage{kind: kind, body: body, err: err}
			if state := s.getCurrent(); state != nil {
				select {
				case state.inbox <- incoming:
					if err != nil {
						return
					}
					continue
				case <-state.done:
				case <-readerContext.Done():
					return
				}
			}
			// No request owns this message. An idle upstream that closed
			// normally ends the client session with the same code.
			if err != nil {
				code := websocket.CloseInternalServerErr
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					code = err.(*websocket.CloseError).Code
				}
				s.shutdown(code, "upstream connection closed")
				return
			}
			if writeErr := s.writeClient(kind, body); writeErr != nil {
				s.shutdown(websocket.CloseInternalServerErr, "")
				return
			}
		}
	})
}

// Detach only between creates or after an explicit rejection before output.
// The old reader exits before the new connection can deliver frames, so a
// close/error from the retired socket cannot end the replacement request.
func (s *responsesWSSession) detachTarget() {
	s.connectionMu.Lock()
	target, unregister, cancelReader, done := s.target, s.unregister, s.targetCancel, s.targetDone
	s.target, s.unregister, s.targetCancel, s.targetDone = nil, nil, nil, nil
	s.connectionMu.Unlock()
	if unregister != nil {
		unregister()
	}
	if cancelReader != nil {
		cancelReader()
	}
	if target != nil {
		_ = target.Close()
	}
	if done != nil {
		<-done
	}
	if state := s.getCurrent(); state != nil {
		for len(state.inbox) > 0 {
			<-state.inbox
		}
	}
	s.lockedChannelID = 0
}

func (s *responsesWSSession) getCurrent() *responsesWSCallState {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.current
}

func (s *responsesWSSession) tryReserveCurrent(state *responsesWSCallState) bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.current != nil {
		return false
	}
	s.current = state
	return true
}

func (s *responsesWSSession) getTarget() *websocket.Conn {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	return s.target
}

func (s *responsesWSSession) setTarget(target *websocket.Conn) bool {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	if s.ctx.Err() != nil {
		_ = target.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		_ = target.Close()
		return false
	}
	s.target = target
	return true
}

func (s *responsesWSSession) writeTarget(kind int, message []byte) error {
	s.targetWriteMu.Lock()
	defer s.targetWriteMu.Unlock()
	target := s.getTarget()
	if target == nil {
		return errors.New("responses websocket upstream is not connected")
	}
	if err := target.SetWriteDeadline(time.Now().Add(responsesWSWriteTimeout)); err != nil {
		return err
	}
	return target.WriteMessage(kind, message)
}

func (s *responsesWSSession) writeClient(kind int, message []byte) error {
	s.clientWriteMu.Lock()
	defer s.clientWriteMu.Unlock()
	if err := s.client.SetWriteDeadline(s.clientWriteBudget.Deadline(time.Now(), len(message))); err != nil {
		return err
	}
	return s.client.WriteMessage(kind, message)
}

func (s *responsesWSSession) sendError(eventID, streamID string, apiErr *types.NewAPIError) {
	if apiErr == nil {
		return
	}
	payload, err := buildResponsesWSErrorPayload(eventID, streamID, apiErr)
	if err == nil {
		_ = s.writeClient(websocket.TextMessage, payload)
	}
}

// shutdown ends the connection once. Each peer gets a close frame that says
// why, bounded to one second, and both sockets close even if a write failed.
// Close frames are control writes, which gorilla allows alongside a blocked
// data writer, so shutdown takes no write lock and never waits for workers.
func (s *responsesWSSession) shutdown(code int, reason string) {
	s.closeOnce.Do(func() {
		s.cancel()
		s.connectionMu.Lock()
		target, unregister := s.target, s.unregister
		s.target, s.unregister = nil, nil
		s.connectionMu.Unlock()
		if unregister != nil {
			unregister()
		}
		closeMessage := websocket.FormatCloseMessage(code, reason)
		_ = s.client.WriteControl(websocket.CloseMessage, closeMessage, time.Now().Add(time.Second))
		if target != nil {
			_ = target.WriteControl(websocket.CloseMessage, closeMessage, time.Now().Add(time.Second))
			_ = target.Close()
		}
		_ = s.client.Close()
	})
}

func (s *responsesWSSession) registerChannelClose(channelID int) {
	unregister := wsmanager.Register(channelID, wsmanager.KindResponses, s.closeForPolicy)
	s.connectionMu.Lock()
	if s.ctx.Err() != nil {
		s.connectionMu.Unlock()
		unregister()
		return
	}
	s.unregister = unregister
	s.connectionMu.Unlock()
}

// closeForPolicy tells both peers why the connection ends, like the realtime
// relay does.
func (s *responsesWSSession) closeForPolicy(reason string) {
	s.shutdown(websocket.ClosePolicyViolation, reason)
}

// Stream identity belongs to the WebSocket envelope. For the legacy wrapped
// input, the top-level field takes precedence over response.stream_id.
func parseResponsesWSEnvelope(message []byte) (responsesWSCreateEvent, string, error) {
	var event responsesWSCreateEvent
	if err := common.Unmarshal(message, &event); err != nil {
		return event, "", fmt.Errorf("invalid websocket event json: %w", err)
	}
	streamRaw := event.StreamID
	if len(streamRaw) == 0 && len(event.Request) > 0 {
		var wrapped struct {
			StreamID common.RawMessage `json:"stream_id"`
		}
		if err := common.Unmarshal(event.Request, &wrapped); err == nil {
			streamRaw = wrapped.StreamID
		}
	}
	var streamID string
	if len(streamRaw) > 0 {
		if err := common.Unmarshal(streamRaw, &streamID); err != nil || len(streamID) < 1 || len(streamID) > 256 {
			return event, "", errors.New("stream_id must contain 1-256 ASCII letters, digits, underscores, hyphens, or periods")
		}
		for _, char := range streamID {
			if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' || char == '.') {
				return event, "", errors.New("stream_id must contain only ASCII letters, digits, underscores, hyphens, or periods")
			}
		}
	}
	if strings.TrimSpace(event.Type) == "" {
		return event, streamID, errors.New("websocket event type is required")
	}
	return event, streamID, nil
}

func newResponsesWSInvalidRequestError(err error) *types.NewAPIError {
	return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
}

// normalizeResponsesWSCreateEvent builds the HTTP-shaped request body from a
// response.create whose envelope the read loop already parsed and validated.
// The body is the message with the WebSocket members removed, and is decoded
// once as the typed request; the envelope is not parsed again.
func normalizeResponsesWSCreateEvent(message []byte, event responsesWSCreateEvent, streamID string) (responsesWSCreateRequest, error) {
	create := responsesWSCreateRequest{StreamID: streamID, Generate: event.Generate, EventID: event.EventID}
	// Edit the wrapped request on its own so outer fields cannot enter the HTTP body.
	source := message
	if len(event.Request) > 0 {
		source = event.Request
	}
	if !ojson.GetBytesNoCopy(source, "@this").IsObject() {
		return create, errors.New("response.create request must be a JSON object")
	}
	doc := ojson.NewDoc(source)
	if len(event.Request) > 0 {
		if generate := doc.Get("generate"); len(create.Generate) == 0 && generate.Exists() {
			create.Generate = common.RawMessage(generate.Raw)
		}
	} else {
		for _, key := range []string{"type", "event_id", "background", "stream", "stream_options"} {
			if err := relaycommon.DeleteJSONMember(doc, key); err != nil {
				return create, err
			}
		}
	}
	for _, key := range []string{"generate", "stream_id"} {
		if err := relaycommon.DeleteJSONMember(doc, key); err != nil {
			return create, err
		}
	}
	payload := doc.Bytes()
	if err := common.Unmarshal(payload, &create.Request); err != nil {
		return create, err
	}
	if helper.ExceedsMaxTokensLimit(create.Request.MaxOutputTokens) {
		return create, errors.New("max_output_tokens is invalid")
	}
	create.Request.Stream = nil
	create.Request.StreamOptions = nil
	create.Body = payload
	return create, nil
}

func buildResponsesWSCreateEvent(jsonData []byte, generate common.RawMessage, streamID string) ([]byte, error) {
	if !ojson.ValidBytes(jsonData) || !ojson.GetBytesNoCopy(jsonData, "@this").IsObject() {
		return nil, errors.New("response.create body must be a JSON object")
	}
	event := ojson.NewDoc(jsonData)
	// The normalized body never carries stream_id; a channel parameter
	// override may inject one, and it must not shadow the envelope's value.
	keys := []string{"type", "stream_id", "event_id", "background", "stream", "stream_options"}
	if len(generate) > 0 {
		keys = append(keys, "generate")
	}
	for _, key := range keys {
		if err := relaycommon.DeleteJSONMember(event, key); err != nil {
			return nil, err
		}
	}
	if err := event.Set("type", responsesWSEventTypeResponseCreate); err != nil {
		return nil, err
	}
	if streamID != "" {
		if err := event.Set("stream_id", streamID); err != nil {
			return nil, err
		}
	}
	if len(generate) > 0 {
		if err := event.SetRawBytes("generate", generate); err != nil {
			return nil, err
		}
	}
	return event.Bytes(), nil
}

func buildResponsesWSErrorPayload(eventID, streamID string, apiErr *types.NewAPIError, upstreamURLs ...string) ([]byte, error) {
	if apiErr == nil {
		return nil, errors.New("api error is nil")
	}
	status := apiErr.StatusCode
	if status == 0 {
		status = http.StatusInternalServerError
	}
	openaiErr := apiErr.ToOpenAIError(upstreamURLs...)
	return common.Marshal(&responsesWSErrorEvent{
		Type:     "error",
		Status:   status,
		EventID:  eventID,
		StreamID: streamID,
		Error:    &openaiErr,
	})
}

// checkResponsesWSModelAccess applies the token model limit with the same name
// matching as the HTTP distributor. Unlike HTTP, it also runs for requests
// pinned to a channel: the check is stricter there on purpose, because a
// persistent connection keeps serving the model after the pin was resolved.
func checkResponsesWSModelAccess(c *gin.Context, modelName string) *types.NewAPIError {
	if !common.GetContextKeyBool(c, appconstant.ContextKeyTokenModelLimitEnabled) {
		return nil
	}
	raw, ok := common.GetContextKey(c, appconstant.ContextKeyTokenModelLimit)
	if !ok {
		return types.NewErrorWithStatusCode(errors.New("token has no model access"), types.ErrorCodeAccessDenied, http.StatusForbidden, types.ErrOptionWithSkipRetry())
	}
	tokenModelLimit, ok := raw.(map[string]bool)
	if !ok {
		tokenModelLimit = map[string]bool{}
	}
	if !middleware.TokenModelLimitAllows(tokenModelLimit, modelName) {
		return types.NewErrorWithStatusCode(fmt.Errorf("token is not allowed to use model %s", modelName), types.ErrorCodeAccessDenied, http.StatusForbidden, types.ErrOptionWithSkipRetry())
	}
	return nil
}

func selectResponsesWSChannel(c *gin.Context, modelName string, retryParam *service.RetryParam) (*appmodel.Channel, *types.NewAPIError) {
	channel, apiErr := chooseResponsesWSChannel(c, modelName, retryParam)
	if apiErr != nil {
		return nil, apiErr
	}
	if err := middleware.SetupContextForSelectedChannel(c, channel, modelName); err != nil {
		return nil, err
	}
	return channel, nil
}

// chooseResponsesWSChannel selects a Responses WebSocket-capable channel
// without rotating the upstream credential of a connection that can be reused.
func chooseResponsesWSChannel(c *gin.Context, modelName string, retryParam *service.RetryParam) (*appmodel.Channel, *types.NewAPIError) {
	constraints := service.GetChannelConstraints(c)
	if !slices.ContainsFunc(constraints.Filters, func(filter appdto.ChannelFilter) bool {
		return filter.Kind == appdto.FilterResponsesWebSocket
	}) {
		constraints.AddFilter(appdto.ChannelFilter{Kind: appdto.FilterResponsesWebSocket})
	}
	channel, _, selectErr := service.SelectChannelForRequest(c, modelName, retryParam)
	if selectErr != nil {
		if selectErr.Upstream != nil {
			return nil, selectErr.Upstream
		}
		message := selectErr.Message
		if selectErr.MessageID != "" {
			message = i18n.T(c, selectErr.MessageID, selectErr.Params)
		}
		code := selectErr.Code
		if code == "" {
			code = types.ErrorCodeGetChannelFailed
		}
		return nil, types.NewErrorWithStatusCode(errors.New(message), code, selectErr.StatusCode, types.ErrOptionWithSkipRetry())
	}
	return channel, nil
}
