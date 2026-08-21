package store

import (
	"time"

	"gorm.io/gorm"
)

// AICharge represents a single AI call charge record
type AICharge struct {
	ID       int64   `gorm:"primaryKey;autoIncrement" json:"id"`
	TraderID string  `gorm:"column:trader_id;not null;index:idx_ai_charges_trader" json:"trader_id"`
	Model    string  `gorm:"column:model;not null" json:"model"`
	Provider string  `gorm:"column:provider;not null" json:"provider"`
	CostUSD  float64 `gorm:"column:cost_usd;not null" json:"cost_usd"`
	// PromptTokens/CompletionTokens/PromptCacheHitTokens are 0 when the
	// client didn't report usage for this call (e.g. models/providers this
	// codebase only prices with the flat per-call estimate) - do not treat
	// 0 as "no tokens were actually used," only as "usage wasn't available."
	PromptTokens         int       `gorm:"column:prompt_tokens;not null;default:0" json:"prompt_tokens"`
	CompletionTokens     int       `gorm:"column:completion_tokens;not null;default:0" json:"completion_tokens"`
	PromptCacheHitTokens int       `gorm:"column:prompt_cache_hit_tokens;not null;default:0" json:"prompt_cache_hit_tokens"`
	CreatedAt            time.Time `gorm:"autoCreateTime" json:"created_at"`
}

func (AICharge) TableName() string { return "ai_charges" }

// modelPrices maps model ID to approximate cost per call in USD
var modelPrices = map[string]float64{
	"deepseek":          0.003,
	"deepseek-reasoner": 0.005,
	"deepseek-v4-flash": 0.003,
	"deepseek-v4-pro":   0.01,
	"gpt-5.6":           0.06,
	"gpt-5.6-terra":     0.03,
	"gpt-5.6-luna":      0.012,
	"claude-fable":      0.24,
	"claude-opus":       0.12,
	"glm-5":             0.003,
}

// GetModelPrice returns the price per call for a given model
func GetModelPrice(model string) float64 {
	if price, ok := modelPrices[model]; ok {
		return price
	}
	return 0.01 // default fallback
}

// modelTokenPrices maps model → USD per 1M tokens, mirroring the claw402
// gateway pricing config (providers/*.yaml): In (full-price/cache-miss
// prompt tokens) and Out (completion tokens). Used to derive the actual
// upto-settled cost from streamed token usage, where the gateway cannot
// deliver the settlement header (SSE headers are flushed before usage is
// known).
var modelTokenPrices = map[string]struct{ In, Out float64 }{
	"gpt-5.6":           {5, 30},
	"gpt-5.6-terra":     {2.5, 15},
	"gpt-5.6-luna":      {1, 6},
	"claude-fable":      {10, 50},
	"claude-opus":       {5, 25},
	"deepseek-v4-flash": {0.14, 0.28},
	"deepseek-v4-pro":   {1.74, 3.48},
	"deepseek":          {0.27, 1.1},
	"deepseek-reasoner": {0.55, 2.19},
	"glm-5":             {0.6, 2},
}

// modelCacheHitPrices maps model → USD per 1M prompt tokens served from the
// provider's own automatic prompt cache (e.g. DeepSeek's Context Caching on
// Disk - enabled by default, no integration required, billed automatically
// at this reduced rate whenever a request's prefix matches a prior one).
// Deliberately separate from modelTokenPrices above: only models we have a
// verified, current cache-hit rate for belong here. Absent = ComputeUsageCostWithCache
// treats cache-hit tokens at the same price as cache-miss ones for that
// model (i.e. assumes no discount rather than guessing at one), so adding
// an entry can only ever lower a previously-overstated cost, never invent
// savings that aren't real.
var modelCacheHitPrices = map[string]float64{
	"deepseek-v4-flash": 0.0028,
	"deepseek-v4-pro":   0.003625,
}

// Gateway upto settlement formula constants (see claw402 token_estimate
// pricing: token_safety_margin 0.15, token_min_price 0.0001).
const (
	uptoSafetyMargin = 1.15
	uptoMinPriceUSD  = 0.0001
)

// ComputeUsageCost derives the upto-settled cost of a call from token usage,
// using the same formula as the claw402 gateway. ok is false for models
// without a token price entry.
func ComputeUsageCost(model string, promptTokens, completionTokens int) (float64, bool) {
	p, ok := modelTokenPrices[model]
	if !ok {
		return 0, false
	}
	cost := (float64(promptTokens)*p.In + float64(completionTokens)*p.Out) / 1e6 * uptoSafetyMargin
	if cost < uptoMinPriceUSD {
		cost = uptoMinPriceUSD
	}
	return cost, true
}

// ComputeUsageCostWithCache is ComputeUsageCost split by whether the
// provider's own automatic prompt caching served the prompt tokens from
// cache. cacheHitTokens+cacheMissTokens should sum to the call's total
// prompt tokens; pass cacheHitTokens=0 and the full prompt token count as
// cacheMissTokens when the caller doesn't know the split (e.g. a provider
// that doesn't report it), which reduces to the same result as
// ComputeUsageCost. ok is false for models without a token price entry.
func ComputeUsageCostWithCache(model string, cacheHitTokens, cacheMissTokens, completionTokens int) (float64, bool) {
	p, ok := modelTokenPrices[model]
	if !ok {
		return 0, false
	}
	hitPrice := p.In // no verified discount for this model - price hits same as misses
	if discounted, hasDiscount := modelCacheHitPrices[model]; hasDiscount {
		hitPrice = discounted
	}
	cost := (float64(cacheHitTokens)*hitPrice + float64(cacheMissTokens)*p.In + float64(completionTokens)*p.Out) / 1e6 * uptoSafetyMargin
	if cost < uptoMinPriceUSD {
		cost = uptoMinPriceUSD
	}
	return cost, true
}

// AIChargeStore handles AI charge records
type AIChargeStore struct {
	db *gorm.DB
}

// NewAIChargeStore creates a new AIChargeStore
func NewAIChargeStore(db *gorm.DB) *AIChargeStore {
	return &AIChargeStore{db: db}
}

func (s *AIChargeStore) initTables() error {
	return s.db.AutoMigrate(&AICharge{})
}

// Record records a new AI charge
func (s *AIChargeStore) Record(traderID, model, provider string) error {
	return s.RecordWithCost(traderID, model, provider, GetModelPrice(model))
}

// RecordWithCost records a charge with an explicit cost — e.g. the actual
// settled amount reported by the payment gateway (upto scheme) — instead of
// the flat per-call estimate from modelPrices.
func (s *AIChargeStore) RecordWithCost(traderID, model, provider string, costUSD float64) error {
	return s.RecordWithUsage(traderID, model, provider, costUSD, 0, 0, 0)
}

// RecordWithUsage records a charge alongside the raw token usage that
// produced it, so cache-hit rate is directly queryable later instead of
// only ever being reflected (invisibly) inside costUSD. Pass zeros for
// promptTokens/completionTokens/promptCacheHitTokens when usage wasn't
// available for this call - RecordWithCost does exactly that.
func (s *AIChargeStore) RecordWithUsage(traderID, model, provider string, costUSD float64, promptTokens, completionTokens, promptCacheHitTokens int) error {
	charge := &AICharge{
		TraderID:             traderID,
		Model:                model,
		Provider:             provider,
		CostUSD:              costUSD,
		PromptTokens:         promptTokens,
		CompletionTokens:     completionTokens,
		PromptCacheHitTokens: promptCacheHitTokens,
	}
	return s.db.Create(charge).Error
}

// GetCharges returns charges for a trader within a period, plus total cost
func (s *AIChargeStore) GetCharges(traderID string, period string) ([]AICharge, float64, error) {
	var charges []AICharge
	query := s.db.Where("trader_id = ?", traderID)
	query = applyPeriodFilter(query, period)
	if err := query.Order("created_at DESC").Find(&charges).Error; err != nil {
		return nil, 0, err
	}

	var total float64
	for _, c := range charges {
		total += c.CostUSD
	}
	return charges, total, nil
}

// GetDailyCost returns total cost across all traders for a period
func (s *AIChargeStore) GetDailyCost(period string) float64 {
	var total float64
	query := s.db.Model(&AICharge{}).Select("COALESCE(SUM(cost_usd), 0)")
	query = applyPeriodFilter(query, period)
	query.Scan(&total)
	return total
}

// GetSummary returns summary stats for a period
func (s *AIChargeStore) GetSummary(period string) (total float64, count int64, byModel map[string]float64) {
	byModel = make(map[string]float64)

	query := s.db.Model(&AICharge{})
	query = applyPeriodFilter(query, period)
	query.Count(&count)

	query2 := s.db.Model(&AICharge{}).Select("COALESCE(SUM(cost_usd), 0)")
	query2 = applyPeriodFilter(query2, period)
	query2.Scan(&total)

	// By model breakdown
	type modelCost struct {
		Model string  `gorm:"column:model"`
		Total float64 `gorm:"column:total"`
	}
	var results []modelCost
	query3 := s.db.Model(&AICharge{}).Select("model, SUM(cost_usd) as total").Group("model")
	query3 = applyPeriodFilter(query3, period)
	query3.Find(&results)
	for _, r := range results {
		byModel[r.Model] = r.Total
	}

	return total, count, byModel
}

func applyPeriodFilter(query *gorm.DB, period string) *gorm.DB {
	now := time.Now()
	switch period {
	case "today":
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		return query.Where("created_at >= ?", start)
	case "week":
		return query.Where("created_at >= ?", now.AddDate(0, 0, -7))
	case "month":
		return query.Where("created_at >= ?", now.AddDate(0, -1, 0))
	case "all":
		return query
	default:
		// Try parse as date
		if t, err := time.Parse("2006-01-02", period); err == nil {
			end := t.AddDate(0, 0, 1)
			return query.Where("created_at >= ? AND created_at < ?", t, end)
		}
		// Default to today
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		return query.Where("created_at >= ?", start)
	}
}

// IsClaw402Config checks if a trader config uses claw402 payment provider
func IsClaw402Config(aiModel string) bool {
	return aiModel == "claw402"
}

// EstimateRunway estimates how many days the given USDC balance will last
func EstimateRunway(usdcBalance float64, modelName string, scanIntervalMinutes int) (dailyCost float64, runwayDays float64) {
	if scanIntervalMinutes <= 0 {
		scanIntervalMinutes = 15
	}
	callsPerDay := float64(24*60) / float64(scanIntervalMinutes)
	pricePerCall := GetModelPrice(modelName)
	dailyCost = callsPerDay * pricePerCall
	if dailyCost > 0 && usdcBalance > 0 {
		runwayDays = usdcBalance / dailyCost
	}
	return dailyCost, runwayDays
}
