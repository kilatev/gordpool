package planner

import (
	"math"
	"math/rand"
	"testing"
	"testing/quick"
	"time"

	qt "github.com/frankban/quicktest"
)

func TestToSolaxDataHub(t *testing.T) {
	c := qt.New(t)
	riga, err := time.LoadLocation("Europe/Riga") // UTC+2 in December
	c.Assert(err, qt.IsNil)

	plan, err := OptimizeBattery(hourly(20, 2, 3, 25, 30, 10), "LV", idealSpec(), t0)
	c.Assert(err, qt.IsNil)
	out, err := plan.ToSolaxDataHub(SolaxOptions{Location: riga, Inverters: 2})
	c.Assert(err, qt.IsNil)

	c.Assert(out.Format, qt.Equals, SolaxFormat)
	c.Assert(out.Timezone, qt.Equals, "Europe/Riga")
	c.Assert(out.Inverters, qt.Equals, 2)
	c.Assert(out.Windows, qt.DeepEquals, []SolaxWindow{{
		Date:             "2025-12-08",
		StartLocal:       "03:00",
		EndLocal:         "04:00",
		Start:            "2025-12-08T01:00:00Z",
		End:              "2025-12-08T02:00:00Z",
		Action:           "charge",
		PowerKW:          8,
		PerInverterKW:    4,
		EnergyKWh:        8,
		StartSoCPercent:  10,
		TargetSoCPercent: 90,
		AvgPricePerMWh:   20,
		SmartScene:       "IF Date & Time 2025-12-08 03:00-04:00 THEN Inverter Charge as 8.0 kW till Battery SOC 90 %",
		Modbus:           SolaxModbusCommand{Mode: "Enabled Power Control", ActivePowerW: 8000, PerInverterPowerW: 4000, DurationS: 3600},
	}, {
		Date:             "2025-12-08",
		StartLocal:       "06:00",
		EndLocal:         "07:00",
		Start:            "2025-12-08T04:00:00Z",
		End:              "2025-12-08T05:00:00Z",
		Action:           "discharge",
		PowerKW:          8,
		PerInverterKW:    4,
		EnergyKWh:        8,
		StartSoCPercent:  90,
		TargetSoCPercent: 10,
		AvgPricePerMWh:   300,
		SmartScene:       "IF Date & Time 2025-12-08 06:00-07:00 THEN Inverter Discharge as 8.0 kW till Battery SOC 10 %",
		Modbus:           SolaxModbusCommand{Mode: "Enabled Power Control", ActivePowerW: -8000, PerInverterPowerW: -4000, DurationS: 3600},
	}})
}

func TestToSolaxDataHubMergesAndSplits(t *testing.T) {
	c := qt.New(t)
	spec := idealSpec()
	spec.MaxChargeKW, spec.MaxDischargeKW = 2, 2 // 20% per hour → 4 equal hours to fill

	// 20:00–24:00 UTC cheap, then expensive next day.
	start := t0.Add(20 * time.Hour)
	var prices []PriceSlot
	for i, p := range []float64{1, 1.1, 1.2, 1.3, 50, 50, 50, 50} {
		prices = append(prices, PriceSlot{Timestamp: start.Add(time.Duration(i) * time.Hour), Price: p})
	}
	plan, err := OptimizeBattery(prices, "LV", spec, start)
	c.Assert(err, qt.IsNil)

	utc, err := plan.ToSolaxDataHub(SolaxOptions{})
	c.Assert(err, qt.IsNil)
	c.Assert(utc.Timezone, qt.Equals, "UTC")
	c.Assert(utc.Windows, qt.HasLen, 2)
	c.Assert(utc.Windows[0].StartLocal+"-"+utc.Windows[0].EndLocal, qt.Equals, "20:00-24:00")
	c.Assert(utc.Windows[0].Modbus.DurationS, qt.Equals, 4*3600)

	// In Riga (UTC+2) the charge runs 22:00–02:00 and must split at midnight.
	riga, _ := time.LoadLocation("Europe/Riga")
	local, err := plan.ToSolaxDataHub(SolaxOptions{Location: riga})
	c.Assert(err, qt.IsNil)
	c.Assert(local.Windows, qt.HasLen, 3)
	c.Assert(local.Windows[0].Date+" "+local.Windows[0].StartLocal+"-"+local.Windows[0].EndLocal, qt.Equals, "2025-12-08 22:00-24:00")
	c.Assert(local.Windows[0].TargetSoCPercent, qt.Equals, 50.0)
	c.Assert(local.Windows[1].Date+" "+local.Windows[1].StartLocal+"-"+local.Windows[1].EndLocal, qt.Equals, "2025-12-09 00:00-02:00")
	c.Assert(local.Windows[1].TargetSoCPercent, qt.Equals, 90.0)
}

func TestToSolaxDataHubHalfHourZoneSplitsSlot(t *testing.T) {
	c := qt.New(t)
	kolkata, err := time.LoadLocation("Asia/Kolkata") // UTC+05:30
	c.Assert(err, qt.IsNil)

	// 18:00–19:00 UTC is 23:30–00:30 in Kolkata.
	start := t0.Add(18 * time.Hour)
	plan, err := OptimizeBattery([]PriceSlot{
		{Timestamp: start, Price: 1},
		{Timestamp: start.Add(time.Hour), Price: 50},
	}, "IN", idealSpec(), start)
	c.Assert(err, qt.IsNil)

	out, err := plan.ToSolaxDataHub(SolaxOptions{Location: kolkata})
	c.Assert(err, qt.IsNil)
	c.Assert(out.Windows, qt.HasLen, 3)
	c.Assert(out.Windows[0].Date+" "+out.Windows[0].StartLocal+"-"+out.Windows[0].EndLocal, qt.Equals, "2025-12-08 23:30-24:00")
	c.Assert(out.Windows[0].TargetSoCPercent, qt.Equals, 50.0) // half of the 10→90 move
	c.Assert(out.Windows[0].EnergyKWh, qt.Equals, 4.0)
	c.Assert(out.Windows[1].Date+" "+out.Windows[1].StartLocal+"-"+out.Windows[1].EndLocal, qt.Equals, "2025-12-09 00:00-00:30")
	c.Assert(out.Windows[1].TargetSoCPercent, qt.Equals, 90.0)
	c.Assert(out.Windows[2].Action, qt.Equals, "discharge")
}

func TestToSolaxDataHubMinPower(t *testing.T) {
	c := qt.New(t)
	plan := OptimizedScheduleJSON{
		ResolutionMinutes: ptrInt(60),
		Slots: []OptimizedSlotJSON{
			{Timestamp: t0.Format(time.RFC3339), Action: "charge", GridKWh: 0.2, SoCStartPct: 30, SoCEndPct: 31},
			{Timestamp: t0.Add(time.Hour).Format(time.RFC3339), Action: "charge", GridKWh: 5, SoCStartPct: 31, SoCEndPct: 56},
		},
	}
	out, err := plan.ToSolaxDataHub(SolaxOptions{MinPowerKW: 0.5})
	c.Assert(err, qt.IsNil)
	c.Assert(out.Windows, qt.HasLen, 1)
	c.Assert(out.Windows[0].StartLocal, qt.Equals, "01:00")
	c.Assert(out.Windows[0].TargetSoCPercent, qt.Equals, 56.0)

	out, err = plan.ToSolaxDataHub(SolaxOptions{})
	c.Assert(err, qt.IsNil)
	c.Assert(out.Windows, qt.HasLen, 2)
}

func ptrInt(i int) *int { return &i }

func TestToSolaxDataHubEmpty(t *testing.T) {
	c := qt.New(t)
	out, err := OptimizedScheduleJSON{Area: "LV"}.ToSolaxDataHub(SolaxOptions{})
	c.Assert(err, qt.IsNil)
	c.Assert(out.Windows, qt.HasLen, 0)
	c.Assert(out.Inverters, qt.Equals, 1)
}

func TestPropertySolaxWindowsMatchPlan(t *testing.T) {
	c := qt.New(t)
	zones := []string{"UTC", "Europe/Riga", "America/New_York", "Asia/Kolkata"}
	prop := func(s scenario, zone uint8, inverters uint8, minPower uint8) bool {
		minKW := float64(minPower%4) / 2 // 0, 0.5, 1, 1.5
		loc, _ := time.LoadLocation(zones[int(zone)%len(zones)])
		plan, err := OptimizeBattery(s.Prices, "LV", s.Spec, t0)
		if err != nil {
			return false
		}
		out, err := plan.ToSolaxDataHub(SolaxOptions{Location: loc, Inverters: int(inverters % 5), MinPowerKW: minKW})
		if err != nil {
			return false
		}
		step := time.Duration(*plan.ResolutionMinutes) * time.Minute
		var active int
		var energy float64
		for _, sl := range plan.Slots {
			if p := math.Abs(round(sl.GridKWh/step.Hours(), 1)); p > 0 && p >= minKW {
				active++
				energy += math.Abs(sl.GridKWh)
			}
		}
		var covered time.Duration
		var windowEnergy float64
		for _, w := range out.Windows {
			st, _ := time.Parse(time.RFC3339, w.Start)
			en, _ := time.Parse(time.RFC3339, w.End)
			covered += en.Sub(st)
			windowEnergy += w.EnergyKWh
			// a window starts and ends on its own local date (or at the next midnight)
			if st.In(loc).Format("2006-01-02") != w.Date ||
				en.In(loc).Format("2006-01-02") != w.Date && w.EndLocal != "24:00" {
				t.Logf("window %+v crosses local midnight in %s", w, loc)
				return false
			}
			if w.PowerKW < minKW || w.PowerKW == 0 {
				t.Logf("window %+v below min power %v", w, minKW)
				return false
			}
			if (w.Action == "charge") != (w.Modbus.ActivePowerW > 0) {
				t.Logf("window %+v has wrong power sign", w)
				return false
			}
			if w.Modbus.DurationS != int(en.Sub(st).Seconds()) {
				t.Logf("window %+v has wrong duration", w)
				return false
			}
		}
		if covered != time.Duration(active)*step {
			t.Logf("windows cover %v, active slots %v", covered, time.Duration(active)*step)
			return false
		}
		if math.Abs(windowEnergy-energy) > 0.001*float64(len(out.Windows)+1) {
			t.Logf("window energy %v, plan energy %v", windowEnergy, energy)
			return false
		}
		return true
	}
	c.Assert(quick.Check(prop, &quick.Config{MaxCount: 200, Rand: rand.New(rand.NewSource(4))}), qt.IsNil)
}
