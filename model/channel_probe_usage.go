package model

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"golang.org/x/sync/singleflight"
)

const (
	channelProbeUsageLimit   = 1000
	channelProbeUsageMinimum = 10
	channelProbeUsageTTL     = 30 * time.Second
)

// ChannelProbeUsageStats estimates realized cost at a 1x group ratio from at most
// the newest 1000 consume logs in the selected scope/window. Each metric needs
// 10 independently eligible requests. Cache hit rate is input-token weighted;
// price is sum(actual quota / historical effective group_ratio) / sum(input+output)
// * 1e8 / QuotaPerUnit, in system USD. The service applies the viewer's current
// effective group ratio. This is not a configured upstream price.
type ChannelProbeUsageStats struct {
	AverageCacheHitRate         *float64 `json:"average_cache_hit_rate"`
	EstimatedPricePer100MTokens *float64 `json:"estimated_price_per_100m_tokens"`
	CacheSampleCount            int64    `json:"cache_sample_count"`
	PriceSampleCount            int64    `json:"price_sample_count"`
}

var channelProbeUsageCache = struct {
	sync.Mutex
	entries map[string]channelProbeUsageCacheEntry
	loads   singleflight.Group
}{entries: make(map[string]channelProbeUsageCacheEntry)}

type channelProbeUsageCacheEntry struct {
	stats   ChannelProbeUsageStats
	expires time.Time
}

func GetChannelProbeUsageStats(probe ChannelProbe, name string, start, end int64) (ChannelProbeUsageStats, error) {
	if !common.LogConsumeEnabled || LOG_DB == nil || start >= end {
		return ChannelProbeUsageStats{}, nil
	}
	logDB := LOG_DB
	quotaPerUnit := common.QuotaPerUnit
	key := fmt.Sprintf("%p:%s:%d:%q:%q:%d:%d:%g", logDB, probe.Scope, probe.ChannelID, probe.GroupName, name, start, end, quotaPerUnit)
	result, err, _ := channelProbeUsageCache.loads.Do(key, func() (any, error) {
		channelProbeUsageCache.Lock()
		cached, found := channelProbeUsageCache.entries[key]
		channelProbeUsageCache.Unlock()
		if found && time.Now().Before(cached.expires) {
			return cached.stats, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		query := logDB.WithContext(ctx).Model(&Log{}).
			Select("prompt_tokens", "completion_tokens", "quota", "other").
			Where("type = ? AND model_name = ? AND created_at >= ? AND created_at < ?", LogTypeConsume, name, start, end)
		switch probe.Scope {
		case "group":
			if probe.GroupName == "" {
				return ChannelProbeUsageStats{}, nil
			}
			query = query.Where(map[string]any{"group": probe.GroupName})
		case "upstream":
			if probe.ChannelID <= 0 {
				return ChannelProbeUsageStats{}, nil
			}
			query = query.Where("channel_id = ?", probe.ChannelID)
		default:
			return ChannelProbeUsageStats{}, nil
		}
		order := "created_at desc, id desc"
		if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
			order = clickHouseLogOrder("")
		}
		var logs []Log
		if err := query.Order(order).Limit(channelProbeUsageLimit).Find(&logs).Error; err != nil {
			return ChannelProbeUsageStats{}, err
		}
		stats := aggregateChannelProbeUsage(logs, quotaPerUnit)
		channelProbeUsageCache.Lock()
		defer channelProbeUsageCache.Unlock()
		// Bound retained scope/window keys as well as rows per database query.
		if len(channelProbeUsageCache.entries) >= 4096 {
			for k, entry := range channelProbeUsageCache.entries {
				if !time.Now().Before(entry.expires) {
					delete(channelProbeUsageCache.entries, k)
				}
			}
			if len(channelProbeUsageCache.entries) >= 4096 {
				clear(channelProbeUsageCache.entries)
			}
		}
		channelProbeUsageCache.entries[key] = channelProbeUsageCacheEntry{stats: stats, expires: time.Now().Add(channelProbeUsageTTL)}
		return stats, nil
	})
	if err != nil {
		return ChannelProbeUsageStats{}, err
	}
	return result.(ChannelProbeUsageStats), nil
}

type channelProbeUsageMetadata struct {
	CacheTokens          *int64              `json:"cache_tokens"`
	CacheWriteTokens     *int64              `json:"cache_write_tokens"`
	CacheCreationTokens  *int64              `json:"cache_creation_tokens"`
	CacheCreation5m      *int64              `json:"cache_creation_tokens_5m"`
	CacheCreation1h      *int64              `json:"cache_creation_tokens_1h"`
	InputTokensTotal     *int64              `json:"input_tokens_total"`
	UsageSemantic        string              `json:"usage_semantic"`
	Claude               bool                `json:"claude"`
	BillingMode          string              `json:"billing_mode"`
	BillingUnit          string              `json:"billing_unit"`
	ModelPrice           *float64            `json:"model_price"`
	ModelRatio           *float64            `json:"model_ratio"`
	GroupRatio           *float64            `json:"group_ratio"`
	FixedPrice           *float64            `json:"fixed_price"`
	BillingSource        string              `json:"billing_source"`
	SubscriptionConsumed *int64              `json:"subscription_consumed"`
	RequestPath          string              `json:"request_path"`
	Image                bool                `json:"image"`
	Audio                bool                `json:"audio"`
	WS                   bool                `json:"ws"`
	IsTask               bool                `json:"is_task"`
	TaskSync             bool                `json:"task_sync"`
	TaskID               string              `json:"task_id"`
	ImageCount           *float64            `json:"image_count"`
	ImageOutput          float64             `json:"image_output"`
	ImageCacheTokens     float64             `json:"image_cache_tokens"`
	AudioInput           float64             `json:"audio_input"`
	AudioOutput          float64             `json:"audio_output"`
	AudioSeparate        bool                `json:"audio_input_seperate_price"`
	AudioInputTokenCount float64             `json:"audio_input_token_count"`
	BillingTokens        map[string]float64  `json:"billing_tokens"`
	ToolSurcharges       []common.RawMessage `json:"tool_surcharges"`
	WebSearch            bool                `json:"web_search"`
	FileSearch           bool                `json:"file_search"`
	ImageGenerationCall  bool                `json:"image_generation_call"`
	ViolationFee         bool                `json:"violation_fee"`
	ViolationFeeCode     string              `json:"violation_fee_code"`
	ViolationFeeMarker   string              `json:"violation_fee_marker"`
	RejectReason         string              `json:"reject_reason"`
	QuotaSaturation      common.RawMessage   `json:"quota_saturation"`
	Admin                struct {
		RejectReason     string            `json:"reject_reason"`
		QuotaSaturation  common.RawMessage `json:"quota_saturation"`
		LocalCountTokens bool              `json:"local_count_tokens"`
		UsageBillingPath string            `json:"usage_billing_path"`
	} `json:"admin_info"`
	StreamStatus struct {
		Status         string `json:"status"`
		EndReason      string `json:"end_reason"`
		ResponseStatus string `json:"response_status"`
		ErrorCount     int    `json:"error_count"`
	} `json:"stream_status"`
}

func aggregateChannelProbeUsage(logs []Log, quotaPerUnit float64) ChannelProbeUsageStats {
	var stats ChannelProbeUsageStats
	var cacheInput, cachedTokens, pricedTokens, baseQuota float64
	for _, log := range logs {
		var other channelProbeUsageMetadata
		if log.PromptTokens < 0 || log.CompletionTokens < 0 || log.Quota < 0 || int64(log.Quota) > 1<<53-1 || common.UnmarshalJsonStr(log.Other, &other) != nil {
			continue
		}
		if other.Image || other.Audio || other.WS || other.IsTask || other.TaskSync || other.TaskID != "" || other.ImageCount != nil ||
			other.ImageOutput != 0 || other.ImageCacheTokens != 0 || other.AudioInput != 0 || other.AudioOutput != 0 || other.AudioSeparate || other.AudioInputTokenCount != 0 ||
			other.BillingTokens["img"] != 0 || other.BillingTokens["img_cr"] != 0 || other.BillingTokens["img_o"] != 0 || other.BillingTokens["ai"] != 0 || other.BillingTokens["ao"] != 0 ||
			other.FixedPrice != nil || other.ViolationFee || other.ViolationFeeCode != "" || other.ViolationFeeMarker != "" ||
			other.RejectReason != "" || other.Admin.RejectReason != "" || len(other.Admin.QuotaSaturation) > 0 || len(other.QuotaSaturation) > 0 ||
			(other.StreamStatus.Status != "" && other.StreamStatus.Status != "ok") || other.StreamStatus.ErrorCount != 0 ||
			(other.StreamStatus.ResponseStatus != "" && other.StreamStatus.ResponseStatus != "completed") ||
			(other.StreamStatus.EndReason != "" && other.StreamStatus.EndReason != "done" && other.StreamStatus.EndReason != "eof") ||
			other.Admin.LocalCountTokens || other.Admin.UsageBillingPath == "local" || strings.HasSuffix(other.Admin.UsageBillingPath, "-estimated") {
			continue
		}
		// These paths also identify old media/task logs without modern markers.
		if strings.Contains(other.RequestPath, "/images") || strings.Contains(other.RequestPath, "/audio") || strings.Contains(other.RequestPath, "/realtime") ||
			strings.Contains(other.RequestPath, "/tasks") || strings.Contains(other.RequestPath, "/videos") || strings.Contains(other.RequestPath, "/mj/") {
			continue
		}
		if other.BillingMode == "tiered_expr" {
			if other.BillingUnit != "token" {
				continue
			}
		} else if (other.BillingMode != "" && other.BillingMode != "ratio") || (other.BillingUnit != "" && other.BillingUnit != "token") ||
			other.ModelRatio == nil || *other.ModelRatio < 0 || (other.ModelPrice != nil && *other.ModelPrice != -1) {
			continue
		}
		valid := true
		for _, count := range []*int64{other.CacheTokens, other.CacheWriteTokens, other.CacheCreationTokens, other.CacheCreation5m, other.CacheCreation1h, other.InputTokensTotal} {
			if count != nil && (*count < 0 || *count > 1<<53-1) {
				valid = false
			}
		}
		if !valid || int64(log.PromptTokens) > 1<<53-1 || int64(log.CompletionTokens) > 1<<53-1 {
			continue
		}
		input := float64(log.PromptTokens)
		cacheRead, cacheWrite, splitWrite := float64(0), float64(0), float64(0)
		if other.CacheTokens != nil {
			cacheRead = float64(*other.CacheTokens)
		}
		for _, count := range []*int64{other.CacheCreation5m, other.CacheCreation1h} {
			if count != nil {
				splitWrite += float64(*count)
			}
		}
		for _, count := range []*int64{other.CacheWriteTokens, other.CacheCreationTokens} {
			if count != nil {
				cacheWrite = max(cacheWrite, float64(*count))
			}
		}
		cacheWrite = max(cacheWrite, splitWrite)
		// The claude flag describes the final request format, not necessarily
		// usage semantics. Modern semantic/path tags override that legacy flag.
		if other.UsageSemantic != "" && other.UsageSemantic != "anthropic" && other.UsageSemantic != "openai" && other.UsageSemantic != "gemini" {
			continue
		}
		anthropic := other.UsageSemantic == "anthropic"
		if other.UsageSemantic == "" {
			anthropic = strings.HasPrefix(other.Admin.UsageBillingPath, "billing-usage-anthropic")
			if other.Admin.UsageBillingPath == "" || other.Admin.UsageBillingPath == "upstream" {
				anthropic = other.Claude
			}
		}
		if other.InputTokensTotal != nil {
			input = float64(*other.InputTokensTotal)
		} else if anthropic {
			// An absent cache category is unknown, not zero: raw Anthropic
			// prompt tokens alone cannot establish the complete denominator.
			if other.CacheTokens == nil || (other.CacheWriteTokens == nil && other.CacheCreationTokens == nil && (other.CacheCreation5m == nil || other.CacheCreation1h == nil)) {
				continue
			}
			input += cacheRead + cacheWrite
		}
		if input < float64(log.PromptTokens) || input < cacheRead || input < cacheWrite || input > 1<<53-1 {
			continue
		}
		if input > 0 && other.CacheTokens != nil {
			stats.CacheSampleCount++
			cacheInput += input
			cachedTokens += cacheRead
		}
		total := input + float64(log.CompletionTokens)
		if total <= 0 || len(other.ToolSurcharges) > 0 || other.WebSearch || other.FileSearch || other.ImageGenerationCall {
			continue
		}
		if other.GroupRatio == nil || *other.GroupRatio <= 0 || math.IsNaN(*other.GroupRatio) || math.IsInf(*other.GroupRatio, 0) {
			continue
		}
		quota := float64(log.Quota)
		if other.BillingSource == "subscription" && other.SubscriptionConsumed != nil {
			if *other.SubscriptionConsumed < 0 || *other.SubscriptionConsumed > 1<<53-1 {
				continue
			}
			quota = float64(*other.SubscriptionConsumed)
		} else if other.BillingSource != "" && other.BillingSource != "wallet" && other.BillingSource != "subscription" {
			continue
		}
		// group_ratio already includes any user-specific group override.
		quota /= *other.GroupRatio
		if math.IsNaN(quota) || math.IsInf(quota, 0) {
			continue
		}
		stats.PriceSampleCount++
		pricedTokens += total
		baseQuota += quota
	}
	if stats.CacheSampleCount >= channelProbeUsageMinimum && cacheInput > 0 {
		rate := cachedTokens / cacheInput * 100
		stats.AverageCacheHitRate = &rate
	}
	if stats.PriceSampleCount >= channelProbeUsageMinimum && quotaPerUnit > 0 && !math.IsNaN(quotaPerUnit) && !math.IsInf(quotaPerUnit, 0) {
		price := baseQuota / pricedTokens * 1e8 / quotaPerUnit
		if !math.IsNaN(price) && !math.IsInf(price, 0) {
			stats.EstimatedPricePer100MTokens = &price
		}
	}
	return stats
}
