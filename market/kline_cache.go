package market

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const klineCacheDir = "data/kline_cache"

var klineCacheMu sync.Mutex

// klineCacheKey returns the file path for a symbol+timeframe cache.
func klineCacheKey(symbol, timeframe string) string {
	return filepath.Join(klineCacheDir, fmt.Sprintf("%s_%s.json", symbol, timeframe))
}

// loadKlineCache loads cached klines for a symbol+timeframe from disk.
func loadKlineCache(symbol, timeframe string) ([]Kline, error) {
	path := klineCacheKey(symbol, timeframe)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var klines []Kline
	if err := json.Unmarshal(data, &klines); err != nil {
		return nil, nil // corrupted cache, treat as empty
	}
	return klines, nil
}

// saveKlineCache saves klines to disk, merging with existing cache.
func saveKlineCache(symbol, timeframe string, newKlines []Kline) error {
	if len(newKlines) == 0 {
		return nil
	}
	if err := os.MkdirAll(klineCacheDir, 0o755); err != nil {
		return err
	}

	existing, _ := loadKlineCache(symbol, timeframe)

	// Merge: index by OpenTime to deduplicate
	byTime := make(map[int64]Kline, len(existing)+len(newKlines))
	for _, k := range existing {
		byTime[k.OpenTime] = k
	}
	for _, k := range newKlines {
		byTime[k.OpenTime] = k
	}

	merged := make([]Kline, 0, len(byTime))
	for _, k := range byTime {
		merged = append(merged, k)
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].OpenTime < merged[j].OpenTime
	})

	data, err := json.Marshal(merged)
	if err != nil {
		return err
	}

	path := klineCacheKey(symbol, timeframe)
	return os.WriteFile(path, data, 0o644)
}

// filterKlinesFromCache returns klines within [startMs, endMs] from cache.
// Returns the klines and whether the cache fully covers the range.
func filterKlinesFromCache(cached []Kline, startMs, endMs int64) ([]Kline, bool) {
	if len(cached) == 0 {
		return nil, false
	}

	var result []Kline
	for _, k := range cached {
		if k.OpenTime >= startMs && k.OpenTime <= endMs {
			result = append(result, k)
		}
	}

	if len(result) == 0 {
		return nil, false
	}

	// Check coverage: first kline should be near startMs, last near endMs
	first := result[0].OpenTime
	last := result[len(result)-1].CloseTime
	covered := first <= startMs+60000 && last >= endMs-60000 // 1 minute tolerance
	return result, covered
}
