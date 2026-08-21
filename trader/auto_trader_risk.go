package trader

import (
	"fmt"
	"nofx/kernel"
	"nofx/logger"
	"nofx/market"
	"nofx/store"
	"strings"
	"time"
)

const (
	// The monitor arms only once the underlying PRICE has moved +5% in the
	// position's favor (leverage-independent — at 10x the old margin-basis
	// check armed at a +0.5% price wiggle and strangled every winner), then
	// closes if the position gives back 40% of its peak profit.
	drawdownClosePriceGainPct = 5.0
	drawdownCloseGivebackPct  = 40.0

	// trailingStopATRMultiplier sets how far behind the peak price the
	// trailing stop sits, in ATR14 units. 2.5x is the middle of the 2-3x
	// range commonly used for crypto Chandelier-Exit-style trailing stops —
	// tight enough to protect gains, wide enough to survive normal
	// volatility without getting shaken out early.
	trailingStopATRMultiplier = 2.5
)

// shouldDrawdownClose reports whether the profit-protection close should fire.
// pricePnLPct is the price-basis move in the position's favor; drawdownPct is
// the relative giveback from the position's peak profit.
func shouldDrawdownClose(pricePnLPct, drawdownPct float64) bool {
	return pricePnLPct > drawdownClosePriceGainPct && drawdownPct >= drawdownCloseGivebackPct
}

// startDrawdownMonitor starts drawdown monitoring
func (at *AutoTrader) startDrawdownMonitor() {
	at.monitorWg.Add(1)
	go func() {
		defer at.monitorWg.Done()

		ticker := time.NewTicker(1 * time.Minute) // Check every minute
		defer ticker.Stop()

		logger.Info("📊 Started position drawdown monitoring (check every minute)")

		for {
			select {
			case <-ticker.C:
				at.checkPositionDrawdown()
				at.updateTrailingStops()
				// Last: both calls above already fetched positions, so this
				// one lands inside the exchange client's short position cache
				// and costs no extra API call in practice.
				at.reconcileOpenPositions()
			case <-at.stopMonitorCh:
				logger.Info("⏹ Stopped position drawdown monitoring")
				return
			}
		}
	}()
}

// checkPositionDrawdown checks position drawdown situation
func (at *AutoTrader) checkPositionDrawdown() {
	// Get current positions
	positions, err := at.trader.GetPositions()
	if err != nil {
		logger.Infof("❌ Drawdown monitoring: failed to get positions: %v", err)
		return
	}

	for _, pos := range positions {
		symbol := pos["symbol"].(string)
		side := pos["side"].(string)
		entryPrice := pos["entryPrice"].(float64)
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity // Short position quantity is negative, convert to positive
		}

		// Guard: skip if entry price is zero (prevents division by zero panic)
		if entryPrice <= 0 {
			logger.Warnf("⚠️ Drawdown monitoring: %s %s has zero entry price, skipping", symbol, side)
			continue
		}

		// Calculate current P&L percentage
		leverage := 10 // Default value
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}

		// Price-basis move drives the close decision so the trigger point does
		// not tighten as leverage grows; the margin-basis (leveraged) value is
		// only kept for the peak cache shown alongside margin-based PnL% in
		// prompts.
		var pricePnLPct float64
		if side == "long" {
			pricePnLPct = ((markPrice - entryPrice) / entryPrice) * 100
		} else {
			pricePnLPct = ((entryPrice - markPrice) / entryPrice) * 100
		}
		currentPnLPct := pricePnLPct * float64(leverage)

		// Construct unique position identifier (distinguish long/short)
		posKey := symbol + "_" + side

		// Get historical peak profit for this position
		at.peakPnLCacheMutex.RLock()
		peakPnLPct, exists := at.peakPnLCache[posKey]
		at.peakPnLCacheMutex.RUnlock()

		if !exists {
			// If no historical peak record, use current P&L as initial value
			peakPnLPct = currentPnLPct
			at.UpdatePeakPnL(symbol, side, currentPnLPct)
		} else {
			// Update peak cache
			at.UpdatePeakPnL(symbol, side, currentPnLPct)
		}

		// Calculate drawdown (magnitude of decline from peak)
		var drawdownPct float64
		if peakPnLPct > 0 && currentPnLPct < peakPnLPct {
			drawdownPct = ((peakPnLPct - currentPnLPct) / peakPnLPct) * 100
		}

		minProfit, maxDrawdown := drawdownClosePriceGainPct, drawdownCloseGivebackPct
		if at.config.StrategyConfig != nil {
			if configured := at.config.StrategyConfig.RiskControl.DrawdownMinProfit; configured > 0 {
				minProfit = configured
			}
			if configured := at.config.StrategyConfig.RiskControl.DrawdownMaxDrawdown; configured > 0 {
				maxDrawdown = configured
			}
		}

		if pricePnLPct > minProfit && drawdownPct >= maxDrawdown {
			logger.Infof("🚨 Drawdown close position condition triggered: %s %s | Price move: %.2f%% | Current profit: %.2f%% | Peak profit: %.2f%% | Drawdown: %.2f%%",
				symbol, side, pricePnLPct, currentPnLPct, peakPnLPct, drawdownPct)

			// Execute close position
			if err := at.emergencyClosePosition(symbol, side); err != nil {
				logger.Infof("❌ Drawdown close position failed (%s %s): %v", symbol, side, err)
			} else {
				logger.Infof("✅ Drawdown close position succeeded: %s %s", symbol, side)
				// Clear cache for this position after closing
				at.ClearPeakPnLCache(symbol, side)
				at.ClearTrailingStopCache(symbol, side)
			}
		} else if pricePnLPct > minProfit {
			// Record situations close to close position condition (for debugging)
			logger.Infof("📊 Drawdown monitoring: %s %s | Price move: %.2f%% | Profit: %.2f%% | Peak: %.2f%% | Drawdown: %.2f%%",
				symbol, side, pricePnLPct, currentPnLPct, peakPnLPct, drawdownPct)
		}
	}
}

// updateTrailingStops tightens (never loosens) the resting stop-loss order
// for every open position as price moves in its favor, sized off ATR14. Runs
// from the same 1-minute ticker as checkPositionDrawdown so it shares one
// goroutine instead of polling positions twice; the giveback close above
// stays in place unchanged as a coarser backstop in case a trail update
// fails to land (e.g. a transient exchange API error).
func (at *AutoTrader) updateTrailingStops() {
	positions, err := at.trader.GetPositions()
	if err != nil {
		logger.Infof("❌ Trailing stop: failed to get positions: %v", err)
		return
	}

	for _, pos := range positions {
		symbol, _ := pos["symbol"].(string)
		side, _ := pos["side"].(string)
		markPrice, _ := pos["markPrice"].(float64)
		quantity, _ := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity // Short position quantity is negative, convert to positive
		}
		if symbol == "" || quantity == 0 || markPrice <= 0 {
			continue
		}

		at.lastATRCacheMutex.RLock()
		atr := at.lastATRCache[symbol]
		at.lastATRCacheMutex.RUnlock()
		if atr <= 0 {
			// No ATR sample yet for this symbol (e.g. position opened before
			// the first AI cycle since restart refreshed it) - nothing to
			// size a trail off, wait for the next AI cycle.
			continue
		}

		posKey := symbol + "_" + side

		at.trailingStopCacheMutex.Lock()
		peakPrice, hasPeak := at.peakPriceCache[posKey]
		peakPrice = nextPeakPrice(side, peakPrice, hasPeak, markPrice)
		at.peakPriceCache[posKey] = peakPrice
		currentStop, hasStop := at.currentStopCache[posKey]
		at.trailingStopCacheMutex.Unlock()

		if !hasStop {
			// Cold cache entry - either a fresh deploy/restart with a
			// pre-existing position, or the open-time SetStopLoss call
			// failed. Adopt whatever is actually resting on the exchange
			// first, so a real (possibly wider, deliberately-chosen) stop is
			// never blind-replaced by a guess; only proceed to compare/place
			// once we know the truth. A lookup failure skips this position
			// for this tick rather than assuming "no stop exists".
			realStop, found, lookupErr := at.restingStopPrice(symbol, side)
			if lookupErr != nil {
				logger.Infof("❌ Trailing stop: failed to read open orders for %s %s: %v", symbol, side, lookupErr)
				continue
			}
			if found {
				currentStop, hasStop = realStop, true
				at.trailingStopCacheMutex.Lock()
				at.currentStopCache[posKey] = realStop
				at.trailingStopCacheMutex.Unlock()
			}
		}

		candidateStop := trailingStopCandidate(side, peakPrice, atr)
		if !isMoreFavorableStop(side, candidateStop, currentStop, hasStop) {
			continue
		}

		positionSide := strings.ToUpper(side)
		if err := at.trader.CancelStopLossOrders(symbol); err != nil {
			logger.Infof("❌ Trailing stop: failed to cancel old stop for %s %s: %v", symbol, side, err)
			continue
		}
		if err := at.trader.SetStopLoss(symbol, positionSide, quantity, candidateStop); err != nil {
			logger.Infof("❌ Trailing stop: failed to set new stop for %s %s (old stop canceled, position now unprotected until next tick): %v", symbol, side, err)
			at.trailingStopCacheMutex.Lock()
			delete(at.currentStopCache, posKey)
			at.trailingStopCacheMutex.Unlock()
			continue
		}

		at.trailingStopCacheMutex.Lock()
		at.currentStopCache[posKey] = candidateStop
		at.trailingStopCacheMutex.Unlock()

		logger.Infof("📈 Trailing stop tightened: %s %s | peak=%.6f atr14=%.6f | stop %.6f → %.6f",
			symbol, side, peakPrice, atr, currentStop, candidateStop)
	}
}

// nextPeakPrice returns the best price seen since entry: the running max for
// a long, the running min for a short. hasPeak=false (no prior cache entry)
// always adopts markPrice, since a freshly-tracked position has no history.
func nextPeakPrice(side string, peak float64, hasPeak bool, markPrice float64) float64 {
	if !hasPeak {
		return markPrice
	}
	if side == "long" && markPrice > peak {
		return markPrice
	}
	if side == "short" && markPrice < peak {
		return markPrice
	}
	return peak
}

// trailingStopCandidate computes the ATR-offset stop price behind the peak:
// below peak for a long, above peak for a short.
func trailingStopCandidate(side string, peakPrice, atr float64) float64 {
	if side == "long" {
		return peakPrice - trailingStopATRMultiplier*atr
	}
	return peakPrice + trailingStopATRMultiplier*atr
}

// isMoreFavorableStop reports whether candidate tightens the stop compared to
// current: higher for a long (less room below price), lower for a short
// (less room above price). Any candidate is "more favorable" than no stop at
// all (hasCurrent=false), so a naked position always gets one placed.
func isMoreFavorableStop(side string, candidate, current float64, hasCurrent bool) bool {
	if !hasCurrent {
		return true
	}
	if side == "long" {
		return candidate > current
	}
	return candidate < current
}

// restingStopPrice looks up the real STOP_MARKET order price for a position
// side directly from the exchange, for seeding a cold trailing-stop cache.
func (at *AutoTrader) restingStopPrice(symbol, side string) (price float64, found bool, err error) {
	orders, err := at.trader.GetOpenOrders(symbol)
	if err != nil {
		return 0, false, err
	}

	positionSide := strings.ToUpper(side)
	for _, o := range orders {
		if o.Type == "STOP_MARKET" && strings.ToUpper(o.PositionSide) == positionSide && o.StopPrice > 0 {
			return o.StopPrice, true, nil
		}
	}
	return 0, false, nil
}

// ClearTrailingStopCache clears the ATR trailing-stop cache for a closed
// position. Mirrors ClearPeakPnLCache; lastATRCache is intentionally left
// alone since it is keyed by symbol (not symbol_side) and is naturally kept
// fresh by the next AI cycle regardless of position state.
func (at *AutoTrader) ClearTrailingStopCache(symbol, side string) {
	at.trailingStopCacheMutex.Lock()
	defer at.trailingStopCacheMutex.Unlock()

	posKey := symbol + "_" + side
	delete(at.peakPriceCache, posKey)
	delete(at.currentStopCache, posKey)
}

// emergencyClosePosition emergency close position function
func (at *AutoTrader) emergencyClosePosition(symbol, side string) error {
	switch side {
	case "long":
		order, err := at.trader.CloseLong(symbol, 0) // 0 = close all
		if err != nil {
			return err
		}
		logger.Infof("✅ Emergency close long position succeeded, order ID: %v", order["orderId"])
	case "short":
		order, err := at.trader.CloseShort(symbol, 0) // 0 = close all
		if err != nil {
			return err
		}
		logger.Infof("✅ Emergency close short position succeeded, order ID: %v", order["orderId"])
	default:
		return fmt.Errorf("unknown position direction: %s", side)
	}

	return nil
}

// GetPeakPnLCache gets peak profit cache
func (at *AutoTrader) GetPeakPnLCache() map[string]float64 {
	at.peakPnLCacheMutex.RLock()
	defer at.peakPnLCacheMutex.RUnlock()

	// Return a copy of the cache
	cache := make(map[string]float64)
	for k, v := range at.peakPnLCache {
		cache[k] = v
	}
	return cache
}

// UpdatePeakPnL updates peak profit cache
func (at *AutoTrader) UpdatePeakPnL(symbol, side string, currentPnLPct float64) {
	at.peakPnLCacheMutex.Lock()
	defer at.peakPnLCacheMutex.Unlock()

	posKey := symbol + "_" + side
	if peak, exists := at.peakPnLCache[posKey]; exists {
		// Update peak (if long, take larger value; if short, currentPnLPct is negative, also compare)
		if currentPnLPct > peak {
			at.peakPnLCache[posKey] = currentPnLPct
		}
	} else {
		// First time recording
		at.peakPnLCache[posKey] = currentPnLPct
	}
}

// ClearPeakPnLCache clears peak cache for specified position
func (at *AutoTrader) ClearPeakPnLCache(symbol, side string) {
	at.peakPnLCacheMutex.Lock()
	defer at.peakPnLCacheMutex.Unlock()

	posKey := symbol + "_" + side
	delete(at.peakPnLCache, posKey)
}

// ============================================================================
// Risk Control Helpers
// ============================================================================

// isBTCETH checks if a symbol is BTC or ETH
func isBTCETH(symbol string) bool {
	symbol = strings.ToUpper(symbol)
	return strings.HasPrefix(symbol, "BTC") || strings.HasPrefix(symbol, "ETH")
}

// isMajorAsset returns true for assets that should use the BTC/ETH higher
// position-value tier rather than the altcoin (1x equity) tier. This covers
// BTC/ETH crypto perps AND Hyperliquid XYZ assets (US equities, commodities,
// forex) — none of which are "altcoins" and all of which deserve the higher
// per-position cap so the AI can actually take meaningful positions.
func isMajorAsset(symbol string) bool {
	if isBTCETH(symbol) {
		return true
	}
	return market.IsXyzDexAsset(symbol)
}

// enforcePositionValueRatio checks and enforces position value ratio limits (CODE ENFORCED)
// Returns the adjusted position size (capped if necessary) and whether the position was capped
// positionSizeUSD: the original position size in USD
// equity: the account equity
// symbol: the trading symbol
func (at *AutoTrader) enforcePositionValueRatio(positionSizeUSD float64, equity float64, symbol string) (float64, bool) {
	if at.config.StrategyConfig == nil {
		return positionSizeUSD, false
	}

	riskControl := at.config.StrategyConfig.RiskControl

	// Get the appropriate position value ratio limit. BTC/ETH AND Hyperliquid
	// XYZ assets (US stocks etc.) use the higher tier; pure altcoins use the
	// lower tier.
	var maxPositionValueRatio float64
	if isMajorAsset(symbol) {
		maxPositionValueRatio = riskControl.BTCETHMaxPositionValueRatio
		if maxPositionValueRatio <= 0 {
			maxPositionValueRatio = 5.0 // Default: 5x for BTC/ETH and XYZ assets
		}
	} else {
		maxPositionValueRatio = riskControl.AltcoinMaxPositionValueRatio
		if maxPositionValueRatio <= 0 {
			maxPositionValueRatio = 1.0 // Default: 1x for altcoins
		}
	}

	// Calculate max allowed position value = equity × ratio
	maxPositionValue := equity * maxPositionValueRatio

	// Check if position size exceeds limit
	if positionSizeUSD > maxPositionValue {
		logger.Infof("  ⚠️ [RISK CONTROL] Position %.2f USDT exceeds limit (equity %.2f × %.1fx = %.2f USDT max for %s), capping",
			positionSizeUSD, equity, maxPositionValueRatio, maxPositionValue, symbol)
		return maxPositionValue, true
	}

	return positionSizeUSD, false
}

func (at *AutoTrader) applyAutopilotFullSizeOpen(decision *kernel.Decision, equity float64) {
	if at == nil || decision == nil || at.config.StrategyConfig == nil || equity <= 0 {
		return
	}

	cfg := at.config.StrategyConfig
	if cfg.CoinSource.SourceType != "vergex_signal" {
		return
	}

	riskControl := cfg.RiskControl
	leverage := riskControl.AltcoinMaxLeverage
	positionValueRatio := riskControl.AltcoinMaxPositionValueRatio
	if isMajorAsset(decision.Symbol) {
		leverage = riskControl.BTCETHMaxLeverage
		positionValueRatio = riskControl.BTCETHMaxPositionValueRatio
	}
	if leverage < store.MinLeverage {
		leverage = store.MinLeverage
	}
	if leverage > store.MaxAltLeverage {
		leverage = store.MaxAltLeverage
	}
	if positionValueRatio <= 0 {
		positionValueRatio = 1.0
	}

	fullPositionSize := equity * positionValueRatio
	if fullPositionSize <= 0 {
		return
	}

	if decision.Leverage != leverage || decision.PositionSizeUSD != fullPositionSize {
		logger.Infof("  📏 [AUTOPILOT] Full-size open enforced for %s: leverage %dx → %dx, notional %.2f → %.2f USDT",
			decision.Symbol, decision.Leverage, leverage, decision.PositionSizeUSD, fullPositionSize)
	}
	decision.Leverage = leverage
	decision.PositionSizeUSD = fullPositionSize
}

// enforceMinPositionSize checks minimum position size (CODE ENFORCED)
func (at *AutoTrader) enforceMinPositionSize(positionSizeUSD float64) error {
	if at.config.StrategyConfig == nil {
		return nil
	}

	minSize := at.config.StrategyConfig.RiskControl.MinPositionSize
	if minSize <= 0 {
		minSize = 12 // Default: 12 USDT
	}

	if positionSizeUSD < minSize {
		return fmt.Errorf("❌ [RISK CONTROL] Position %.2f USDT below minimum (%.2f USDT)", positionSizeUSD, minSize)
	}
	return nil
}

// enforceMaxPositions checks maximum positions count (CODE ENFORCED)
func (at *AutoTrader) enforceMaxPositions(currentPositionCount int) error {
	if at.config.StrategyConfig == nil {
		return nil
	}

	maxPositions := at.config.StrategyConfig.RiskControl.MaxPositions
	if maxPositions <= 0 {
		maxPositions = 3 // Default: 3 positions
	}

	if currentPositionCount >= maxPositions {
		return fmt.Errorf("❌ [RISK CONTROL] Already at max positions (%d/%d)", currentPositionCount, maxPositions)
	}
	return nil
}

// getSideFromAction converts order action to side (BUY/SELL)
func getSideFromAction(action string) string {
	switch action {
	case "open_long", "close_short":
		return "BUY"
	case "open_short", "close_long":
		return "SELL"
	default:
		return "BUY"
	}
}
