package store

import (
	"testing"
	"time"
)

func newReconcileTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := New(t.TempDir() + "/nofx.db")
	if err != nil {
		t.Fatalf("store.New failed: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func openRow(t *testing.T, st *Store, traderID, exchangeID, symbol, side string, qty, pnl float64, entryMs int64) int64 {
	t.Helper()
	pos := &TraderPosition{
		TraderID:      traderID,
		ExchangeID:    exchangeID,
		ExchangeType:  "hyperliquid",
		Symbol:        symbol,
		Side:          side,
		Quantity:      qty,
		EntryQuantity: qty,
		EntryPrice:    100,
		EntryTime:     entryMs,
		RealizedPnL:   pnl,
		Status:        "OPEN",
		Source:        "sync",
		CreatedAt:     entryMs,
		UpdatedAt:     entryMs,
	}
	if err := st.Position().CreateOpenPosition(pos); err != nil {
		t.Fatalf("create open position: %v", err)
	}
	return pos.ID
}

func TestReconcileClosesZombiesKeepsLiveAndTrims(t *testing.T) {
	st := newReconcileTestStore(t)
	const exch = "ex-hl"
	base := time.Now().Add(-48 * time.Hour).UnixMilli()

	// Zombie under the CURRENT trader id: exchange holds nothing for DRAM.
	zombieID := openRow(t, st, "trader-now", exch, "xyz:DRAM", "LONG", 6.8, -20.34, base)

	// Zombie left by a PRIOR autopilot incarnation on the SAME exchange —
	// this is the case a per-trader-id reconcile would miss.
	legacyID := openRow(t, st, "trader-old", exch, "SOLUSDT", "SHORT", 6.94, -3.5, base+500)

	// Duplicates for SP500 (different incarnations): newest survives trimmed,
	// older closes.
	oldSP := openRow(t, st, "trader-old", exch, "xyz:SP500", "LONG", 0.07, -1.5, base+1000)
	newSP := openRow(t, st, "trader-now", exch, "xyz:SP500", "LONG", 0.124, 2.5, base+2000)

	// Healthy row exactly matching live — untouched.
	healthy := openRow(t, st, "trader-now", exch, "BTCUSDT", "LONG", 0.01, 0, base+3000)

	// Row on a DIFFERENT exchange account — must be out of scope.
	otherExch := openRow(t, st, "trader-now", "ex-other", "ETHUSDT", "LONG", 2.0, 0, base+4000)

	live := map[string]float64{
		LivePositionKey("xyz:SP500", "long"): 0.057,
		LivePositionKey("BTCUSDT", "long"):   0.01,
	}

	// These rows have no closing fills on record, so reconcile waits
	// zombiePassesBeforeClosing passes before treating them as abandoned —
	// that wait is what stops it from booking a just-closed position as 0.00.
	var closed int
	for i := 0; i < zombiePassesBeforeClosing; i++ {
		c, err := st.Position().ReconcileOpenPositionsWithLive(exch, live, 0)
		if err != nil {
			t.Fatalf("reconcile failed: %v", err)
		}
		closed = c
		if i == 0 && c != 0 {
			t.Fatalf("first pass must not close fill-less zombies, got %d", c)
		}
	}
	if closed != 3 {
		t.Fatalf("expected 3 zombies closed after %d passes, got %d", zombiePassesBeforeClosing, closed)
	}

	get := func(id int64) *TraderPosition {
		t.Helper()
		var pos TraderPosition
		if err := st.Position().db.First(&pos, id).Error; err != nil {
			t.Fatalf("load position %d: %v", id, err)
		}
		return &pos
	}

	if dram := get(zombieID); dram.Status != "CLOSED" || dram.RealizedPnL != -20.34 || dram.CloseReason != "reconcile" {
		t.Fatalf("DRAM zombie should close via reconcile keeping PnL, got %+v", dram)
	}
	if sol := get(legacyID); sol.Status != "CLOSED" {
		t.Fatalf("legacy-incarnation SOL zombie on same exchange should close, got %+v", sol)
	}
	if sp := get(oldSP); sp.Status != "CLOSED" {
		t.Fatalf("older duplicate SP500 row should close, got %+v", sp)
	}
	if sp := get(newSP); sp.Status != "OPEN" || sp.Quantity > 0.0571 || sp.Quantity < 0.0569 {
		t.Fatalf("newest SP500 row should stay open trimmed to live 0.057, got status=%s qty=%v", sp.Status, sp.Quantity)
	}
	if btc := get(healthy); btc.Status != "OPEN" || btc.Quantity != 0.01 {
		t.Fatalf("healthy row must be untouched, got %+v", btc)
	}
	if eth := get(otherExch); eth.Status != "OPEN" {
		t.Fatalf("row on a different exchange must be out of scope, got %+v", eth)
	}
}

func TestReconcileNoOpenRowsIsNoop(t *testing.T) {
	st := newReconcileTestStore(t)
	closed, err := st.Position().ReconcileOpenPositionsWithLive("ex-empty", map[string]float64{}, 0)
	if err != nil || closed != 0 {
		t.Fatalf("expected clean noop, got closed=%d err=%v", closed, err)
	}
}

// TestReconcileGracePeriodProtectsRecentlyTouchedRows pins the fix for a real
// production regression: a reconciler running on its own timer closed a
// position 4 seconds after its closing fills had landed but before the sync
// had applied them, booking a +34.30 USDT winner as a flat trade. A row
// touched within the grace period must be left for the sync to close with the
// correct PnL; the same row must still close once it is genuinely stale.
func TestReconcileGracePeriodProtectsRecentlyTouchedRows(t *testing.T) {
	const exch = "ex-race"
	nowMs := time.Now().UnixMilli()

	t.Run("row touched seconds ago is left for the sync", func(t *testing.T) {
		st := newReconcileTestStore(t)
		id := openRow(t, st, "trader-now", exch, "TAOUSDT", "LONG", 1.122, 0, nowMs)

		// Exchange holds nothing, but the fill-sync only just touched this row.
		closed, err := st.Position().ReconcileOpenPositionsWithLive(exch, map[string]float64{}, 10*time.Minute)
		if err != nil {
			t.Fatalf("reconcile failed: %v", err)
		}
		if closed != 0 {
			t.Fatalf("expected the recently-touched row to be spared, got closed=%d", closed)
		}

		var pos TraderPosition
		if err := st.Position().db.First(&pos, id).Error; err != nil {
			t.Fatalf("load position: %v", err)
		}
		if pos.Status != "OPEN" {
			t.Fatalf("row must stay OPEN so the sync can close it with real PnL, got %s", pos.Status)
		}
	})

	t.Run("row untouched past the grace period still closes", func(t *testing.T) {
		st := newReconcileTestStore(t)
		oldMs := nowMs - (2 * time.Hour).Milliseconds()
		openRow(t, st, "trader-now", exch, "TAOUSDT", "LONG", 1.122, 0, oldMs)

		var closed int
		for i := 0; i < zombiePassesBeforeClosing; i++ {
			c, err := st.Position().ReconcileOpenPositionsWithLive(exch, map[string]float64{}, 10*time.Minute)
			if err != nil {
				t.Fatalf("reconcile failed: %v", err)
			}
			closed = c
		}
		if closed != 1 {
			t.Fatalf("expected the stale row to close after %d passes, got closed=%d", zombiePassesBeforeClosing, closed)
		}
	})
}

// TestReconcileGracePeriodKeysOnFillsNotRowTimestamp reproduces the exact
// production timeline that the first version of the grace period missed:
// XMRUSDT's row was last touched at open (21h earlier), so a row-timestamp
// grace period considered it long abandoned — but its closing fill had landed
// 24 seconds before, and the sync was one pass away from applying the -5.85.
// Reconcile closed it first and booked the loss as 0.00. The row's own
// UpdatedAt is therefore not a valid "sync is done with this" signal; the
// newest fill is.
func TestReconcileGracePeriodKeysOnFillsNotRowTimestamp(t *testing.T) {
	const exch = "ex-fillrace"
	nowMs := time.Now().UnixMilli()
	staleRowMs := nowMs - (21 * time.Hour).Milliseconds()

	st := newReconcileTestStore(t)
	id := openRow(t, st, "trader-now", exch, "XMRUSDT", "LONG", 0.403, 0, staleRowMs)

	// The closing fill landed seconds ago and still carries the real PnL.
	if err := st.Order().db.Create(&TraderFill{
		TraderID:   "trader-now",
		ExchangeID: exch,
		Symbol:     "XMRUSDT",
		Side:       "SELL",
		Price:      419.03,
		Quantity:   0.403,
		// Unique per row: the fills table has a uniqueIndex on this column.
		ExchangeTradeID: "fill-race-1",
		RealizedPnL:     -5.849,
		CreatedAt:       nowMs - (24 * time.Second).Milliseconds(),
	}).Error; err != nil {
		t.Fatalf("seed fill: %v", err)
	}

	closed, err := st.Position().ReconcileOpenPositionsWithLive(exch, map[string]float64{}, 10*time.Minute)
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if closed != 0 {
		t.Fatalf("row with a 24s-old closing fill must be left for the sync, got closed=%d", closed)
	}

	var pos TraderPosition
	if err := st.Position().db.First(&pos, id).Error; err != nil {
		t.Fatalf("load position: %v", err)
	}
	if pos.Status != "OPEN" {
		t.Fatalf("row must stay OPEN so the sync can attribute the real PnL, got %s", pos.Status)
	}
}

// TestReconcileZombieCloseTakesPnLFromFills pins the fix for the third
// occurrence of this data-loss class: the fill-sync records fills reliably
// but intermittently fails to APPLY them to position rows, so a zombie row
// closed with its own (stale) bookkeeping booked real losses as 0.00.
// Reproduces the production timeline of rows 226-248 (Sep-Oct 2026): opening
// fill recorded at entry, closing fill recorded a day later, sync never
// applied it, grace period expired, reconcile closed the row.
func TestReconcileZombieCloseTakesPnLFromFills(t *testing.T) {
	const exch = "ex-attrib"
	nowMs := time.Now().UnixMilli()
	entryMs := nowMs - (25 * time.Hour).Milliseconds()

	st := newReconcileTestStore(t)
	id := openRow(t, st, "trader-now", exch, "HYPEUSDT", "LONG", 1.81, 0, entryMs)

	// Opening fill at entry (pnl 0), closing fill 23h later with the real loss.
	if err := st.Order().db.Create(&TraderFill{
		TraderID: "trader-now", ExchangeID: exch, Symbol: "HYPEUSDT",
		Side: "BUY", Price: 93.914, Quantity: 1.81,
		ExchangeTradeID: "attrib-buy", CreatedAt: entryMs,
	}).Error; err != nil {
		t.Fatalf("seed opening fill: %v", err)
	}
	closeMs := nowMs - (2 * time.Hour).Milliseconds()
	if err := st.Order().db.Create(&TraderFill{
		TraderID: "trader-now", ExchangeID: exch, Symbol: "HYPEUSDT",
		Side: "SELL", Price: 93.53, Quantity: 1.81,
		ExchangeTradeID: "attrib-sell", RealizedPnL: -0.695, CreatedAt: closeMs,
	}).Error; err != nil {
		t.Fatalf("seed closing fill: %v", err)
	}

	closed, err := st.Position().ReconcileOpenPositionsWithLive(exch, map[string]float64{}, 10*time.Minute)
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if closed != 1 {
		t.Fatalf("expected 1 zombie closed, got %d", closed)
	}

	var pos TraderPosition
	if err := st.Position().db.First(&pos, id).Error; err != nil {
		t.Fatalf("load position: %v", err)
	}
	if pos.RealizedPnL != -0.695 {
		t.Fatalf("zombie must be closed with the fills' PnL (-0.695), got %v", pos.RealizedPnL)
	}
	if pos.ExitPrice != 93.53 {
		t.Fatalf("exit price must come from the closing fill, got %v", pos.ExitPrice)
	}
	if pos.ExitTime != closeMs {
		t.Fatalf("exit time must come from the closing fill, got %v", pos.ExitTime)
	}
}

// TestReconcileZombieFillAttributionDoesNotBleedNextPosition guards the
// bounds: a LATER position for the same symbol — live on the exchange, with
// its own row and profitable closing fills — must not bleed its PnL into the
// zombie. This mirrors production: at reconcile time the next position is
// still live (present in liveQty), while the zombie is not.
func TestReconcileZombieFillAttributionDoesNotBleedNextPosition(t *testing.T) {
	const exch = "ex-bleed"
	nowMs := time.Now().UnixMilli()
	zombieEntry := nowMs - (25 * time.Hour).Milliseconds()
	nextEntry := nowMs - time.Hour.Milliseconds()

	st := newReconcileTestStore(t)
	zombieID := openRow(t, st, "trader-now", exch, "HYPEUSDT", "LONG", 1.81, 0, zombieEntry)
	// The next position is still held on the exchange.
	openRow(t, st, "trader-now", exch, "HYPEUSDT", "LONG", 1.78, 0, nextEntry)

	// Zombie's own closing fill: -0.70.
	st.Order().db.Create(&TraderFill{
		TraderID: "trader-now", ExchangeID: exch, Symbol: "HYPEUSDT",
		Side: "SELL", Price: 93.5, Quantity: 1.81,
		ExchangeTradeID: "bleed-zombie", RealizedPnL: -0.70,
		CreatedAt: nowMs - (23 * time.Hour).Milliseconds(),
	})
	// The live next position's closing fill: +5.00.
	st.Order().db.Create(&TraderFill{
		TraderID: "trader-now", ExchangeID: exch, Symbol: "HYPEUSDT",
		Side: "SELL", Price: 91.0, Quantity: 1.78,
		ExchangeTradeID: "bleed-next", RealizedPnL: 5.00,
		CreatedAt: nextEntry,
	})

	live := map[string]float64{LivePositionKey("HYPEUSDT", "long"): 1.78}
	closed, err := st.Position().ReconcileOpenPositionsWithLive(exch, live, 10*time.Minute)
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if closed != 1 {
		t.Fatalf("expected only the zombie closed, got %d", closed)
	}

	var pos TraderPosition
	if err := st.Position().db.First(&pos, zombieID).Error; err != nil {
		t.Fatalf("load zombie: %v", err)
	}
	if pos.RealizedPnL != -0.70 {
		t.Fatalf("next position's +5.00 must not bleed into the zombie; got %v", pos.RealizedPnL)
	}
	if pos.ExitPrice != 93.5 {
		t.Fatalf("exit price must come from the zombie's own closing fill, got %v", pos.ExitPrice)
	}
}

// TestReconcileWaitsForClosingFillBeforeClosing reproduces the production race
// that motivated the multi-pass guard: reconcile fires on its own 60s timer
// and landed in the SAME SECOND a position closed, before the 30s fill-sync had
// recorded the closing fill. Attribution found nothing and the trade was booked
// as 0.00 — six times in production (LITUSDT +3.20, HYPEUSDT -1.54, and four
// others that together understated the ledger by ~12.4 USDT).
//
// The guard: a fill-less zombie is not closed until it has looked that way for
// zombiePassesBeforeClosing consecutive passes, by which point a real closing
// fill has almost certainly arrived.
func TestReconcileWaitsForClosingFillBeforeClosing(t *testing.T) {
	const exch = "ex-race-close"
	nowMs := time.Now().UnixMilli()
	entryMs := nowMs - (3 * 24 * time.Hour).Milliseconds() // stale row, grace irrelevant

	st := newReconcileTestStore(t)
	id := openRow(t, st, "trader-now", exch, "LITUSDT", "SHORT", 20.7, 0, entryMs)

	// Pass 1: the position has just vanished from the exchange and the closing
	// fill has NOT been synced yet. Reconcile must leave it alone.
	closed, err := st.Position().ReconcileOpenPositionsWithLive(exch, map[string]float64{}, 10*time.Minute)
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if closed != 0 {
		t.Fatalf("must not close a fill-less zombie on the first pass, got closed=%d", closed)
	}
	var pos TraderPosition
	if err := st.Position().db.First(&pos, id).Error; err != nil {
		t.Fatalf("load position: %v", err)
	}
	if pos.Status != "OPEN" {
		t.Fatalf("row must stay OPEN while the fill-sync catches up, got %s", pos.Status)
	}

	// The fill-sync lands the closing fill between passes.
	if err := st.Order().db.Create(&TraderFill{
		TraderID: "trader-now", ExchangeID: exch, Symbol: "LITUSDT",
		Side: "BUY", Price: 3.2109, Quantity: 20.7,
		ExchangeTradeID: "race-close-1", RealizedPnL: 3.20229,
		// Older than the 10-minute grace period, so the row is eligible to
		// close this pass — the point under test is that the fill is now
		// present and gets used, not the grace window itself.
		CreatedAt: nowMs - (20 * time.Minute).Milliseconds(),
	}).Error; err != nil {
		t.Fatalf("seed closing fill: %v", err)
	}

	// Pass 2: the closing fill is now available, so the row closes with the
	// real result instead of 0.00.
	closed, err = st.Position().ReconcileOpenPositionsWithLive(exch, map[string]float64{}, 10*time.Minute)
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if closed != 1 {
		t.Fatalf("expected the row to close once the fill arrived, got closed=%d", closed)
	}
	if err := st.Position().db.First(&pos, id).Error; err != nil {
		t.Fatalf("load position: %v", err)
	}
	if pos.RealizedPnL != 3.20229 {
		t.Fatalf("must book the fill's +3.20229, got %v", pos.RealizedPnL)
	}
	if pos.ExitPrice != 3.2109 {
		t.Fatalf("exit price must come from the closing fill, got %v", pos.ExitPrice)
	}
}

// TestReconcilePhantomRowStillCloses: a row with no closing fill EVER must not
// be stuck open forever — it closes once the pass threshold is reached.
func TestReconcilePhantomRowStillCloses(t *testing.T) {
	const exch = "ex-phantom-close"
	nowMs := time.Now().UnixMilli()
	entryMs := nowMs - (3 * 24 * time.Hour).Milliseconds()

	st := newReconcileTestStore(t)
	id := openRow(t, st, "trader-now", exch, "GHOSTUSDT", "LONG", 5, 0, entryMs)

	var closed int
	for pass := 1; pass <= zombiePassesBeforeClosing; pass++ {
		c, err := st.Position().ReconcileOpenPositionsWithLive(exch, map[string]float64{}, 10*time.Minute)
		if err != nil {
			t.Fatalf("reconcile failed on pass %d: %v", pass, err)
		}
		if pass < zombiePassesBeforeClosing && c != 0 {
			t.Fatalf("closed too early on pass %d", pass)
		}
		closed = c
	}
	if closed != 1 {
		t.Fatalf("phantom row must close by pass %d, got closed=%d", zombiePassesBeforeClosing, closed)
	}
	var pos TraderPosition
	if err := st.Position().db.First(&pos, id).Error; err != nil {
		t.Fatalf("load position: %v", err)
	}
	if pos.Status != "CLOSED" {
		t.Fatalf("phantom row should be CLOSED, got %s", pos.Status)
	}
}
