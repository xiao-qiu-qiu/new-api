package setting

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

type SpecialPricingRule struct {
	UserGroup   string  `json:"user_group"`
	Model       string  `json:"model"`
	Price       float64 `json:"price"`
	BillingUnit string  `json:"billing_unit"`
}

var specialPricingRules = []SpecialPricingRule{}
var specialPricingRulesMutex sync.RWMutex

func ResetSpecialPricingRules() {
	specialPricingRulesMutex.Lock()
	defer specialPricingRulesMutex.Unlock()
	specialPricingRules = []SpecialPricingRule{}
}

func GetSpecialPricingRulesCopy() []SpecialPricingRule {
	specialPricingRulesMutex.RLock()
	defer specialPricingRulesMutex.RUnlock()
	return slices.Clone(specialPricingRules)
}

func SpecialPricingRules2JSONString() string {
	data, _ := common.Marshal(GetSpecialPricingRulesCopy())
	return string(data)
}

func ParseSpecialPricingRules(value string) ([]SpecialPricingRule, error) {
	// A pointer distinguishes an explicitly free price from an omitted/null one.
	var entries []struct {
		UserGroup   string   `json:"user_group"`
		Model       string   `json:"model"`
		Price       *float64 `json:"price"`
		BillingUnit string   `json:"billing_unit"`
	}
	if err := common.UnmarshalJsonStr(value, &entries); err != nil || entries == nil {
		return nil, common.NewMessage("Invalid parameters")
	}
	rules := make([]SpecialPricingRule, 0, len(entries))
	seen := make(map[[2]string]bool, len(entries))
	for _, entry := range entries {
		if entry.Price == nil {
			return nil, common.NewMessage("Invalid parameters")
		}
		rule := SpecialPricingRule{UserGroup: entry.UserGroup, Model: entry.Model, Price: *entry.Price, BillingUnit: entry.BillingUnit}
		if err := rule.Validate(); err != nil {
			return nil, err
		}
		key := [2]string{rule.UserGroup, rule.Model}
		if seen[key] {
			return nil, common.NewMessage("Invalid parameters")
		}
		seen[key] = true
		rules = append(rules, rule)
	}
	return rules, nil
}

// Validate checks the standalone rule; registry references are checked against
// the final group settings in the same transaction as the option update.
func (r SpecialPricingRule) Validate() error {
	if r.UserGroup == "" || r.UserGroup == "auto" || r.UserGroup != strings.TrimSpace(r.UserGroup) ||
		r.Model == "" || r.Model != strings.TrimSpace(r.Model) || r.Price < 0 || math.IsNaN(r.Price) || math.IsInf(r.Price, 0) {
		return common.NewMessage("Invalid parameters")
	}
	quantity := 1
	switch r.BillingUnit {
	case "image":
		quantity = dto.MaxImageN
	case "request":
	default:
		return common.NewMessage("Invalid parameters")
	}
	if _, err := common.QuotaFromFloatStrict(r.Price * float64(quantity) * common.QuotaPerUnit); err != nil {
		return common.NewMessage("Invalid parameters")
	}
	return nil
}

func UpdateSpecialPricingRulesByJSONString(value string) error {
	rules, err := ParseSpecialPricingRules(value)
	if err != nil {
		return err
	}
	specialPricingRulesMutex.Lock()
	defer specialPricingRulesMutex.Unlock()
	specialPricingRules = rules
	return nil
}

func (r SpecialPricingRule) BillingExpression() string {
	expression := `tier("special", fixed(` + strconv.FormatFloat(r.Price, 'g', -1, 64) + `))`
	if r.BillingUnit == "image" {
		expression += " * image_count"
	}
	return expression
}

// ResolveSpecialPricing expects the original client model name. Routing groups
// and channel model mappings do not participate in rule selection.
func (s *GroupSettingsSnapshot) ResolveSpecialPricing(userGroup, modelName string) (SpecialPricingRule, bool) {
	for _, source := range []string{userGroup, "*"} {
		best, specificity := -1, -1
		for index, rule := range s.SpecialPricingRules {
			if rule.UserGroup != source {
				continue
			}
			if !strings.Contains(rule.Model, "*") {
				if rule.Model == modelName {
					return rule, true
				}
				continue
			}
			if rule.matchesModel(modelName) {
				literalCount := utf8.RuneCountInString(rule.Model) - strings.Count(rule.Model, "*")
				if literalCount > specificity {
					best, specificity = index, literalCount
				}
			}
		}
		if best >= 0 {
			return s.SpecialPricingRules[best], true
		}
	}
	return SpecialPricingRule{}, false
}

// Only '*' is special; it matches any sequence, including slashes. Other glob
// syntax is literal, so provider/model names retain their exact spelling.
func (r SpecialPricingRule) matchesModel(modelName string) bool {
	pattern, name, star, retry := 0, 0, -1, 0
	for name < len(modelName) {
		switch {
		case pattern < len(r.Model) && r.Model[pattern] == '*':
			star = pattern
			pattern++
			retry = name
		case pattern < len(r.Model) && r.Model[pattern] == modelName[name]:
			pattern++
			name++
		case star >= 0:
			retry++
			name = retry
			pattern = star + 1
		default:
			return false
		}
	}
	for pattern < len(r.Model) && r.Model[pattern] == '*' {
		pattern++
	}
	return pattern == len(r.Model)
}
