//go:build js && wasm
// +build js,wasm

package main

import (
	"context"
	"encoding/json"
	"syscall/js"
	"time"

	"gordpool/pkg/planner"
	"gordpool/pkg/textchart"
)

// planPromise exposes planner.FetchNordpoolPrices + BuildBatterySchedule as a JS Promise.
func planPromise(this js.Value, args []js.Value) any {
	if len(args) == 0 {
		return js.Global().Get("Promise").New(js.FuncOf(func(_ js.Value, innerArgs []js.Value) any {
			reject := innerArgs[1]
			reject.Invoke("params object required")
			return nil
		}))
	}

	in := args[0]
	toString := func(key, def string) string {
		v := in.Get(key)
		if v.Type() == js.TypeUndefined || v.Type() == js.TypeNull {
			return def
		}
		s := v.String()
		if s == "" {
			return def
		}
		return s
	}
	toFloat := func(key string, def float64) float64 {
		v := in.Get(key)
		if v.Type() == js.TypeUndefined || v.Type() == js.TypeNull {
			return def
		}
		f := v.Float()
		if f == 0 {
			return def
		}
		return f
	}

	params := planner.BatteryStrategyParams{
		Area:              toString("area", "LV"),
		Market:            toString("market", "DayAhead"),
		Currency:          toString("currency", "EUR"),
		MaxChargeHours:    toFloat("maxChargeHours", 3),
		MaxDischargeHours: toFloat("maxDischargeHours", 3),
		LastPriceCharged:  toFloat("lastPriceCharged", 15),
		Epsilon:           toFloat("epsilon", 2),
	}

	baseURL := toString("baseURL", "https://dataportal-api.nordpoolgroup.com/api/DayAheadPrices")

	promiseBody := js.FuncOf(func(_ js.Value, innerArgs []js.Value) any {
		resolve := innerArgs[0]
		reject := innerArgs[1]

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			prices, err := planner.FetchNordpoolPricesWithBase(ctx, baseURL, params.Area, params.Market, params.Currency)
			if err != nil {
				reject.Invoke(err.Error())
				return
			}
			now := time.Now().UTC()
			schedule := planner.BuildBatterySchedule(prices, params, now)
			chart := textchart.Build(prices, schedule, now, textchart.FilterAll, textchart.Options{Colorize: true})

			payload := map[string]any{
				"schedule": schedule,
				"prices":   prices,
				"chart":    chart,
			}
			b, err := json.Marshal(payload)
			if err != nil {
				reject.Invoke(err.Error())
				return
			}
			resolve.Invoke(string(b))
		}()
		return nil
	})

	return js.Global().Get("Promise").New(promiseBody)
}

func main() {
	js.Global().Set("plan", js.FuncOf(planPromise))
	select {} // block forever; WASM module stays alive
}
