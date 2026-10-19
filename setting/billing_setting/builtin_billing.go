package billing_setting

// Built-in token prices use actual USD per million tokens. Keep new model
// defaults here instead of splitting them across the legacy ratio tables.
var builtinBillingExpr = map[string]string{
	// https://developers.openai.com/api/docs/pricing (Standard, 2026-09-09).
	// The Images API reports image output in output_tokens, normalized to c.
	"gpt-image-2":            `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,
	"gpt-image-2.5-sunburst": `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,
	"gpt-image-2.5-flare":    `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,
	// https://developers.openai.com/api/docs/pricing (2026-10-19).
	// Long-context prices apply to the whole request. service_tier is the
	// final outbound tier, superseded by an explicit upstream response tier;
	// the original param("service_tier") is deliberately not used here.
	// Astra and 6.1 Sol publish Ultrafast at 6x; other models do not inherit it.
	"gpt-6-astra": `(len <= 272000 ? tier("standard", p * 10 + c * 50 + cr * 1 + cc * 12.5) : tier("long_context", p * 20 + c * 75 + cr * 2 + cc * 25)) * (service_tier == "priority" ? 2 : 1) * (service_tier == "ultrafast" ? 6 : 1) * (service_tier == "flex" ? 0.5 : 1)`,
	"gpt-6.1-sol": `(len <= 272000 ? tier("standard", p * 2 + c * 10 + cr * 0.1 + cc * 2.5) : tier("long_context", p * 4 + c * 15 + cr * 0.2 + cc * 5)) * (service_tier == "priority" ? 2 : 1) * (service_tier == "ultrafast" ? 6 : 1) * (service_tier == "flex" ? 0.5 : 1)`,
	"gpt-6-sol":   `(len <= 272000 ? tier("standard", p * 2 + c * 10 + cr * 0.2 + cc * 2.5) : tier("long_context", p * 4 + c * 15 + cr * 0.4 + cc * 5)) * (service_tier == "priority" ? 2 : 1) * (service_tier == "flex" ? 0.5 : 1)`,
	"gpt-6-luna":  `(len <= 272000 ? tier("standard", p * 0.1 + c * 0.5 + cr * 0.01 + cc * 0.125) : tier("long_context", p * 0.2 + c * 0.75 + cr * 0.02 + cc * 0.25)) * (service_tier == "priority" ? 2 : 1) * (service_tier == "flex" ? 0.5 : 1)`,
}
