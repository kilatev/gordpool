package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"gordpool/pkg/planner"
	"gordpool/pkg/textchart"
)

// ---------- TUI ----------

func main() {
	app := tview.NewApplication()

	counter := 0
	counterView := tview.NewTextView().
		SetDynamicColors(true).
		SetWrap(true).
		SetChangedFunc(func() {
			app.Draw()
		})
	counterView.SetBorder(true).SetTitle("Counter (demo)")
	updateCounter := func() {
		counterView.SetText(fmt.Sprintf("Value: [yellow]%d[-:-:-]\nHotkeys: + / - / 0 (reset)", counter))
	}
	updateCounter()

	output := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(true).
		SetChangedFunc(func() {
			app.Draw()
		})
	output.SetBorder(true).SetTitle("Schedule chart (A/C/D to filter)")

	form := tview.NewForm().
		AddInputField("Area", "LV", 4, nil, nil).
		AddInputField("Market", "DayAhead", 10, nil, nil).
		AddInputField("Currency", "EUR", 4, nil, nil).
		// values below — in HOURS and CENTS/kWh:
		AddInputField("Max charge hours", "3", 5, nil, nil).
		AddInputField("Max discharge hours", "3", 5, nil, nil).
		AddInputField("Last price charged (c/kWh)", "15", 10, nil, nil).
		AddInputField("Epsilon (c/kWh)", "2", 10, nil, nil)

	form.SetBorder(true).SetTitle("Params (TAB to move, ENTER to edit)")

	getFieldText := func(idx int) string {
		item := form.GetFormItem(idx)
		if input, ok := item.(*tview.InputField); ok {
			return input.GetText()
		}
		return ""
	}

	// state for hotkeys
	var lastPrices []planner.PriceSlot
	var lastSchedule *planner.ScheduleJSON
	filterMode := textchart.FilterAll

	renderIfReady := func() {
		if lastSchedule == nil || len(lastPrices) == 0 {
			return
		}
		now := time.Now().UTC()
		output.Clear()
		chart := textchart.Build(lastPrices, *lastSchedule, now, filterMode, textchart.Options{Colorize: true})
		fmt.Fprint(output, chart)
	}

	form.AddButton("Fetch & Plan", func() {
		area := getFieldText(0)
		market := getFieldText(1)
		currency := getFieldText(2)

		maxChargeStr := getFieldText(3)
		maxDischargeStr := getFieldText(4)
		lastPriceStr := getFieldText(5)
		epsilonStr := getFieldText(6)

		maxCharge, err1 := strconv.ParseFloat(maxChargeStr, 64)
		maxDischarge, err2 := strconv.ParseFloat(maxDischargeStr, 64)
		lastPrice, err3 := strconv.ParseFloat(lastPriceStr, 64)
		epsilon, err4 := strconv.ParseFloat(epsilonStr, 64)

		output.Clear()

		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			fmt.Fprintf(output, "[red]Error parsing numeric inputs.[-:-:-]\n")
			return
		}

		params := planner.BatteryStrategyParams{
			Area:              area,
			MaxChargeHours:    maxCharge,
			MaxDischargeHours: maxDischarge,
			LastPriceCharged:  lastPrice, // cents/kWh
			Epsilon:           epsilon,   // cents/kWh
			Market:            market,
			Currency:          currency,
		}

		fmt.Fprintf(output, "[yellow]Fetching prices for %s...[-:-:-]\n\n", area)

		prices, err := planner.FetchNordpoolPrices(context.Background(), area, market, currency)
		if err != nil {
			fmt.Fprintf(output, "[red]Fetch error: %v[-:-:-]\n", err)
			return
		}
		if len(prices) == 0 {
			fmt.Fprintf(output, "[red]No prices returned.[-:-:-]\n")
			return
		}

		now := time.Now().UTC()
		schedule := planner.BuildBatterySchedule(prices, params, now)

		lastPrices = prices
		lastSchedule = &schedule
		filterMode = textchart.FilterAll

		output.Clear()
		chart := textchart.Build(prices, schedule, now, filterMode, textchart.Options{Colorize: true})
		fmt.Fprint(output, chart)
	})

	form.AddButton("Quit", func() {
		app.Stop()
	})

	flex := tview.NewFlex().
		AddItem(form, 40, 0, true).
		AddItem(output, 0, 1, false)

	root := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(counterView, 4, 0, false).
		AddItem(flex, 0, 1, true)

	// hotkeys: Esc = quit, A/C/D = filter, +/-/0 = counter demo
	root.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEsc {
			app.Stop()
			return nil
		}
		if event.Key() == tcell.KeyRune {
			switch event.Rune() {
			case 'a', 'A':
				filterMode = textchart.FilterAll
				renderIfReady()
				return nil
			case 'c', 'C':
				filterMode = textchart.FilterChargeOnly
				renderIfReady()
				return nil
			case 'd', 'D':
				filterMode = textchart.FilterDischargeOnly
				renderIfReady()
				return nil
			case '+':
				counter++
				updateCounter()
				return nil
			case '-':
				counter--
				updateCounter()
				return nil
			case '0':
				counter = 0
				updateCounter()
				return nil
			}
		}
		return event
	})

	if err := app.SetRoot(root, true).EnableMouse(true).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
