package service

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/pkg/cachex"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/samber/hot"
	"github.com/tidwall/gjson"
)

const (
	cyberSessionStateKey                    = "cyber_session_block_state"
	cyberSessionBlockedCode types.ErrorCode = "session_blocked_by_cyber_policy"
)

type cyberSessionState struct {
	key       string
	kind      string
	checked   bool
	marked    bool
	rejection *types.NewAPIError
}

var cyberSessionBlockCache = sync.OnceValue(newCyberSessionBlockCache)
var cyberSessionBlockNow = time.Now

func newCyberSessionBlockCache() *cachex.HybridCache[string] {
	return cachex.NewHybridCache(cachex.HybridCacheConfig[string]{
		Namespace:    cachex.Namespace("new-api:cyber_session_block:v1"),
		Redis:        common.RDB,
		RedisEnabled: func() bool { return common.RedisEnabled && common.RDB != nil },
		RedisCodec:   cachex.StringCodec{},
		Memory: func() *hot.HotCache[string, string] {
			return hot.NewHotCache[string, string](hot.LRU, 100_000).WithTTL(time.Hour).WithJanitor().Build()
		},
	})
}

// CheckCyberSessionBlock runs after authentication and before routing or billing.
// An explicit thread/session belongs to the authenticated user and API token,
// independently of the selected model, group, or upstream channel.
func CheckCyberSessionBlock(c *gin.Context) *types.NewAPIError {
	if c == nil || c.Request == nil || !CyberSessionBlockEnabled() {
		return nil
	}
	state := cyberSessionForRequest(c)
	if state.checked {
		return state.rejection
	}
	state.checked = true
	if state.key == "" {
		return nil
	}
	value, found, err := cyberSessionBlockCache().Get(state.key)
	if err != nil {
		common.SysError(common.LogText("cyber session block lookup failed: %v", err))
		return nil
	}
	if !found {
		return nil
	}
	expiresAt, err := strconv.ParseInt(value, 10, 64)
	remaining := time.UnixMilli(expiresAt).Sub(cyberSessionBlockNow())
	if err != nil || remaining <= 0 {
		return nil
	}
	c.Header("Retry-After", strconv.FormatInt(int64((remaining+time.Second-1)/time.Second), 10))
	state.rejection = CyberSessionBlockedError(c)
	return state.rejection
}

func CyberSessionBlockEnabled() bool {
	return common.GetEnvOrDefaultBool("CYBER_SESSION_BLOCK_ENABLED", true)
}

func CyberSessionBlockTTL() time.Duration {
	seconds := common.GetEnvOrDefault("CYBER_SESSION_BLOCK_TTL_SECONDS", 3600)
	if seconds < 1 || seconds > 86400 {
		seconds = 3600
	}
	return time.Duration(seconds) * time.Second
}

func CyberSessionBlockedError(c *gin.Context) *types.NewAPIError {
	RequestPolicy(c).AddEvent(PolicyEvent{Status: http.StatusForbidden, ErrorCode: string(cyberSessionBlockedCode), ErrorSource: "local", Health: "unchanged", Decision: PolicyDecision{Action: "stop", Reason: "cyber_session_blocked", Source: "cyber_policy"}})
	return types.NewErrorWithStatusCode(errors.New(i18n.T(c, i18n.MsgRelayCyberSessionBlocked)), cyberSessionBlockedCode, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
}

// IsCyberPolicyError uses machine-readable codes, never error message keywords.
// An existing upstream block has the same retry/health semantics as a new hit.
func IsCyberPolicyError(apiErr *types.NewAPIError) bool {
	if apiErr == nil {
		return false
	}
	return isCyberPolicyCode(string(apiErr.GetErrorCode()))
}

func isCyberPolicyCode(code string) bool {
	return strings.EqualFold(strings.TrimSpace(code), "cyber_policy") || strings.EqualFold(strings.TrimSpace(code), string(cyberSessionBlockedCode))
}

func ObserveCyberPolicyError(c *gin.Context, apiErr *types.NewAPIError) {
	if IsCyberPolicyError(apiErr) {
		markCyberSessionBlocked(c)
	}
}

func HasCyberSessionIdentity(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	state := cyberSessionForRequest(c)
	return state.key != "" && (state.kind == "thread" || state.kind == "session")
}

// CyberPolicyErrorFromPayload preserves structured policy failures before a
// protocol conversion can replace the original error envelope.
func CyberPolicyErrorFromPayload(payload []byte, status int) *types.NewAPIError {
	code := cyberPolicyPayloadCode(payload)
	if code == "" {
		return nil
	}
	raw := gjson.GetBytes(payload, "error")
	if strings.TrimSpace(gjson.GetBytes(payload, "error.code").String()) == "" {
		raw = gjson.GetBytes(payload, "response.error")
	}
	if !raw.IsObject() {
		raw = gjson.ParseBytes(payload)
	}
	var upstream types.OpenAIError
	_ = common.Unmarshal([]byte(raw.Raw), &upstream)
	upstream.Code = code
	if status < 400 || status > 599 {
		status = http.StatusForbidden
	}
	if upstream.Message == "" {
		upstream.Message = http.StatusText(status)
	}
	return types.WithOpenAIError(upstream, status, types.ErrOptionWithSkipRetry())
}

// ObserveCyberPolicyPayload observes JSON error envelopes and SSE/WS failure
// events before protocol conversion. It does not change the forwarded payload.
func ObserveCyberPolicyPayload(c *gin.Context, payload []byte) bool {
	if c == nil || cyberPolicyPayloadCode(payload) == "" {
		return false
	}
	markCyberSessionBlocked(c)
	return true
}

func cyberPolicyPayloadCode(payload []byte) string {
	eventType := gjson.GetBytes(payload, "type").String()
	if eventType != "" && eventType != "error" && eventType != "response.error" && eventType != "response.failed" {
		return ""
	}
	code := gjson.GetBytes(payload, "error.code")
	if code.Type == gjson.Null || code.Type == gjson.String && strings.TrimSpace(code.Str) == "" {
		code = gjson.GetBytes(payload, "response.error.code")
	}
	if code.Type == gjson.Null && (eventType == "error" || eventType == "response.error") {
		code = gjson.GetBytes(payload, "code")
	}
	if code.Type != gjson.String || !isCyberPolicyCode(code.Str) || !gjson.ValidBytes(payload) {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(code.Str))
}

func markCyberSessionBlocked(c *gin.Context) {
	if c == nil || c.Request == nil || !CyberSessionBlockEnabled() {
		return
	}
	state := cyberSessionForRequest(c)
	if state.key == "" || state.marked {
		return
	}
	state.marked = true
	ttl := CyberSessionBlockTTL()
	expiresAt := strconv.FormatInt(cyberSessionBlockNow().Add(ttl).UnixMilli(), 10)
	if err := cyberSessionBlockCache().SetWithTTL(state.key, expiresAt, ttl); err != nil {
		common.SysError(common.LogText("cyber session block write failed: %v", err))
	}
}

func cyberSessionForRequest(c *gin.Context) *cyberSessionState {
	if value, ok := c.Get(cyberSessionStateKey); ok {
		return value.(*cyberSessionState)
	}
	state := &cyberSessionState{}
	c.Set(cyberSessionStateKey, state)
	userID := common.GetContextKeyInt(c, constant.ContextKeyUserId)
	if userID <= 0 || c.Request == nil {
		return state
	}
	var body []byte
	if strings.HasPrefix(c.GetHeader("Content-Type"), "application/json") {
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return state
		}
		body, err = storage.Bytes()
		if err != nil {
			return state
		}
		// GetBodyStorage may consume the incoming reader on the first call.
		c.Request.Body = io.NopCloser(storage)
	}
	kind, identity, valid := cyberSessionIdentity(c.Request.Header, body)
	if !valid {
		return state
	}
	if identity == "" {
		// With no explicit identity, only exact content retries are comparable.
		// Never query this fallback when a distinct explicit session is supplied.
		kind = "request"
		contentBody := body
		if response := gjson.GetBytes(body, "response"); response.IsObject() && gjson.GetBytes(body, "type").String() == "response.create" {
			contentBody = common.StringToByteSlice(response.Raw)
		}
		var content strings.Builder
		for _, field := range []string{"messages", "input", "instructions", "system", "prompt", "tools", "tool_choice", "previous_response_id", "conversation", "thread_id", "session_id", "text"} {
			value := gjson.GetBytes(contentBody, field)
			if value.Exists() {
				content.WriteString(field)
				content.WriteByte('=')
				content.WriteString(value.Raw)
				content.WriteByte('\n')
			}
		}
		identity = content.String()
		if identity == "" {
			return state
		}
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "cyber-session:v1|user=%d|token=%d|kind=%s|id=%s", userID, common.GetContextKeyInt(c, constant.ContextKeyTokenId), kind, identity))
	state.key = fmt.Sprintf("%x", sum)
	state.kind = kind
	return state
}

// cyberSessionIdentity follows Sub2API's thread-before-session precedence.
// Conflicting or invalid identifiers never create a cross-request block.
func cyberSessionIdentity(headers http.Header, body []byte) (kind, identity string, valid bool) {
	for _, source := range []struct {
		kind    string
		headers []string
	}{
		{"thread", []string{"conversation_id", "thread_id", "thread-id", "X-Conversation-ID"}},
		{"session", []string{"session_id", "session-id", "X-Session-Id", "X-OpenCode-Session"}},
	} {
		var values []string
		for _, name := range source.headers {
			values = append(values, headers.Values(name)...)
		}
		for _, prefix := range []string{"client_metadata.", "response.client_metadata."} {
			value := gjson.GetBytes(body, prefix+source.kind+"_id")
			if value.Type == gjson.Null {
				continue
			}
			if value.Type != gjson.String {
				return "", "", false
			}
			values = append(values, value.Str)
		}
		for _, raw := range values {
			value := strings.TrimSpace(raw)
			if value == "" {
				continue
			}
			if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 255 || strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
				return "", "", false
			}
			if identity != "" && identity != value {
				return "", "", false
			}
			identity = value
		}
		if identity != "" {
			return source.kind, identity, true
		}
	}
	return "", "", true
}
