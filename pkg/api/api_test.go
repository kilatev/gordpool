package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"

	"gordpool/pkg/planner"
)

var t0 = time.Date(2025, 12, 8, 0, 0, 0, 0, time.UTC)

func hourly(prices ...float64) []planner.PriceSlot {
	out := make([]planner.PriceSlot, len(prices))
	for i, p := range prices {
		out[i] = planner.PriceSlot{Timestamp: t0.Add(time.Duration(i) * time.Hour), Price: p}
	}
	return out
}

type call struct{ Area, Market, Currency string }

// newTestServer returns a server whose price source records its calls.
func newTestServer(prices []planner.PriceSlot, err error) (*Server, *[]call) {
	var calls []call
	s := &Server{
		Prices: func(_ context.Context, area, market, currency string) ([]planner.PriceSlot, error) {
			calls = append(calls, call{area, market, currency})
			return prices, err
		},
		Now: func() time.Time { return t0 },
	}
	return s, &calls
}

func do(c *qt.C, h http.Handler, method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	var out map[string]any
	if rec.Body.Len() > 0 && strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		c.Assert(json.Unmarshal(rec.Body.Bytes(), &out), qt.IsNil)
	}
	return rec, out
}

func TestOptimizePostWithFetchedPrices(t *testing.T) {
	c := qt.New(t)
	s, calls := newTestServer(hourly(20, 2, 3, 25, 30, 10), nil)

	rec, _ := do(c, s.Handler(), http.MethodPost, "/v1/battery/optimize", `{
		"area": "EE",
		"battery": {
			"capacity_kwh": 10, "max_charge_kw": 10, "max_discharge_kw": 10,
			"initial_soc_percent": 10, "round_trip_efficiency": 1,
			"stored_energy_value_cents": 0
		}
	}`)
	c.Assert(rec.Code, qt.Equals, http.StatusOK, qt.Commentf("%s", rec.Body))
	c.Assert(*calls, qt.DeepEquals, []call{{"EE", "DayAhead", "EUR"}})

	var resp OptimizeResponse
	c.Assert(json.Unmarshal(rec.Body.Bytes(), &resp), qt.IsNil)
	c.Assert(resp.Area, qt.Equals, "EE")
	c.Assert(resp.Prices, qt.HasLen, 6)
	c.Assert(resp.Slots, qt.HasLen, 6)
	c.Assert(resp.Slots[1].Action, qt.Equals, "charge")
	c.Assert(resp.Slots[4].Action, qt.Equals, "discharge")
	c.Assert(resp.Summary.NetBenefitCents, qt.Equals, 8*28.0)
	// defaults applied and echoed back
	c.Assert(resp.Battery.MinSoCPercent, qt.Equals, float64(planner.DefaultMinSoCPercent))
}

func TestOptimizePostWithInlinePrices(t *testing.T) {
	c := qt.New(t)
	s, calls := newTestServer(nil, errors.New("must not be called"))

	rec, out := do(c, s.Handler(), http.MethodPost, "/v1/battery/optimize", `{
		"battery": {"capacity_kwh": 5, "initial_soc_percent": 50},
		"now": "2025-12-08T01:00:00Z",
		"prices": [
			{"timestamp": "2025-12-08T00:00:00Z", "price": 1},
			{"timestamp": "2025-12-08T01:00:00Z", "price": 2},
			{"timestamp": "2025-12-08T02:00:00Z", "price": 3}
		]
	}`)
	c.Assert(rec.Code, qt.Equals, http.StatusOK, qt.Commentf("%v", out))
	c.Assert(*calls, qt.HasLen, 0)
	c.Assert(out["slots"], qt.HasLen, 2) // the 00:00 slot is in the past
}

func TestOptimizeGetQuery(t *testing.T) {
	c := qt.New(t)
	s, calls := newTestServer(hourly(1, 50, 1, 50), nil)

	rec, out := do(c, s.Handler(), http.MethodGet,
		"/v1/battery/optimize?area=LT&capacity_kwh=10&max_charge_kw=10&max_discharge_kw=10&initial_soc_percent=10&max_cycles_per_day=6&stored_energy_value_cents=0", "")
	c.Assert(rec.Code, qt.Equals, http.StatusOK, qt.Commentf("%v", out))
	c.Assert(*calls, qt.DeepEquals, []call{{"LT", "DayAhead", "EUR"}})
	// 4h * 6/24 = one cycle allowed
	summary := out["summary"].(map[string]any)
	c.Assert(summary["equivalent_cycles"], qt.Equals, 1.0)
}

func TestOptimizeErrors(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		target    string
		body      string
		sourceErr error
		code      int
		err       string
	}{{
		name:   "method not allowed",
		method: http.MethodDelete, target: "/v1/battery/optimize",
		code: http.StatusMethodNotAllowed, err: "method DELETE not allowed",
	}, {
		name:   "bad json",
		method: http.MethodPost, target: "/v1/battery/optimize", body: `{`,
		code: http.StatusBadRequest, err: "invalid JSON body.*",
	}, {
		name:   "unknown field",
		method: http.MethodPost, target: "/v1/battery/optimize", body: `{"battery":{"capacity_kwh":1},"foo":1}`,
		code: http.StatusBadRequest, err: `invalid JSON body: json: unknown field "foo"`,
	}, {
		name:   "invalid battery",
		method: http.MethodPost, target: "/v1/battery/optimize", body: `{"battery":{}}`,
		code: http.StatusBadRequest, err: "capacity_kwh must be > 0",
	}, {
		name:   "bad query number",
		method: http.MethodGet, target: "/v1/battery/optimize?capacity_kwh=abc",
		code: http.StatusBadRequest, err: "capacity_kwh: .*",
	}, {
		name:   "upstream failure",
		method: http.MethodGet, target: "/v1/battery/optimize?capacity_kwh=10",
		sourceErr: errors.New("nordpool down"),
		code:      http.StatusBadGateway, err: "fetch prices: nordpool down",
	}, {
		name:   "schedule negative hours",
		method: http.MethodGet, target: "/v1/battery/schedule?max_charge_hours=-1",
		code: http.StatusBadRequest, err: "max hours must be >= 0",
	}}
	for _, test := range tests {
		qt.New(t).Run(test.name, func(c *qt.C) {
			s, _ := newTestServer(hourly(1, 2), test.sourceErr)
			rec, out := do(c, s.Handler(), test.method, test.target, test.body)
			c.Assert(rec.Code, qt.Equals, test.code)
			c.Assert(out["error"], qt.Matches, test.err)
		})
	}
}

func TestNoPriceSource(t *testing.T) {
	c := qt.New(t)
	s := &Server{}
	rec, out := do(c, s.Handler(), http.MethodGet, "/v1/battery/optimize?capacity_kwh=10", "")
	c.Assert(rec.Code, qt.Equals, http.StatusBadRequest)
	c.Assert(out["error"], qt.Matches, "no price source configured.*")
}

func TestLegacySchedule(t *testing.T) {
	c := qt.New(t)
	prices := hourly(20, 2, 3, 25, 30, 10)
	s, _ := newTestServer(prices, nil)

	rec, _ := do(c, s.Handler(), http.MethodGet,
		"/v1/battery/schedule?max_charge_hours=2&max_discharge_hours=1&last_price_charged=15&epsilon=2", "")
	c.Assert(rec.Code, qt.Equals, http.StatusOK)

	var got planner.ScheduleJSON
	c.Assert(json.Unmarshal(rec.Body.Bytes(), &got), qt.IsNil)
	want := planner.BuildBatterySchedule(prices, planner.BatteryStrategyParams{
		Area: "LV", Market: "DayAhead", Currency: "EUR",
		MaxChargeHours: 2, MaxDischargeHours: 1, LastPriceCharged: 15, Epsilon: 2,
	}, t0)
	c.Assert(got, qt.DeepEquals, want)
}

func TestHealthz(t *testing.T) {
	c := qt.New(t)
	rec, _ := do(c, (&Server{}).Handler(), http.MethodGet, "/healthz", "")
	c.Assert(rec.Code, qt.Equals, http.StatusOK)
	c.Assert(rec.Body.String(), qt.Equals, "ok")
}

func TestSolaxEndpoint(t *testing.T) {
	c := qt.New(t)
	s, _ := newTestServer(hourly(20, 2, 3, 25, 30, 10), nil)

	rec, _ := do(c, s.Handler(), http.MethodPost, "/v1/battery/solax", `{
		"timezone": "Europe/Riga",
		"inverters": 2,
		"battery": {
			"capacity_kwh": 10, "max_charge_kw": 10, "max_discharge_kw": 10,
			"initial_soc_percent": 10, "round_trip_efficiency": 1,
			"stored_energy_value_cents": 0
		}
	}`)
	c.Assert(rec.Code, qt.Equals, http.StatusOK, qt.Commentf("%s", rec.Body))

	var out planner.SolaxDataHubPlan
	c.Assert(json.Unmarshal(rec.Body.Bytes(), &out), qt.IsNil)
	c.Assert(out.Format, qt.Equals, planner.SolaxFormat)
	c.Assert(out.Windows, qt.HasLen, 2)
	c.Assert(out.Windows[0].SmartScene, qt.Equals, "IF Date & Time 2025-12-08 03:00-04:00 THEN Inverter Charge as 8.0 kW till Battery SOC 90 %")
	c.Assert(out.Windows[1].Modbus.PerInverterPowerW, qt.Equals, -4000)
}

func TestSolaxEndpointQueryAndErrors(t *testing.T) {
	c := qt.New(t)
	s, _ := newTestServer(hourly(1, 50), nil)

	rec, out := do(c, s.Handler(), http.MethodGet, "/v1/battery/solax?capacity_kwh=10&timezone=Europe/Vilnius&inverters=3", "")
	c.Assert(rec.Code, qt.Equals, http.StatusOK, qt.Commentf("%v", out))
	c.Assert(out["timezone"], qt.Equals, "Europe/Vilnius")
	c.Assert(out["inverters"], qt.Equals, 3.0)

	rec, out = do(c, s.Handler(), http.MethodGet, "/v1/battery/solax?capacity_kwh=10&timezone=Mars/Base", "")
	c.Assert(rec.Code, qt.Equals, http.StatusBadRequest)
	c.Assert(out["error"], qt.Matches, "timezone: .*")

	rec, out = do(c, s.Handler(), http.MethodGet, "/v1/battery/solax?capacity_kwh=10&inverters=x", "")
	c.Assert(rec.Code, qt.Equals, http.StatusBadRequest)
	c.Assert(out["error"], qt.Matches, "inverters: .*")

	rec, out = do(c, s.Handler(), http.MethodGet, "/v1/battery/solax?capacity_kwh=10&min_power_kw=x", "")
	c.Assert(rec.Code, qt.Equals, http.StatusBadRequest)
	c.Assert(out["error"], qt.Matches, "min_power_kw: .*")
}
