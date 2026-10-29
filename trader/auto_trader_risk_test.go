package trader

import (
	"math"
	"testing"
)

func TestDrawdownCloseArmsOnPriceBasisOnly(t *testing.T) {
	cases := []struct {
		name        string
		pricePnLPct float64
		drawdownPct float64
		shouldClose bool
	}{
		// +0.5% price move (what +5% margin at 10x used to arm on) must NOT
		// arm the monitor, no matter how large the relative drawdown is.
		{"tiny price gain big drawdown", 0.5, 60.0, false},
		// Armed only from a real +5% price move, and still needs the 40% giveback.
		{"real gain small drawdown", 6.0, 20.0, false},
		{"real gain big drawdown", 6.0, 45.0, true},
		{"at threshold not armed", 5.0, 45.0, false},
		{"loss never triggers", -3.0, 80.0, false},
	}
	for _, c := range cases {
		if got := shouldDrawdownClose(c.pricePnLPct, c.drawdownPct); got != c.shouldClose {
			t.Fatalf("%s: shouldDrawdownClose(%.1f, %.1f) = %v, want %v",
				c.name, c.pricePnLPct, c.drawdownPct, got, c.shouldClose)
		}
	}
}

func TestNextPeakPrice(t *testing.T) {
	cases := []struct {
		name    string
		side    string
		peak    float64
		hasPeak bool
		mark    float64
		want    float64
	}{
		{"long no prior peak adopts mark", "long", 0, false, 100, 100},
		{"short no prior peak adopts mark", "short", 0, false, 100, 100},
		{"long new high extends peak", "long", 100, true, 110, 110},
		{"long pullback keeps peak", "long", 110, true, 105, 110},
		{"short new low extends peak", "short", 100, true, 90, 90},
		{"short bounce keeps peak", "short", 90, true, 95, 90},
		{"long unchanged mark keeps peak", "long", 100, true, 100, 100},
	}
	for _, c := range cases {
		if got := nextPeakPrice(c.side, c.peak, c.hasPeak, c.mark); got != c.want {
			t.Fatalf("%s: nextPeakPrice(%q, %.2f, %v, %.2f) = %.2f, want %.2f",
				c.name, c.side, c.peak, c.hasPeak, c.mark, got, c.want)
		}
	}
}

func TestTrailingStopCandidate(t *testing.T) {
	cases := []struct {
		name string
		side string
		peak float64
		atr  float64
		want float64
	}{
		// 1.25x multiplier is a package-level const; these pin its current value.
		{"long sits below peak by 1.25x ATR", "long", 100, 4, 95},
		{"short sits above peak by 1.25x ATR", "short", 100, 4, 105},
		{"zero ATR collapses to peak", "long", 100, 0, 100},
	}
	for _, c := range cases {
		if got := trailingStopCandidate(c.side, c.peak, c.atr); got != c.want {
			t.Fatalf("%s: trailingStopCandidate(%q, %.2f, %.2f) = %.2f, want %.2f",
				c.name, c.side, c.peak, c.atr, got, c.want)
		}
	}
}

func TestIsMoreFavorableStop(t *testing.T) {
	cases := []struct {
		name       string
		side       string
		candidate  float64
		current    float64
		hasCurrent bool
		want       bool
	}{
		{"no existing stop always favorable", "long", 90, 0, false, true},
		{"long tighter stop is favorable", "long", 95, 90, true, true},
		{"long looser stop is rejected", "long", 85, 90, true, false},
		{"long equal stop is rejected", "long", 90, 90, true, false},
		{"short tighter stop is favorable", "short", 105, 110, true, true},
		{"short looser stop is rejected", "short", 115, 110, true, false},
		{"short equal stop is rejected", "short", 110, 110, true, false},
	}
	for _, c := range cases {
		if got := isMoreFavorableStop(c.side, c.candidate, c.current, c.hasCurrent); got != c.want {
			t.Fatalf("%s: isMoreFavorableStop(%q, %.2f, %.2f, %v) = %v, want %v",
				c.name, c.side, c.candidate, c.current, c.hasCurrent, got, c.want)
		}
	}
}

// TestExitStopCandidate pins the exit rules picked from the 1-minute replay
// of all live trades: an ATR trail from entry, raised to entry+0.5% once the
// move has reached 0.5x ATR in the position's favour.
func TestExitStopCandidate(t *testing.T) {
	const atr = 4.0 // 4% of a 100 entry
	cases := []struct {
		name              string
		side              string
		entry, peak, want float64
	}{
		// Not yet 0.5xATR (2.0) in profit: plain trail, peak - 1.25*4 = peak - 5.
		{"long below lock trigger trails from peak", "long", 100, 101, 96},
		// Exactly 0.5xATR in profit: lock (100.5) beats the trail (97).
		{"long at lock trigger is locked at +0.5%", "long", 100, 102, 100.5},
		// Far in profit: the trail (110-5=105) is above the lock and wins.
		{"long far in profit trails above the lock", "long", 100, 110, 105},
		{"short below lock trigger trails from peak", "short", 100, 99, 104},
		{"short at lock trigger is locked at -0.5%", "short", 100, 98, 99.5},
		{"short far in profit trails below the lock", "short", 100, 90, 95},
		// No usable entry price: plain trail, no lock.
		{"missing entry falls back to the trail", "long", 0, 110, 105},
	}
	for _, c := range cases {
		got := exitStopCandidate(c.side, c.entry, c.peak, atr)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: exitStopCandidate(%q, entry=%.2f, peak=%.2f) = %.4f, want %.4f", c.name, c.side, c.entry, c.peak, got, c.want)
		}
	}
}

// TestProfitLockIsAboveFees: the lock must leave a trade net-positive after
// ~0.1% round-trip fees, otherwise "locked" trades still book small losses.
func TestProfitLockIsAboveFees(t *testing.T) {
	if profitLockPct <= 0.1 {
		t.Fatalf("profitLockPct = %.2f%% does not clear ~0.1%% round-trip fees", profitLockPct)
	}
}

func TestTightenInitialStop(t *testing.T) {
	cases := []struct {
		name               string
		side               string
		entry, aiStop, atr float64
		want               float64
	}{
		{"long AI stop wider than trail is tightened", "long", 100, 92, 4, 95},
		{"long AI stop already tighter is kept", "long", 100, 97, 4, 97},
		{"short AI stop wider than trail is tightened", "short", 100, 108, 4, 105},
		{"short AI stop already tighter is kept", "short", 100, 103, 4, 103},
		{"no ATR keeps the AI stop", "long", 100, 92, 0, 92},
		{"no AI stop gets the trail", "long", 100, 0, 4, 95},
	}
	for _, c := range cases {
		if got := tightenInitialStop(c.side, c.entry, c.aiStop, c.atr); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: got %.4f, want %.4f", c.name, got, c.want)
		}
	}
}

func TestCapTakeProfit(t *testing.T) {
	cases := []struct {
		name             string
		side             string
		entry, aiTP, atr float64
		want             float64
	}{
		// Cap is 2x ATR = 8 from entry.
		{"long far target is capped", "long", 100, 120, 4, 108},
		{"long near target is kept", "long", 100, 105, 4, 105},
		{"short far target is capped", "short", 100, 80, 4, 92},
		{"short near target is kept", "short", 100, 95, 4, 95},
		{"no ATR keeps the AI target", "long", 100, 120, 0, 120},
		{"no AI target stays unset", "long", 100, 0, 4, 0},
	}
	for _, c := range cases {
		if got := capTakeProfit(c.side, c.entry, c.aiTP, c.atr); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: got %.4f, want %.4f", c.name, got, c.want)
		}
	}
}

// TestStopWouldTriggerNow guards the market-close fallback: a stop on the
// wrong side of the mark is rejected by the exchange, and because the old stop
// is cancelled first, placing it anyway would leave the position unprotected.
func TestStopWouldTriggerNow(t *testing.T) {
	cases := []struct {
		side       string
		stop, mark float64
		want       bool
	}{
		{"long", 99, 100, false},
		{"long", 100, 100, true},
		{"long", 101, 100, true},
		{"short", 101, 100, false},
		{"short", 100, 100, true},
		{"short", 99, 100, true},
		{"long", 0, 100, false},
	}
	for _, c := range cases {
		if got := stopWouldTriggerNow(c.side, c.stop, c.mark); got != c.want {
			t.Errorf("stopWouldTriggerNow(%q, stop=%.2f, mark=%.2f) = %v, want %v", c.side, c.stop, c.mark, got, c.want)
		}
	}
}

// TestPrunePeakPnLCache pins the fix for peaks leaking across positions: a
// closed position's peak must not survive to be inherited by the next one.
func TestPrunePeakPnLCache(t *testing.T) {
	at := &AutoTrader{peakPnLCache: map[string]float64{
		"NEARUSDT_long":  88.15, // closed: stale 17.63% x 5
		"HYPEUSDT_short": 20.1,  // still open
	}}
	at.prunePeakPnLCache(map[string]bool{"HYPEUSDT_short": true})
	if _, ok := at.peakPnLCache["NEARUSDT_long"]; ok {
		t.Fatal("peak of a closed position must be pruned")
	}
	if v, ok := at.peakPnLCache["HYPEUSDT_short"]; !ok || v != 20.1 {
		t.Fatalf("peak of an open position must be kept, got %v (present=%v)", v, ok)
	}
}
