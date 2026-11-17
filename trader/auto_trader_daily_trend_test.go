package trader

import (
	"math"
	"testing"
	"time"

	"nofx/market"
	"nofx/store"
)

func flatBars(closes ...float64) []market.Kline {
	bars := make([]market.Kline, len(closes))
	for i, c := range closes {
		bars[i] = market.Kline{High: c + 1, Low: c - 1, Close: c, CloseTime: int64(i+1) * 1000}
	}
	return bars
}

func TestDailyBreakout(t *testing.T) {
	rising := make([]float64, 60)
	for i := range rising {
		rising[i] = 100 + float64(i)
	}
	ok, strength := dailyBreakout(rising, 20, 50)
	if !ok || math.Abs(strength-(159.0/158.0-1)) > 1e-12 {
		t.Fatalf("rising series: got ok=%v strength=%v", ok, strength)
	}

	equal := append(append([]float64{}, rising[:59]...), 158) // ties the prior high
	if ok, _ := dailyBreakout(equal, 20, 50); ok {
		t.Fatal("a close equal to the prior high must not count as a breakout")
	}

	// Pops above its 20-day high but is still far below the 50-day EMA.
	belowTrend := make([]float64, 0, 61)
	for i := 0; i < 40; i++ {
		belowTrend = append(belowTrend, 200)
	}
	for i := 0; i < 20; i++ {
		belowTrend = append(belowTrend, 100)
	}
	belowTrend = append(belowTrend, 110)
	if ok, _ := dailyBreakout(belowTrend, 20, 50); ok {
		t.Fatal("a breakout below the trend EMA must not count")
	}

	if ok, _ := dailyBreakout(rising[:20], 20, 50); ok {
		t.Fatal("too little history must not signal")
	}
}

func TestWilderATRConstantRange(t *testing.T) {
	// Every bar spans close±1 around a flat close: true range is 2 throughout.
	if got := wilderATR(flatBars(100, 100, 100, 100, 100, 100), 3); math.Abs(got-2) > 1e-12 {
		t.Fatalf("ATR = %v, want 2", got)
	}
	if got := wilderATR(flatBars(100, 100), 3); got != 0 {
		t.Fatalf("too few bars should give 0, got %v", got)
	}
}

func TestChandelierStopUsesOnlyClosesAfterEntry(t *testing.T) {
	bars := []market.Kline{
		{Close: 150, CloseTime: 500}, // before entry: ignored
		{Close: 110, CloseTime: 1500},
		{Close: 105, CloseTime: 2500},
	}
	if got := chandelierStop(bars, 1000, 100, 2, 3); got != 104 {
		t.Fatalf("stop = %v, want best close 110 − 3×2 = 104", got)
	}
	if got := chandelierStop(bars[:1], 1000, 100, 2, 3); got != 94 {
		t.Fatalf("no close since entry: stop = %v, want entry 100 − 6 = 94", got)
	}
}

func TestClosedDailyBarsDropsTheFormingBar(t *testing.T) {
	now := time.UnixMilli(2_500)
	got := closedDailyBars(flatBars(1, 2, 3), now) // close times 1000, 2000, 3000
	if len(got) != 2 || got[1].Close != 2 {
		t.Fatalf("expected the bar closing after now to be dropped, got %+v", got)
	}
}

func TestDailyTrendNotional(t *testing.T) {
	// 1.5% of 164 = 2.46 at risk; a 3×4 = 12 stop on a 100 price is 12% away.
	if got := dailyTrendNotional(164, 0.015, 100, 4, 3); math.Abs(got-20.5) > 1e-9 {
		t.Fatalf("notional = %v, want 20.5", got)
	}
	if got := dailyTrendNotional(164, 0.015, 100, 0, 3); got != 0 {
		t.Fatalf("zero ATR must size to 0, got %v", got)
	}
}

func TestDailyTrendConfigSwitchAndDefaults(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("en")
	at := &AutoTrader{config: AutoTraderConfig{StrategyConfig: &cfg}}
	if _, on := at.dailyTrendConfig(); on {
		t.Fatal("no daily_trend block: mode must be off")
	}
	cfg.RiskControl.DailyTrend = &store.DailyTrendConfig{Enabled: false}
	if _, on := at.dailyTrendConfig(); on {
		t.Fatal("enabled=false: mode must be off")
	}
	cfg.RiskControl.DailyTrend = &store.DailyTrendConfig{Enabled: true, RiskPerTrade: 0.015}
	got, on := at.dailyTrendConfig()
	if !on {
		t.Fatal("enabled=true: mode must be on")
	}
	if got.RiskPerTrade != 0.015 || got.BreakoutDays != 20 || got.TrendEMADays != 50 || got.ATRDays != 14 ||
		got.ATRMultiple != 3 || got.MaxHoldDays != 60 || got.MaxPositions != 3 || got.EntryWindowHours != 4 ||
		len(got.Symbols) != len(store.DefaultDailyTrendSymbols) {
		t.Fatalf("defaults not applied / explicit value lost: %+v", got)
	}
}

// Regression for 2026-11-07…11-11: risk-sized notional of ~9.5 USDT sat under
// the trader's 10 USDT order floor and every signal was rejected, so the mode
// never traded. It must now top up to the floor, not skip.
func TestDailyTrendSizedNotionalTopsUpToTheOrderFloor(t *testing.T) {
	// The live failure: 1.5% of 164 = 2.46 risk against a very wide daily stop
	// (3 × 115 / 1330 ≈ 26%) sizes to ~9.5 USDT — under the floor, so it must
	// be topped up to 10.5 rather than skipped.
	if n := dailyTrendNotional(164, 0.015, 1330, 115, 3); n < 9 || n > 10 {
		t.Fatalf("test premise broken: risk-sized notional %.2f is not ~9.5", n)
	}
	notional, ok := dailyTrendSizedNotional(164, 0.015, 1330, 115, 3, 10)
	if !ok || notional != 10.5 {
		t.Fatalf("got (%v, %v), want the 10.5 USDT floor", notional, ok)
	}
	// A coin whose stop is so wide that the floor would risk >3% of equity is
	// still skipped: 60% stop ⇒ 10.5×0.6/164 = 3.8%.
	if _, ok := dailyTrendSizedNotional(164, 0.015, 100, 20, 3, 10); ok {
		t.Fatal("a top-up that doubles the risk must be refused")
	}
	// Sizes above the floor are left alone.
	if n, ok := dailyTrendSizedNotional(10000, 0.015, 100, 2, 3, 10); !ok || n != 2500 {
		t.Fatalf("got (%v, %v), want the risk-sized 2500", n, ok)
	}
	if _, ok := dailyTrendSizedNotional(164, 0.015, 100, 0, 3, 10); ok {
		t.Fatal("zero ATR must not size a trade")
	}
}

func TestDailyTrendFloorDefaultsToTheTraderMinimum(t *testing.T) {
	// trader/binance rejects anything under 10 USDT on every symbol; the
	// daily-trend floor must never be lower than that.
	if dailyTrendDefaultMinNotional < 10 {
		t.Fatalf("default floor %.1f is under the trader's 10 USDT check", dailyTrendDefaultMinNotional)
	}
	for _, sym := range []string{"ZECUSDT", "1000PEPEUSDT", "XMRUSDT"} {
		floor := dailyTrendMinNotional[sym]
		if floor == 0 {
			floor = dailyTrendDefaultMinNotional
		}
		if floor < 10 {
			t.Fatalf("%s floor %.1f would be rejected", sym, floor)
		}
	}
}
