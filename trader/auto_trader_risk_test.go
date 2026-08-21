package trader

import "testing"

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
		// 2.5x multiplier is a package-level const; these pin its current value.
		{"long sits below peak by 2.5x ATR", "long", 100, 4, 90},
		{"short sits above peak by 2.5x ATR", "short", 100, 4, 110},
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
