package controller

import (
	"maps"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

func filterPricingByUsableGroups(pricing []model.Pricing, usableGroup map[string]string) []model.Pricing {
	if len(pricing) == 0 {
		return pricing
	}
	if len(usableGroup) == 0 {
		return []model.Pricing{}
	}

	filtered := make([]model.Pricing, 0, len(pricing))
	for _, item := range pricing {
		if common.StringsContains(item.EnableGroup, "all") {
			filtered = append(filtered, item)
			continue
		}
		for _, group := range item.EnableGroup {
			if _, ok := usableGroup[group]; ok {
				filtered = append(filtered, item)
				break
			}
		}
	}
	return filtered
}

func pricingForUser(pricing []model.Pricing, groupSettings *setting.GroupSettingsSnapshot, userGroup string) []model.Pricing {
	// GetPricing returns shared cached entries. Replace billing fields only on
	// a request-owned slice; capability metadata stays intact and read-only.
	pricing = slices.Clone(pricing)
	for i := range pricing {
		rule, matched := groupSettings.ResolveSpecialPricing(userGroup, pricing[i].ModelName)
		if !matched {
			continue
		}
		item := &pricing[i]
		item.BillingMode = billing_setting.BillingModeTieredExpr
		item.BillingExpr = rule.BillingExpression()
		item.IgnoreGroupRatio = true
		item.QuotaType = 0
		item.ModelRatio, item.ModelPrice, item.CompletionRatio = 0, 0, 0
		item.CacheRatio, item.CreateCacheRatio, item.ImageRatio = nil, nil, nil
		item.AudioRatio, item.AudioCompletionRatio = nil, nil
		item.BillingUsageSchema = nil
		item.BillingUsageExamples = nil
		item.BillingPluginVariants = nil
	}
	return pricing
}

func GetPricing(c *gin.Context) {
	pricing := model.GetPricing()
	userId, exists := c.Get("id")
	usableGroup := map[string]string{}
	groupRatio := map[string]float64{}
	groupSettings := setting.RequestGroupSettings(c)
	maps.Copy(groupRatio, groupSettings.Ratios)
	var group string
	if exists {
		user, err := model.GetUserCache(userId.(int))
		if err == nil {
			group = user.Group
			for g := range groupRatio {
				ratio, ok := groupSettings.SpecialRatios[group][g]
				if ok {
					groupRatio[g] = ratio
				}
			}
		}
	}

	usableGroup = groupSettings.Usable(group)
	pricing = filterPricingByUsableGroups(pricing, usableGroup)
	pricing = pricingForUser(pricing, groupSettings, group)
	// check groupRatio contains usableGroup
	for group := range groupSettings.Ratios {
		if _, ok := usableGroup[group]; !ok {
			delete(groupRatio, group)
		}
	}

	c.JSON(200, gin.H{
		"success":            true,
		"data":               pricing,
		"vendors":            model.GetVendors(),
		"group_ratio":        groupRatio,
		"usable_group":       usableGroup,
		"supported_endpoint": model.GetSupportedEndpointMap(),
		"auto_groups":        groupSettings.FilterAutoGroups(group, groupSettings.AutoGroups, len(groupSettings.AutoGroups)),
		"pricing_version":    "a42d372ccf0b5dd13ecf71203521f9d2",
	})
}

func ResetModelRatio(c *gin.Context) {
	defaultStr := ratio_setting.DefaultModelRatio2JSONString()
	err := model.UpdateOption("ModelRatio", defaultStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	err = ratio_setting.UpdateModelRatioByJSONString(defaultStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccessT(c, "Model ratios reset", nil)
}
