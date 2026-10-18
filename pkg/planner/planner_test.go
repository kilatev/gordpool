package planner

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"testing/quick"
	"time"

	qt "github.com/frankban/quicktest"
)

func TestInferResolutionMinutes(t *testing.T) {
	c := qt.New(t)
	c.Assert(inferResolutionMinutes(nil), qt.Equals, 60)
	c.Assert(inferResolutionMinutes(hourly(1, 2, 3)), qt.Equals, 60)

	quarter := []PriceSlot{{Timestamp: t0}, {Timestamp: t0.Add(15 * time.Minute)}, {Timestamp: t0.Add(30 * time.Minute)}}
	c.Assert(inferResolutionMinutes(quarter), qt.Equals, 15)
}

func TestSlotsForHours(t *testing.T) {
	c := qt.New(t)
	c.Assert(slotsForHours(3, 60), qt.Equals, 3)
	c.Assert(slotsForHours(3, 15), qt.Equals, 12)
	c.Assert(slotsForHours(1.1, 60), qt.Equals, 2)
}

func TestGroupConsecutiveSlots(t *testing.T) {
	c := qt.New(t)
	c.Assert(groupConsecutiveSlots(nil, 60), qt.IsNil)

	slots := hourly(1, 3, 100, 5)
	got := groupConsecutiveSlots([]PriceSlot{slots[3], slots[0], slots[1]}, 60)
	c.Assert(got, qt.DeepEquals, []IntervalJSON{{
		Start:    t0.Format(time.RFC3339),
		End:      t0.Add(2 * time.Hour).Format(time.RFC3339),
		AvgPrice: 2,
	}, {
		Start:    t0.Add(3 * time.Hour).Format(time.RFC3339),
		End:      t0.Add(4 * time.Hour).Format(time.RFC3339),
		AvgPrice: 5,
	}})
}

func TestBuildBatterySchedule(t *testing.T) {
	c := qt.New(t)
	params := BatteryStrategyParams{Area: "LV", MaxChargeHours: 2, MaxDischargeHours: 1, LastPriceCharged: 15, Epsilon: 2}
	s := BuildBatterySchedule(hourly(20, 2, 3, 25, 30, 10), params, t0)

	c.Assert(*s.ResolutionMinutes, qt.Equals, 60)
	c.Assert(s.ChargeSlots, qt.DeepEquals, []SlotJSON{
		{Timestamp: t0.Add(1 * time.Hour).Format(time.RFC3339), Price: 2},
		{Timestamp: t0.Add(2 * time.Hour).Format(time.RFC3339), Price: 3},
	})
	// threshold capped at 8c → most expensive slot wins
	c.Assert(s.DischargeSlots, qt.DeepEquals, []SlotJSON{
		{Timestamp: t0.Add(4 * time.Hour).Format(time.RFC3339), Price: 30},
	})
	c.Assert(s.ChargeIntervals, qt.HasLen, 1)
}

func TestBuildBatteryScheduleNoFuture(t *testing.T) {
	c := qt.New(t)
	s := BuildBatterySchedule(hourly(1, 2), BatteryStrategyParams{Area: "LV"}, t0.Add(5*time.Hour))
	c.Assert(s.ResolutionMinutes, qt.IsNil)
	c.Assert(s.ChargeSlots, qt.HasLen, 0)
	c.Assert(s.DischargeSlots, qt.HasLen, 0)
}

type legacyScenario struct {
	Prices []PriceSlot
	Params BatteryStrategyParams
}

func (legacyScenario) Generate(r *rand.Rand, _ int) reflect.Value {
	n := 1 + r.Intn(48)
	prices := make([]PriceSlot, n)
	for i := range prices {
		prices[i] = PriceSlot{Timestamp: t0.Add(time.Duration(i) * time.Hour), Price: r.Float64() * 40}
	}
	return reflect.ValueOf(legacyScenario{Prices: prices, Params: BatteryStrategyParams{
		MaxChargeHours:    r.Float64() * 6,
		MaxDischargeHours: r.Float64() * 6,
		LastPriceCharged:  r.Float64() * 30,
		Epsilon:           r.Float64() * 5,
	}})
}

func TestPropertyLegacyScheduleRespectsLimits(t *testing.T) {
	c := qt.New(t)
	prop := func(s legacyScenario) bool {
		sch := BuildBatterySchedule(s.Prices, s.Params, t0)
		if len(sch.ChargeSlots) > slotsForHours(s.Params.MaxChargeHours, 60) ||
			len(sch.DischargeSlots) > slotsForHours(s.Params.MaxDischargeHours, 60) {
			return false
		}
		for _, sl := range sch.ChargeSlots {
			if s.Params.LastPriceCharged-sl.Price < s.Params.Epsilon {
				return false
			}
		}
		return sort.SliceIsSorted(sch.ChargeSlots, func(i, j int) bool {
			return sch.ChargeSlots[i].Timestamp < sch.ChargeSlots[j].Timestamp
		})
	}
	c.Assert(quick.Check(prop, &quick.Config{MaxCount: 300, Rand: rand.New(rand.NewSource(1))}), qt.IsNil)
}
