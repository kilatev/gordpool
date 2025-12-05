package textchart

import (
	"fmt"
	"math"
	"strings"
	"time"

	"gordpool/pkg/planner"
)

// FilterMode matches the TUI filters.
type FilterMode int

const (
	FilterAll FilterMode = iota
	FilterChargeOnly
	FilterDischargeOnly
)

// Options controls rendering details.
type Options struct {
	Colorize  bool // when true, use tview color tags
	MaxWidth  int  // bar width
	MaxPoints int  // sparkline downsample limit
}

func defaultOptions() Options {
	return Options{
		Colorize:  false,
		MaxWidth:  30,
		MaxPoints: 80,
	}
}

// Build renders a textual chart similar to the TUI view.
func Build(prices []planner.PriceSlot, schedule planner.ScheduleJSON, now time.Time, mode FilterMode, opts Options) string {
	def := defaultOptions()
	if opts.MaxWidth == 0 {
		opts.MaxWidth = def.MaxWidth
	}
	if opts.MaxPoints == 0 {
		opts.MaxPoints = def.MaxPoints
	}

	future := filterFuture(prices, now)
	if len(future) == 0 {
		return colorize("[red]No future slots available.[-:-:-]\n", opts.Colorize)
	}

	minP := future[0].Price
	maxP := future[0].Price
	for _, s := range future {
		if s.Price < minP {
			minP = s.Price
		}
		if s.Price > maxP {
			maxP = s.Price
		}
	}

	chargeSet := setFromSlots(schedule.ChargeSlots)
	dischargeSet := setFromSlots(schedule.DischargeSlots)

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", colorize(fmt.Sprintf("[yellow]Nord Pool chart for %s (c/kWh)[-:-:-]", schedule.Area), opts.Colorize))
	b.WriteString("Legend: ")
	b.WriteString(colorize("[lime]C[-:-:-]", opts.Colorize))
	b.WriteString("=charge  ")
	b.WriteString(colorize("[red]D[-:-:-]", opts.Colorize))
	b.WriteString("=discharge  ")
	b.WriteString(colorize("[dodgerblue].[−][-:-:-]", opts.Colorize))
	b.WriteString("=idle\n")

	b.WriteString("Filter: ")
	switch mode {
	case FilterChargeOnly:
		b.WriteString("Charge only (C)")
	case FilterDischargeOnly:
		b.WriteString("Discharge only (D)")
	default:
		b.WriteString("All (A)")
	}
	b.WriteString("\n\n")

	b.WriteString(buildSparkline(future, chargeSet, dischargeSet, minP, maxP, mode, opts))

	// lineInfo: type 0=idle,1=charge,2=discharge
	type lineInfo struct {
		slot planner.PriceSlot
		typ  int
	}
	var lines []lineInfo
	for _, s := range future {
		_, isC := chargeSet[s.Timestamp]
		_, isD := dischargeSet[s.Timestamp]

		// filter mode
		if mode == FilterChargeOnly && !isC {
			continue
		}
		if mode == FilterDischargeOnly && !isD {
			continue
		}

		t := 0
		if isC {
			t = 1
		} else if isD {
			t = 2
		}
		lines = append(lines, lineInfo{slot: s, typ: t})
	}

	if len(lines) == 0 {
		b.WriteString(colorize("[red]No slots for this filter.[-:-:-]\n", opts.Colorize))
		return b.String()
	}

	for i, ln := range lines {
		s := ln.slot
		typ := ln.typ

		color := ""
		if opts.Colorize {
			switch typ {
			case 1:
				color = "[lime]"
			case 2:
				color = "[red]"
			default:
				color = "[dodgerblue]"
			}
		}

		frame := " "
		if typ != 0 {
			prevSame := i > 0 && lines[i-1].typ == typ
			nextSame := i < len(lines)-1 && lines[i+1].typ == typ

			var sym string
			switch {
			case !prevSame && nextSame:
				sym = "╭"
			case prevSame && nextSame:
				sym = "│"
			case prevSame && !nextSame:
				sym = "╰"
			default:
				sym = "•"
			}
			frame = wrap(sym, color, opts.Colorize)
		}

		markChar := '.'
		markColor := ""
		switch typ {
		case 1:
			markChar = 'C'
			if opts.Colorize {
				markColor = "[lime]"
			}
		case 2:
			markChar = 'D'
			if opts.Colorize {
				markColor = "[red]"
			}
		default:
			if opts.Colorize {
				markColor = "[dodgerblue]"
			}
		}

		rel := 0.0
		if maxP > minP {
			rel = (s.Price - minP) / (maxP - minP)
		}
		length := int(math.Round(rel * float64(opts.MaxWidth)))
		if length < 1 {
			length = 1
		}
		bar := wrap(strings.Repeat("█", length), color, opts.Colorize)

		ts := s.Timestamp.Format("01-02 15:04")

		fmt.Fprintf(
			&b,
			"%s %s | %6.2f c/kWh | %s%c%s | %s\n",
			frame,
			ts,
			s.Price,
			markColor,
			markChar,
			reset(opts.Colorize),
			bar,
		)
	}

	return b.String()
}

func buildSparkline(slots []planner.PriceSlot, chargeSet, dischargeSet map[time.Time]bool, minP, maxP float64, mode FilterMode, opts Options) string {
	if len(slots) == 0 {
		return ""
	}

	blocks := []rune("▁▂▃▄▅▆▇█")
	n := len(blocks) - 1

	step := 1
	if len(slots) > opts.MaxPoints {
		step = int(math.Ceil(float64(len(slots)) / float64(opts.MaxPoints)))
	}

	var line1 strings.Builder
	var line2 strings.Builder

	for i := 0; i < len(slots); i += step {
		s := slots[i]

		_, isC := chargeSet[s.Timestamp]
		_, isD := dischargeSet[s.Timestamp]
		if mode == FilterChargeOnly && !isC {
			continue
		}
		if mode == FilterDischargeOnly && !isD {
			continue
		}

		rel := 0.0
		if maxP > minP {
			rel = (s.Price - minP) / (maxP - minP)
		}
		idx := int(math.Round(rel * float64(n)))
		if idx < 0 {
			idx = 0
		}
		if idx > n {
			idx = n
		}
		ch := blocks[idx]

		color := ""
		mark := "."
		switch {
		case isC:
			color = "[lime]"
			mark = "C"
		case isD:
			color = "[red]"
			mark = "D"
		default:
			if opts.Colorize {
				color = "[dodgerblue]"
			}
		}

		line1.WriteString(wrap(string(ch), color, opts.Colorize))
		line2.WriteString(wrap(mark, color, opts.Colorize))
	}

	if line1.Len() == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("Sparkline: prices (blocks) / mode (C/D/.)\n")
	b.WriteString(line1.String())
	b.WriteString("\n")
	b.WriteString(line2.String())
	b.WriteString("\n\n")
	return b.String()
}

func setFromSlots(slots []planner.SlotJSON) map[time.Time]bool {
	out := make(map[time.Time]bool, len(slots))
	for _, s := range slots {
		ts, err := time.Parse(time.RFC3339, s.Timestamp)
		if err == nil {
			out[ts] = true
		}
	}
	return out
}

func filterFuture(prices []planner.PriceSlot, now time.Time) []planner.PriceSlot {
	var future []planner.PriceSlot
	for _, p := range prices {
		if !p.Timestamp.Before(now) {
			future = append(future, p)
		}
	}
	return future
}

func colorize(s string, colorize bool) string {
	if !colorize {
		return stripTags(s)
	}
	return s
}

func wrap(s, color string, colorize bool) string {
	if colorize && color != "" {
		return color + s + "[-:-:-]"
	}
	return s
}

func reset(colorize bool) string {
	if colorize {
		return "[-:-:-]"
	}
	return ""
}

func stripTags(s string) string {
	// Remove [color] and [-:-:-] tags for plain output.
	var b strings.Builder
	inside := false
	for i := 0; i < len(s); i++ {
		if s[i] == '[' {
			inside = true
			continue
		}
		if inside && s[i] == ']' {
			inside = false
			continue
		}
		if inside {
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
