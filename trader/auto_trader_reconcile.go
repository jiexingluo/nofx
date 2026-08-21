package trader

import (
	"strings"

	"nofx/logger"
	"nofx/market"
	"nofx/store"
)

// buildLiveQtyMap converts a Trader.GetPositions() result into the
// (symbol, side) → quantity map store.ReconcileOpenPositionsWithLive expects.
//
// Two details here are load-bearing, both learned the hard way:
//
//  1. positionAmt is SIGNED on some exchanges (Binance parses the raw field,
//     Bybit negates it for sell side) and pre-absolute on others. Every other
//     per-cycle consumer in this package defensively flips the sign (see
//     checkPositionDrawdown, updateTrailingStops, buildTradingContext), and so
//     must this one. hyperliquid's own reconcilePositions skips negatives with
//     `qty <= 0` instead — safe only because Hyperliquid's GetPositions never
//     returns them. Copying that shape here would drop every live SHORT out of
//     the map, and reconcile would then force-close those rows as "zombies" —
//     losing the bot's record of a real, open, live-money position.
//
//  2. Both the raw and the market-normalized symbol are registered as keys.
//     Local rows store whatever the exchange reported when the position was
//     opened, and symbol formatting differs per exchange (Gate BTC_USDT, OKX
//     BTC-USDT-SWAP). For Binance the two forms are identical so this is a
//     no-op, but where they diverge, an unmatched key would mean force-closing
//     a row whose position is genuinely still open. Registering both makes the
//     worst case "a stale row survives one more cycle" (harmless, self-heals)
//     rather than "a live position's row is destroyed" (not recoverable).
func buildLiveQtyMap(positions []map[string]interface{}) map[string]float64 {
	liveQty := make(map[string]float64, len(positions))
	for _, pos := range positions {
		symbol, _ := pos["symbol"].(string)
		side, _ := pos["side"].(string)
		qty, _ := pos["positionAmt"].(float64)
		if qty < 0 {
			qty = -qty // see (1) above — never drop shorts
		}
		if symbol == "" || side == "" || qty == 0 {
			continue
		}

		raw := strings.ToUpper(symbol)
		liveQty[store.LivePositionKey(raw, side)] += qty
		if norm := strings.ToUpper(market.Normalize(symbol)); norm != raw {
			liveQty[store.LivePositionKey(norm, side)] += qty // see (2) above
		}
	}
	return liveQty
}

// reconcileOpenPositions closes local OPEN position rows this exchange account
// no longer actually holds.
//
// Why this exists: store.ReconcileOpenPositionsWithLive has been available all
// along, but its only call site was inside Hyperliquid's order-sync pipeline,
// so every CEX-family trader (Binance, Bybit, OKX, Bitget, ...) ran with no
// reconciliation whatsoever. A position closed on the exchange without nofx
// recording the closing fill leaves an OPEN row behind forever: invisible to
// the AI (which is fed live exchange positions, not local rows), excluded from
// closed-trade statistics, and silently wrong on the dashboard. Found in
// production with four such rows stranded since March — the exchange had held
// nothing for months while the local DB still called them open.
//
// Runs from the drawdown-monitor goroutine, which ticks every minute for both
// grid and non-grid strategies and is not gated by safe mode or scan-interval
// pauses, so healing does not depend on the AI cycle actually reaching a
// decision.
func (at *AutoTrader) reconcileOpenPositions() {
	if at.store == nil || at.exchangeID == "" {
		return
	}

	positions, err := at.trader.GetPositions()
	if err != nil {
		logger.Infof("❌ Position reconcile: failed to get positions: %v", err)
		return
	}

	// Deliberately skip the fully-flat case. An empty result is ambiguous: it
	// means "this account holds nothing" but would also be what a degraded or
	// partial exchange response looks like, and acting on that would close the
	// local rows of every genuinely-open position at once. Waiting for at least
	// one confirmed live position costs only that stale rows linger while the
	// account is flat — they are cleaned on the next tick that sees any
	// position, which is the strictly safer direction to fail in.
	if len(positions) == 0 {
		return
	}

	closed, err := at.store.Position().ReconcileOpenPositionsWithLive(at.exchangeID, buildLiveQtyMap(positions))
	if err != nil {
		logger.Infof("❌ Position reconcile: failed for exchange %s: %v", at.exchangeID, err)
		return
	}
	if closed > 0 {
		logger.Infof("🧹 Position reconcile: closed %d stale OPEN row(s) the exchange no longer holds", closed)
	}
}
