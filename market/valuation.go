package market

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"nofx/logger"
)

// ============================================================================
// Valuation Data Service — 估值数据服务
// ============================================================================
// Fetches crypto market valuation data from CoinGecko (free tier)
// ============================================================================

// ValuationData crypto market valuation metrics
type ValuationData struct {
	// Total crypto market cap in USD
	TotalMarketCap float64 `json:"total_market_cap"`
	// BTC dominance percentage
	BTCDominance float64 `json:"btc_dominance"`
	// ETH dominance percentage
	ETHDominance float64 `json:"eth_dominance"`
	// Total market cap change in 24h (percentage)
	MarketCapChange24h float64 `json:"market_cap_change_24h"`
	// Active cryptocurrencies count
	ActiveCryptos int `json:"active_cryptos"`
	// Data timestamp
	DataTime string `json:"data_time"`
}

// CoinGeckoGlobalResponse CoinGecko /global API response
type CoinGeckoGlobalResponse struct {
	Data struct {
		ActiveCryptocurrencies       int                `json:"active_cryptocurrencies"`
		TotalMarketCap               map[string]float64 `json:"total_market_cap"`
		MarketCapPercentage          map[string]float64 `json:"market_cap_percentage"`
		MarketCapChangePercentage24h float64            `json:"market_cap_change_percentage_24h_usd"`
	} `json:"data"`
}

var valuationHTTPClient = &http.Client{Timeout: 10 * time.Second}

// FetchValuationData fetches global crypto valuation data from CoinGecko
func FetchValuationData() (*ValuationData, error) {
	req, err := http.NewRequest("GET", "https://api.coingecko.com/api/v3/global", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create CoinGecko request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := valuationHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch CoinGecko global data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CoinGecko API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read CoinGecko response: %w", err)
	}

	var cgResp CoinGeckoGlobalResponse
	if err := json.Unmarshal(body, &cgResp); err != nil {
		return nil, fmt.Errorf("failed to parse CoinGecko response: %w", err)
	}

	totalMarketCap := cgResp.Data.TotalMarketCap["usd"]
	btcDominance := cgResp.Data.MarketCapPercentage["btc"]
	ethDominance := cgResp.Data.MarketCapPercentage["eth"]

	result := &ValuationData{
		TotalMarketCap:     totalMarketCap,
		BTCDominance:       btcDominance,
		ETHDominance:       ethDominance,
		MarketCapChange24h: cgResp.Data.MarketCapChangePercentage24h,
		ActiveCryptos:      cgResp.Data.ActiveCryptocurrencies,
		DataTime:           time.Now().UTC().Format("2006-01-02 15:04:05"),
	}

	logger.Infof("📊 Valuation: Total MCap $%.0fB | BTC %.1f%% | ETH %.1f%% | 24h %+.1f%%",
		totalMarketCap/1e9, btcDominance, ethDominance, cgResp.Data.MarketCapChangePercentage24h)

	return result, nil
}
