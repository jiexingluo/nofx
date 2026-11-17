package trader

import (
	"strings"
	"testing"
	"time"

	"nofx/kernel"
	"nofx/market"
	"nofx/store"
)

func TestParseReviewVerdicts(t *testing.T) {
	raw := "<reasoning>**Checking news**</reasoning>\n" +
		`[{"symbol":"xmrusdt","action":"Approve","confidence":72,"reasoning":"No event risk.","leverage":null,"position_size_usd":null,"stop_loss":null,"take_profit":null,"price":null,"quantity":null,"risk_usd":null},` +
		`{"symbol":"UNIUSDT","action":"reject","confidence":64.6,"reasoning":"Exploit reported."},` +
		`{"symbol":"ZECUSDT","action":"maybe","confidence":50,"reasoning":"?"}]`
	got, reasoning, err := parseReviewVerdicts(raw)
	if err != nil {
		t.Fatal(err)
	}
	if reasoning != "**Checking news**" {
		t.Fatalf("reasoning = %q", reasoning)
	}
	if v := got["XMRUSDT"]; v.Action != "approve" || v.Confidence != 72 {
		t.Fatalf("XMR verdict = %+v (symbol and action should be normalised)", v)
	}
	if v := got["UNIUSDT"]; v.Action != "reject" || v.Confidence != 65 || v.Reasoning != "Exploit reported." {
		t.Fatalf("UNI verdict = %+v", v)
	}
	if _, ok := got["ZECUSDT"]; ok {
		t.Fatal("an unknown action must be dropped")
	}

	if _, _, err := parseReviewVerdicts("I cannot help with that."); err == nil {
		t.Fatal("a response without a JSON array must be an error")
	}
	if _, _, err := parseReviewVerdicts(`[{"symbol":"X","action":"buy"}]`); err == nil {
		t.Fatal("a response with no usable verdict must be an error")
	}
}

func TestDailyReviewMode(t *testing.T) {
	for in, want := range map[string]string{"": reviewOff, "off": reviewOff, " Shadow ": reviewShadow, "enforce": reviewEnforce, "yes": reviewOff} {
		if got := dailyReviewMode(store.DailyTrendConfig{AIReview: in}); got != want {
			t.Errorf("dailyReviewMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func risingBars(n int, start float64) []market.Kline {
	bars := make([]market.Kline, n)
	for i := range bars {
		c := start * (1 + 0.01*float64(i))
		bars[i] = market.Kline{High: c * 1.02, Low: c * 0.98, Close: c, CloseTime: int64(i+1) * 86_400_000}
	}
	return bars
}

func TestBuildReviewDossier(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 30, 0, 0, time.UTC)
	cand := dailyCandidate{symbol: "XMRUSDT", reason: "daily close 400 > 20-day high 390 (+2.56%) and above EMA50"}
	pos := kernel.PositionInfo{Symbol: "ZECUSDT", Side: "long", EntryPrice: 100, MarkPrice: 110, UpdateTime: now.Add(-48 * time.Hour).UnixMilli()}
	funding := make([]market.FundingPoint, 10)
	for i := range funding {
		funding[i] = market.FundingPoint{Time: int64(i) * 28_800_000, Rate: 0.0001 * float64(i+1)}
	}
	c := reviewContext{
		now:       now,
		btc:       risingBars(60, 70000),
		valuation: &market.ValuationData{TotalMarketCap: 2.6e12, MarketCapChange24h: -1.2, BTCDominance: 59.3},
		fearGreed: []market.FearGreedPoint{{Value: 30, Label: "Fear"}, {Value: 28, Label: "Fear"}},
		news: []market.NewsItem{
			{Title: "Monero exchange delisting rumours denied", Source: "decrypt.co", Published: now.Add(-5 * time.Hour)},
			{Title: "Bitcoin steadies after selloff", Source: "cointelegraph.com", Published: now.Add(-2 * time.Hour)},
		},
		coins: []coinContext{
			{symbol: "XMRUSDT", candidate: &cand, bars: risingBars(60, 300), funding: funding,
				oi: []market.OpenInterestPoint{{Value: 100e6}, {Value: 110e6}}, longShort: []market.LongShortPoint{{Ratio: 1.8}}},
			{symbol: "ZECUSDT", position: &pos}, // every data source failed
		},
	}
	got := buildReviewDossier(c)
	for _, want := range []string{
		"## Market", "BTC daily close", "Latest crypto headlines (72h):", "Total crypto market cap $2600B (24h -1.2%)", "Fear & Greed: 28 (Fear)",
		"2h ago, cointelegraph.com: Bitcoin steadies after selloff",
		"## Candidate XMRUSDT — daily close 400", "Funding: last 0.1000%, 3-print mean 0.0900% = 90th percentile",
		"Open interest: $110.0M, 1d +10.0%", "Long/short account ratio: 1.80",
		"Headlines mentioning XMR (72h):\n- 5h ago, decrypt.co: Monero exchange delisting rumours denied",
		"## Open position ZECUSDT — entry 100, mark 110 (+10.00%), held 2.0 days",
		"Funding: unavailable", "Open interest: unavailable", "Headlines mentioning ZEC (72h):\n- none found",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dossier missing %q\n---\n%s", want, got)
		}
	}
}

func TestApplyReviewVerdictsDropsRejectedCandidates(t *testing.T) {
	at := &AutoTrader{}
	cands := []dailyCandidate{{symbol: "AUSDT"}, {symbol: "BUSDT"}, {symbol: "CUSDT"}}
	verdicts := map[string]reviewVerdict{"BUSDT": {Action: "reject", Reasoning: "hack"}, "AUSDT": {Action: "approve"}}
	var logs []string
	logf := func(f string, a ...interface{}) { logs = append(logs, f) }
	kept := at.applyReviewVerdicts(cands, nil, verdicts, logf, &store.DecisionRecord{})
	if len(kept) != 2 || kept[0].symbol != "AUSDT" || kept[1].symbol != "CUSDT" {
		t.Fatalf("kept = %+v, want A and C (no verdict means kept)", kept)
	}
}
