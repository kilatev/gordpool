"""Go gordpool DP port: a hypothetical battery, not a load-following controller.

Prices: EUR/MWh. Costs: EUR. Power limits: battery/DC side.
Discharge is valued as potential avoided purchases, NOT an export instruction.
"""
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from math import ceil, floor, isfinite, sqrt
from statistics import median

from .periods import PeriodLimits, choose_moves

UTC = timezone.utc
QUARTER = timedelta(minutes=15)


def utc(value: datetime) -> datetime:
    if value.tzinfo is None or value.utcoffset() is None:
        raise ValueError("Timestamp must include a timezone")
    return value.astimezone(UTC)


@dataclass(frozen=True)
class Price:
    start: datetime
    end: datetime
    eur_mwh: float


def validate_prices(prices: list[Price]) -> list[Price]:
    result = sorted((Price(utc(p.start), utc(p.end), p.eur_mwh) for p in prices), key=lambda p: p.start)
    for i, p in enumerate(result):
        if not isfinite(p.eur_mwh) or p.end - p.start != QUARTER:
            raise ValueError("Prices must be finite, 15-minute intervals")
        if i and result[i - 1].end != p.start:
            raise ValueError("Price timeline has gaps, overlaps or duplicates")
    return result


@dataclass(frozen=True)
class Battery:
    capacity_kwh: float = 10
    max_charge_kw: float = 5
    max_discharge_kw: float = 5
    initial_soc_percent: float = 10
    min_soc_percent: float = 10
    max_soc_percent: float = 90
    round_trip_efficiency: float = .85
    degradation_eur_kwh: float = .03  # per battery-side discharged kWh
    stored_energy_value_eur_kwh: float | None = None
    max_cycles_per_day: float = 0

    def validate(self):
        values = [v for v in vars(self).values() if v is not None]
        if not all(isfinite(v) for v in values):
            raise ValueError("Battery parameters must be finite")
        if not 0 < self.capacity_kwh <= 1000:
            raise ValueError("capacity_kwh must be in (0, 1000]")
        if not 0 <= self.max_charge_kw <= 1000 or not 0 <= self.max_discharge_kw <= 1000:
            raise ValueError("Power must be in [0, 1000] kW")
        if not 0 <= self.min_soc_percent < self.max_soc_percent <= 100:
            raise ValueError("Invalid SOC window")
        if not self.min_soc_percent <= self.initial_soc_percent <= self.max_soc_percent:
            raise ValueError("Initial SOC must be inside configured window")
        if not 0 < self.round_trip_efficiency <= 1:
            raise ValueError("Efficiency must be in (0, 1]")
        if self.degradation_eur_kwh < 0 or not 0 <= self.max_cycles_per_day <= 10:
            raise ValueError("Invalid wear or cycle limit")


def optimize(prices: list[Price], battery: Battery, now: datetime,
             limits: PeriodLimits | None = None) -> dict:
    battery.validate()
    if limits is not None:
        limits.validate()
    now = utc(now)
    prices = validate_prices(prices)
    # Keep the remaining part of the current interval, using its published price.
    future = [Price(max(p.start, now), p.end, p.eur_mwh) for p in prices if p.end > now]
    if limits is not None:
        end = now + timedelta(hours=limits.horizon_hours)
        future = [Price(p.start, min(p.end, end), p.eur_mwh) for p in future if p.start < end]
    if len(future) > 200:
        raise ValueError("At most 200 future intervals are supported")
    if not future:
        raise ValueError("No published future prices")
    b = battery
    step = b.capacity_kwh / 100
    eta = sqrt(b.round_trip_efficiency)
    lo, hi = ceil(b.min_soc_percent), floor(b.max_soc_percent)
    start = floor(b.initial_soc_percent + .5)  # Go math.Round for positive SOC
    if not lo < hi or not lo <= start <= hi:
        raise ValueError("SOC window is too narrow for the 1% grid")
    terminal = b.stored_energy_value_eur_kwh
    if terminal is None:
        terminal = median(p.eur_mwh / 1000 for p in future)
    hours = sum((p.end - p.start).total_seconds() / 3600 for p in future)
    cap = floor(b.max_cycles_per_day * hours / 24 * (hi - lo) + 1e-9) if b.max_cycles_per_day else None
    if limits is None:
        # ponytail: 1% SOC DP as in Go; continuous MILP when sub-percent moves matter.
        budget = min(cap, len(future) * 100) if cap is not None else 0
        costs = {(a, u): -terminal * a * step for a in range(lo, hi + 1) for u in range(budget + 1)}
        choices = []
        for p in reversed(future):
            dt = (p.end - p.start).total_seconds() / 3600
            up = min(100, floor(b.max_charge_kw * dt / step + 1e-9))
            down = min(100, floor(b.max_discharge_kw * dt / step + 1e-9))
            moves = {}
            for d in range(-down, up + 1):
                energy = d * step
                ac = energy / eta if d > 0 else energy * eta
                moves[d] = p.eur_mwh / 1000 * ac + max(-energy, 0) * b.degradation_eur_kwh
            current, choice = {}, {}
            for a, used in costs:
                best, best_d = float("inf"), 0
                # Idle first means an equal-cost move never creates a gratuitous cycle.
                for d in [0] + list(range(max(lo - a, -down), 0)) + list(range(1, min(hi - a, up) + 1)):
                    next_used = used + max(-d, 0) if cap is not None else 0
                    tail = costs.get((a + d, next_used), float("inf"))
                    value = moves[d] + tail + 1e-9 * abs(d)
                    if value < best:
                        best, best_d = value, d
                current[a, used], choice[a, used] = best, best_d
            costs = current
            choices.append(choice)
        path, level, used = [], start, 0
        for choice in reversed(choices):
            d = choice[level, used]
            path.append(d)
            level += d
            if cap is not None:
                used += max(-d, 0)
    else:
        path = choose_moves(future, b, start, lo, hi, terminal, cap, limits)
    slots, level, used = [], start, 0
    purchase_cost = avoided_cost = wear = imported = delivered = discharged = 0.0
    for p, d in zip(future, path):
        energy = d * step
        ac = energy / eta if d > 0 else energy * eta
        action = "CHARGE" if d > 0 else "DISCHARGE" if d < 0 else "HOLD"
        buy, use = max(ac, 0), max(-ac, 0)
        purchase_cost += buy * p.eur_mwh / 1000
        avoided_cost += use * p.eur_mwh / 1000
        wear += max(-energy, 0) * b.degradation_eur_kwh
        imported += buy
        delivered += use
        discharged += max(-energy, 0)
        slots.append(dict(start=p.start.isoformat(), end=p.end.isoformat(), price_eur_mwh=p.eur_mwh,
                          action=action, power_battery_kw=energy / ((p.end-p.start).total_seconds()/3600),
                          charge_from_grid_kwh=buy, potential_use_kwh=use,
                          soc_start_percent=level, soc_end_percent=level+d))
        level += d
        if cap is not None:
            used += max(-d, 0)
    terminal_delta = (level - start) * step * terminal
    cash_benefit = avoided_cost - purchase_cost - wear
    windows = []
    for s in slots:
        if windows and windows[-1]["action"] == s["action"] and windows[-1]["end"] == s["start"]:
            w = windows[-1]
            w["end"], w["soc_end_percent"] = s["end"], s["soc_end_percent"]
            w["charge_from_grid_kwh"] += s["charge_from_grid_kwh"]
            w["potential_use_kwh"] += s["potential_use_kwh"]
        else:
            windows.append({k:s[k] for k in ("start", "end", "action", "soc_start_percent", "soc_end_percent", "charge_from_grid_kwh", "potential_use_kwh")})
    reasons = {"CHARGE":"Покупка энергии выбрана по всему горизонту с учётом мощности, потерь и износа.",
               "DISCHARGE":"Ценовая возможность использовать накопленную энергию; фактическая нагрузка пока не проверена.",
               "HOLD":"В этом интервале выгоднее сохранить заряд или не выполнять цикл."}
    for w in windows:
        w["reason"] = reasons[w["action"]]
    result = dict(kind="price_opportunity_only", executable=False, slots=slots, windows=windows,
                initial_soc_grid_percent=start, final_soc_percent=level,
                summary=dict(purchase_cost_eur=purchase_cost, potential_avoided_cost_eur=avoided_cost,
                             wear_cost_eur=wear, potential_cash_benefit_eur=cash_benefit,
                             terminal_value_change_eur=terminal_delta, objective_benefit_eur=cash_benefit+terminal_delta,
                             charge_from_grid_kwh=imported, potential_use_kwh=delivered,
                             equivalent_cycles=discharged/((hi-lo)*step)),
                assumptions=["Hypothetical battery; no real SOC or household load supplied.",
                             "Discharge value assumes enough household demand; this is not verified savings.",
                             "Power limits and wear are battery-side; prices exclude tariff add-ons.",
                             "Terminal value is a modelling assumption, not cash income."])

    if limits is not None:
        result["planning_constraints"] = dict(
            horizon_hours=limits.horizon_hours, max_charge_windows=limits.max_charge_windows,
            max_discharge_windows=limits.max_discharge_windows,
            max_grid_charge_kw=limits.max_grid_charge_kw,
            effective_charge_battery_kw=min(b.max_charge_kw, limits.max_grid_charge_kw * eta),
            window_semantics="Contiguous active UTC intervals; HOLD closes a window",
            terminal_value_eur_kwh=terminal,
        )
    return result


def optimize_periods(prices: list[Price], battery: Battery, now: datetime,
                     limits: PeriodLimits = PeriodLimits()) -> dict:
    """Choose up to two charge/use windows on the next 24h, with no I/O."""
    return optimize(prices, battery, now, limits)
