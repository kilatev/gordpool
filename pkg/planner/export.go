//go:build !js

package planner

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// LoadRecentPrices loads price slots for the past N days (UTC) into memory, ordered by timestamp asc.
func LoadRecentPrices(ctx context.Context, dbPath, area, market, currency string, days int) ([]PriceSlot, error) {
	if days <= 0 {
		days = 7
	}

	db, err := openCacheDB(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	end := time.Now().UTC()
	start := end.Add(-time.Duration(days) * 24 * time.Hour)

	rows, err := db.QueryContext(ctx, `
		SELECT ts, price_cents FROM prices
		WHERE area = ? AND market = ? AND currency = ?
		  AND ts >= ? AND ts <= ?
		ORDER BY ts ASC`, area, market, currency, start, end)
	if err != nil {
		return nil, fmt.Errorf("load recent prices: %w", err)
	}
	defer rows.Close()

	var slots []PriceSlot
	for rows.Next() {
		var ts time.Time
		var price float64
		if err := rows.Scan(&ts, &price); err != nil {
			return nil, fmt.Errorf("scan recent price: %w", err)
		}
		slots = append(slots, PriceSlot{Timestamp: ts.UTC(), Price: price})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recent rows error: %w", err)
	}
	return slots, nil
}

// PricesToCSV renders price slots as CSV with header "timestamp,price_cents".
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

// PricesToJSON renders price slots as JSON array with timestamp (RFC3339) and price_cents.
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
