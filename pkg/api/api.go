// Package api exposes the battery planners over HTTP.
//
//	POST /v1/battery/optimize   SoC-aware optimal plan (planner.OptimizeBattery)
//	POST /v1/battery/solax      optimal plan as SolaX DataHub 1000 windows
//	POST /v1/battery/schedule   legacy threshold plan (planner.BuildBatterySchedule)
//	GET  /healthz
//
// Both battery endpoints also accept GET with the same fields as query
// parameters. Prices are fetched from Nordpool unless the request carries its
// own "prices" array.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
	_ "time/tzdata" // the runtime image has no zoneinfo

	"gordpool/pkg/planner"
)

// PriceSource returns price slots (cents/kWh) for an area.
type PriceSource func(ctx context.Context, area, market, currency string) ([]planner.PriceSlot, error)

type Server struct {
	Prices PriceSource
	// Now is overridable for tests.
	Now func() time.Time
}

type PriceJSON struct {
	Timestamp time.Time `json:"timestamp"`
	Price     float64   `json:"price"` // cents/kWh
}

type OptimizeRequest struct {
	Area     string              `json:"area"`
	Market   string              `json:"market"`
	Currency string              `json:"currency"`
	Battery  planner.BatterySpec `json:"battery"`
	Prices   []PriceJSON         `json:"prices,omitempty"`
	Now      *time.Time          `json:"now,omitempty"`
}

type OptimizeResponse struct {
	planner.OptimizedScheduleJSON
	Prices []planner.SlotJSON `json:"prices"`
}

type SolaxRequest struct {
	OptimizeRequest
	// Timezone is the site's IANA zone (e.g. "Europe/Riga"); defaults to UTC.
	Timezone  string `json:"timezone"`
	Inverters int    `json:"inverters"`
	// MinPowerKW skips windows weaker than this; nil means DefaultSolaxMinPowerKW.
	MinPowerKW *float64 `json:"min_power_kw,omitempty"`
}

// DefaultSolaxMinPowerKW keeps sub-0.5 kW top-ups out of Smart Scene rules.
const DefaultSolaxMinPowerKW = 0.5

type ScheduleRequest struct {
	Area              string      `json:"area"`
	Market            string      `json:"market"`
	Currency          string      `json:"currency"`
	MaxChargeHours    float64     `json:"max_charge_hours"`
	MaxDischargeHours float64     `json:"max_discharge_hours"`
	LastPriceCharged  float64     `json:"last_price_charged"`
	Epsilon           float64     `json:"epsilon"`
	Prices            []PriceJSON `json:"prices,omitempty"`
	Now               *time.Time  `json:"now,omitempty"`
}

// NewServer returns a Server that fetches prices through the SQLite cache at dbPath.
func NewServer(dbPath string) *Server {
	return &Server{
		Prices: func(ctx context.Context, area, market, currency string) ([]planner.PriceSlot, error) {
			return planner.FetchNordpoolPricesCached(ctx, dbPath, area, market, currency)
		},
	}
}

// Register mounts the API routes on mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("/v1/battery/optimize", s.handleOptimize)
	mux.HandleFunc("/v1/battery/solax", s.handleSolax)
	mux.HandleFunc("/v1/battery/schedule", s.handleSchedule)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

// Handler returns a standalone handler with all routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.Register(mux)
	return mux
}

func (s *Server) handleOptimize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
		return
	}
	var req OptimizeRequest
	if err := decode(w, r, &req, optimizeFromQuery); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	plan, prices, status, err := s.optimize(r, &req)
	if err != nil {
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, OptimizeResponse{OptimizedScheduleJSON: plan, Prices: toSlotJSON(prices)})
}

func (s *Server) handleSolax(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
		return
	}
	var req SolaxRequest
	if err := decode(w, r, &req, solaxFromQuery); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	loc := time.UTC
	if req.Timezone != "" {
		l, err := time.LoadLocation(req.Timezone)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("timezone: %w", err))
			return
		}
		loc = l
	}
	if req.Inverters < 0 {
		writeError(w, http.StatusBadRequest, errors.New("inverters must be >= 0"))
		return
	}
	minPower := DefaultSolaxMinPowerKW
	if req.MinPowerKW != nil {
		minPower = *req.MinPowerKW
	}
	plan, _, status, err := s.optimize(r, &req.OptimizeRequest)
	if err != nil {
		writeError(w, status, err)
		return
	}
	out, err := plan.ToSolaxDataHub(planner.SolaxOptions{Location: loc, Inverters: req.Inverters, MinPowerKW: minPower})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) optimize(r *http.Request, req *OptimizeRequest) (planner.OptimizedScheduleJSON, []planner.PriceSlot, int, error) {
	setMarketDefaults(&req.Area, &req.Market, &req.Currency)
	prices, status, err := s.prices(r.Context(), req.Prices, req.Area, req.Market, req.Currency)
	if err != nil {
		return planner.OptimizedScheduleJSON{}, nil, status, err
	}
	plan, err := planner.OptimizeBattery(prices, req.Area, req.Battery, s.now(req.Now))
	if err != nil {
		return planner.OptimizedScheduleJSON{}, nil, http.StatusBadRequest, err
	}
	return plan, prices, 0, nil
}

func (s *Server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
		return
	}
	var req ScheduleRequest
	if err := decode(w, r, &req, scheduleFromQuery); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	setMarketDefaults(&req.Area, &req.Market, &req.Currency)
	if req.MaxChargeHours < 0 || req.MaxDischargeHours < 0 {
		writeError(w, http.StatusBadRequest, errors.New("max hours must be >= 0"))
		return
	}

	prices, status, err := s.prices(r.Context(), req.Prices, req.Area, req.Market, req.Currency)
	if err != nil {
		writeError(w, status, err)
		return
	}
	params := planner.BatteryStrategyParams{
		Area:              req.Area,
		Market:            req.Market,
		Currency:          req.Currency,
		MaxChargeHours:    req.MaxChargeHours,
		MaxDischargeHours: req.MaxDischargeHours,
		LastPriceCharged:  req.LastPriceCharged,
		Epsilon:           req.Epsilon,
	}
	writeJSON(w, http.StatusOK, planner.BuildBatterySchedule(prices, params, s.now(req.Now)))
}

func (s *Server) prices(ctx context.Context, given []PriceJSON, area, market, currency string) ([]planner.PriceSlot, int, error) {
	if len(given) > 0 {
		out := make([]planner.PriceSlot, len(given))
		for i, p := range given {
			out[i] = planner.PriceSlot{Timestamp: p.Timestamp.UTC(), Price: p.Price}
		}
		return out, 0, nil
	}
	if s.Prices == nil {
		return nil, http.StatusBadRequest, errors.New("no price source configured; pass prices in the request")
	}
	prices, err := s.Prices(ctx, area, market, currency)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("fetch prices: %w", err)
	}
	return prices, 0, nil
}

func (s *Server) now(override *time.Time) time.Time {
	if override != nil {
		return override.UTC()
	}
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func setMarketDefaults(area, market, currency *string) {
	if *area == "" {
		*area = "LV"
	}
	if *market == "" {
		*market = "DayAhead"
	}
	if *currency == "" {
		*currency = "EUR"
	}
}

func decode[T any](w http.ResponseWriter, r *http.Request, dst *T, fromQuery func(url.Values, *T) error) error {
	if r.Method == http.MethodGet {
		return fromQuery(r.URL.Query(), dst)
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func optimizeFromQuery(q url.Values, req *OptimizeRequest) error {
	req.Area, req.Market, req.Currency = q.Get("area"), q.Get("market"), q.Get("currency")
	b := &req.Battery
	fields := map[string]*float64{
		"capacity_kwh":           &b.CapacityKWh,
		"max_charge_kw":          &b.MaxChargeKW,
		"max_discharge_kw":       &b.MaxDischargeKW,
		"initial_soc_percent":    &b.InitialSoCPercent,
		"min_soc_percent":        &b.MinSoCPercent,
		"max_soc_percent":        &b.MaxSoCPercent,
		"round_trip_efficiency":  &b.RoundTripEfficiency,
		"degradation_cost_cents": &b.DegradationCostCents,
		"max_cycles_per_day":     &b.MaxCyclesPerDay,
	}
	if err := parseFloats(q, fields); err != nil {
		return err
	}
	if v := q.Get("stored_energy_value_cents"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("stored_energy_value_cents: %w", err)
		}
		b.StoredEnergyValueCents = &f
	}
	return nil
}

func solaxFromQuery(q url.Values, req *SolaxRequest) error {
	if err := optimizeFromQuery(q, &req.OptimizeRequest); err != nil {
		return err
	}
	req.Timezone = q.Get("timezone")
	if v := q.Get("inverters"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("inverters: %w", err)
		}
		req.Inverters = n
	}
	if v := q.Get("min_power_kw"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("min_power_kw: %w", err)
		}
		req.MinPowerKW = &f
	}
	return nil
}

func scheduleFromQuery(q url.Values, req *ScheduleRequest) error {
	req.Area, req.Market, req.Currency = q.Get("area"), q.Get("market"), q.Get("currency")
	return parseFloats(q, map[string]*float64{
		"max_charge_hours":    &req.MaxChargeHours,
		"max_discharge_hours": &req.MaxDischargeHours,
		"last_price_charged":  &req.LastPriceCharged,
		"epsilon":             &req.Epsilon,
	})
}

func parseFloats(q url.Values, fields map[string]*float64) error {
	for name, dst := range fields {
		v := q.Get(name)
		if v == "" {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*dst = f
	}
	return nil
}

func toSlotJSON(prices []planner.PriceSlot) []planner.SlotJSON {
	out := make([]planner.SlotJSON, 0, len(prices))
	for _, p := range prices {
		out = append(out, planner.SlotJSON{Timestamp: p.Timestamp.Format(time.RFC3339), Price: p.Price})
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
