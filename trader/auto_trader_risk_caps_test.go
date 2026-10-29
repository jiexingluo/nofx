package trader

import (
	"math"
	"strings"
	"testing"

	"nofx/kernel"
	"nofx/store"
)

func riskCapTrader(maxRisk float64) *AutoTrader {
	cfg := store.GetDefaultStrategyConfig("en")
	cfg.RiskControl.MaxRiskPerTrade = maxRisk
	// Keep the notional cap out of the way so these tests isolate the risk cap.
	cfg.RiskControl.AltcoinMaxPositionValueRatio = 100
	cfg.RiskControl.BTCETHMaxPositionValueRatio = 100
	return &AutoTrader{config: AutoTraderConfig{StrategyConfig: &cfg}}
}

// TestEnforceMaxRiskPerTrade pins the guard added after a 13-trade losing
// streak took 41% of the account, with single losses reaching ~7% of equity.
func TestEnforceMaxRiskPerTrade(t *testing.T) {
	t.Run("caps a position whose stop would cost more than the limit", func(t *testing.T) {
		at := riskCapTrader(0.03)
		// entry 100, stop 90 => 10% of notional at risk.
		// equity 1000, cap 3% => max risk 30 USDT => max notional 300.
		got, capped := at.enforceMaxRiskPerTrade(1000, 100, 90, 1000, "TESTUSDT")
		if !capped {
			t.Fatal("expected the position to be capped")
		}
		if math.Abs(got-300) > 0.01 {
			t.Fatalf("expected notional capped to 300, got %.4f", got)
		}
		// The capped size must actually satisfy the limit.
		if risk := got * 0.10; risk > 1000*0.03+0.01 {
			t.Fatalf("capped size still risks %.4f, above the 30 limit", risk)
		}
	})

	t.Run("leaves a position already within the limit untouched", func(t *testing.T) {
		at := riskCapTrader(0.03)
		// entry 100, stop 99 => 1% at risk; 200 notional => 2 USDT on 1000 equity.
		got, capped := at.enforceMaxRiskPerTrade(200, 100, 99, 1000, "TESTUSDT")
		if capped {
			t.Fatal("position within the limit must not be capped")
		}
		if got != 200 {
			t.Fatalf("size should be unchanged, got %.4f", got)
		}
	})

	t.Run("a wider stop yields a proportionally smaller position", func(t *testing.T) {
		at := riskCapTrader(0.03)
		tight, _ := at.enforceMaxRiskPerTrade(10000, 100, 98, 1000, "TESTUSDT") // 2% stop
		wide, _ := at.enforceMaxRiskPerTrade(10000, 100, 92, 1000, "TESTUSDT")  // 8% stop
		if !(wide < tight) {
			t.Fatalf("wider stop must produce the smaller position: tight=%.2f wide=%.2f", tight, wide)
		}
	})

	t.Run("short side works off the same absolute distance", func(t *testing.T) {
		at := riskCapTrader(0.03)
		// Short: stop ABOVE entry. 100 -> 110 is the same 10% distance.
		got, capped := at.enforceMaxRiskPerTrade(1000, 100, 110, 1000, "TESTUSDT")
		if !capped || math.Abs(got-300) > 0.01 {
			t.Fatalf("short stop should cap identically to long, got %.4f capped=%v", got, capped)
		}
	})

	t.Run("missing stop-loss cannot be capped and passes through", func(t *testing.T) {
		at := riskCapTrader(0.03)
		got, capped := at.enforceMaxRiskPerTrade(1000, 100, 0, 1000, "TESTUSDT")
		if capped || got != 1000 {
			t.Fatalf("with no stop there is nothing to size against; got %.4f capped=%v", got, capped)
		}
	})

	t.Run("unset config falls back to the 3% default rather than no cap", func(t *testing.T) {
		at := riskCapTrader(0) // explicitly unset
		got, capped := at.enforceMaxRiskPerTrade(1000, 100, 90, 1000, "TESTUSDT")
		if !capped || math.Abs(got-300) > 0.01 {
			t.Fatalf("unset MaxRiskPerTrade must default to 3%%, got %.4f capped=%v", got, capped)
		}
	})
}

func shortBarTrader() *AutoTrader {
	cfg := store.GetDefaultStrategyConfig("en")
	cfg.RiskControl.MinConfidence = 80
	cfg.RiskControl.MinRiskRewardRatio = 2.5
	cfg.RiskControl.ShortMinConfidence = 0      // derive
	cfg.RiskControl.ShortMinRiskRewardRatio = 0 // derive
	return &AutoTrader{config: AutoTraderConfig{StrategyConfig: &cfg}}
}

func TestShortEntryBar(t *testing.T) {
	t.Run("derives a higher bar than the base limits when unset", func(t *testing.T) {
		conf, rr := shortBarTrader().shortEntryBar()
		if conf != 90 {
			t.Fatalf("expected base 80 + 10 = 90, got %d", conf)
		}
		if math.Abs(rr-3.5) > 0.001 {
			t.Fatalf("expected base 2.5 + 1.0 = 3.5, got %.2f", rr)
		}
	})

	t.Run("explicit config overrides the derived values", func(t *testing.T) {
		at := shortBarTrader()
		at.config.StrategyConfig.RiskControl.ShortMinConfidence = 95
		at.config.StrategyConfig.RiskControl.ShortMinRiskRewardRatio = 4.0
		conf, rr := at.shortEntryBar()
		if conf != 95 || math.Abs(rr-4.0) > 0.001 {
			t.Fatalf("explicit config ignored: conf=%d rr=%.2f", conf, rr)
		}
	})
}

func TestEnforceShortEntryBar(t *testing.T) {
	// Short at 100, stop 104 (risk 4), target 88 (reward 12) => 3.0:1
	lowRR := &kernel.Decision{Symbol: "X", Confidence: 95, Price: 100, StopLoss: 104, TakeProfit: 88}
	// target 86 => reward 14 => 3.5:1
	okDecision := &kernel.Decision{Symbol: "X", Confidence: 95, Price: 100, StopLoss: 104, TakeProfit: 86}

	t.Run("rejects reward-to-risk below the short bar", func(t *testing.T) {
		err := shortBarTrader().enforceShortEntryBar(lowRR)
		if err == nil || !strings.Contains(err.Error(), "reward-to-risk") {
			t.Fatalf("expected an R/R rejection, got %v", err)
		}
	})

	t.Run("rejects confidence below the short bar", func(t *testing.T) {
		d := *okDecision
		d.Confidence = 85 // clears the base 80 but not the short 90
		err := shortBarTrader().enforceShortEntryBar(&d)
		if err == nil || !strings.Contains(err.Error(), "confidence") {
			t.Fatalf("expected a confidence rejection, got %v", err)
		}
	})

	t.Run("accepts a short that clears both", func(t *testing.T) {
		if err := shortBarTrader().enforceShortEntryBar(okDecision); err != nil {
			t.Fatalf("expected acceptance, got %v", err)
		}
	})

	// An unverifiable short is exactly what this guard exists to stop, so a
	// decision missing the levels must not slip through as "nothing to check".
	t.Run("rejects a short with no stop or target to verify", func(t *testing.T) {
		d := &kernel.Decision{Symbol: "X", Confidence: 99, Price: 100}
		if err := shortBarTrader().enforceShortEntryBar(d); err == nil {
			t.Fatal("expected rejection when R/R cannot be verified")
		}
	})

	t.Run("rejects even a short that clears the bar when shorts are disabled", func(t *testing.T) {
		at := shortBarTrader()
		at.config.StrategyConfig.RiskControl.DisableShorts = true
		err := at.enforceShortEntryBar(okDecision)
		if err == nil || !strings.Contains(err.Error(), "disable_shorts") {
			t.Fatalf("expected a disabled-shorts rejection, got %v", err)
		}
	})
}
