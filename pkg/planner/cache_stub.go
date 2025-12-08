//go:build js

package planner

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// FetchNordpoolPricesCached is a no-op cache in wasm; it falls back to direct fetch.
func FetchNordpoolPricesCached(ctx context.Context, _ string, area, market, currency string) ([]PriceSlot, error) {
	return FetchNordpoolPricesWithBase(ctx, "https://dataportal-api.nordpoolgroup.com/api/DayAheadPrices", area, market, currency)
}

// FetchNordpoolPricesCachedWithBase is a no-op cache in wasm; it falls back to direct fetch.
func FetchNordpoolPricesCachedWithBase(ctx context.Context, baseURL, area, market, currency string) ([]PriceSlot, error) {
	return FetchNordpoolPricesWithBase(ctx, baseURL, area, market, currency)
}

// LoadRecentPrices is not supported in wasm (no sqlite); returns an error.
func LoadRecentPrices(_ context.Context, _, _, _, _ string, _ int) ([]PriceSlot, error) {
	return nil, fmt.Errorf("LoadRecentPrices not available in wasm build")
}

// PricesToCSV is available in wasm as a formatting helper.
func PricesToCSV(prices []PriceSlot) (string, error) {
	var b strings.Builder
	w := csv.NewWriter(&b)
	if err := w.Write([]string{"timestamp", "price_cents"}); err != nil {
		return "", fmt.Errorf("write header: %w", err)
	}
	for _, p := range prices {
		record := []string{p.Timestamp.Format(time.RFC3339), fmt.Sprintf("%.6f", p.Price)}
		if err := w.Write(record); err != nil {
			return "", fmt.Errorf("write record: %w", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", fmt.Errorf("flush csv: %w", err)
	}
	return b.String(), nil
}

// PricesToJSON is available in wasm as a formatting helper.
func PricesToJSON(prices []PriceSlot) ([]byte, error) {
	type row struct {
		Timestamp string  `json:"timestamp"`
		Price     float64 `json:"price_cents"`
	}
	out := make([]row, 0, len(prices))
	for _, p := range prices {
		out = append(out, row{
			Timestamp: p.Timestamp.Format(time.RFC3339),
			Price:     p.Price,
		})
	}
	return json.Marshal(out)
}
