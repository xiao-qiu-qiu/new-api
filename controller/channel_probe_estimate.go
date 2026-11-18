package controller

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/gin-gonic/gin"
)

func GetChannelProbePriceEstimate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	query := c.Request.URL.Query()
	for key, values := range query {
		if !slices.Contains([]string{"probe_id", "model", "input_tokens", "output_tokens", "cache_read_tokens", "service_tier"}, key) || len(values) != 1 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": common.NewMessage("Invalid parameters")})
			return
		}
	}
	probeID, err := strconv.Atoi(query.Get("probe_id"))
	modelName := query.Get("model")
	if err != nil || probeID <= 0 || modelName == "" || modelName != strings.TrimSpace(modelName) || utf8.RuneCountInString(modelName) > 191 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": common.NewMessage("Invalid parameters")})
		return
	}
	input := service.ChannelProbePriceInput{ServiceTier: query.Get("service_tier")}
	for _, field := range []struct {
		name  string
		value *int
	}{
		{"input_tokens", &input.InputTokens},
		{"output_tokens", &input.OutputTokens},
		{"cache_read_tokens", &input.CacheReadTokens},
	} {
		value := query.Get(field.name)
		if value == "" || len(value) > 8 || strings.IndexFunc(value, func(char rune) bool { return char < '0' || char > '9' }) >= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": common.NewMessage("Invalid parameters")})
			return
		}
		*field.value, err = strconv.Atoi(value)
		if err != nil || *field.value > service.ChannelProbePriceTokenLimit {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": common.NewMessage("Invalid parameters")})
			return
		}
	}
	if input.CacheReadTokens > input.InputTokens || (input.ServiceTier != "" && !slices.Contains([]string{"default", "flex", "priority", "ultrafast", "fast"}, input.ServiceTier)) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": common.NewMessage("Invalid parameters")})
		return
	}
	settings := setting.PinGroupSettings(c)
	userID, userGroup := c.GetInt("id"), c.GetString("group")
	probe, err := model.GetChannelProbe(probeID)
	if err != nil || !slices.Contains(probe.Models, modelName) || !publicGroupProbeVisible(probe, userID, userGroup, settings) ||
		!settings.Selectable(userGroup, probe.GroupName) {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": common.NewMessage("Price unavailable")})
		return
	}
	estimate, err := estimateChannelProbePrice(c, modelName, userGroup, probe.GroupName, input)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "message": common.NewMessage("This request cannot be estimated with the current pricing rules.")})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": estimate})
}

func estimateChannelProbePrice(c *gin.Context, modelName, userGroup, group string, input service.ChannelProbePriceInput) (service.ChannelProbePriceEstimate, error) {
	unavailable := common.NewMessage("price estimate unavailable for this scenario")
	settings := setting.PinGroupSettings(c)
	billingModelName := service.ResolveBillingModelName(modelName)
	rule, special := settings.ResolveSpecialPricing(userGroup, modelName)
	if !special && !service.ChannelProbeSupportsPriceEstimate(modelName, userGroup, settings) {
		return service.ChannelProbePriceEstimate{}, unavailable
	}
	ctx := c.Copy()
	ctx.Request = &http.Request{Header: make(http.Header)}
	originalKeys := ctx.Keys
	ctx.Keys = make(map[string]any)
	for key, value := range originalKeys {
		if snapshot, ok := value.(*setting.GroupSettingsSnapshot); ok && snapshot == settings {
			ctx.Set(key, snapshot)
		}
	}
	info := &relaycommon.RelayInfo{
		OriginModelName:     modelName,
		BillingModelName:    billingModelName,
		UsingGroup:          group,
		UserGroup:           userGroup,
		StartTime:           time.Now(),
		RelayFormat:         types.RelayFormatOpenAI,
		RelayMode:           relayconstant.RelayModeChatCompletions,
		ServiceTier:         relaycommon.NormalizeServiceTier(input.ServiceTier),
		BillingRequestInput: &billingexpr.RequestInput{Body: []byte(`{}`)},
	}
	if special && rule.BillingUnit == "image" {
		info.Request = &dto.ImageRequest{Model: modelName, N: common.GetPointer(uint(1))}
	}
	if special || billing_setting.GetBillingMode(billingModelName) == billing_setting.BillingModeTieredExpr {
		expression, ok := billing_setting.GetBillingExpr(billingModelName)
		if special {
			expression, ok = rule.BillingExpression(), true
		}
		if !ok {
			return service.ChannelProbePriceEstimate{}, unavailable
		}
		info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(ctx, info)
		info.TieredBillingSnapshot = &billingexpr.BillingSnapshot{
			BillingMode:  billing_setting.BillingModeTieredExpr,
			ModelName:    billingModelName,
			ExprString:   expression,
			ExprHash:     billingexpr.ExprHashString(expression),
			ExprVersion:  billingexpr.ExprVersion(expression),
			GroupRatio:   info.PriceData.GroupRatioInfo.GroupRatio,
			QuotaPerUnit: common.QuotaPerUnit,
		}
		if special && rule.BillingUnit == "image" {
			count := 1
			info.BillingRequestInput.ImageCount = &count
		}
	} else {
		if _, err := helper.ModelPriceHelper(ctx, info, 0, &types.TokenCountMeta{}); err != nil {
			return service.ChannelProbePriceEstimate{}, unavailable
		}
	}
	return service.EstimateChannelProbePrice(ctx, info, input)
}
