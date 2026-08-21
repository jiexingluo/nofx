package trader

import (
	"testing"

	"nofx/store"
)

func TestBuildLiveQtyMap(t *testing.T) {
	pos := func(symbol, side string, amt float64) map[string]interface{} {
		return map[string]interface{}{"symbol": symbol, "side": side, "positionAmt": amt}
	}

	t.Run("long position maps to its quantity", func(t *testing.T) {
		got := buildLiveQtyMap([]map[string]interface{}{pos("BTCUSDT", "long", 0.5)})
		if got[store.LivePositionKey("BTCUSDT", "long")] != 0.5 {
			t.Fatalf("expected 0.5 for BTCUSDT long, got %v", got)
		}
	})

	// The regression this whole file exists to prevent. Binance and Bybit
	// report shorts with a NEGATIVE positionAmt. If the sign is not flipped,
	// the short drops out of the map, reconcile sees no live backing for it,
	// and force-closes the local row of a position that is genuinely open.
	t.Run("short position with negative amount is kept, not dropped", func(t *testing.T) {
		got := buildLiveQtyMap([]map[string]interface{}{pos("ETHUSDT", "short", -2.5)})
		key := store.LivePositionKey("ETHUSDT", "short")
		if _, ok := got[key]; !ok {
			t.Fatal("short position was dropped from the live map - reconcile would force-close a live position's row")
		}
		if got[key] != 2.5 {
			t.Fatalf("expected absolute quantity 2.5, got %v", got[key])
		}
	})

	t.Run("long and short on the same symbol stay separate", func(t *testing.T) {
		got := buildLiveQtyMap([]map[string]interface{}{
			pos("BTCUSDT", "long", 1.0),
			pos("BTCUSDT", "short", -3.0),
		})
		if got[store.LivePositionKey("BTCUSDT", "long")] != 1.0 {
			t.Fatalf("long side wrong: %v", got)
		}
		if got[store.LivePositionKey("BTCUSDT", "short")] != 3.0 {
			t.Fatalf("short side wrong: %v", got)
		}
	})

	t.Run("zero-quantity and malformed entries are skipped", func(t *testing.T) {
		got := buildLiveQtyMap([]map[string]interface{}{
			pos("BTCUSDT", "long", 0),
			pos("", "long", 1.0),
			pos("ETHUSDT", "", 1.0),
			{}, // no keys at all - must not panic
		})
		if len(got) != 0 {
			t.Fatalf("expected an empty map, got %v", got)
		}
	})

	t.Run("duplicate rows for one symbol+side accumulate", func(t *testing.T) {
		got := buildLiveQtyMap([]map[string]interface{}{
			pos("SOLUSDT", "long", 1.5),
			pos("SOLUSDT", "long", 2.5),
		})
		if got[store.LivePositionKey("SOLUSDT", "long")] != 4.0 {
			t.Fatalf("expected quantities to sum to 4.0, got %v", got)
		}
	})

	// Binance symbols normalize to themselves, so exactly one key is produced;
	// a second, differently-formatted key here would mean the normalizer is
	// mangling plain CEX symbols.
	t.Run("binance-style symbol produces exactly one key", func(t *testing.T) {
		got := buildLiveQtyMap([]map[string]interface{}{pos("SIRENUSDT", "long", 58)})
		if len(got) != 1 {
			t.Fatalf("expected exactly 1 key for an already-normal symbol, got %v", got)
		}
	})

	// Where the exchange's raw format differs from the normalized one, BOTH
	// must be registered: local rows may store either form, and a missed match
	// means force-closing a live position's row.
	t.Run("separator-style symbol registers raw and normalized keys", func(t *testing.T) {
		got := buildLiveQtyMap([]map[string]interface{}{pos("BTC_USDT", "long", 0.25)})
		if got[store.LivePositionKey("BTC_USDT", "long")] != 0.25 {
			t.Fatalf("raw form missing: %v", got)
		}
		if got[store.LivePositionKey("BTCUSDT", "long")] != 0.25 {
			t.Fatalf("normalized form missing: %v", got)
		}
	})

	t.Run("empty input yields an empty map", func(t *testing.T) {
		if got := buildLiveQtyMap(nil); len(got) != 0 {
			t.Fatalf("expected empty map, got %v", got)
		}
	})
}
