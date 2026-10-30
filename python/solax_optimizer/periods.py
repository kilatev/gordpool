"""Pure constrained planning. No browser, credentials or equipment access."""
from collections import defaultdict, deque
from dataclasses import dataclass
from math import floor, isfinite


def prefer(candidate, incumbent):
    # Treat sub-pico-euro roundoff as a tie, never as a reason for an extra cycle.
    delta = candidate[0] - incumbent[0]
    return delta < -1e-12 or (abs(delta) <= 1e-12 and candidate[1:] < incumbent[1:])


@dataclass(frozen=True)
class PeriodLimits:
    horizon_hours: float = 24
    max_charge_windows: int = 2
    max_discharge_windows: int = 2
    max_grid_charge_kw: float = 6

    def validate(self):
        if not isfinite(self.horizon_hours) or not 0 < self.horizon_hours <= 24:
            raise ValueError("Period horizon must be in (0, 24] hours")
        for count in (self.max_charge_windows, self.max_discharge_windows):
            if type(count) is not int or not 0 <= count <= 2:
                raise ValueError("Window counts must be integers in [0, 2]")
        if not isfinite(self.max_grid_charge_kw) or not 0 <= self.max_grid_charge_kw <= 6:
            raise ValueError("Grid charge power must be in [0, 6] kW")


def choose_moves(prices, battery, start, lo, hi, terminal, cap, limits):
    """Exact DP on SOC, throughput, window counts and previous action.

    A window is a contiguous run of strictly positive or negative SOC moves.
    HOLD closes a run. Midnight does not close a UTC-contiguous window.
    Only reachable states are retained. Idle wins ties on energy throughput.
    """
    step = battery.capacity_kwh / 100
    eta = battery.round_trip_efficiency ** .5
    charge_power = min(battery.max_charge_kw, limits.max_grid_charge_kw * eta)
    # Each discharge window can traverse the SOC range only once.
    if cap is not None and cap >= limits.max_discharge_windows * (hi - lo):
        cap = None
    # state: SOC, discharged battery steps, charge windows, discharge windows, action
    initial = (start, 0, 0, 0, 0)
    costs = {initial: (0.0, 0, 0)}
    parents = []
    for price in prices:
        hours = (price.end - price.start).total_seconds() / 3600
        up = min(hi - lo, floor(charge_power * hours / step + 1e-9))
        down = min(hi - lo, floor(battery.max_discharge_kw * hours / step + 1e-9))
        current, previous = {}, {}
        groups = {1: defaultdict(list), -1: defaultdict(list)}
        coefficients = {1: step * price.eur_mwh / 1000 / eta,
                        -1: step * (price.eur_mwh / 1000 * eta - battery.degradation_eur_kwh)}
        for state, value in costs.items():
            level, used, charges, discharges, action = state
            # HOLD closes an active window without moving energy.
            key = (level, used, charges, discharges, 0)
            if key not in current or prefer(value, current[key]):
                current[key], previous[key] = value, state
            for sign, bound in ((1, up), (-1, down)):
                nc = charges + (sign == 1 and action != 1)
                nd = discharges + (sign == -1 and action != -1)
                if not bound or nc > limits.max_charge_windows or nd > limits.max_discharge_windows:
                    continue
                # Discharge preserves SOC + used; charge preserves used.
                budget_axis = used + level if sign == -1 and cap is not None else used
                adjusted = (value[0] - coefficients[sign] * level,
                            value[1] - sign * level, value[2] + 1)
                groups[sign][budget_axis, nc, nd].append((sign * level, adjusted, state))

        # Move cost is linear in SOC change. A sliding minimum replaces the
        # quadratic scan over all start/end SOC pairs, including cycle budgets.
        for sign, bound in ((1, up), (-1, down)):
            axis_lo, axis_hi = (lo, hi) if sign == 1 else (-hi, -lo)
            for (budget_axis, nc, nd), sources in groups[sign].items():
                sources.sort(key=lambda item: item[0])
                candidates, cursor = deque(), 0
                begin = max(axis_lo, sources[0][0] + 1)
                finish = min(axis_hi, sources[-1][0] + bound)
                for position in range(begin, finish + 1):
                    while cursor < len(sources) and sources[cursor][0] < position:
                        item = sources[cursor]
                        while candidates and prefer(item[1], candidates[-1][1]):
                            candidates.pop()
                        candidates.append(item)
                        cursor += 1
                    while candidates and candidates[0][0] < position - bound:
                        candidates.popleft()
                    if not candidates:
                        continue
                    level = sign * position
                    used = budget_axis - level if sign == -1 and cap is not None else budget_axis
                    if cap is not None and not 0 <= used <= cap:
                        continue
                    parent = candidates[0][2]
                    delta = level - parent[0]
                    value = (costs[parent][0] + coefficients[sign] * delta,
                             costs[parent][1] + abs(delta), costs[parent][2] + 1)
                    key = (level, used, nc, nd, sign)
                    current[key], previous[key] = value, parent
        costs = current
        parents.append(previous)
    state, best = None, None
    for key, (cost, throughput, active_slots) in costs.items():
        value = (cost - terminal * key[0] * step, throughput, key[2] + key[3], active_slots)
        if best is None or prefer(value, best):
            state, best = key, value
    moves = []
    for previous in reversed(parents):
        parent = previous[state]
        moves.append(state[0] - parent[0])
        state = parent
    return list(reversed(moves))
