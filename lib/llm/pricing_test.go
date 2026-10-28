package llm

import "testing"

func TestCostForOpenAIUsageUsesCachedInputRate(t *testing.T) {
	usage := Usage{
		PromptTokens:       1_000_000,
		CachedPromptTokens: 400_000,
		CompletionTokens:   200_000,
		TotalTokens:        1_200_000,
	}

	cost, ok := CostForOpenAIUsage("gpt-5", usage)
	if !ok {
		t.Fatal("expected pricing for gpt-5")
	}

	want := 0.6*1.25 + 0.4*0.125 + 0.2*10.00
	if cost.TotalCost != want {
		t.Fatalf("cost = %.6f, want %.6f", cost.TotalCost, want)
	}
}

func TestCostForOpenAIUsageUnknownModel(t *testing.T) {
	if _, ok := CostForOpenAIUsage("custom-model", Usage{TotalTokens: 1}); ok {
		t.Fatal("expected no pricing for custom model")
	}
}

func TestUsageUncachedPromptTokensClampsAtZero(t *testing.T) {
	usage := Usage{PromptTokens: 10, CachedPromptTokens: 20}
	if got := usage.UncachedPromptTokens(); got != 0 {
		t.Fatalf("uncached prompt tokens = %d, want 0", got)
	}
}

func TestOpenAIStandardPricingNormalizesAliases(t *testing.T) {
	if _, ok := OpenAIStandardPricing("gpt-5.6-sol-2026-01-01"); !ok {
		t.Fatal("expected pricing for gpt-5.6-sol snapshot")
	}
	if _, ok := OpenAIStandardPricing("gpt-5-chat-latest"); !ok {
		t.Fatal("expected pricing for gpt-5 chat alias")
	}
}

func TestCostForBuiltInUsageSupportsOpenCodeGoModelRefs(t *testing.T) {
	usage := Usage{
		PromptTokens:       100_000,
		CachedPromptTokens: 80_000,
		CompletionTokens:   10_000,
		TotalTokens:        110_000,
	}

	cost, ok := CostForBuiltInUsage("opencode-go/kimi-k2.7-code", usage)
	if !ok {
		t.Fatal("expected pricing for OpenCode Go model ref")
	}

	want := 0.02*0.95 + 0.08*0.19 + 0.01*4.00
	if cost.TotalCost != want {
		t.Fatalf("cost = %.6f, want %.6f", cost.TotalCost, want)
	}
}

func TestCostForBuiltInUsageUsesOpenCodeGoLongContextTier(t *testing.T) {
	usage := Usage{
		PromptTokens:       300_000,
		CachedPromptTokens: 100_000,
		CompletionTokens:   50_000,
		TotalTokens:        350_000,
	}

	cost, ok := CostForBuiltInUsage("qwen3.7-plus", usage)
	if !ok {
		t.Fatal("expected pricing for qwen3.7-plus")
	}

	want := 0.2*1.20 + 0.1*0.12 + 0.05*4.80
	if cost.TotalCost != want {
		t.Fatalf("cost = %.6f, want %.6f", cost.TotalCost, want)
	}
}
