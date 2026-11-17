package trader

import (
	"fmt"
	"math"
	"sort"
	"time"

	"nofx/kernel"
	"nofx/market"
	"nofx/store"
)

// Rule-based daily trend following (risk_control.daily_trend). When enabled it
// replaces the AI entirely:
//   - entry: in the first EntryWindowHours after a daily close (00:00 UTC),
//     go long any universe coin whose close beat every close of the prior
//     BreakoutDays and sits above its TrendEMADays EMA — strongest breakout
//     first, until MaxPositions are held in total;
//   - size: the initial stop, ATRMultiple daily ATRs below the price, costs
//     RiskPerTrade of equity;
//   - exit: a resting stop order raised at every daily close to the highest
//     daily close since entry minus ATRMultiple ATRs (never lowered), and a
//     market close after MaxHoldDays. No take-profit.
//
// This is the scheme tested in /tmp/bt/bt_1y.py on crs (see the strategy.go
// comment on DailyTrend for the numbers). The 1-minute AI exits — drawdown
// close, ATR-4h trail, profit lock — are off in this mode because they would
// fight the daily stop.

// dailyTrendMinNotional is the order value the exchange will accept. The
// default is the 10 USDT that trader/binance's CheckMinNotional enforces on
// every symbol; BTC/ETH/LINK reject even that via their own exchange filter.
//
// This used to default to 5 (exchangeInfo MIN_NOTIONAL), and every signal in
// 2026-11-07 → 11-11 was then sized at 9.3–9.9 USDT and rejected by the
// trader's own 10 USDT check — the strategy could not open a position at all.
var dailyTrendMinNotional = map[string]float64{"BTCUSDT": 50, "ETHUSDT": 20, "LINKUSDT": 20}

const (
	dailyTrendDefaultMinNotional = 10.0
	// Orders are sized this much above the floor: quantity is rounded to the
	// exchange step, which cost up to 1% on 1000PEPE.
	dailyTrendMinNotionalHeadroom = 1.05
	// Enough history for the EMA's start-up bias to vanish ((49/51)^150 ≈ 0.2%).
	dailyTrendKlineLimit = 200
)

// dailyTrendConfig returns the daily trend rules (defaults filled in) and
// whether the mode is on.
func (at *AutoTrader) dailyTrendConfig() (store.DailyTrendConfig, bool) {
	if at.config.StrategyConfig == nil || at.config.StrategyConfig.RiskControl.DailyTrend == nil {
		return store.DailyTrendConfig{}, false
	}
	cfg := at.config.StrategyConfig.RiskControl.DailyTrend.WithDefaults()
	return cfg, cfg.Enabled
}

// runDailyTrendCycle is the whole decision cycle in daily trend mode.
func (at *AutoTrader) runDailyTrendCycle(ctx *kernel.Context, record *store.DecisionRecord, cfg store.DailyTrendConfig) error {
	now := time.Now().UTC()
	dayClose := now.Truncate(24 * time.Hour) // the latest daily close, 00:00 UTC

	record.SystemPrompt = "daily trend rule mode (risk_control.daily_trend): no AI call"
	record.AccountState = store.AccountSnapshot{
		TotalBalance:          ctx.Account.TotalEquity,
		AvailableBalance:      ctx.Account.AvailableBalance,
		TotalUnrealizedProfit: ctx.Account.UnrealizedPnL,
		PositionCount:         ctx.Account.PositionCount,
		InitialBalance:        at.initialBalance,
	}
	logf := func(format string, args ...interface{}) {
		msg := fmt.Sprintf(format, args...)
		at.logInfof("📅 %s", msg)
		record.ExecutionLog = append(record.ExecutionLog, msg)
	}

	api := market.NewAPIClient()
	cache := map[string][]market.Kline{}
	bars := func(symbol string) ([]market.Kline, error) {
		if b, ok := cache[symbol]; ok {
			return b, nil
		}
		raw, err := api.GetKlines(symbol, "1d", dailyTrendKlineLimit)
		if err != nil {
			return nil, err
		}
		b := closedDailyBars(raw, now)
		cache[symbol] = b
		return b, nil
	}

	held := make(map[string]bool, len(ctx.Positions))
	longs := 0
	for _, pos := range ctx.Positions {
		held[pos.Symbol] = true
		if pos.Side != "long" {
			logf("%s %s: not a long, left to its resting orders", pos.Symbol, pos.Side)
			continue
		}
		longs++
		at.manageDailyTrendPosition(pos, cfg, now, bars, logf, record)
	}

	if since := now.Sub(dayClose); since <= time.Duration(cfg.EntryWindowHours)*time.Hour {
		cands := at.findDailyBreakouts(cfg, dayClose, held, bars, logf)
		// One review per daily close; a failed review retries next cycle.
		if mode := dailyReviewMode(cfg); mode != reviewOff && at.dailyReviewedClose != dayClose.UnixMilli() && (len(cands) > 0 || longs > 0) {
			if verdicts := at.reviewDailyTrend(mode, cands, ctx.Positions, bars, logf, record); verdicts != nil {
				at.dailyReviewedClose = dayClose.UnixMilli()
				if mode == reviewEnforce {
					cands = at.applyReviewVerdicts(cands, ctx.Positions, verdicts, logf, record)
				}
			}
		}
		at.openDailyBreakouts(ctx, cfg, cands, logf, record)
	} else {
		logf("no entries: %.1fh since the %s close, window is %dh", since.Hours(), dayClose.Format("2006-01-02"), cfg.EntryWindowHours)
	}

	record.Success = true
	if err := at.saveDecision(record); err != nil {
		at.logWarnf("⚠ Failed to save decision record: %v", err)
	}
	return nil
}

// manageDailyTrendPosition enforces max hold and ratchets the chandelier stop.
func (at *AutoTrader) manageDailyTrendPosition(pos kernel.PositionInfo, cfg store.DailyTrendConfig, now time.Time,
	bars func(string) ([]market.Kline, error), logf func(string, ...interface{}), record *store.DecisionRecord) {
	entryMs := pos.UpdateTime // entry time from trader_positions when known
	if entryMs > 0 && now.Sub(time.UnixMilli(entryMs)) >= time.Duration(cfg.MaxHoldDays)*24*time.Hour {
		logf("%s: held %d days (max %d), closing at market", pos.Symbol, int(now.Sub(time.UnixMilli(entryMs)).Hours()/24), cfg.MaxHoldDays)
		at.closeDailyTrendPosition(pos, "max hold reached", logf, record)
		return
	}

	b, err := bars(pos.Symbol)
	if err != nil || len(b) <= cfg.ATRDays {
		logf("%s: daily bars unavailable (%v), resting stop left as is", pos.Symbol, err)
		return
	}
	atr := wilderATR(b, cfg.ATRDays)
	candidate := chandelierStop(b, entryMs, pos.EntryPrice, atr, cfg.ATRMultiple)
	current, found, err := at.restingStopPrice(pos.Symbol, "long")
	if err != nil {
		logf("%s: could not read open orders (%v), stop left as is this cycle", pos.Symbol, err)
		return
	}
	if candidate <= 0 || (found && candidate <= current) {
		return // stops only ever move up
	}
	if candidate >= pos.MarkPrice {
		logf("%s: wanted stop %.6f is at/above mark %.6f, closing at market", pos.Symbol, candidate, pos.MarkPrice)
		at.closeDailyTrendPosition(pos, "daily stop already crossed", logf, record)
		return
	}
	if found {
		if err := at.trader.CancelStopLossOrders(pos.Symbol); err != nil {
			logf("%s: could not cancel the old stop (%v), keeping it", pos.Symbol, err)
			return
		}
	}
	if err := at.trader.SetStopLoss(pos.Symbol, "LONG", pos.Quantity, candidate); err != nil {
		logf("%s: failed to place stop %.6f (%v), position unprotected until next cycle", pos.Symbol, candidate, err)
		return
	}
	logf("%s: stop %.6f → %.6f (best close since entry − %.1f×ATR %.6f)", pos.Symbol, current, candidate, cfg.ATRMultiple, atr)
}

func (at *AutoTrader) closeDailyTrendPosition(pos kernel.PositionInfo, why string, logf func(string, ...interface{}), record *store.DecisionRecord) {
	action := store.DecisionAction{Action: "close_long", Symbol: pos.Symbol, Quantity: pos.Quantity, Price: pos.MarkPrice,
		Reasoning: "daily trend: " + why, Timestamp: time.Now().UTC()}
	if err := at.emergencyClosePosition(pos.Symbol, "long"); err != nil {
		logf("%s: market close failed: %v", pos.Symbol, err)
		action.Error = err.Error()
	} else {
		action.Success = true
	}
	record.Decisions = append(record.Decisions, action)
}

// dailyCandidate is one breakout at the latest daily close.
type dailyCandidate struct {
	symbol   string
	strength float64 // how far the close cleared the prior high
	atr      float64
	reason   string
}

// findDailyBreakouts returns this close's breakouts among coins not held and
// not already traded off this close, strongest first.
func (at *AutoTrader) findDailyBreakouts(cfg store.DailyTrendConfig, dayClose time.Time, held map[string]bool,
	bars func(string) ([]market.Kline, error), logf func(string, ...interface{})) []dailyCandidate {
	var found []dailyCandidate
	lastExit := at.lastExitBySymbol()
	closeMs := dayClose.UnixMilli()
	for _, sym := range cfg.Symbols {
		if held[sym] || lastExit[sym] >= closeMs {
			continue // held, or already traded off this close (no same-day re-entry)
		}
		b, err := bars(sym)
		if err != nil {
			logf("%s: daily bars unavailable: %v", sym, err)
			continue
		}
		if len(b) == 0 || b[len(b)-1].CloseTime < closeMs-1 {
			continue // the bar for this close is not in yet
		}
		closes := make([]float64, len(b))
		for i, k := range b {
			closes[i] = k.Close
		}
		if ok, strength := dailyBreakout(closes, cfg.BreakoutDays, cfg.TrendEMADays); ok {
			last := closes[len(closes)-1]
			found = append(found, dailyCandidate{symbol: sym, strength: strength, atr: wilderATR(b, cfg.ATRDays),
				reason: fmt.Sprintf("daily close %.6f > %d-day high %.6f (+%.2f%%) and above EMA%d",
					last, cfg.BreakoutDays, last/(1+strength), 100*strength, cfg.TrendEMADays)})
		}
	}
	if len(found) == 0 {
		logf("no breakout at the %s close", dayClose.Format("2006-01-02"))
	}
	sort.Slice(found, func(i, j int) bool { return found[i].strength > found[j].strength })
	return found
}

// openDailyBreakouts fills free slots with the candidates, in order.
func (at *AutoTrader) openDailyBreakouts(ctx *kernel.Context, cfg store.DailyTrendConfig, cands []dailyCandidate,
	logf func(string, ...interface{}), record *store.DecisionRecord) {
	if len(cands) == 0 {
		return
	}
	slots := cfg.MaxPositions - len(ctx.Positions)
	available := ctx.Account.AvailableBalance
	for _, c := range cands {
		if slots <= 0 {
			logf("%s: breakout (+%.2f%% over the %d-day high) skipped, %d of %d positions in use",
				c.symbol, 100*c.strength, cfg.BreakoutDays, len(ctx.Positions), cfg.MaxPositions)
			continue
		}
		if at.openDailyTrendPosition(c.symbol, c.atr, c.reason, cfg, ctx.Account.TotalEquity, &available, logf, record) {
			slots--
		}
	}
}

// applyReviewVerdicts acts on the AI's verdicts in enforce mode: rejected
// candidates are dropped and "exit" positions are closed at market.
func (at *AutoTrader) applyReviewVerdicts(cands []dailyCandidate, positions []kernel.PositionInfo, verdicts map[string]reviewVerdict,
	logf func(string, ...interface{}), record *store.DecisionRecord) []dailyCandidate {
	kept := cands[:0]
	for _, c := range cands {
		if v, ok := verdicts[c.symbol]; ok && v.Action == "reject" {
			logf("%s: breakout dropped by the AI review: %s", c.symbol, v.Reasoning)
			continue
		}
		kept = append(kept, c)
	}
	for _, p := range positions {
		if v, ok := verdicts[p.Symbol]; ok && p.Side == "long" && v.Action == "exit" {
			at.closeDailyTrendPosition(p, "AI review exit: "+v.Reasoning, logf, record)
		}
	}
	return kept
}

func (at *AutoTrader) openDailyTrendPosition(symbol string, atr float64, reason string, cfg store.DailyTrendConfig, equity float64,
	available *float64, logf func(string, ...interface{}), record *store.DecisionRecord) bool {
	price, err := at.trader.GetMarketPrice(symbol)
	if err != nil || price <= 0 || atr <= 0 {
		logf("%s: no price/ATR (price %.6f, atr %.6f, err %v), skipped", symbol, price, atr, err)
		return false
	}
	stop := price - cfg.ATRMultiple*atr
	if stop <= 0 {
		logf("%s: stop %.6f would be below zero, skipped", symbol, stop)
		return false
	}

	minNotional := dailyTrendMinNotional[symbol]
	if minNotional == 0 {
		minNotional = dailyTrendDefaultMinNotional
	}
	notional, ok := dailyTrendSizedNotional(equity, cfg.RiskPerTrade, price, atr, cfg.ATRMultiple, minNotional)
	if !ok {
		logf("%s: breakout, but %.2f USDT at %.1f%% risk is under the %.0f USDT minimum and topping up to it would risk %.2f%% of equity, skipped",
			symbol, dailyTrendNotional(equity, cfg.RiskPerTrade, price, atr, cfg.ATRMultiple), 100*cfg.RiskPerTrade,
			minNotional, 100*minNotional*(cfg.ATRMultiple*atr/price)/equity)
		return false
	}
	if floor := minNotional * dailyTrendMinNotionalHeadroom; notional < floor {
		logf("%s: %.2f USDT at %.1f%% risk is under the exchange minimum, opening at %.2f USDT instead",
			symbol, notional, 100*cfg.RiskPerTrade, floor)
		notional = floor
	}
	if capped, wasCapped := at.enforcePositionValueRatio(notional, equity, symbol); wasCapped {
		notional = capped
	}
	leverage := at.dailyTrendLeverage(symbol)
	marginFactor := marginOverheadFactor/float64(leverage) + takerFeeRate
	if affordable := *available / marginFactor * positionSizeSafetyFactor; notional > affordable {
		notional = affordable
		if notional < minNotional {
			logf("%s: only %.2f USDT margin available, under the %.0f USDT order minimum, skipped", symbol, notional, minNotional)
			return false
		}
	}
	quantity := notional / price
	if quantity*price < minNotional {
		// Quantity rounding to the exchange step can land under the floor.
		quantity = notional * dailyTrendMinNotionalHeadroom / price
	}
	action := store.DecisionAction{Action: "open_long", Symbol: symbol, Quantity: quantity, Leverage: leverage, Price: price,
		StopLoss: stop, Reasoning: "daily trend: " + reason, Timestamp: time.Now().UTC()}
	defer func() { record.Decisions = append(record.Decisions, action) }()

	if err := at.trader.SetMarginMode(symbol, at.config.IsCrossMargin); err != nil {
		logf("%s: set margin mode failed (%v), continuing", symbol, err)
	}
	order, err := at.trader.OpenLong(symbol, quantity, leverage)
	if err != nil {
		logf("%s: open failed: %v", symbol, err)
		action.Error = err.Error()
		return false
	}
	if orderID, ok := order["orderId"].(int64); ok {
		action.OrderID = orderID
	}
	at.recordAndConfirmOrder(order, symbol, "open_long", quantity, price, leverage, 0)
	at.positionFirstSeenTime[symbol+"_long"] = time.Now().UnixMilli()

	if err := at.trader.SetStopLoss(symbol, "LONG", quantity, stop); err != nil {
		// Never hold a position this mode cannot protect.
		logf("%s: stop placement failed (%v), closing instead of holding it unprotected", symbol, err)
		if cerr := at.emergencyClosePosition(symbol, "long"); cerr != nil {
			logf("%s: emergency close also failed: %v", symbol, cerr)
		}
		action.Error = "stop placement failed: " + err.Error()
		return false
	}
	*available -= notional * marginFactor
	action.Success = true
	logf("%s: opened long %.6f @ ~%.6f (%.2f USDT, %dx), stop %.6f (%.1f×ATR %.6f, %.1f%% below); %s",
		symbol, quantity, price, notional, leverage, stop, cfg.ATRMultiple, atr, 100*(price-stop)/price, reason)
	return true
}

func (at *AutoTrader) dailyTrendLeverage(symbol string) int {
	lev := 5
	if at.config.StrategyConfig != nil {
		rc := at.config.StrategyConfig.RiskControl
		lev = rc.AltcoinMaxLeverage
		if symbol == "BTCUSDT" || symbol == "ETHUSDT" {
			lev = rc.BTCETHMaxLeverage
		}
	}
	if lev <= 0 {
		lev = 5
	}
	return lev
}

// lastExitBySymbol maps symbol → most recent exit time among this trader's
// recent closed positions.
func (at *AutoTrader) lastExitBySymbol() map[string]int64 {
	out := map[string]int64{}
	if at.store == nil {
		return out
	}
	closed, err := at.store.Position().GetClosedPositions(at.id, 200)
	if err != nil {
		at.logWarnf("⚠️ daily trend: could not read closed positions: %v", err)
		return out
	}
	for _, p := range closed {
		if p.ExitTime > out[p.Symbol] {
			out[p.Symbol] = p.ExitTime
		}
	}
	return out
}

// closedDailyBars drops the still-forming bar the exchange returns last.
func closedDailyBars(bars []market.Kline, now time.Time) []market.Kline {
	n := len(bars)
	for n > 0 && bars[n-1].CloseTime >= now.UnixMilli() {
		n--
	}
	return bars[:n]
}

// dailyBreakout reports whether the last close beat every close of the prior
// breakoutDays and the emaDays EMA, and by how much it cleared that high.
func dailyBreakout(closes []float64, breakoutDays, emaDays int) (bool, float64) {
	n := len(closes)
	if n < breakoutDays+1 || n < emaDays {
		return false, 0
	}
	last := closes[n-1]
	high := closes[n-1-breakoutDays]
	for _, c := range closes[n-breakoutDays : n-1] {
		high = math.Max(high, c)
	}
	if last <= high || last <= emaLast(closes, emaDays) {
		return false, 0
	}
	return true, last/high - 1
}

// emaLast is the final value of an EMA seeded with the first close.
func emaLast(closes []float64, period int) float64 {
	a := 2 / float64(period+1)
	e := closes[0]
	for _, c := range closes[1:] {
		e = a*c + (1-a)*e
	}
	return e
}

// wilderATR seeds with the mean of the first n true ranges, then smooths.
func wilderATR(bars []market.Kline, n int) float64 {
	if n <= 0 || len(bars) <= n {
		return 0
	}
	var atr float64
	for i := 1; i < len(bars); i++ {
		h, l, pc := bars[i].High, bars[i].Low, bars[i-1].Close
		tr := math.Max(h-l, math.Max(math.Abs(h-pc), math.Abs(l-pc)))
		if i <= n {
			atr += tr / float64(n)
		} else {
			atr = (atr*float64(n-1) + tr) / float64(n)
		}
	}
	return atr
}

// chandelierStop is the best of the entry price and every daily close since
// entry, minus mult ATRs.
func chandelierStop(bars []market.Kline, entryMs int64, entryPrice, atr, mult float64) float64 {
	best := entryPrice
	for _, b := range bars {
		if b.CloseTime > entryMs && b.Close > best {
			best = b.Close
		}
	}
	return best - mult*atr
}

// dailyTrendSizedNotional returns the notional to trade: the risk-sized one,
// or the exchange minimum when that is below it, as long as trading the
// minimum never risks more than twice RiskPerTrade. ok is false when no size
// both reaches the minimum and stays inside that ceiling.
func dailyTrendSizedNotional(equity, riskFraction, price, atr, mult, minNotional float64) (float64, bool) {
	notional := dailyTrendNotional(equity, riskFraction, price, atr, mult)
	if notional <= 0 {
		return 0, false
	}
	floor := minNotional * dailyTrendMinNotionalHeadroom
	if notional >= floor {
		return notional, true
	}
	riskAtFloor := floor * (mult * atr / price) / equity
	if riskAtFloor > 2*riskFraction {
		return 0, false
	}
	return floor, true
}

// dailyTrendNotional sizes a position so that a stop mult ATRs below price
// costs riskFraction of equity.
func dailyTrendNotional(equity, riskFraction, price, atr, mult float64) float64 {
	if price <= 0 || atr <= 0 || mult <= 0 {
		return 0
	}
	return equity * riskFraction / (mult * atr / price)
}
