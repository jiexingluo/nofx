package market

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Binance USDT-M positioning data — funding history, open-interest history and
// the global long/short account ratio — used as context for the daily trend
// AI review. Public endpoints, no API key. Binance keeps only the last 30 days
// of open-interest and long/short history, so these are live-only inputs.

var positioningHTTPClient = &http.Client{Timeout: 10 * time.Second}

// FundingPoint is one funding settlement.
type FundingPoint struct {
	Time int64
	Rate float64 // per funding interval, e.g. 0.0001 = 0.01%
}

// OpenInterestPoint is open interest in USDT at a period close.
type OpenInterestPoint struct {
	Time  int64
	Value float64
}

// LongShortPoint is the global long/short account ratio at a period close.
type LongShortPoint struct {
	Time  int64
	Ratio float64
}

func getPositioningJSON(url string, out interface{}) error {
	resp, err := positioningHTTPClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %.200s", resp.StatusCode, body)
	}
	return json.Unmarshal(body, out)
}

// FetchFundingHistory returns up to limit (max 1000) recent funding prints,
// oldest first.
func FetchFundingHistory(symbol string, limit int) ([]FundingPoint, error) {
	var raw []struct {
		FundingTime int64  `json:"fundingTime"`
		FundingRate string `json:"fundingRate"`
	}
	if err := getPositioningJSON(fmt.Sprintf("%s/fapi/v1/fundingRate?symbol=%s&limit=%d", baseURL, symbol, limit), &raw); err != nil {
		return nil, fmt.Errorf("funding history %s: %w", symbol, err)
	}
	out := make([]FundingPoint, 0, len(raw))
	for _, r := range raw {
		if rate, err := strconv.ParseFloat(r.FundingRate, 64); err == nil {
			out = append(out, FundingPoint{Time: r.FundingTime, Rate: rate})
		}
	}
	return out, nil
}

// FetchOpenInterestHistory returns open interest (USDT) per period, oldest
// first. period is a Binance period such as "1h" or "1d"; limit max 500.
func FetchOpenInterestHistory(symbol, period string, limit int) ([]OpenInterestPoint, error) {
	var raw []struct {
		Value     string `json:"sumOpenInterestValue"`
		Timestamp int64  `json:"timestamp"`
	}
	if err := getPositioningJSON(fmt.Sprintf("%s/futures/data/openInterestHist?symbol=%s&period=%s&limit=%d", baseURL, symbol, period, limit), &raw); err != nil {
		return nil, fmt.Errorf("open interest history %s: %w", symbol, err)
	}
	out := make([]OpenInterestPoint, 0, len(raw))
	for _, r := range raw {
		if v, err := strconv.ParseFloat(r.Value, 64); err == nil {
			out = append(out, OpenInterestPoint{Time: r.Timestamp, Value: v})
		}
	}
	return out, nil
}

// FetchLongShortAccountRatio returns the global long/short account ratio per
// period, oldest first.
func FetchLongShortAccountRatio(symbol, period string, limit int) ([]LongShortPoint, error) {
	var raw []struct {
		Ratio     string `json:"longShortRatio"`
		Timestamp int64  `json:"timestamp"`
	}
	if err := getPositioningJSON(fmt.Sprintf("%s/futures/data/globalLongShortAccountRatio?symbol=%s&period=%s&limit=%d", baseURL, symbol, period, limit), &raw); err != nil {
		return nil, fmt.Errorf("long/short ratio %s: %w", symbol, err)
	}
	out := make([]LongShortPoint, 0, len(raw))
	for _, r := range raw {
		if v, err := strconv.ParseFloat(r.Ratio, 64); err == nil {
			out = append(out, LongShortPoint{Time: r.Timestamp, Ratio: v})
		}
	}
	return out, nil
}
