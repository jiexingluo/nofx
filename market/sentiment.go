package market

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"nofx/logger"
)

// ============================================================================
// Sentiment Data Service — 市场情绪数据服务
// ============================================================================
// Fetches market sentiment from multiple sources:
// 1. Alternative.me Fear & Greed Index (free, no API key)
// 2. CryptoRacle API (optional, requires API key)
// ============================================================================

// SentimentData aggregated sentiment data from multiple sources
type SentimentData struct {
	// Fear & Greed Index (0-100, 0=Extreme Fear, 100=Extreme Greed)
	FearGreedIndex int    `json:"fear_greed_index"`
	FearGreedLabel string `json:"fear_greed_label"`
	// CryptoRacle sentiment (optional)
	PositiveRatio float64 `json:"positive_ratio,omitempty"`
	NegativeRatio float64 `json:"negative_ratio,omitempty"`
	NetSentiment  float64 `json:"net_sentiment,omitempty"`
	// Metadata
	DataTime string   `json:"data_time"`
	Sources  []string `json:"sources"`
}

// FearGreedResponse Alternative.me Fear & Greed API response
type FearGreedResponse struct {
	Name string `json:"name"`
	Data []struct {
		Value               string `json:"value"`
		ValueClassification string `json:"value_classification"`
		Timestamp           string `json:"timestamp"`
	} `json:"data"`
}

// CryptoRacleRequest CryptoRacle API request body
type CryptoRacleRequest struct {
	APIKey    string   `json:"apiKey"`
	Endpoints []string `json:"endpoints"`
	StartTime string   `json:"startTime"`
	EndTime   string   `json:"endTime"`
	TimeType  string   `json:"timeType"`
	Token     []string `json:"token"`
}

// CryptoRacleResponse CryptoRacle API response
type CryptoRacleResponse struct {
	Code int `json:"code"`
	Data []struct {
		TimePeriods []struct {
			StartTime string `json:"startTime"`
			Data      []struct {
				Endpoint string `json:"endpoint"`
				Value    string `json:"value"`
			} `json:"data"`
		} `json:"timePeriods"`
	} `json:"data"`
}

var sentimentHTTPClient = &http.Client{Timeout: 10 * time.Second}

// FetchFearGreedIndex fetches the Fear & Greed Index from Alternative.me
func FetchFearGreedIndex() (*SentimentData, error) {
	resp, err := sentimentHTTPClient.Get("https://api.alternative.me/fng/?limit=1")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Fear & Greed Index: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read Fear & Greed response: %w", err)
	}

	var fngResp FearGreedResponse
	if err := json.Unmarshal(body, &fngResp); err != nil {
		return nil, fmt.Errorf("failed to parse Fear & Greed response: %w", err)
	}

	if len(fngResp.Data) == 0 {
		return nil, fmt.Errorf("no Fear & Greed data available")
	}

	var index int
	fmt.Sscanf(fngResp.Data[0].Value, "%d", &index)

	return &SentimentData{
		FearGreedIndex: index,
		FearGreedLabel: fngResp.Data[0].ValueClassification,
		DataTime:       time.Now().UTC().Format("2006-01-02 15:04:05"),
		Sources:        []string{"alternative.me"},
	}, nil
}

// FetchCryptoRacleSentiment fetches sentiment from CryptoRacle API
func FetchCryptoRacleSentiment(apiKey string, endpoints []string) (*SentimentData, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("CryptoRacle API key not configured")
	}

	if len(endpoints) == 0 {
		endpoints = []string{"CO-A-02-01", "CO-A-02-02"}
	}

	endTime := time.Now()
	startTime := endTime.Add(-4 * time.Hour)

	reqBody := CryptoRacleRequest{
		APIKey:    apiKey,
		Endpoints: endpoints,
		StartTime: startTime.Format("2006-01-02 15:04:05"),
		EndTime:   endTime.Format("2006-01-02 15:04:05"),
		TimeType:  "15m",
		Token:     []string{"BTC"},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal CryptoRacle request: %w", err)
	}

	req, err := http.NewRequest("POST", "https://service.cryptoracle.network/openapi/v2/endpoint",
		io.NopCloser(strings.NewReader(string(jsonBody))))
	if err != nil {
		return nil, fmt.Errorf("failed to create CryptoRacle request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", apiKey)

	resp, err := sentimentHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch CryptoRacle data: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read CryptoRacle response: %w", err)
	}

	var crResp CryptoRacleResponse
	if err := json.Unmarshal(body, &crResp); err != nil {
		return nil, fmt.Errorf("failed to parse CryptoRacle response: %w", err)
	}

	if crResp.Code != 200 || len(crResp.Data) == 0 {
		return nil, fmt.Errorf("CryptoRacle API error: code=%d", crResp.Code)
	}

	// Find the first time period with valid data
	for _, period := range crResp.Data[0].TimePeriods {
		sentiment := make(map[string]float64)
		for _, item := range period.Data {
			if item.Value == "" {
				continue
			}
			var val float64
			if _, err := fmt.Sscanf(item.Value, "%f", &val); err == nil {
				sentiment[item.Endpoint] = val
			}
		}

		positive, hasPos := sentiment["CO-A-02-01"]
		negative, hasNeg := sentiment["CO-A-02-02"]
		if hasPos && hasNeg {
			return &SentimentData{
				PositiveRatio: positive,
				NegativeRatio: negative,
				NetSentiment:  positive - negative,
				DataTime:      period.StartTime,
				Sources:       []string{"cryptoracle"},
			}, nil
		}
	}

	return nil, fmt.Errorf("no valid CryptoRacle sentiment data found")
}

// FetchSentiment fetches sentiment from all configured sources and merges them
func FetchSentiment(enableFearGreed, enableCryptoRacle bool, cryptoRacleAPIKey string, cryptoRacleEndpoints []string) *SentimentData {
	result := &SentimentData{
		DataTime: time.Now().UTC().Format("2006-01-02 15:04:05"),
		Sources:  []string{},
	}

	// 1. Fear & Greed Index (free)
	if enableFearGreed {
		if fng, err := FetchFearGreedIndex(); err == nil {
			result.FearGreedIndex = fng.FearGreedIndex
			result.FearGreedLabel = fng.FearGreedLabel
			result.Sources = append(result.Sources, "alternative.me")
			logger.Infof("📊 Fear & Greed Index: %d (%s)", fng.FearGreedIndex, fng.FearGreedLabel)
		} else {
			logger.Warnf("⚠️ Failed to fetch Fear & Greed Index: %v", err)
		}
	}

	// 2. CryptoRacle (optional)
	if enableCryptoRacle && cryptoRacleAPIKey != "" {
		if cr, err := FetchCryptoRacleSentiment(cryptoRacleAPIKey, cryptoRacleEndpoints); err == nil {
			result.PositiveRatio = cr.PositiveRatio
			result.NegativeRatio = cr.NegativeRatio
			result.NetSentiment = cr.NetSentiment
			result.Sources = append(result.Sources, "cryptoracle")
			logger.Infof("📊 CryptoRacle Sentiment: +%.3f / -%.3f (net: %+.3f)",
				cr.PositiveRatio, cr.NegativeRatio, cr.NetSentiment)
		} else {
			logger.Warnf("⚠️ Failed to fetch CryptoRacle sentiment: %v", err)
		}
	}

	if len(result.Sources) == 0 {
		return nil
	}

	return result
}
