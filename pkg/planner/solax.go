package planner

import (
	"fmt"
	"math"
	"time"
)

// SolaxFormat identifies the recommendation layout produced by ToSolaxDataHub.
const SolaxFormat = "solax-datahub1000/v1"

// SolaxOptions tunes the DataHub 1000 recommendation.
type SolaxOptions struct {
	// Location is the site time zone; DataHub Smart Scene times are local.
	Location *time.Location
	// Inverters is the number of parallel inverters behind the DataHub; the
	// site power is split evenly between them. Zero means 1.
	Inverters int
	// MinPowerKW drops slots whose site power is below it: tiny top-up moves
	// make noisy Smart Scene rules, and the SoC targets of the remaining
	// windows still steer the battery to the planned level.
	MinPowerKW float64
}

// SolaxModbusCommand is the "Enabled Power Control" remote-control command an
// EMS writes to each inverter for the window. Positive power charges the
// battery, negative discharges it (SolaX remote-control sign convention).
type SolaxModbusCommand struct {
	Mode              string `json:"mode"`
	ActivePowerW      int    `json:"active_power_w"`
	PerInverterPowerW int    `json:"per_inverter_power_w"`
	DurationS         int    `json:"duration_s"`
}

// SolaxWindow is one Smart Scene rule: between Start and End run the inverter
// in Action at PowerKW until the battery reaches TargetSoCPercent. Windows never
// cross local midnight, so each maps to a single-date Date & Time condition.
type SolaxWindow struct {
	Date             string  `json:"date"`        // local, YYYY-MM-DD
	StartLocal       string  `json:"start_local"` // HH:MM
	EndLocal         string  `json:"end_local"`   // HH:MM, may be "24:00"
	Start            string  `json:"start"`       // RFC3339 UTC
	End              string  `json:"end"`
	Action           string  `json:"action"` // "charge" or "discharge"
	PowerKW          float64 `json:"power_kw"`
	PerInverterKW    float64 `json:"per_inverter_kw"`
	EnergyKWh        float64 `json:"energy_kwh"` // AC energy for the window
	StartSoCPercent  float64 `json:"start_soc_percent"`
	TargetSoCPercent float64 `json:"target_soc_percent"`
	AvgPricePerMWh   float64 `json:"avg_price_per_mwh"`
	// SmartScene is the rule spelled the way the DataHub Smart Scene editor
	// phrases it, ready to copy into IF/THEN.
	SmartScene string             `json:"smart_scene"`
	Modbus     SolaxModbusCommand `json:"modbus"`
}

type SolaxDataHubPlan struct {
	Format    string                  `json:"format"`
	Area      string                  `json:"area"`
	Timezone  string                  `json:"timezone"`
	Inverters int                     `json:"inverters"`
	Windows   []SolaxWindow           `json:"windows"`
	Summary   OptimizationSummaryJSON `json:"summary"`
}

// ToSolaxDataHub turns the optimized plan into DataHub 1000 friendly windows:
// consecutive slots with the same action and power become one rule, with the
// SoC reached at its end as the "till Battery SOC" target.
func (o OptimizedScheduleJSON) ToSolaxDataHub(opts SolaxOptions) (SolaxDataHubPlan, error) {
	loc := opts.Location
	if loc == nil {
		loc = time.UTC
	}
	inverters := opts.Inverters
	if inverters <= 0 {
		inverters = 1
	}
	plan := SolaxDataHubPlan{
		Format:    SolaxFormat,
		Area:      o.Area,
		Timezone:  loc.String(),
		Inverters: inverters,
		Windows:   []SolaxWindow{},
		Summary:   o.Summary,
	}
	if o.ResolutionMinutes == nil || len(o.Slots) == 0 {
		return plan, nil
	}
	step := time.Duration(*o.ResolutionMinutes) * time.Minute
	dtH := step.Hours()

	// Cut active slots at local midnight (zones like +05:30 split even hourly
	// slots), then merge neighbouring pieces with the same action and power.
	type piece struct {
		start, end       time.Time
		action           string
		powerKW          float64 // signed AC power, rounded to 0.1 kW
		energy, priceSum float64 // priceSum is price weighted by slot fraction
		weight           float64
		socStart, socEnd float64
	}
	var pieces []piece
	for i, sl := range o.Slots {
		if sl.Action == "idle" {
			continue
		}
		ts, err := time.Parse(time.RFC3339, sl.Timestamp)
		if err != nil {
			return plan, fmt.Errorf("slot %d: %w", i, err)
		}
		// Inverter setpoints are AC side, so use grid energy, not battery energy.
		power := round(sl.GridKWh/dtH, 1)
		if math.Abs(power) < opts.MinPowerKW || power == 0 {
			continue
		}
		end := ts.Add(step)
		for from := ts; from.Before(end); {
			to := end
			if m := nextLocalMidnight(from, loc); m.Before(to) {
				to = m
			}
			f0, f1 := from.Sub(ts).Hours()/dtH, to.Sub(ts).Hours()/dtH
			soc := func(f float64) float64 { return round(sl.SoCStartPct+(sl.SoCEndPct-sl.SoCStartPct)*f, 1) }
			pieces = append(pieces, piece{
				start: from, end: to, action: sl.Action, powerKW: power,
				energy: sl.GridKWh * (f1 - f0), priceSum: sl.Price * (f1 - f0), weight: f1 - f0,
				socStart: soc(f0), socEnd: soc(f1),
			})
			from = to
		}
	}

	var merged []piece
	for _, p := range pieces {
		if n := len(merged); n > 0 {
			m := &merged[n-1]
			sameDay := m.start.In(loc).Format("2006-01-02") == p.start.In(loc).Format("2006-01-02")
			if m.end.Equal(p.start) && m.action == p.action && m.powerKW == p.powerKW && sameDay {
				m.end, m.socEnd = p.end, p.socEnd
				m.energy += p.energy
				m.priceSum += p.priceSum
				m.weight += p.weight
				continue
			}
		}
		merged = append(merged, p)
	}

	for _, s := range merged {
		powerKW := math.Abs(s.powerKW)
		signedW := int(math.Round(s.powerKW * 1000))
		startL, endL := s.start.In(loc), s.end.In(loc)
		w := SolaxWindow{
			Date:             startL.Format("2006-01-02"),
			StartLocal:       startL.Format("15:04"),
			EndLocal:         localEnd(startL, endL),
			Start:            s.start.UTC().Format(time.RFC3339),
			End:              s.end.UTC().Format(time.RFC3339),
			Action:           s.action,
			PowerKW:          powerKW,
			PerInverterKW:    round(powerKW/float64(inverters), 2),
			EnergyKWh:        round(math.Abs(s.energy), 3),
			StartSoCPercent:  s.socStart,
			TargetSoCPercent: s.socEnd,
			// Prices are kept in cents/kWh internally; DataHub uses per-MWh.
			AvgPricePerMWh: round(s.priceSum/s.weight*10, 2),
			Modbus: SolaxModbusCommand{
				Mode:              "Enabled Power Control",
				ActivePowerW:      signedW,
				PerInverterPowerW: int(math.Round(float64(signedW) / float64(inverters))),
				DurationS:         int(s.end.Sub(s.start).Seconds()),
			},
		}
		verb := "Charge"
		if w.Action == "discharge" {
			verb = "Discharge"
		}
		w.SmartScene = fmt.Sprintf("IF Date & Time %s %s-%s THEN Inverter %s as %.1f kW till Battery SOC %.0f %%",
			w.Date, w.StartLocal, w.EndLocal, verb, w.PowerKW, w.TargetSoCPercent)
		plan.Windows = append(plan.Windows, w)
	}
	return plan, nil
}

// nextLocalMidnight returns the first local midnight strictly after t.
func nextLocalMidnight(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, loc)
}

// localEnd formats a window end. Windows never cross local midnight, so one
// ending on the next date ends exactly at midnight and is shown as "24:00" to
// stay on the same Smart Scene date.
func localEnd(start, end time.Time) string {
	if end.Format("2006-01-02") != start.Format("2006-01-02") {
		return "24:00"
	}
	return end.Format("15:04")
}
