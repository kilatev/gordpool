package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"time"
)

// Domain types shared between TUI and browser frontends.

type BatteryStrategyParams struct {
	Area              string
	MaxChargeHours    float64
	MaxDischargeHours float64
	LastPriceCharged  float64 // cents/kWh
	Epsilon           float64 // cents/kWh
	Market            string
	Currency          string
}

type PriceSlot struct {
	Timestamp time.Time
	Price     float64 // cents/kWh
}

type SlotJSON struct {
	Timestamp string  `json:"timestamp"`
	Price     float64 `json:"price"`
}

type IntervalJSON struct {
	Start    string  `json:"start"`
	End      string  `json:"end"`
	AvgPrice float64 `json:"avg_price"`
}

type ScheduleJSON struct {
	Area               string         `json:"area"`
	LastPriceCharged   float64        `json:"last_price_charged"`
	Epsilon            float64        `json:"epsilon"`
	ResolutionMinutes  *int           `json:"resolution_minutes"`
	ChargeSlots        []SlotJSON     `json:"charge_slots"`
	DischargeSlots     []SlotJSON     `json:"discharge_slots"`
	ChargeIntervals    []IntervalJSON `json:"charge_intervals"`
	DischargeIntervals []IntervalJSON `json:"discharge_intervals"`
}

type dayAheadResponse struct {
	DeliveryDateCET string   `json:"deliveryDateCET"`
	Version         int      `json:"version"`
	UpdatedAt       string   `json:"updatedAt"`
	DeliveryAreas   []string `json:"deliveryAreas"`
	Market          string   `json:"market"`
	Currency        string   `json:"currency"`
	ExchangeRate    float64  `json:"exchangeRate"`
	AreaAverages    []struct {
		AreaCode string  `json:"areaCode"`
		Price    float64 `json:"price"`
	} `json:"areaAverages"`
	MultiAreaEntries []struct {
		DeliveryStart string             `json:"deliveryStart"`
		DeliveryEnd   string             `json:"deliveryEnd"`
		EntryPerArea  map[string]float64 `json:"entryPerArea"`
	} `json:"multiAreaEntries"`
}

// getTodayAndTomorrowUTC returns midnight UTC for today and tomorrow.
func getTodayAndTomorrowUTC() (time.Time, time.Time) {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	tomorrow := today.Add(24 * time.Hour)
	return today, tomorrow
}

// FetchNordpoolPrices fetches today+tomorrow prices in EUR/MWh and converts to cents/kWh.
func FetchNordpoolPrices(ctx context.Context, area, market, currency string) ([]PriceSlot, error) {
	return FetchNordpoolPricesWithBase(ctx, "https://dataportal-api.nordpoolgroup.com/api/DayAheadPrices", area, market, currency)
}

// FetchNordpoolPricesWithBase is like FetchNordpoolPrices but allows overriding the base URL (useful for proxies/CORS).
func FetchNordpoolPricesWithBase(ctx context.Context, baseURL, area, market, currency string) ([]PriceSlot, error) {
	today, tomorrow := getTodayAndTomorrowUTC()
	dates := []time.Time{today, tomorrow}

	client := &http.Client{Timeout: 10 * time.Second}
	var allSlots []PriceSlot

	for _, d := range dates {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
		if err != nil {
			return nil, err
		}
		q := req.URL.Query()
		q.Add("date", d.Format("2006-01-02"))
		q.Add("market", market)
		q.Add("deliveryArea", area)
		q.Add("currency", currency)
		req.URL.RawQuery = q.Encode()
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "gordpool/1.0 (+https://github.com/)")

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed for %s: %w", d.Format("2006-01-02"), err)
		}

		func() {
			defer resp.Body.Close()

			if resp.StatusCode >= 300 {
				err = fmt.Errorf("Nordpool API %s: %s", d.Format("2006-01-02"), resp.Status)
				return
			}

			var raw dayAheadResponse
			decErr := json.NewDecoder(resp.Body).Decode(&raw)
			if decErr == io.EOF {
				// No data yet for this date (e.g. tomorrow not published) – skip.
				return
			}
			if decErr != nil {
				err = fmt.Errorf("JSON decode failed for %s: %w", d.Format("2006-01-02"), decErr)
				return
			}

			for _, entry := range raw.MultiAreaEntries {
				ts, parseErr := time.Parse(time.RFC3339, entry.DeliveryStart)
				if parseErr != nil {
					continue
				}
				priceEurPerMWh, ok := entry.EntryPerArea[area]
				if !ok {
					continue
				}

				// Convert EUR/MWh → cents/kWh:
				// EUR/MWh / 1000 = EUR/kWh; *100 = cents/kWh => divide by 10.
				priceCentsPerKWh := priceEurPerMWh / 10.0

				allSlots = append(allSlots, PriceSlot{
					Timestamp: ts,
					Price:     priceCentsPerKWh,
				})
			}
		}()
		if err != nil {
			return nil, err
		}
	}

	sort.Slice(allSlots, func(i, j int) bool {
		return allSlots[i].Timestamp.Before(allSlots[j].Timestamp)
	})

	return allSlots, nil
}

func inferResolutionMinutes(prices []PriceSlot) int {
	if len(prices) < 2 {
		return 60
	}
	var deltas []float64
	for i := 1; i < len(prices); i++ {
		dt := prices[i].Timestamp.Sub(prices[i-1].Timestamp).Minutes()
		deltas = append(deltas, dt)
	}
	sort.Float64s(deltas)
	mid := len(deltas) / 2
	var median float64
	if len(deltas)%2 == 0 {
		median = (deltas[mid-1] + deltas[mid]) / 2
	} else {
		median = deltas[mid]
	}
	return int(math.Round(median))
}

func slotsForHours(maxHours float64, resolutionMinutes int) int {
	totalMinutes := maxHours * 60
	return int(math.Ceil(totalMinutes / float64(resolutionMinutes)))
}

func groupConsecutiveSlots(slots []PriceSlot, resolutionMinutes int) []IntervalJSON {
	if len(slots) == 0 {
		return nil
	}

	sort.Slice(slots, func(i, j int) bool {
		return slots[i].Timestamp.Before(slots[j].Timestamp)
	})

	step := time.Duration(resolutionMinutes) * time.Minute
	var groups [][]PriceSlot
	group := []PriceSlot{slots[0]}

	for i := 1; i < len(slots); i++ {
		prev := slots[i-1]
		cur := slots[i]
		if cur.Timestamp.Sub(prev.Timestamp) == step {
			group = append(group, cur)
		} else {
			groups = append(groups, group)
			group = []PriceSlot{cur}
		}
	}
	groups = append(groups, group)

	var intervals []IntervalJSON
	for _, g := range groups {
		if len(g) == 0 {
			continue
		}
		start := g[0].Timestamp
		end := g[len(g)-1].Timestamp.Add(step)

		sum := 0.0
		for _, s := range g {
			sum += s.Price
		}
		avg := sum / float64(len(g))

		intervals = append(intervals, IntervalJSON{
			Start:    start.Format(time.RFC3339),
			End:      end.Format(time.RFC3339),
			AvgPrice: avg,
		})
	}

	return intervals
}

// BuildBatterySchedule builds a charge/discharge schedule for the next slots.
func BuildBatterySchedule(prices []PriceSlot, params BatteryStrategyParams, now time.Time) ScheduleJSON {
	var future []PriceSlot
	for _, p := range prices {
		if !p.Timestamp.Before(now) {
			future = append(future, p)
		}
	}
	if len(future) == 0 {
		return ScheduleJSON{
			Area:               params.Area,
			LastPriceCharged:   params.LastPriceCharged,
			Epsilon:            params.Epsilon,
			ResolutionMinutes:  nil,
			ChargeSlots:        []SlotJSON{},
			DischargeSlots:     []SlotJSON{},
			ChargeIntervals:    []IntervalJSON{},
			DischargeIntervals: []IntervalJSON{},
		}
	}

	resolution := inferResolutionMinutes(future)
	resPtr := &resolution

	maxChargeSlots := slotsForHours(params.MaxChargeHours, resolution)
	maxDischargeSlots := slotsForHours(params.MaxDischargeHours, resolution)

	var chargeCandidates []PriceSlot
	var dischargeCandidates []PriceSlot

	// Discharge threshold is capped to 8 c/kWh to allow discharging even when
	// last price + epsilon is too high for current market prices.
	dischargeThreshold := params.LastPriceCharged + params.Epsilon
	if dischargeThreshold > 8 {
		dischargeThreshold = 8
	}

	for _, s := range future {
		if params.LastPriceCharged-s.Price >= params.Epsilon {
			chargeCandidates = append(chargeCandidates, s)
		}
		if s.Price >= dischargeThreshold {
			dischargeCandidates = append(dischargeCandidates, s)
		}
	}

	sort.Slice(chargeCandidates, func(i, j int) bool {
		return chargeCandidates[i].Price < chargeCandidates[j].Price
	})
	sort.Slice(dischargeCandidates, func(i, j int) bool {
		return dischargeCandidates[i].Price > dischargeCandidates[j].Price
	})

	if len(chargeCandidates) > maxChargeSlots {
		chargeCandidates = chargeCandidates[:maxChargeSlots]
	}
	if len(dischargeCandidates) > maxDischargeSlots {
		dischargeCandidates = dischargeCandidates[:maxDischargeSlots]
	}

	sort.Slice(chargeCandidates, func(i, j int) bool {
		return chargeCandidates[i].Timestamp.Before(chargeCandidates[j].Timestamp)
	})
	sort.Slice(dischargeCandidates, func(i, j int) bool {
		return dischargeCandidates[i].Timestamp.Before(dischargeCandidates[j].Timestamp)
	})

	chargeIntervals := groupConsecutiveSlots(chargeCandidates, resolution)
	dischargeIntervals := groupConsecutiveSlots(dischargeCandidates, resolution)

	toSlotJSON := func(slots []PriceSlot) []SlotJSON {
		out := make([]SlotJSON, 0, len(slots))
		for _, s := range slots {
			out = append(out, SlotJSON{
				Timestamp: s.Timestamp.Format(time.RFC3339),
				Price:     s.Price,
			})
		}
		return out
	}

	return ScheduleJSON{
		Area:               params.Area,
		LastPriceCharged:   params.LastPriceCharged,
		Epsilon:            params.Epsilon,
		ResolutionMinutes:  resPtr,
		ChargeSlots:        toSlotJSON(chargeCandidates),
		DischargeSlots:     toSlotJSON(dischargeCandidates),
		ChargeIntervals:    chargeIntervals,
		DischargeIntervals: dischargeIntervals,
	}
}
