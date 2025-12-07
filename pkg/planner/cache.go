package planner

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // SQLite driver (CGO-free)
)

const (
	// Allow a bit of slack for DST hours; Nordpool usually publishes 24 slots.
	minSlotsPerDay = 20
)

// FetchNordpoolPricesCached fetches prices using a SQLite-backed cache.
// Data for today and tomorrow is considered stale once valid_until has passed,
// prompting a refetch. dbPath will be created if it does not exist.
func FetchNordpoolPricesCached(ctx context.Context, dbPath, area, market, currency string) ([]PriceSlot, error) {
	return FetchNordpoolPricesCachedWithBase(ctx, dbPath, "https://dataportal-api.nordpoolgroup.com/api/DayAheadPrices", area, market, currency)
}

// FetchNordpoolPricesCachedWithBase is like FetchNordpoolPricesCached but allows overriding the API base URL.
func FetchNordpoolPricesCachedWithBase(ctx context.Context, dbPath, baseURL, area, market, currency string) ([]PriceSlot, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("creating cache dir: %w", err)
	}

	db, err := openCacheDB(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	now := time.Now().UTC()
	today, tomorrow := getTodayAndTomorrowUTC()
	dates := []time.Time{today, tomorrow}

	needsRefresh := false
	for _, day := range dates {
		fresh, err := hasFreshDay(ctx, db, area, market, currency, day, now)
		if err != nil {
			return nil, err
		}
		if !fresh {
			needsRefresh = true
			break
		}
	}

	if needsRefresh {
		prices, err := FetchNordpoolPricesWithBase(ctx, baseURL, area, market, currency)
		if err != nil {
			return nil, err
		}
		if err := storePrices(ctx, db, prices, area, market, currency); err != nil {
			return nil, err
		}
	}

	return loadPrices(ctx, db, area, market, currency)
}

func openCacheDB(ctx context.Context, dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open cache db: %w", err)
	}
	db.SetMaxOpenConns(1)

	if err := applyPragmas(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureSchema(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func applyPragmas(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL;"); err != nil {
		return fmt.Errorf("set WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=5000;"); err != nil {
		return fmt.Errorf("set busy_timeout: %w", err)
	}
	return nil
}

func ensureSchema(ctx context.Context, db *sql.DB) error {
	create := `
	CREATE TABLE IF NOT EXISTS prices (
		area TEXT NOT NULL,
		market TEXT NOT NULL,
		currency TEXT NOT NULL,
		ts DATETIME NOT NULL,
		price_cents REAL NOT NULL,
		fetched_at DATETIME NOT NULL,
		valid_until DATETIME NOT NULL,
		PRIMARY KEY (area, market, currency, ts)
	);
	CREATE INDEX IF NOT EXISTS idx_prices_area_ts ON prices(area, market, currency, ts);
	CREATE INDEX IF NOT EXISTS idx_prices_valid_until ON prices(area, market, currency, valid_until);
	`
	if _, err := db.ExecContext(ctx, create); err != nil {
		return fmt.Errorf("ensure schema: %w", err)
	}
	return nil
}

func hasFreshDay(ctx context.Context, db *sql.DB, area, market, currency string, dayStart time.Time, now time.Time) (bool, error) {
	dayStart = dayStart.UTC()
	dayEnd := dayStart.Add(24 * time.Hour)
	var count int
	row := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM prices
		WHERE area = ? AND market = ? AND currency = ?
		  AND ts >= ? AND ts < ?
		  AND valid_until > ?`,
		area, market, currency, dayStart, dayEnd, now)
	if err := row.Scan(&count); err != nil {
		return false, fmt.Errorf("check freshness: %w", err)
	}
	return count >= minSlotsPerDay, nil
}

func storePrices(ctx context.Context, db *sql.DB, prices []PriceSlot, area, market, currency string) error {
	if len(prices) == 0 {
		return nil
	}
	now := time.Now().UTC()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO prices(area, market, currency, ts, price_cents, fetched_at, valid_until)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(area, market, currency, ts) DO UPDATE SET
			price_cents = excluded.price_cents,
			fetched_at = excluded.fetched_at,
			valid_until = excluded.valid_until`)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("prep insert: %w", err)
	}
	defer stmt.Close()

	for _, p := range prices {
		ts := p.Timestamp.UTC()
		dayStart := time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC)
		validUntil := dayStart.Add(24 * time.Hour)

		if _, err := stmt.ExecContext(ctx, area, market, currency, ts, p.Price, now, validUntil); err != nil {
			tx.Rollback()
			return fmt.Errorf("insert price %s: %w", ts, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit prices: %w", err)
	}
	return nil
}

func loadPrices(ctx context.Context, db *sql.DB, area, market, currency string) ([]PriceSlot, error) {
	today, tomorrow := getTodayAndTomorrowUTC()
	start := today
	end := tomorrow.Add(24 * time.Hour)

	rows, err := db.QueryContext(ctx, `
		SELECT ts, price_cents FROM prices
		WHERE area = ? AND market = ? AND currency = ?
		  AND ts >= ? AND ts < ?
		ORDER BY ts ASC`, area, market, currency, start, end)
	if err != nil {
		return nil, fmt.Errorf("load prices: %w", err)
	}
	defer rows.Close()

	var slots []PriceSlot
	for rows.Next() {
		var ts time.Time
		var price float64
		if err := rows.Scan(&ts, &price); err != nil {
			return nil, fmt.Errorf("scan price: %w", err)
		}
		slots = append(slots, PriceSlot{Timestamp: ts.UTC(), Price: price})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return slots, nil
}
