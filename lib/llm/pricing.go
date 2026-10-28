package llm

import "strings"

type TokenPricing struct {
	InputPerMillion       float64
	CachedInputPerMillion float64
	OutputPerMillion      float64
}

type UsageCost struct {
	InputCost       float64
	CachedInputCost float64
	OutputCost      float64
	TotalCost       float64
}

const opencodeGoLongContextThreshold = 256_000

var builtInStandardPricing = map[string]TokenPricing{
	"gpt-5.6":       {InputPerMillion: 5.00, CachedInputPerMillion: 0.50, OutputPerMillion: 30.00},
	"gpt-5.6-sol":   {InputPerMillion: 5.00, CachedInputPerMillion: 0.50, OutputPerMillion: 30.00},
	"gpt-5.6-terra": {InputPerMillion: 2.50, CachedInputPerMillion: 0.25, OutputPerMillion: 15.00},
	"gpt-5.6-luna":  {InputPerMillion: 1.00, CachedInputPerMillion: 0.10, OutputPerMillion: 6.00},
	"gpt-5.4":       {InputPerMillion: 2.50, CachedInputPerMillion: 0.25, OutputPerMillion: 15.00},
	"gpt-5.2":       {InputPerMillion: 1.75, CachedInputPerMillion: 0.175, OutputPerMillion: 14.00},
	"gpt-5.1":       {InputPerMillion: 1.25, CachedInputPerMillion: 0.125, OutputPerMillion: 10.00},
	"gpt-5":         {InputPerMillion: 1.25, CachedInputPerMillion: 0.125, OutputPerMillion: 10.00},

	"grok-4.5":        {InputPerMillion: 2.00, CachedInputPerMillion: 0.30, OutputPerMillion: 6.00},
	"glm-5.2":         {InputPerMillion: 1.40, CachedInputPerMillion: 0.26, OutputPerMillion: 4.40},
	"glm-5.1":         {InputPerMillion: 1.40, CachedInputPerMillion: 0.26, OutputPerMillion: 4.40},
	"kimi-k3":         {InputPerMillion: 3.00, CachedInputPerMillion: 0.30, OutputPerMillion: 15.00},
	"kimi-k2.7-code":  {InputPerMillion: 0.95, CachedInputPerMillion: 0.19, OutputPerMillion: 4.00},
	"kimi-k2.6":       {InputPerMillion: 0.95, CachedInputPerMillion: 0.16, OutputPerMillion: 4.00},
	"mimo-v2.5":       {InputPerMillion: 0.14, CachedInputPerMillion: 0.0028, OutputPerMillion: 0.28},
	"mimo-v2.5-pro":   {InputPerMillion: 0.435, CachedInputPerMillion: 0.003625, OutputPerMillion: 0.87},
	"minimax-m3":      {InputPerMillion: 0.30, CachedInputPerMillion: 0.06, OutputPerMillion: 1.20},
	"minimax-m2.7":    {InputPerMillion: 0.30, CachedInputPerMillion: 0.06, OutputPerMillion: 1.20},
	"minimax-m2.5":    {InputPerMillion: 0.30, CachedInputPerMillion: 0.06, OutputPerMillion: 1.20},
	"qwen3.7-max":     {InputPerMillion: 2.50, CachedInputPerMillion: 0.50, OutputPerMillion: 7.50},
	"qwen3.7-plus":    {InputPerMillion: 0.40, CachedInputPerMillion: 0.04, OutputPerMillion: 1.60},
	"qwen3.6-plus":    {InputPerMillion: 0.50, CachedInputPerMillion: 0.05, OutputPerMillion: 3.00},
	"deepseek-v4-pro": {InputPerMillion: 0.435, CachedInputPerMillion: 0.003625, OutputPerMillion: 0.87},
	"deepseek-v4-flash": {
		InputPerMillion:       0.14,
		CachedInputPerMillion: 0.0028,
		OutputPerMillion:      0.28,
	},
}

var opencodeGoLongContextPricing = map[string]TokenPricing{
	"qwen3.7-plus": {InputPerMillion: 1.20, CachedInputPerMillion: 0.12, OutputPerMillion: 4.80},
	"qwen3.6-plus": {InputPerMillion: 2.00, CachedInputPerMillion: 0.20, OutputPerMillion: 6.00},
}

func OpenAIStandardPricing(model string) (TokenPricing, bool) {
	return BuiltInStandardPricing(model)
}

func BuiltInStandardPricing(model string) (TokenPricing, bool) {
	key := normalizeModelForPricing(model)
	price, ok := builtInStandardPricing[key]
	return price, ok
}

func builtInStandardPricingForUsage(model string, usage Usage) (TokenPricing, bool) {
	key := normalizeModelForPricing(model)
	if usage.PromptTokens > opencodeGoLongContextThreshold {
		if price, ok := opencodeGoLongContextPricing[key]; ok {
			return price, true
		}
	}
	price, ok := builtInStandardPricing[key]
	return price, ok
}

func CostForUsage(usage Usage, pricing TokenPricing) UsageCost {
	uncached := usage.UncachedPromptTokens()
	cached := usage.CachedPromptTokens
	if cached < 0 {
		cached = 0
	}
	inputCost := perMillionCost(uncached, pricing.InputPerMillion)
	cachedInputCost := perMillionCost(cached, pricing.CachedInputPerMillion)
	outputCost := perMillionCost(usage.CompletionTokens, pricing.OutputPerMillion)
	return UsageCost{
		InputCost:       inputCost,
		CachedInputCost: cachedInputCost,
		OutputCost:      outputCost,
		TotalCost:       inputCost + cachedInputCost + outputCost,
	}
}

func CostForOpenAIUsage(model string, usage Usage) (UsageCost, bool) {
	return CostForBuiltInUsage(model, usage)
}

func CostForBuiltInUsage(model string, usage Usage) (UsageCost, bool) {
	pricing, ok := builtInStandardPricingForUsage(model, usage)
	if !ok {
		return UsageCost{}, false
	}
	return CostForUsage(usage, pricing), true
}

func perMillionCost(tokens int64, rate float64) float64 {
	if tokens <= 0 || rate <= 0 {
		return 0
	}
	return float64(tokens) / 1_000_000 * rate
}

func normalizeModelForPricing(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	model = strings.TrimSuffix(model, "-latest")
	model = strings.TrimPrefix(model, "opencode-go/")
	model = strings.TrimPrefix(model, "opencodego/")

	switch {
	case strings.HasPrefix(model, "gpt-5.6-sol"):
		return "gpt-5.6-sol"
	case strings.HasPrefix(model, "gpt-5.6-terra"):
		return "gpt-5.6-terra"
	case strings.HasPrefix(model, "gpt-5.6-luna"):
		return "gpt-5.6-luna"
	case model == "gpt-5.6":
		return "gpt-5.6"
	case strings.HasPrefix(model, "gpt-5.4"):
		return "gpt-5.4"
	case strings.HasPrefix(model, "gpt-5.2"):
		return "gpt-5.2"
	case strings.HasPrefix(model, "gpt-5.1"):
		return "gpt-5.1"
	case strings.HasPrefix(model, "gpt-5"):
		return "gpt-5"
	default:
		return model
	}
}
