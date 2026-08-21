package kernel

import (
	"strings"
	"testing"

	"nofx/market"
	"nofx/store"
)

func TestBuildSystemPromptUsesVergexClaw402Prompt(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("zh")
	cfg.CoinSource.SourceType = "vergex_signal"
	cfg.CoinSource.VergexLimit = 5
	cfg.PromptSections.RoleDefinition = "# You are a professional Hyperliquid USDC multi-asset trading AI"
	cfg.CustomPrompt = "Long only, no shorts."

	engine := NewStrategyEngine(&cfg)
	prompt := engine.BuildSystemPrompt(30, "balanced")

	if !strings.Contains(prompt, "NOFX Claw402 auto-trader") {
		t.Fatalf("prompt did not use the Claw402/Vergex TradeFi role:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Claw402.ai Signal Ranking") || !strings.Contains(prompt, "Signal Lab") || !strings.Contains(prompt, "Cost/Liquidation Heatmap") {
		t.Fatalf("prompt is missing Claw402/Vergex detail data guidance:\n%s", prompt)
	}
	if !strings.Contains(prompt, "open_short") {
		t.Fatalf("prompt should explicitly allow short entries:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Direction must be data-driven") {
		t.Fatalf("prompt should explain that direction is data-driven, not long-only:\n%s", prompt)
	}
	if !strings.Contains(prompt, "every open position must use exactly 10x") {
		t.Fatalf("prompt should force 10x leverage for Claw402 opens:\n%s", prompt)
	}
	if !strings.Contains(prompt, "use the full max notional per position") {
		t.Fatalf("prompt should force full-size Claw402 opens:\n%s", prompt)
	}
	if containsCJK(prompt) {
		t.Fatalf("system prompt must be English-only, got CJK text:\n%s", prompt)
	}
	legacyPhrases := []string{
		"Hyperliquid USDC multi-asset trading AI",
		"Long only",
		"Altcoin",
		"BTC/ETH",
		"LONG-ONLY",
		"Do not short",
		"MUST open a long",
	}
	for _, phrase := range legacyPhrases {
		if strings.Contains(prompt, phrase) {
			t.Fatalf("prompt still contains legacy phrase %q:\n%s", phrase, prompt)
		}
	}
}

func TestBuildSystemPromptFallsBackToEnglishWhenConfiguredLanguageIsChinese(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("zh")
	cfg.CoinSource.SourceType = "static"
	cfg.CoinSource.StaticCoins = []string{"BTCUSDT", "ETHUSDT"}
	cfg.CoinSource.VergexLimit = 0
	cfg.CoinSource.VergexMarketType = ""
	cfg.CoinSource.VergexChain = ""
	cfg.PromptSections.RoleDefinition = "# You are a Chinese system prompt"
	cfg.PromptSections.TradingFrequency = "# High-frequency trading\nTrade every minute."
	cfg.PromptSections.EntryStandards = "# Entry\nOpen positions freely."
	cfg.PromptSections.DecisionProcess = "# Decision\nOutput directly."
	cfg.CustomPrompt = "Chinese preference should not enter the system prompt."

	engine := NewStrategyEngine(&cfg)
	prompt := engine.BuildSystemPrompt(30, "balanced")

	required := []string{
		"Data Dictionary & Trading Rules",
		"You are a professional Hyperliquid USDC multi-asset trading AI",
		"Trading Frequency Awareness",
		"Entry Standards",
		"Decision Process",
	}
	for _, phrase := range required {
		if !strings.Contains(prompt, phrase) {
			t.Fatalf("English fallback prompt missing %q:\n%s", phrase, prompt)
		}
	}
	if containsCJK(prompt) {
		t.Fatalf("system prompt must be English-only, got CJK text:\n%s", prompt)
	}
}

func TestBuildSystemPromptDoesNotForceLongOnlyForSingleXYZ(t *testing.T) {
	prompt := buildXYZStockCustomPrompt("XYZ:INTC")

	required := []string{
		"DIRECTIONAL, SIGNAL-DRIVEN",
		"You may open long or short",
		"open_short",
	}
	for _, phrase := range required {
		if !strings.Contains(prompt, phrase) {
			t.Fatalf("single XYZ prompt missing %q:\n%s", phrase, prompt)
		}
	}

	forbidden := []string{
		"LONG-ONLY",
		"Do not short",
		"MUST open a long",
		"Probing > waiting",
	}
	for _, phrase := range forbidden {
		if strings.Contains(prompt, phrase) {
			t.Fatalf("single XYZ prompt still contains forced-long phrase %q:\n%s", phrase, prompt)
		}
	}
}

// TestBuildUserPromptRendersDecisionWeights pins down a real gap found in
// production: decision_weights was only wired into the unused formatter.go/
// PromptBuilder code path, so a strategy's configured weights (e.g. after
// rebalancing them) never actually reached the AI via BuildUserPrompt, the
// function this trader's live decision loop actually calls.
func TestBuildUserPromptRendersDecisionWeights(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("en")
	engine := NewStrategyEngine(&cfg)

	ctx := &Context{
		DecisionWeights: &store.DecisionWeightsConfig{
			TechnicalWeight:   45,
			SentimentWeight:   20,
			ValuationWeight:   25,
			FundamentalWeight: 10,
		},
	}
	prompt := engine.BuildUserPrompt(ctx)

	if !strings.Contains(prompt, "## Decision Weights") {
		t.Fatalf("user prompt is missing the Decision Weights section:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Technical 45%") || !strings.Contains(prompt, "Sentiment 20%") ||
		!strings.Contains(prompt, "Valuation 25%") || !strings.Contains(prompt, "Fundamental 10%") {
		t.Fatalf("user prompt does not render the configured weight values:\n%s", prompt)
	}
}

// TestBuildSystemPromptDefinesRiskUsdFormula pins down a real gap found by
// tracing the AI's own reasoning in production: risk_usd was listed as a
// required output field but never given a formula anywhere in the prompt,
// so the AI had to guess each cycle whether leverage multiplies in - and was
// observed spending many reasoning steps oscillating between a $50 and a $5
// answer for the identical trade before settling on one.
func TestBuildSystemPromptDefinesRiskUsdFormula(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("en")
	cfg.CoinSource.SourceType = "static"
	cfg.CoinSource.StaticCoins = []string{"BTCUSDT", "ETHUSDT"}
	cfg.CoinSource.VergexLimit = 0
	cfg.CoinSource.VergexMarketType = ""
	cfg.CoinSource.VergexChain = ""

	engine := NewStrategyEngine(&cfg)
	prompt := engine.BuildSystemPrompt(200, "balanced")

	if !strings.Contains(prompt, "risk_usd = position_size_usd × |entry_price - stop_loss| / entry_price") {
		t.Fatalf("system prompt is missing an explicit risk_usd formula:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Leverage is NOT a factor in this formula") {
		t.Fatalf("system prompt should explicitly rule out leverage in the risk_usd formula:\n%s", prompt)
	}
}

// TestBuildUserPromptOrdersStableSectionsBeforeVolatileOnes locks in the
// prefix-caching-friendly ordering: DeepSeek's automatic disk caching (and
// equivalent features on other providers) matches the exact byte prefix of
// a request, so a single early token that differs from the previous call -
// a timestamp being the worst offender, previously the very first line of
// this prompt - forces a full recompute of everything after it regardless
// of how stable that later content is. Sections that rarely change
// (decision weights, historical stats, recent trades) must sit ahead of
// sections that change every cycle by design (the system-status line,
// live account/position numbers, market data).
func TestBuildUserPromptOrdersStableSectionsBeforeVolatileOnes(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("en")
	engine := NewStrategyEngine(&cfg)

	ctx := &Context{
		DecisionWeights: &store.DecisionWeightsConfig{TechnicalWeight: 45, SentimentWeight: 20, ValuationWeight: 25, FundamentalWeight: 10},
		TradingStats:    &TradingStats{TotalTrades: 10, ProfitFactor: 1.2, SharpeRatio: 0.5},
		RecentOrders:    []RecentOrder{{Symbol: "BTCUSDT", Side: "long", RealizedPnL: 5}},
		Positions:       []PositionInfo{{Symbol: "ETHUSDT", Side: "long", EntryPrice: 1900, MarkPrice: 1950}},
		CandidateCoins:  []CandidateCoin{{Symbol: "SOLUSDT"}},
		MarketDataMap:   map[string]*market.Data{"SOLUSDT": {Symbol: "SOLUSDT", CurrentPrice: 150}},
	}
	prompt := engine.BuildUserPrompt(ctx)

	sections := []string{
		"## Decision Weights",
		"## Historical Trading Statistics",
		"## Recent Completed Trades",
		"Time:",
		"Account:",
		"## Current Positions",
		"## Candidate Coins",
	}
	positions := make([]int, len(sections))
	for i, s := range sections {
		idx := strings.Index(prompt, s)
		if idx < 0 {
			t.Fatalf("prompt missing expected section %q:\n%s", s, prompt)
		}
		positions[i] = idx
	}
	for i := 1; i < len(positions); i++ {
		if positions[i] <= positions[i-1] {
			t.Fatalf("section %q (at %d) should come after %q (at %d) - stable sections must precede volatile ones for prefix caching:\n%s",
				sections[i], positions[i], sections[i-1], positions[i-1], prompt)
		}
	}
}

func containsCJK(text string) bool {
	for _, r := range text {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}
