package planner

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

// BatterySpec describes the physical battery and how gently it must be treated.
// Energy is in kWh, power in kW, prices/costs in cents/kWh.
type BatterySpec struct {
	CapacityKWh       float64 `json:"capacity_kwh"`
	MaxChargeKW       float64 `json:"max_charge_kw"`
	MaxDischargeKW    float64 `json:"max_discharge_kw"`
	InitialSoCPercent float64 `json:"initial_soc_percent"`
	// SoC window the optimizer keeps the battery in. Staying away from 0% and
	// 100% is the main thing that slows down lithium cell wear.
	MinSoCPercent float64 `json:"min_soc_percent"`
	MaxSoCPercent float64 `json:"max_soc_percent"`
	// RoundTripEfficiency is AC→battery→AC efficiency in (0, 1].
	RoundTripEfficiency float64 `json:"round_trip_efficiency"`
	// DegradationCostCents is the wear cost per kWh discharged from the battery,
	// e.g. battery price / (rated cycles * usable capacity). Prevents cycling for
	// spreads that do not pay for the wear.
	DegradationCostCents float64 `json:"degradation_cost_cents"`
	// MaxCyclesPerDay caps equivalent full cycles (discharged kWh / usable kWh).
	// Zero means no cap.
	MaxCyclesPerDay float64 `json:"max_cycles_per_day"`
	// StoredEnergyValueCents is the value of 1 kWh left in the battery at the end
	// of the horizon. Nil means the median horizon price.
	StoredEnergyValueCents *float64 `json:"stored_energy_value_cents,omitempty"`
}

const (
	DefaultMinSoCPercent       = 10
	DefaultMaxSoCPercent       = 90
	DefaultRoundTripEfficiency = 0.9

	// socGridSteps is the number of SoC levels between 0% and 100% used by the DP.
	socGridSteps = 100
)

// WithDefaults fills unset optional fields with battery-friendly defaults.
func (b BatterySpec) WithDefaults() BatterySpec {
	if b.MinSoCPercent == 0 && b.MaxSoCPercent == 0 {
		b.MinSoCPercent = DefaultMinSoCPercent
		b.MaxSoCPercent = DefaultMaxSoCPercent
	}
	if b.RoundTripEfficiency == 0 {
		b.RoundTripEfficiency = DefaultRoundTripEfficiency
	}
	// 0.5C is a gentle default rate for home batteries.
	if b.MaxChargeKW == 0 {
		b.MaxChargeKW = b.CapacityKWh / 2
	}
	if b.MaxDischargeKW == 0 {
		b.MaxDischargeKW = b.CapacityKWh / 2
	}
	return b
}

// Validate reports the first invalid field.
func (b BatterySpec) Validate() error {
	switch {
	case !(b.CapacityKWh > 0):
		return errors.New("capacity_kwh must be > 0")
	case b.MaxChargeKW < 0 || b.MaxDischargeKW < 0:
		return errors.New("max_charge_kw and max_discharge_kw must be >= 0")
	case b.MinSoCPercent < 0 || b.MaxSoCPercent > 100 || b.MinSoCPercent >= b.MaxSoCPercent:
		return errors.New("soc window must satisfy 0 <= min_soc_percent < max_soc_percent <= 100")
	case b.InitialSoCPercent < 0 || b.InitialSoCPercent > 100:
		return errors.New("initial_soc_percent must be within [0, 100]")
	case !(b.RoundTripEfficiency > 0) || b.RoundTripEfficiency > 1:
		return errors.New("round_trip_efficiency must be within (0, 1]")
	case b.DegradationCostCents < 0:
		return errors.New("degradation_cost_cents must be >= 0")
	case b.MaxCyclesPerDay < 0:
		return errors.New("max_cycles_per_day must be >= 0")
	}
	return nil
}

type OptimizedSlotJSON struct {
	Timestamp string  `json:"timestamp"`
	Price     float64 `json:"price"`
	Action    string  `json:"action"` // "charge", "discharge" or "idle"
	// PowerKW is the battery-side power: positive charges, negative discharges.
	PowerKW float64 `json:"power_kw"`
	// GridKWh is energy exchanged with the grid: positive import, negative export.
	GridKWh        float64 `json:"grid_kwh"`
	SoCStartPct    float64 `json:"soc_start_percent"`
	SoCEndPct      float64 `json:"soc_end_percent"`
	EnergyCostCent float64 `json:"energy_cost_cents"`
}

type OptimizationSummaryJSON struct {
	GridImportKWh        float64 `json:"grid_import_kwh"`
	GridExportKWh        float64 `json:"grid_export_kwh"`
	EquivalentCycles     float64 `json:"equivalent_cycles"`
	EnergyCostCents      float64 `json:"energy_cost_cents"` // import cost minus export revenue
	DegradationCostCents float64 `json:"degradation_cost_cents"`
	StoredValueDeltaCent float64 `json:"stored_value_delta_cents"`
	// NetBenefitCents compares the plan to leaving the battery idle:
	// -(energy cost + degradation) + value of the change in stored energy.
	NetBenefitCents        float64 `json:"net_benefit_cents"`
	StoredEnergyValueCents float64 `json:"stored_energy_value_cents"`
}

type OptimizedScheduleJSON struct {
	Area               string                  `json:"area"`
	ResolutionMinutes  *int                    `json:"resolution_minutes"`
	Battery            BatterySpec             `json:"battery"`
	Slots              []OptimizedSlotJSON     `json:"slots"`
	ChargeIntervals    []IntervalJSON          `json:"charge_intervals"`
	DischargeIntervals []IntervalJSON          `json:"discharge_intervals"`
	Summary            OptimizationSummaryJSON `json:"summary"`
}

// ToSchedule converts the plan into the legacy ScheduleJSON so existing
// renderers (textchart, web UI) can display it.
func (o OptimizedScheduleJSON) ToSchedule() ScheduleJSON {
	s := ScheduleJSON{
		Area:               o.Area,
		ResolutionMinutes:  o.ResolutionMinutes,
		ChargeSlots:        []SlotJSON{},
		DischargeSlots:     []SlotJSON{},
		ChargeIntervals:    o.ChargeIntervals,
		DischargeIntervals: o.DischargeIntervals,
	}
	for _, sl := range o.Slots {
		switch sl.Action {
		case "charge":
			s.ChargeSlots = append(s.ChargeSlots, SlotJSON{Timestamp: sl.Timestamp, Price: sl.Price})
		case "discharge":
			s.DischargeSlots = append(s.DischargeSlots, SlotJSON{Timestamp: sl.Timestamp, Price: sl.Price})
		}
	}
	return s
}

// OptimizeBattery finds the charge/discharge plan for future slots that
// minimises electricity cost plus battery wear, while keeping SoC inside the
// configured window and respecting power and cycle limits.
//
// It runs a dynamic program over discretised SoC levels (1% of capacity), so
// the result is optimal for that grid rather than a greedy threshold choice.
func OptimizeBattery(prices []PriceSlot, area string, spec BatterySpec, now time.Time) (OptimizedScheduleJSON, error) {
	spec = spec.WithDefaults()
	if err := spec.Validate(); err != nil {
		return OptimizedScheduleJSON{}, err
	}

	var future []PriceSlot
	for _, p := range prices {
		if !p.Timestamp.Before(now) {
			future = append(future, p)
		}
	}
	sort.Slice(future, func(i, j int) bool { return future[i].Timestamp.Before(future[j].Timestamp) })

	out := OptimizedScheduleJSON{
		Area:               area,
		Battery:            spec,
		Slots:              []OptimizedSlotJSON{},
		ChargeIntervals:    []IntervalJSON{},
		DischargeIntervals: []IntervalJSON{},
	}
	if len(future) == 0 {
		return out, nil
	}

	resolution := inferResolutionMinutes(future)
	out.ResolutionMinutes = &resolution

	m := newBatteryModel(spec, future, resolution)
	path := m.solve()
	m.fill(&out, future, path)
	return out, nil
}

type batteryModel struct {
	spec        BatterySpec
	prices      []float64
	stepKWh     float64
	maxUp       int // SoC steps per slot when charging
	maxDown     int // SoC steps per slot when discharging
	minLvl      int
	maxLvl      int
	startLvl    int
	etaCharge   float64
	etaDisch    float64
	storedValue float64
	// dischargeBudget is the number of SoC steps that may be discharged over the
	// horizon (from MaxCyclesPerDay); -1 means unlimited.
	dischargeBudget int
}

func newBatteryModel(spec BatterySpec, slots []PriceSlot, resolution int) *batteryModel {
	dtH := float64(resolution) / 60
	step := spec.CapacityKWh / socGridSteps
	eta := math.Sqrt(spec.RoundTripEfficiency)

	prices := make([]float64, len(slots))
	for i, s := range slots {
		prices[i] = s.Price
	}

	m := &batteryModel{
		spec:            spec,
		prices:          prices,
		stepKWh:         step,
		maxUp:           int(math.Floor(spec.MaxChargeKW*dtH/step + 1e-9)),
		maxDown:         int(math.Floor(spec.MaxDischargeKW*dtH/step + 1e-9)),
		minLvl:          int(math.Ceil(spec.MinSoCPercent - 1e-9)),
		maxLvl:          int(math.Floor(spec.MaxSoCPercent + 1e-9)),
		startLvl:        int(math.Round(spec.InitialSoCPercent)),
		etaCharge:       eta,
		etaDisch:        eta,
		dischargeBudget: -1,
	}
	if spec.StoredEnergyValueCents != nil {
		m.storedValue = *spec.StoredEnergyValueCents
	} else {
		m.storedValue = median(prices)
	}
	if spec.MaxCyclesPerDay > 0 {
		hours := float64(len(slots)) * dtH
		cycles := spec.MaxCyclesPerDay * hours / 24
		m.dischargeBudget = int(math.Floor(cycles*float64(m.maxLvl-m.minLvl) + 1e-9))
	}
	return m
}

// reach returns the levels reachable from a in one slot. Inside the window
// anything within power limits goes; outside it the battery may only idle or
// move back towards the window, never further out.
func (m *batteryModel) reach(a int) (lo, hi int) {
	lo, hi = m.minLvl, m.maxLvl
	if a < m.minLvl {
		lo = a
	}
	if a > m.maxLvl {
		hi = a
	}
	return max(lo, a-m.maxDown, 0), min(hi, a+m.maxUp, socGridSteps)
}

// stepCost is the cost in cents of moving d SoC steps during slot t.
func (m *batteryModel) stepCost(t, d int) float64 {
	p := m.prices[t]
	e := float64(d) * m.stepKWh
	if d > 0 {
		return p * e / m.etaCharge
	}
	out := -e
	return -p*out*m.etaDisch + m.spec.DegradationCostCents*out
}

// tieBreak makes the DP prefer fewer and smaller moves when costs are equal,
// so the battery is not cycled for zero gain.
const tieBreak = 1e-9

// solve runs a backward dynamic program over (SoC level, discharged steps so
// far) and returns the SoC level at every slot boundary. The second dimension
// is only used when a cycle cap is set.
func (m *batteryModel) solve() []int {
	n := len(m.prices)
	levels := socGridSteps + 1
	budget := 1
	if m.dischargeBudget >= 0 {
		budget = m.dischargeBudget + 1
	}
	states := levels * budget
	inf := math.Inf(1)

	// cost[a*budget+u] = min cost from slot t to the end at level a having
	// already discharged u steps.
	cost := make([]float64, states)
	for a := 0; a < levels; a++ {
		for u := 0; u < budget; u++ {
			cost[a*budget+u] = -m.storedValue * float64(a) * m.stepKWh
		}
	}
	next := make([]float64, states)
	choice := make([][]int8, n)
	moveCost := make([]float64, m.maxDown+m.maxUp+1)

	for t := n - 1; t >= 0; t-- {
		for d := -m.maxDown; d <= m.maxUp; d++ {
			moveCost[d+m.maxDown] = m.stepCost(t, d) + tieBreak*math.Abs(float64(d))
		}
		// At most t*maxDown steps can have been discharged before slot t.
		maxU := budget - 1
		if m.dischargeBudget >= 0 {
			maxU = min(maxU, t*m.maxDown)
		}
		choice[t] = make([]int8, states)
		for a := 0; a < levels; a++ {
			lo, hi := m.reach(a)
			for u := 0; u <= maxU; u++ {
				best, bestD := inf, 0
				for b := lo; b <= hi; b++ {
					d := b - a
					v := u
					if d < 0 && m.dischargeBudget >= 0 {
						v = u - d
						if v >= budget {
							continue
						}
					}
					c := moveCost[d+m.maxDown] + cost[b*budget+v]
					if c < best {
						best, bestD = c, d
					}
				}
				next[a*budget+u] = best
				choice[t][a*budget+u] = int8(bestD)
			}
		}
		cost, next = next, cost
	}

	path := make([]int, n+1)
	path[0] = m.startLvl
	u := 0
	for t := 0; t < n; t++ {
		d := int(choice[t][path[t]*budget+u])
		path[t+1] = path[t] + d
		if d < 0 && m.dischargeBudget >= 0 {
			u -= d
		}
	}
	return path
}

func (m *batteryModel) cycles(path []int) float64 {
	usable := float64(m.maxLvl - m.minLvl)
	if usable <= 0 {
		return 0
	}
	down := 0
	for t := 1; t < len(path); t++ {
		if d := path[t] - path[t-1]; d < 0 {
			down -= d
		}
	}
	return float64(down) / usable
}

func (m *batteryModel) fill(out *OptimizedScheduleJSON, slots []PriceSlot, path []int) {
	resolution := *out.ResolutionMinutes
	dtH := float64(resolution) / 60

	var charge, discharge []PriceSlot
	var sum OptimizationSummaryJSON
	sum.StoredEnergyValueCents = m.storedValue
	for t, s := range slots {
		d := path[t+1] - path[t]
		e := float64(d) * m.stepKWh
		slot := OptimizedSlotJSON{
			Timestamp:   s.Timestamp.Format(time.RFC3339),
			Price:       s.Price,
			Action:      "idle",
			PowerKW:     round(e/dtH, 4),
			SoCStartPct: float64(path[t]),
			SoCEndPct:   float64(path[t+1]),
		}
		switch {
		case d > 0:
			slot.Action = "charge"
			slot.GridKWh = e / m.etaCharge
			sum.GridImportKWh += slot.GridKWh
			charge = append(charge, s)
		case d < 0:
			slot.Action = "discharge"
			slot.GridKWh = e * m.etaDisch
			sum.GridExportKWh -= slot.GridKWh
			sum.DegradationCostCents += -e * m.spec.DegradationCostCents
			discharge = append(discharge, s)
		}
		cost := s.Price * slot.GridKWh
		sum.EnergyCostCents += cost
		slot.EnergyCostCent = round(cost, 4)
		slot.GridKWh = round(slot.GridKWh, 4)
		out.Slots = append(out.Slots, slot)
	}

	delta := float64(path[len(path)-1]-path[0]) * m.stepKWh
	sum.StoredValueDeltaCent = delta * m.storedValue
	sum.EquivalentCycles = m.cycles(path)
	sum.NetBenefitCents = -sum.EnergyCostCents - sum.DegradationCostCents + sum.StoredValueDeltaCent

	sum.GridImportKWh = round(sum.GridImportKWh, 4)
	sum.GridExportKWh = round(sum.GridExportKWh, 4)
	sum.EquivalentCycles = round(sum.EquivalentCycles, 4)
	sum.EnergyCostCents = round(sum.EnergyCostCents, 4)
	sum.DegradationCostCents = round(sum.DegradationCostCents, 4)
	sum.StoredValueDeltaCent = round(sum.StoredValueDeltaCent, 4)
	sum.NetBenefitCents = round(sum.NetBenefitCents, 4)
	out.Summary = sum

	if iv := groupConsecutiveSlots(charge, resolution); iv != nil {
		out.ChargeIntervals = iv
	}
	if iv := groupConsecutiveSlots(discharge, resolution); iv != nil {
		out.DischargeIntervals = iv
	}
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	mid := len(s) / 2
	if len(s)%2 == 0 {
		return (s[mid-1] + s[mid]) / 2
	}
	return s[mid]
}

func round(x float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.Round(x*p) / p
}

// String gives a short human description, handy in logs and the TUI.
func (s OptimizationSummaryJSON) String() string {
	return fmt.Sprintf("import %.2f kWh, export %.2f kWh, %.2f cycles, net benefit %.2f c",
		s.GridImportKWh, s.GridExportKWh, s.EquivalentCycles, s.NetBenefitCents)
}
