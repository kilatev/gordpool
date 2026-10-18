package planner

import (
	"math"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	qt "github.com/frankban/quicktest"
)

var t0 = time.Date(2025, 12, 8, 0, 0, 0, 0, time.UTC)

func hourly(prices ...float64) []PriceSlot {
	out := make([]PriceSlot, len(prices))
	for i, p := range prices {
		out[i] = PriceSlot{Timestamp: t0.Add(time.Duration(i) * time.Hour), Price: p}
	}
	return out
}

func ptr(f float64) *float64 { return &f }

// Ideal 10 kWh battery with 1C power so a full window move fits in one hour.
func idealSpec() BatterySpec {
	return BatterySpec{
		CapacityKWh:            10,
		MaxChargeKW:            10,
		MaxDischargeKW:         10,
		InitialSoCPercent:      10,
		MinSoCPercent:          10,
		MaxSoCPercent:          90,
		RoundTripEfficiency:    1,
		StoredEnergyValueCents: ptr(0),
	}
}

func actions(o OptimizedScheduleJSON) []string {
	out := make([]string, len(o.Slots))
	for i, s := range o.Slots {
		out[i] = s.Action
	}
	return out
}

func TestOptimizeBatteryArbitrage(t *testing.T) {
	c := qt.New(t)
	plan, err := OptimizeBattery(hourly(20, 2, 3, 25, 30, 10), "LV", idealSpec(), t0)
	c.Assert(err, qt.IsNil)
	c.Assert(actions(plan), qt.DeepEquals, []string{"idle", "charge", "idle", "idle", "discharge", "idle"})
	c.Assert(plan.Slots[1].SoCEndPct, qt.Equals, 90.0)
	c.Assert(plan.Slots[4].SoCEndPct, qt.Equals, 10.0)
	c.Assert(plan.Summary.GridImportKWh, qt.Equals, 8.0)
	c.Assert(plan.Summary.GridExportKWh, qt.Equals, 8.0)
	c.Assert(plan.Summary.EquivalentCycles, qt.Equals, 1.0)
	// bought 8 kWh at 2c, sold at 30c
	c.Assert(plan.Summary.NetBenefitCents, qt.Equals, 8*(30-2.0))
	c.Assert(plan.ChargeIntervals, qt.HasLen, 1)
	c.Assert(plan.DischargeIntervals, qt.HasLen, 1)
	c.Assert(plan.DischargeIntervals[0].Start, qt.Equals, t0.Add(4*time.Hour).Format(time.RFC3339))
}

func TestOptimizeBatteryStaysIdle(t *testing.T) {
	tests := []struct {
		name   string
		prices []float64
		tweak  func(*BatterySpec)
	}{{
		name:   "flat prices",
		prices: []float64{10, 10, 10, 10},
	}, {
		name:   "spread does not pay for wear",
		prices: []float64{10, 12, 10, 12},
		tweak:  func(b *BatterySpec) { b.DegradationCostCents = 3 },
	}, {
		name:   "spread eaten by efficiency losses",
		prices: []float64{10, 10.5, 10, 10.5},
		tweak:  func(b *BatterySpec) { b.RoundTripEfficiency = 0.9 },
	}, {
		name:   "falling prices",
		prices: []float64{30, 20, 10, 5},
	}}
	for _, test := range tests {
		qt.New(t).Run(test.name, func(c *qt.C) {
			spec := idealSpec()
			if test.tweak != nil {
				test.tweak(&spec)
			}
			plan, err := OptimizeBattery(hourly(test.prices...), "LV", spec, t0)
			c.Assert(err, qt.IsNil)
			for _, s := range plan.Slots {
				c.Assert(s.Action, qt.Equals, "idle", qt.Commentf("slot %s", s.Timestamp))
			}
			c.Assert(plan.Summary.NetBenefitCents, qt.Equals, 0.0)
		})
	}
}

func TestOptimizeBatteryRespectsPowerLimit(t *testing.T) {
	c := qt.New(t)
	spec := idealSpec()
	spec.MaxChargeKW = 4 // 40% per hour → needs two cheap hours to fill
	spec.MaxDischargeKW = 4
	plan, err := OptimizeBattery(hourly(1, 2, 3, 40, 39, 38), "LV", spec, t0)
	c.Assert(err, qt.IsNil)
	c.Assert(actions(plan), qt.DeepEquals, []string{"charge", "charge", "idle", "discharge", "discharge", "idle"})
	for _, s := range plan.Slots {
		c.Assert(math.Abs(s.PowerKW) <= 4+1e-9, qt.IsTrue)
	}
}

func TestOptimizeBatteryCycleCap(t *testing.T) {
	c := qt.New(t)
	// Two profitable cycles available within 6 hours.
	prices := hourly(1, 50, 1, 50, 10, 10)
	spec := idealSpec()

	free, err := OptimizeBattery(prices, "LV", spec, t0)
	c.Assert(err, qt.IsNil)
	c.Assert(free.Summary.EquivalentCycles, qt.Equals, 2.0)

	// 6h horizon * 4 cycles/day = 1 cycle allowed.
	spec.MaxCyclesPerDay = 4
	capped, err := OptimizeBattery(prices, "LV", spec, t0)
	c.Assert(err, qt.IsNil)
	c.Assert(capped.Summary.EquivalentCycles <= 1+1e-9, qt.IsTrue, qt.Commentf("cycles %v", capped.Summary.EquivalentCycles))
	c.Assert(capped.Summary.NetBenefitCents, qt.Equals, 8*49.0)
}

func TestOptimizeBatteryInitialSoCOutsideWindow(t *testing.T) {
	c := qt.New(t)
	spec := idealSpec()
	spec.InitialSoCPercent = 98
	spec.MaxChargeKW = 1 // 10% per hour
	// Prices that would tempt to charge: battery must not go above 98%.
	plan, err := OptimizeBattery(hourly(1, 1, 1, 1), "LV", spec, t0)
	c.Assert(err, qt.IsNil)
	for _, s := range plan.Slots {
		c.Assert(s.SoCEndPct <= 98, qt.IsTrue)
		c.Assert(s.Action, qt.Not(qt.Equals), "charge")
	}
}

func TestOptimizeBatteryIgnoresPastAndEmpty(t *testing.T) {
	c := qt.New(t)
	prices := hourly(1, 50, 1, 50)
	plan, err := OptimizeBattery(prices, "LV", idealSpec(), t0.Add(10*time.Hour))
	c.Assert(err, qt.IsNil)
	c.Assert(plan.Slots, qt.HasLen, 0)
	c.Assert(plan.ResolutionMinutes, qt.IsNil)

	plan, err = OptimizeBattery(prices, "LV", idealSpec(), t0.Add(2*time.Hour))
	c.Assert(err, qt.IsNil)
	c.Assert(plan.Slots, qt.HasLen, 2)
	c.Assert(plan.Slots[0].Timestamp, qt.Equals, prices[2].Timestamp.Format(time.RFC3339))
}

func TestOptimizeBatteryQuarterHourResolution(t *testing.T) {
	c := qt.New(t)
	var prices []PriceSlot
	for i := 0; i < 8; i++ {
		p := float64(i + 1)
		if i >= 4 {
			p = 40
		}
		prices = append(prices, PriceSlot{Timestamp: t0.Add(time.Duration(i) * 15 * time.Minute), Price: p})
	}
	spec := idealSpec() // 10 kW for 15 min = 2.5 kWh = 25%
	plan, err := OptimizeBattery(prices, "LV", spec, t0)
	c.Assert(err, qt.IsNil)
	c.Assert(*plan.ResolutionMinutes, qt.Equals, 15)
	c.Assert(plan.Slots[0].PowerKW, qt.Equals, 10.0)
	c.Assert(plan.Slots[0].SoCEndPct-plan.Slots[0].SoCStartPct, qt.Equals, 25.0)
}

func TestBatterySpecValidate(t *testing.T) {
	tests := []struct {
		name  string
		tweak func(*BatterySpec)
		err   string
	}{
		{"zero capacity", func(b *BatterySpec) { b.CapacityKWh = 0 }, "capacity_kwh.*"},
		{"negative power", func(b *BatterySpec) { b.MaxChargeKW = -1 }, "max_charge_kw.*"},
		{"inverted window", func(b *BatterySpec) { b.MinSoCPercent, b.MaxSoCPercent = 80, 20 }, "soc window.*"},
		{"initial soc", func(b *BatterySpec) { b.InitialSoCPercent = 120 }, "initial_soc_percent.*"},
		{"efficiency", func(b *BatterySpec) { b.RoundTripEfficiency = 1.5 }, "round_trip_efficiency.*"},
		{"degradation", func(b *BatterySpec) { b.DegradationCostCents = -1 }, "degradation_cost_cents.*"},
		{"cycles", func(b *BatterySpec) { b.MaxCyclesPerDay = -1 }, "max_cycles_per_day.*"},
	}
	for _, test := range tests {
		qt.New(t).Run(test.name, func(c *qt.C) {
			spec := idealSpec()
			test.tweak(&spec)
			_, err := OptimizeBattery(hourly(1, 2), "LV", spec, t0)
			c.Assert(err, qt.ErrorMatches, test.err)
		})
	}
}

func TestBatterySpecDefaults(t *testing.T) {
	c := qt.New(t)
	b := BatterySpec{CapacityKWh: 10}.WithDefaults()
	c.Assert(b.MinSoCPercent, qt.Equals, float64(DefaultMinSoCPercent))
	c.Assert(b.MaxSoCPercent, qt.Equals, float64(DefaultMaxSoCPercent))
	c.Assert(b.RoundTripEfficiency, qt.Equals, DefaultRoundTripEfficiency)
	c.Assert(b.MaxChargeKW, qt.Equals, 5.0)
	c.Assert(b.MaxDischargeKW, qt.Equals, 5.0)
}

func TestToScheduleReusesLegacyFormat(t *testing.T) {
	c := qt.New(t)
	plan, err := OptimizeBattery(hourly(20, 2, 3, 25, 30, 10), "LV", idealSpec(), t0)
	c.Assert(err, qt.IsNil)
	s := plan.ToSchedule()
	c.Assert(s.Area, qt.Equals, "LV")
	c.Assert(s.ChargeSlots, qt.DeepEquals, []SlotJSON{{Timestamp: t0.Add(time.Hour).Format(time.RFC3339), Price: 2}})
	c.Assert(s.DischargeSlots, qt.DeepEquals, []SlotJSON{{Timestamp: t0.Add(4 * time.Hour).Format(time.RFC3339), Price: 30}})
	c.Assert(s.ChargeIntervals, qt.DeepEquals, plan.ChargeIntervals)
}

// ---------- property-based tests (testing/quick) ----------

// scenario is a random price curve plus a random but valid battery.
type scenario struct {
	Prices []PriceSlot
	Spec   BatterySpec
}

func (scenario) Generate(r *rand.Rand, _ int) reflect.Value {
	n := 1 + r.Intn(48)
	res := []time.Duration{15 * time.Minute, time.Hour}[r.Intn(2)]
	prices := make([]PriceSlot, n)
	for i := range prices {
		prices[i] = PriceSlot{
			Timestamp: t0.Add(time.Duration(i) * res),
			Price:     r.Float64()*60 - 10, // includes negative prices
		}
	}
	lo := r.Float64() * 40
	hi := lo + 10 + r.Float64()*(100-lo-10)
	spec := BatterySpec{
		CapacityKWh:          1 + r.Float64()*20,
		InitialSoCPercent:    r.Float64() * 100,
		MinSoCPercent:        lo,
		MaxSoCPercent:        hi,
		RoundTripEfficiency:  0.7 + r.Float64()*0.3,
		DegradationCostCents: r.Float64() * 5,
	}
	spec.MaxChargeKW = spec.CapacityKWh * (0.1 + r.Float64())
	spec.MaxDischargeKW = spec.CapacityKWh * (0.1 + r.Float64())
	if r.Intn(2) == 0 {
		spec.MaxCyclesPerDay = r.Float64() * 3
	}
	if r.Intn(2) == 0 {
		spec.StoredEnergyValueCents = ptr(r.Float64() * 30)
	}
	return reflect.ValueOf(scenario{Prices: prices, Spec: spec})
}

// invariant checks one property of a plan produced for a random scenario.
type invariant struct {
	name  string
	holds func(scenario, OptimizedScheduleJSON) bool
}

var invariants = []invariant{{
	name: "SoC stays in window or only moves towards it",
	holds: func(s scenario, plan OptimizedScheduleJSON) bool {
		lo, hi := math.Ceil(s.Spec.MinSoCPercent-1e-9), math.Floor(s.Spec.MaxSoCPercent+1e-9)
		for _, sl := range plan.Slots {
			start, end := sl.SoCStartPct, sl.SoCEndPct
			inside := end >= lo && end <= hi
			towards := (start < lo && end >= start) || (start > hi && end <= start)
			if !inside && !towards && start != end {
				return false
			}
			if end < 0 || end > 100 {
				return false
			}
		}
		return true
	},
}, {
	name: "power within limits",
	holds: func(s scenario, plan OptimizedScheduleJSON) bool {
		for _, sl := range plan.Slots {
			if sl.PowerKW > s.Spec.MaxChargeKW+1e-6 || -sl.PowerKW > s.Spec.MaxDischargeKW+1e-6 {
				return false
			}
		}
		return true
	},
}, {
	name: "never worse than leaving the battery idle",
	holds: func(_ scenario, plan OptimizedScheduleJSON) bool {
		return plan.Summary.NetBenefitCents >= -1e-3
	},
}, {
	name: "cycle cap respected",
	holds: func(s scenario, plan OptimizedScheduleJSON) bool {
		if s.Spec.MaxCyclesPerDay == 0 {
			return true
		}
		hours := float64(len(plan.Slots)) * float64(*plan.ResolutionMinutes) / 60
		return plan.Summary.EquivalentCycles <= s.Spec.MaxCyclesPerDay*hours/24+1e-3
	},
}, {
	name: "SoC chain, action and grid flow are consistent",
	holds: func(s scenario, plan OptimizedScheduleJSON) bool {
		if plan.Slots[0].SoCStartPct != math.Round(s.Spec.InitialSoCPercent) {
			return false
		}
		for i, sl := range plan.Slots {
			if i > 0 && sl.SoCStartPct != plan.Slots[i-1].SoCEndPct {
				return false
			}
			want := "idle"
			if sl.SoCEndPct > sl.SoCStartPct {
				want = "charge"
			} else if sl.SoCEndPct < sl.SoCStartPct {
				want = "discharge"
			}
			if sl.Action != want {
				return false
			}
			// import only when charging, export only when discharging
			if (sl.Action == "charge") != (sl.GridKWh > 0) || (sl.Action == "discharge") != (sl.GridKWh < 0) {
				return false
			}
		}
		return true
	},
}}

func TestPropertyOptimizeBattery(t *testing.T) {
	c := qt.New(t)
	prop := func(s scenario) bool {
		plan, err := OptimizeBattery(s.Prices, "LV", s.Spec, t0)
		if err != nil {
			t.Logf("unexpected error %v for %+v", err, s.Spec)
			return false
		}
		ok := true
		for _, inv := range invariants {
			if !inv.holds(s, plan) {
				t.Logf("invariant %q violated for %+v", inv.name, s.Spec)
				ok = false
			}
		}
		return ok
	}
	c.Assert(quick.Check(prop, &quick.Config{MaxCount: 300, Rand: rand.New(rand.NewSource(1))}), qt.IsNil)
}

// pathCost is the objective the DP minimises, evaluated for an arbitrary path.
func pathCost(m *batteryModel, path []int) float64 {
	c := 0.0
	for t := 0; t+1 < len(path); t++ {
		c += m.stepCost(t, path[t+1]-path[t])
	}
	return c - m.storedValue*float64(path[len(path)-1])*m.stepKWh
}

// randomPath walks random feasible moves, respecting SoC, power and cycle limits.
func randomPath(m *batteryModel, r *rand.Rand) []int {
	path := []int{m.startLvl}
	used := 0
	for t := range m.prices {
		a := path[t]
		lo, hi := m.reach(a)
		b := lo + r.Intn(hi-lo+1)
		if d := a - b; d > 0 && m.dischargeBudget >= 0 && used+d > m.dischargeBudget {
			b = a
		}
		if b < a {
			used += a - b
		}
		path = append(path, b)
	}
	return path
}

func TestPropertyNoRandomPlanBeatsOptimizer(t *testing.T) {
	c := qt.New(t)
	r := rand.New(rand.NewSource(2))
	prop := func(s scenario) bool {
		future := s.Prices
		m := newBatteryModel(s.Spec.WithDefaults(), future, inferResolutionMinutes(future))
		best := pathCost(m, m.solve())
		for i := 0; i < 50; i++ {
			if p := randomPath(m, r); pathCost(m, p) < best-1e-6 {
				t.Logf("random path %v beats optimum: %v < %v", p, pathCost(m, p), best)
				return false
			}
		}
		return true
	}
	c.Assert(quick.Check(prop, &quick.Config{MaxCount: 100, Rand: rand.New(rand.NewSource(3))}), qt.IsNil)
}
