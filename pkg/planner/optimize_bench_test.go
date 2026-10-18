package planner

import (
	"math"
	"testing"
	"time"
)

func benchPrices() []PriceSlot {
	out := make([]PriceSlot, 192)
	for i := range out {
		out[i] = PriceSlot{Timestamp: t0.Add(time.Duration(i) * 15 * time.Minute), Price: 10 + 8*math.Sin(float64(i)/8)}
	}
	return out
}

func BenchmarkOptimizeTwoDays15m(b *testing.B) {
	prices := benchPrices()
	spec := BatterySpec{CapacityKWh: 10, InitialSoCPercent: 50, DegradationCostCents: 1}
	for i := 0; i < b.N; i++ {
		_, _ = OptimizeBattery(prices, "LV", spec, t0)
	}
}

func BenchmarkOptimizeTwoDays15mCycleCap(b *testing.B) {
	prices := benchPrices()
	spec := BatterySpec{CapacityKWh: 10, InitialSoCPercent: 50, DegradationCostCents: 1, MaxCyclesPerDay: 1}
	for i := 0; i < b.N; i++ {
		_, _ = OptimizeBattery(prices, "LV", spec, t0)
	}
}
