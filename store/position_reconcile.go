package store

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"nofx/logger"
)

const reconcileQuantityTolerance = 0.0001

// LivePositionKey builds the map key used by ReconcileOpenPositionsWithLive.
func LivePositionKey(symbol, side string) string {
	return strings.ToUpper(strings.TrimSpace(symbol)) + "|" + strings.ToUpper(strings.TrimSpace(side))
}

// ReconcileOpenPositionsWithLive force-closes local OPEN position rows that the
// exchange no longer holds, and trims rows whose quantity exceeds what is live.
//
// Why this exists: missed or unmatched fills (position flips, liquidations,
// sync gaps) leave "zombie" OPEN rows behind. Every later close on the same
// symbol+side then lands as a partial close against the zombie, so the row
// never reaches CLOSED and its realized PnL never enters the closed-trade
// statistics — the dashboard, the Edge Profile and the AI's own track-record
// context all silently under-report. Reconciling against the exchange's live
// book is the self-healing fix: local OPEN rows must always be a subset of
// what the exchange actually holds.
//
// liveQty maps LivePositionKey(symbol, side) → live quantity on the exchange.
// Rows are matched newest-first so the freshest row survives as the live
// position's bookkeeping and older duplicates get closed.
//
// Scope is by exchange account (all trader IDs), so rows left by prior
// autopilot incarnations on the same exchange are reconciled too.
//
// minStaleAge protects rows that were touched recently. Callers that reconcile
// on their own timer race the fill-sync pipeline: a position that just closed
// on the exchange is briefly both "not live" AND not yet closed locally, and
// force-closing it in that window discards the realized PnL the sync was about
// to attribute from its fills — a real winning trade then lands in the books
// as a flat one. Pass a grace period comfortably longer than the sync interval
// so the sync always gets first claim; genuinely stranded rows are orders of
// magnitude older and close on the next tick regardless. Callers that already
// run *after* fills are processed in the same pipeline have no such race and
// can pass 0.
func (s *PositionStore) ReconcileOpenPositionsWithLive(exchangeID string, liveQty map[string]float64, minStaleAge time.Duration) (int, error) {
	openRows, err := s.GetOpenPositionsByExchange(exchangeID)
	if err != nil {
		return 0, fmt.Errorf("failed to list open positions: %w", err)
	}
	if len(openRows) == 0 {
		return 0, nil
	}

	// Copy so we can consume quantities without mutating the caller's map.
	remaining := make(map[string]float64, len(liveQty))
	for k, v := range liveQty {
		remaining[strings.ToUpper(k)] = v
	}

	// Newest first: the most recent row keeps representing the live position.
	sort.Slice(openRows, func(i, j int) bool {
		return openRows[i].EntryTime > openRows[j].EntryTime
	})

	nowMs := time.Now().UTC().UnixMilli()
	closed := 0

	for _, row := range openRows {
		key := LivePositionKey(row.Symbol, row.Side)
		live := remaining[key]

		if live > reconcileQuantityTolerance {
			// The exchange still holds (part of) this key — this row survives.
			s.clearZombieStreak(row.ID)
			if row.Quantity > live+reconcileQuantityTolerance {
				// Trim the row down to what is actually live so the next real
				// close matches sizes and can fully close it. No PnL is
				// fabricated: the trimmed residue is stale bookkeeping, not a
				// real fill.
				trim := row.Quantity - live
				exitPrice := row.ExitPrice
				if exitPrice <= 0 {
					exitPrice = row.EntryPrice
				}
				if err := s.ReducePositionQuantity(row.ID, trim, exitPrice, 0, 0); err != nil {
					logger.Infof("  ⚠️ Reconcile: failed to trim position %d (%s %s): %v", row.ID, row.Symbol, row.Side, err)
				} else {
					logger.Infof("  🧹 Reconcile: trimmed %s %s row %d by %.6f to match live %.6f", row.Symbol, row.Side, row.ID, trim, live)
				}
				remaining[key] = 0
			} else {
				remaining[key] = live - row.Quantity
			}
			continue
		}

		// Nothing (left) on the exchange for this key. Before treating it as a
		// zombie, let a recently-touched row be: the fill-sync may already be
		// mid-flight on this exact position and owns the correct PnL for it.
		if minStaleAge > 0 {
			lastTouched := row.UpdatedAt
			if lastTouched <= 0 {
				lastTouched = row.EntryTime
			}
			// A row's own UpdatedAt is NOT a reliable "sync is mid-flight"
			// signal: fills land in trader_fills first and are applied to the
			// position row on a later pass, so a row can sit untouched for
			// hours while its closing fill arrived seconds ago. Observed in
			// production: XMRUSDT's closing fill (-5.85) was recorded at
			// 01:01:48, reconcile fired at 01:02:12 against a row last touched
			// 21h earlier, and the loss was booked as 0.00. Take the newest
			// fill for this position as the real activity marker.
			if fillMs := s.latestFillMs(row.Symbol, row.EntryTime); fillMs > lastTouched {
				lastTouched = fillMs
			}
			if nowMs-lastTouched < minStaleAge.Milliseconds() {
				continue
			}
		}

		// The row is a zombie. Prefer the truth in trader_fills over whatever
		// the row accumulated: the fill-sync records fills reliably but has
		// repeatedly failed to APPLY them to position rows, so a zombie closed
		// with its own bookkeeping books a real trade as 0.00.
		//
		// Attribution can come back empty for two very different reasons:
		//   - the position never really existed (phantom row), or
		//   - the position closed moments ago and the fill-sync has not
		//     recorded the closing fill yet.
		// Reconcile fires on its own timer, so it can land in the same second a
		// position closes — observed six times in production, each one booking
		// a real result as 0.00. Wait a few passes before assuming the former.
		attributedPnL, attributedPrice, attributedMs, hasClosingFill := s.zombieFillAttribution(row.Symbol, row.Side, row.EntryTime, row.Quantity)
		if !hasClosingFill {
			if !s.zombiePassReached(row.ID) {
				continue
			}
		}

		exitPrice := row.ExitPrice
		if exitPrice <= 0 {
			exitPrice = row.EntryPrice
		}
		exitTime := row.UpdatedAt
		if exitTime <= 0 {
			exitTime = nowMs
		}
		exitPnL := row.RealizedPnL
		if hasClosingFill {
			exitPnL = attributedPnL
			if attributedPrice > 0 {
				exitPrice = attributedPrice
			}
			if attributedMs > 0 {
				exitTime = attributedMs
			}
		}
		s.clearZombieStreak(row.ID)
		if err := s.ClosePositionFully(row.ID, exitPrice, row.ExitOrderID, exitTime, exitPnL, row.Fee, "reconcile"); err != nil {
			logger.Infof("  ⚠️ Reconcile: failed to close zombie position %d (%s %s): %v", row.ID, row.Symbol, row.Side, err)
			continue
		}
		closed++
		logger.Infof("  🧹 Reconcile: closed zombie %s %s row %d (qty %.6f, accumulated PnL %.2f) — not held on exchange", row.Symbol, row.Side, row.ID, row.Quantity, row.RealizedPnL)
	}

	if closed > 0 {
		logger.Infof("✅ Position reconcile: closed %d zombie row(s) on exchange %s", closed, exchangeID)
	}
	return closed, nil
}

// latestFillMs returns the timestamp of the most recent fill recorded for
// symbol at or after sinceMs, or 0 when there is none. Used to tell whether
// the fill-sync is still working on a position before reconcile treats it as
// abandoned.
func (s *PositionStore) latestFillMs(symbol string, sinceMs int64) int64 {
	var newest int64
	err := s.db.Model(&TraderFill{}).
		Where("symbol = ? AND created_at >= ?", symbol, sinceMs).
		Select("COALESCE(MAX(created_at), 0)").
		Scan(&newest).Error
	if err != nil {
		// Fail safe: report "just now" so the caller leaves the row alone this
		// tick rather than closing it on incomplete information.
		logger.Infof("  ⚠️ Reconcile: fill lookup failed for %s: %v", symbol, err)
		return time.Now().UTC().UnixMilli()
	}
	return newest
}

// zombieFillAttribution reconstructs what the fills actually realized for a
// zombie row: the sum of realized_pnl on closing fills (opposite side to the
// row's) since entry, plus the last closing fill's price and time.
//
// Two bounds keep a later position's fills out of this row:
//   - Time: fills after the next row for the same symbol (when it exists)
//     are excluded outright.
//   - Quantity: closing fills are only attributed up to the row's own
//     quantity, so a phantom row cannot consume a later position's fills
//     even when no next row exists to bound the window.
//
// found is false when there are no attributable closing fills, in which case
// the caller keeps the row's own bookkeeping.
func (s *PositionStore) zombieFillAttribution(symbol, side string, entryMs int64, rowQty float64) (pnl float64, exitPrice float64, exitMs int64, found bool) {
	var capMs int64
	var nextEntry int64
	if err := s.db.Model(&TraderPosition{}).
		Where("symbol = ? AND entry_time > ?", symbol, entryMs).
		Select("COALESCE(MIN(entry_time), 0)").
		Scan(&nextEntry).Error; err == nil && nextEntry > 0 {
		capMs = nextEntry
	} else {
		capMs = time.Now().UTC().UnixMilli()
	}

	var fills []TraderFill
	if err := s.db.Where("symbol = ? AND created_at >= ? AND created_at < ?", symbol, entryMs, capMs).
		Order("created_at ASC").Find(&fills).Error; err != nil {
		logger.Infof("  ⚠️ Reconcile: fill attribution lookup failed for %s: %v", symbol, err)
		return 0, 0, 0, false
	}

	closeSide := "SELL"
	if strings.EqualFold(side, "short") {
		closeSide = "BUY"
	}
	remaining := rowQty
	for _, f := range fills {
		if remaining <= reconcileQuantityTolerance {
			break
		}
		if !strings.EqualFold(f.Side, closeSide) || f.RealizedPnL == 0 || f.Quantity <= 0 {
			continue
		}
		take := f.Quantity
		if take > remaining {
			take = remaining // boundary fill spanning into the next position
		}
		pnl += f.RealizedPnL * (take / f.Quantity)
		exitPrice = f.Price
		exitMs = f.CreatedAt
		remaining -= take
		found = true
	}
	return pnl, exitPrice, exitMs, found
}

// zombiePassReached records one more consecutive observation of rowID as a
// zombie and reports whether it has now been seen enough times to close.
func (s *PositionStore) zombiePassReached(rowID int64) bool {
	s.zombieMu.Lock()
	defer s.zombieMu.Unlock()
	if s.zombieSeen == nil {
		s.zombieSeen = make(map[int64]int)
	}
	s.zombieSeen[rowID]++
	return s.zombieSeen[rowID] >= zombiePassesBeforeClosing
}

// clearZombieStreak forgets any accumulated zombie observations for rowID.
func (s *PositionStore) clearZombieStreak(rowID int64) {
	s.zombieMu.Lock()
	defer s.zombieMu.Unlock()
	delete(s.zombieSeen, rowID)
}
