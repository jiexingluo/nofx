package trader

import (
	"os"
	"strings"
	"testing"
	"time"

	"nofx/kernel"
	"nofx/market"
	"nofx/mcp"
	_ "nofx/mcp/provider" // registers the codex_cli provider
)

// TestLiveReviewDossier hits the real data sources (Binance, alternative.me,
// CoinGecko, news RSS) and prints the dossier the AI would see. Skipped unless
// NOFX_LIVE_REVIEW_CHECK=1 — run it on the server before trusting a deploy:
//
//	NOFX_LIVE_REVIEW_CHECK=1 go test ./trader/ -run TestLiveReviewDossier -v
func TestLiveReviewDossier(t *testing.T) {
	if os.Getenv("NOFX_LIVE_REVIEW_CHECK") == "" {
		t.Skip("set NOFX_LIVE_REVIEW_CHECK=1 to query the live data sources")
	}
	now := time.Now().UTC()
	api := market.NewAPIClient()
	bars := func(symbol string) ([]market.Kline, error) {
		raw, err := api.GetKlines(symbol, "1d", dailyTrendKlineLimit)
		if err != nil {
			return nil, err
		}
		return closedDailyBars(raw, now), nil
	}
	cands := []dailyCandidate{{symbol: "XMRUSDT", reason: "live check candidate"}}
	positions := []kernel.PositionInfo{{Symbol: "UNIUSDT", Side: "long", EntryPrice: 7, MarkPrice: 7.2}}
	ctx := gatherReviewContext(cands, positions, bars, now)
	for _, c := range ctx.coins {
		for _, e := range c.errs {
			t.Errorf("%s: %s", c.symbol, e)
		}
	}
	for _, e := range ctx.newsErrors {
		t.Logf("news source failed: %s", e)
	}
	dossier := buildReviewDossier(ctx)
	t.Log("\n" + dossier)
	for _, want := range []string{"BTC daily close", "Fear & Greed: ", "Funding: last", "Open interest: $", "Long/short account ratio: "} {
		if !strings.Contains(dossier, want) {
			t.Errorf("live dossier missing %q", want)
		}
	}
	if len(ctx.news) == 0 {
		t.Error("no headlines from any news source")
	}

	// One real review call through the same client the trader builds (spends
	// one call of the AI quota): NOFX_LIVE_REVIEW_AI=1, plus CODEX_CLI_BIN_PATH.
	if os.Getenv("NOFX_LIVE_REVIEW_AI") == "" {
		return
	}
	client := mcp.NewAIClientByProvider(mcp.ProviderCodexCLI)
	client.SetAPIKey("unused-by-codex-cli", "", "") // the trader always sets one; codex uses its own login
	client.SetTimeout(5 * time.Minute)
	raw, err := client.CallWithMessages(dailyReviewSystemPrompt, dossier)
	if err != nil {
		t.Fatalf("AI call failed: %v", err)
	}
	verdicts, reasoning, err := parseReviewVerdicts(raw)
	if err != nil {
		t.Fatalf("AI answer unreadable: %v\n%s", err, raw)
	}
	t.Logf("reasoning: %s", reasoning)
	for sym, v := range verdicts {
		t.Logf("%s → %s (%d%%): %s", sym, v.Action, v.Confidence, v.Reasoning)
	}
	if verdicts["XMRUSDT"].Action == "" || verdicts["UNIUSDT"].Action == "" {
		t.Errorf("expected a verdict for both symbols, got %+v", verdicts)
	}
}
