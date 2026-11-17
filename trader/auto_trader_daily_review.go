package trader

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"nofx/kernel"
	"nofx/market"
	"nofx/store"
)

// AI review for the daily trend mode (risk_control.daily_trend.ai_review).
//
// The rules decide what to trade; the AI is asked only what the rules cannot
// see — event risk in the news, a breakout the news contradicts, market-wide
// stress — with market, sentiment, positioning and news context in front of
// it. In "shadow" mode its verdicts are recorded but change nothing, so its
// value can be measured (approved vs rejected outcomes) before it is trusted.
//
// What the AI is told about sentiment and positioning follows the 2026-11
// backtest of this scheme: mechanical filters hurt — skipping entries with
// funding in its top 10% would have cut 6 of the 10 biggest winners — so
// those numbers are context, never a rule.

const (
	reviewOff     = "off"
	reviewShadow  = "shadow"
	reviewEnforce = "enforce"
)

const dailyReviewSystemPrompt = `You are the risk reviewer for a rule-based crypto trend-following system (Binance USDT-M perpetuals, long only).

How the system trades: at each daily close it buys coins whose close beats the highest close of the prior 20 days and sits above the 50-day EMA. The stop sits 3 daily ATRs below and trails the best daily close; there is no profit target; positions are closed after 60 days. Expect a win rate near 33%: most trades lose a little and the profit comes from a few large trends. A breakout that has already risen IS the signal — never reject it for being "already up", "overbought", or for high funding alone. In this system's last 12 months the biggest winners entered with funding in the top 10% of its own history: leverage joining a trend is often fuel, not a warning.

Your job is to catch what the rule cannot see:
- coin-specific event risk in the news: hacks or exploits, delistings, regulatory or legal action, large token unlocks, insider or team selling, network halts, stablecoin depegs;
- a breakout driven by a one-off headline that the market is already selling (news and price disagree);
- market-wide stress: BTC breaking down, liquidation cascades, systemic events such as exchange failures;
- for open positions: a material negative development that the trailing stop would only react to after a large loss.

The Fear & Greed index mostly follows price and is context only; funding near 0.01% per 8h carries no information.

For every candidate decide "approve" or "reject"; for every open position "hold" or "exit". Approve or hold unless you can name a specific, material risk from the data given. Never invent news or numbers; if a section is missing, say so and judge on what is there.

Output only a JSON array, one object per symbol, exactly these fields:
[{"symbol": "XMRUSDT", "action": "approve", "confidence": 70, "reasoning": "at most two sentences citing the data", "leverage": null, "position_size_usd": null, "stop_loss": null, "take_profit": null, "price": null, "quantity": null, "risk_usd": null}]`

// reviewVerdict is the AI's call on one symbol.
type reviewVerdict struct {
	Action     string // approve | reject | hold | exit
	Confidence int
	Reasoning  string
}

func dailyReviewMode(cfg store.DailyTrendConfig) string {
	switch strings.ToLower(strings.TrimSpace(cfg.AIReview)) {
	case reviewShadow:
		return reviewShadow
	case reviewEnforce:
		return reviewEnforce
	}
	return reviewOff
}

// reviewDailyTrend asks the AI about this close's candidates and the open
// positions. A failed call or unparseable answer returns nil: the rules then
// trade unreviewed, exactly as with the review off.
func (at *AutoTrader) reviewDailyTrend(mode string, cands []dailyCandidate, positions []kernel.PositionInfo,
	bars func(string) ([]market.Kline, error), logf func(string, ...interface{}), record *store.DecisionRecord) map[string]reviewVerdict {
	if at.mcpClient == nil {
		logf("AI review (%s) skipped: no AI client", mode)
		return nil
	}
	dossier := buildReviewDossier(gatherReviewContext(cands, positions, bars, time.Now().UTC()))
	record.InputPrompt = dossier
	start := time.Now()
	raw, err := at.mcpClient.CallWithMessages(dailyReviewSystemPrompt, dossier)
	record.AIRequestDurationMs = time.Since(start).Milliseconds()
	record.RawResponse = raw
	if err != nil {
		logf("AI review (%s) failed, rules trade unreviewed: %v", mode, err)
		return nil
	}
	verdicts, reasoning, err := parseReviewVerdicts(raw)
	record.CoTTrace = reasoning
	if err != nil {
		logf("AI review (%s) unreadable, rules trade unreviewed: %v", mode, err)
		return nil
	}
	symbols := make([]string, 0, len(verdicts))
	for sym := range verdicts {
		symbols = append(symbols, sym)
	}
	sort.Strings(symbols)
	for _, sym := range symbols {
		v := verdicts[sym]
		logf("AI review (%s) %s → %s (%d%%): %s", mode, sym, v.Action, v.Confidence, v.Reasoning)
		record.Decisions = append(record.Decisions, store.DecisionAction{Action: "review_" + v.Action, Symbol: sym,
			Confidence: v.Confidence, Reasoning: v.Reasoning, Timestamp: time.Now().UTC(), Success: true})
	}
	return verdicts
}

// parseReviewVerdicts reads the AI's JSON array, tolerating a leading
// <reasoning> block and prose around the array. Unknown actions are dropped.
func parseReviewVerdicts(raw string) (map[string]reviewVerdict, string, error) {
	reasoning := ""
	if i := strings.Index(raw, "<reasoning>"); i >= 0 {
		if j := strings.Index(raw, "</reasoning>"); j > i {
			reasoning = strings.TrimSpace(raw[i+len("<reasoning>") : j])
			raw = raw[:i] + raw[j+len("</reasoning>"):]
		}
	}
	start, end := strings.Index(raw, "["), strings.LastIndex(raw, "]")
	if start < 0 || end <= start {
		return nil, reasoning, fmt.Errorf("no JSON array in the response")
	}
	var items []struct {
		Symbol     string   `json:"symbol"`
		Action     string   `json:"action"`
		Confidence *float64 `json:"confidence"`
		Reasoning  string   `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &items); err != nil {
		return nil, reasoning, fmt.Errorf("bad JSON: %w", err)
	}
	out := make(map[string]reviewVerdict, len(items))
	for _, it := range items {
		action := strings.ToLower(strings.TrimSpace(it.Action))
		switch action {
		case "approve", "reject", "hold", "exit":
		default:
			continue
		}
		v := reviewVerdict{Action: action, Reasoning: strings.TrimSpace(it.Reasoning)}
		if it.Confidence != nil {
			v.Confidence = int(math.Round(*it.Confidence))
		}
		out[strings.ToUpper(strings.TrimSpace(it.Symbol))] = v
	}
	if len(out) == 0 {
		return nil, reasoning, fmt.Errorf("no usable verdicts")
	}
	return out, reasoning, nil
}

// reviewContext is everything the dossier shows, gathered up front so the
// formatting is a pure function (and testable without the network).
type reviewContext struct {
	now        time.Time
	btc        []market.Kline
	valuation  *market.ValuationData
	fearGreed  []market.FearGreedPoint
	news       []market.NewsItem
	newsErrors []string
	coins      []coinContext
}

type coinContext struct {
	symbol    string
	candidate *dailyCandidate      // set for a breakout candidate
	position  *kernel.PositionInfo // set for an open position
	bars      []market.Kline
	funding   []market.FundingPoint
	oi        []market.OpenInterestPoint
	longShort []market.LongShortPoint
	errs      []string
}

func gatherReviewContext(cands []dailyCandidate, positions []kernel.PositionInfo, bars func(string) ([]market.Kline, error), now time.Time) reviewContext {
	ctx := reviewContext{now: now}
	ctx.btc, _ = bars("BTCUSDT")
	if v, err := market.FetchValuationData(); err == nil {
		ctx.valuation = v
	}
	if fg, err := market.FetchFearGreedHistory(31); err == nil {
		ctx.fearGreed = fg
	}
	news, errs := market.FetchRSSNews(market.DefaultNewsFeeds, now.Add(-72*time.Hour))
	for _, err := range errs {
		ctx.newsErrors = append(ctx.newsErrors, err.Error())
	}
	if token := os.Getenv("CRYPTOPANIC_API_TOKEN"); token != "" {
		if cp, err := market.FetchCryptoPanicNews(token, os.Getenv("CRYPTOPANIC_API_PLAN"), nil); err == nil {
			news = append(news, cp...)
		} else {
			ctx.newsErrors = append(ctx.newsErrors, err.Error())
		}
	}
	sort.SliceStable(news, func(i, j int) bool { return news[i].Published.After(news[j].Published) })
	ctx.news = news

	add := func(c coinContext) {
		c.bars, _ = bars(c.symbol)
		var err error
		if c.funding, err = market.FetchFundingHistory(c.symbol, 1000); err != nil {
			c.errs = append(c.errs, err.Error())
		}
		if c.oi, err = market.FetchOpenInterestHistory(c.symbol, "1d", 8); err != nil {
			c.errs = append(c.errs, err.Error())
		}
		if c.longShort, err = market.FetchLongShortAccountRatio(c.symbol, "1h", 25); err != nil {
			c.errs = append(c.errs, err.Error())
		}
		ctx.coins = append(ctx.coins, c)
	}
	for i := range cands {
		add(coinContext{symbol: cands[i].symbol, candidate: &cands[i]})
	}
	for i := range positions {
		if positions[i].Side == "long" {
			add(coinContext{symbol: positions[i].Symbol, position: &positions[i]})
		}
	}
	return ctx
}

// buildReviewDossier renders the context as the AI's user prompt.
func buildReviewDossier(c reviewContext) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Review time: %s UTC\n\n## Market\n", c.now.Format("2006-01-02 15:04")))
	if line := trendLine("BTC", c.btc); line != "" {
		sb.WriteString(line + "\n")
	} else {
		sb.WriteString("BTC daily data: unavailable\n")
	}
	if v := c.valuation; v != nil {
		sb.WriteString(fmt.Sprintf("Total crypto market cap $%.0fB (24h %+.1f%%) | BTC dominance %.1f%%\n", v.TotalMarketCap/1e9, v.MarketCapChange24h, v.BTCDominance))
	}
	if n := len(c.fearGreed); n > 0 {
		last := c.fearGreed[n-1]
		line := fmt.Sprintf("Fear & Greed: %d (%s)", last.Value, last.Label)
		if n > 7 {
			line += fmt.Sprintf(", 7 days ago %d", c.fearGreed[n-8].Value)
		}
		if n > 30 {
			line += fmt.Sprintf(", 30 days ago %d", c.fearGreed[n-31].Value)
		}
		sb.WriteString(line + "\n")
	} else {
		sb.WriteString("Fear & Greed: unavailable\n")
	}
	sb.WriteString("\nLatest crypto headlines (72h):\n")
	var marketNews []market.NewsItem
	for _, n := range c.news {
		if market.IsCryptoHeadline(n) {
			marketNews = append(marketNews, n)
		}
	}
	writeHeadlines(&sb, marketNews, 10, c.now)
	if len(c.newsErrors) > 0 {
		sb.WriteString(fmt.Sprintf("(news sources that failed: %d)\n", len(c.newsErrors)))
	}

	for _, coin := range c.coins {
		sb.WriteString("\n")
		switch {
		case coin.candidate != nil:
			sb.WriteString(fmt.Sprintf("## Candidate %s — %s\n", coin.symbol, coin.candidate.reason))
		case coin.position != nil:
			p := coin.position
			pnl := 0.0
			if p.EntryPrice > 0 {
				pnl = (p.MarkPrice/p.EntryPrice - 1) * 100
			}
			held := ""
			if p.UpdateTime > 0 {
				held = fmt.Sprintf(", held %.1f days", c.now.Sub(time.UnixMilli(p.UpdateTime)).Hours()/24)
			}
			sb.WriteString(fmt.Sprintf("## Open position %s — entry %.6g, mark %.6g (%+.2f%%)%s\n", coin.symbol, p.EntryPrice, p.MarkPrice, pnl, held))
		}
		if line := trendLine(coin.symbol, coin.bars); line != "" {
			sb.WriteString(line + "\n")
		}
		sb.WriteString(fundingLine(coin.funding) + "\n")
		sb.WriteString(openInterestLine(coin.oi) + "\n")
		sb.WriteString(longShortLine(coin.longShort) + "\n")
		var mine []market.NewsItem
		for _, n := range c.news {
			if market.NewsMentions(n, coin.symbol) {
				mine = append(mine, n)
			}
		}
		sb.WriteString(fmt.Sprintf("Headlines mentioning %s (72h):\n", market.NewsBaseAsset(coin.symbol)))
		writeHeadlines(&sb, mine, 6, c.now)
	}
	return sb.String()
}

func writeHeadlines(sb *strings.Builder, items []market.NewsItem, max int, now time.Time) {
	if len(items) == 0 {
		sb.WriteString("- none found\n")
		return
	}
	for i, n := range items {
		if i == max {
			break
		}
		votes := ""
		if n.Positive+n.Negative+n.Important > 0 {
			votes = fmt.Sprintf(" [votes +%d/-%d, important %d]", n.Positive, n.Negative, n.Important)
		}
		sb.WriteString(fmt.Sprintf("- %.0fh ago, %s: %s%s\n", now.Sub(n.Published).Hours(), n.Source, n.Title, votes))
	}
}

// trendLine summarises a coin's daily trend from closed daily bars.
func trendLine(name string, bars []market.Kline) string {
	n := len(bars)
	if n < 51 {
		return ""
	}
	closes := make([]float64, n)
	for i, b := range bars {
		closes[i] = b.Close
	}
	last := closes[n-1]
	ema := emaLast(closes, 50)
	return fmt.Sprintf("%s daily close %.6g | 7d %+.1f%% | 30d %+.1f%% | %+.1f%% vs EMA50 | ATR14 %.1f%% of price",
		name, last, (last/closes[n-8]-1)*100, (last/closes[n-31]-1)*100, (last/ema-1)*100, wilderATR(bars, 14)/last*100)
}

// fundingLine reports the latest funding and where its 3-print mean sits in
// the coin's own recent history.
func fundingLine(f []market.FundingPoint) string {
	if len(f) < 4 {
		return "Funding: unavailable"
	}
	recent := (f[len(f)-1].Rate + f[len(f)-2].Rate + f[len(f)-3].Rate) / 3
	below := 0
	for _, p := range f {
		if p.Rate <= recent {
			below++
		}
	}
	days := float64(f[len(f)-1].Time-f[0].Time) / 86_400_000
	return fmt.Sprintf("Funding: last %.4f%%, 3-print mean %.4f%% = %.0fth percentile of the last %.0f days",
		f[len(f)-1].Rate*100, recent*100, 100*float64(below)/float64(len(f)), days)
}

func openInterestLine(oi []market.OpenInterestPoint) string {
	n := len(oi)
	if n < 2 || oi[n-2].Value <= 0 {
		return "Open interest: unavailable"
	}
	line := fmt.Sprintf("Open interest: $%.1fM, 1d %+.1f%%", oi[n-1].Value/1e6, (oi[n-1].Value/oi[n-2].Value-1)*100)
	if n >= 8 && oi[n-8].Value > 0 {
		line += fmt.Sprintf(", 7d %+.1f%%", (oi[n-1].Value/oi[n-8].Value-1)*100)
	}
	return line
}

func longShortLine(ls []market.LongShortPoint) string {
	n := len(ls)
	if n == 0 {
		return "Long/short account ratio: unavailable"
	}
	line := fmt.Sprintf("Long/short account ratio: %.2f", ls[n-1].Ratio)
	if n >= 25 {
		line += fmt.Sprintf(" (24h ago %.2f)", ls[n-25].Ratio)
	}
	return line
}
