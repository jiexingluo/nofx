package store

import "testing"

func TestComputeUsageCostWithCache(t *testing.T) {
	// deepseek-v4-flash: In=0.14, Out=0.28 (modelTokenPrices), cache-hit
	// price 0.0028 (modelCacheHitPrices) - both per 1M tokens, safety
	// margin 1.15 applied on top same as ComputeUsageCost.
	t.Run("all cache miss matches the plain (non-cache) formula", func(t *testing.T) {
		withCache, ok := ComputeUsageCostWithCache("deepseek-v4-flash", 0, 100000, 500)
		if !ok {
			t.Fatal("expected a known model price")
		}
		plain, ok := ComputeUsageCost("deepseek-v4-flash", 100000, 500)
		if !ok {
			t.Fatal("expected a known model price")
		}
		if withCache != plain {
			t.Fatalf("all-cache-miss should cost exactly the same as the plain formula: got %v, want %v", withCache, plain)
		}
	})

	t.Run("cache hit tokens are cheaper than an equal number of misses", func(t *testing.T) {
		allMiss, _ := ComputeUsageCostWithCache("deepseek-v4-flash", 0, 100000, 0)
		allHit, _ := ComputeUsageCostWithCache("deepseek-v4-flash", 100000, 0, 0)
		if allHit >= allMiss {
			t.Fatalf("100k cache-hit tokens (%v) should cost less than 100k cache-miss tokens (%v)", allHit, allMiss)
		}
		// 0.0028 vs 0.14 per 1M is a 50x difference - sanity check the ratio
		// is in that neighborhood, not just "any" discount.
		ratio := allMiss / allHit
		if ratio < 20 {
			t.Fatalf("expected roughly a 50x discount for cache hits on deepseek-v4-flash, got only %.1fx (allMiss=%v allHit=%v)", ratio, allMiss, allHit)
		}
	})

	t.Run("model without a known cache-hit rate prices hits at the miss rate", func(t *testing.T) {
		// gpt-5.6 has a modelTokenPrices entry but no modelCacheHitPrices
		// entry - hits must not be silently treated as free or discounted.
		hitCost, ok := ComputeUsageCostWithCache("gpt-5.6", 1000, 0, 0)
		if !ok {
			t.Fatal("expected a known model price")
		}
		missCost, ok := ComputeUsageCostWithCache("gpt-5.6", 0, 1000, 0)
		if !ok {
			t.Fatal("expected a known model price")
		}
		if hitCost != missCost {
			t.Fatalf("model with no verified cache-hit rate should price hits == misses, got hit=%v miss=%v", hitCost, missCost)
		}
	})

	t.Run("unknown model returns ok=false", func(t *testing.T) {
		if _, ok := ComputeUsageCostWithCache("not-a-real-model", 1, 1, 1); ok {
			t.Fatal("expected ok=false for a model with no price entry")
		}
	})
}

func TestRecordWithUsagePersistsTokenCounts(t *testing.T) {
	st, err := New(t.TempDir() + "/nofx.db")
	if err != nil {
		t.Fatalf("store.New failed: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const traderID = "trader-1"
	if err := st.AICharge().RecordWithUsage(traderID, "deepseek-v4-flash", "deepseek", 0.005, 40000, 200, 38000); err != nil {
		t.Fatalf("RecordWithUsage failed: %v", err)
	}
	if err := st.AICharge().RecordWithCost(traderID, "deepseek-v4-flash", "deepseek", 0.003); err != nil {
		t.Fatalf("RecordWithCost failed: %v", err)
	}

	charges, total, err := st.AICharge().GetCharges(traderID, "all")
	if err != nil {
		t.Fatalf("GetCharges failed: %v", err)
	}
	if len(charges) != 2 {
		t.Fatalf("expected 2 charges, got %d", len(charges))
	}
	if total != 0.008 {
		t.Fatalf("expected total cost 0.008, got %v", total)
	}

	// Charges come back ordered by created_at DESC; RecordWithCost's zero-usage
	// fallback should be first, then the usage-bearing RecordWithUsage record.
	byCost := make(map[float64]AICharge)
	for _, c := range charges {
		byCost[c.CostUSD] = c
	}

	withUsage, ok := byCost[0.005]
	if !ok {
		t.Fatalf("missing the RecordWithUsage charge in results: %+v", charges)
	}
	if withUsage.PromptTokens != 40000 || withUsage.CompletionTokens != 200 || withUsage.PromptCacheHitTokens != 38000 {
		t.Fatalf("RecordWithUsage charge has wrong token counts: %+v", withUsage)
	}

	withoutUsage, ok := byCost[0.003]
	if !ok {
		t.Fatalf("missing the RecordWithCost charge in results: %+v", charges)
	}
	if withoutUsage.PromptTokens != 0 || withoutUsage.CompletionTokens != 0 || withoutUsage.PromptCacheHitTokens != 0 {
		t.Fatalf("RecordWithCost charge should have zero token counts (usage wasn't available), got: %+v", withoutUsage)
	}
}
