"""QuickCheck-style generated cases plus an independent exhaustive oracle."""
import itertools
import math
import random
import unittest
from dataclasses import replace
from datetime import datetime, timedelta
from unittest.mock import patch
from zoneinfo import ZoneInfo

from solax_optimizer.optimizer import Battery, UTC, optimize, optimize_periods
from solax_optimizer.periods import PeriodLimits
from tests.test_optimizer import T, curve, ideal


def oracle(prices, battery, limits):
    """Enumerate every SOC trajectory; never call the production planner."""
    lo, hi = math.ceil(battery.min_soc_percent), math.floor(battery.max_soc_percent)
    initial = math.floor(battery.initial_soc_percent + .5)
    step, eta = battery.capacity_kwh / 100, math.sqrt(battery.round_trip_efficiency)
    terminal = battery.stored_energy_value_eur_kwh
    if terminal is None:
        from statistics import median
        terminal = median(p.eur_mwh / 1000 for p in prices)
    hours = sum((p.end - p.start).total_seconds() / 3600 for p in prices)
    cap = battery.max_cycles_per_day * hours / 24 * (hi - lo) * step
    best = -math.inf
    for tail in itertools.product(range(lo, hi + 1), repeat=len(prices)):
        path, previous, counts = (initial,) + tail, 0, {1: 0, -1: 0}
        cash = discharged = 0.0
        for i, price in enumerate(prices):
            energy = (path[i + 1] - path[i]) * step
            duration = (price.end - price.start).total_seconds() / 3600
            grid = energy / eta if energy > 0 else energy * eta
            if (energy > battery.max_charge_kw * duration + 1e-10
                or grid > limits.max_grid_charge_kw * duration + 1e-10
                or -energy > battery.max_discharge_kw * duration + 1e-10):
                break
            action = (energy > 0) - (energy < 0)
            if action and action != previous:
                counts[action] += 1
            previous = action
            if counts[1] > limits.max_charge_windows or counts[-1] > limits.max_discharge_windows:
                break
            discharged += max(-energy, 0)
            cash -= grid * price.eur_mwh / 1000 + max(-energy, 0) * battery.degradation_eur_kwh
        else:
            if battery.max_cycles_per_day and discharged > cap + 1e-10:
                continue
            best = max(best, cash + (path[-1] - initial) * step * terminal)
    return best


class PeriodTests(unittest.TestCase):
    def test_night_charge_needs_multiple_quarters(self):
        b = ideal(max_charge_kw=6)
        prices = curve([20] * 12 + [100] * 12 + [400] * 12)
        plan = optimize_periods(prices, b, T)
        charged = [s for s in plan["slots"] if s["action"] == "CHARGE"]
        self.assertGreaterEqual(len(charged), 6)  # 8 kWh cannot fit into a 1.5 kWh quarter.
        self.assertAlmostEqual(plan["summary"]["charge_from_grid_kwh"], 8)
        self.assertAlmostEqual(plan["summary"]["potential_use_kwh"], 8)
        self.assertAlmostEqual(plan["summary"]["potential_cash_benefit_eur"], 3.04)

    def test_best_two_of_three_separated_opportunities(self):
        b = ideal(capacity_kwh=1, max_charge_kw=6, max_discharge_kw=4)
        p = optimize_periods(curve([10, 100, 10, 400, 10, 300]), b, T)
        self.assertEqual([i for i, s in enumerate(p["slots"]) if s["action"] == "DISCHARGE"], [3, 5])
        charges = [i for i, s in enumerate(p["slots"]) if s["action"] == "CHARGE"]
        self.assertIn(charges, ([0, 4], [2, 4]))  # Equal-price charging times are interchangeable.
        self.assertAlmostEqual(p["summary"]["potential_cash_benefit_eur"], .544)

    def test_grid_power_limit_includes_charging_losses(self):
        b = replace(Battery(), max_charge_kw=100, stored_energy_value_eur_kwh=0)
        p = optimize_periods(curve([1] * 8 + [400] * 8), b, T)
        for s in p["slots"]:
            hours = (datetime.fromisoformat(s["end"]) - datetime.fromisoformat(s["start"])).total_seconds() / 3600
            self.assertLessEqual(s["charge_from_grid_kwh"] / hours, 6 + 1e-9)

    def test_partial_intervals_clip_both_ends_of_24h(self):
        now = T + timedelta(minutes=7)
        prices = [100] * 100
        prices[0] = prices[96] = -100
        p = optimize_periods(curve(prices), ideal(max_charge_kw=6), now)
        self.assertEqual(p["slots"][0]["start"], now.isoformat())
        self.assertEqual(p["slots"][-1]["end"], (now + timedelta(hours=24)).isoformat())
        self.assertEqual(len(p["slots"]), 97)
        self.assertGreater(p["slots"][0]["charge_from_grid_kwh"], 0)
        self.assertGreater(p["slots"][-1]["charge_from_grid_kwh"], 0)
        self.assertLessEqual(p["slots"][0]["charge_from_grid_kwh"], .8 + 1e-9)
        self.assertLessEqual(p["slots"][-1]["charge_from_grid_kwh"], .7 + 1e-9)

    def test_new_soc_replans_without_reading_cloud(self):
        prices = curve([10] * 12 + [400] * 12)
        with patch("socket.create_connection", side_effect=AssertionError("No network")), \
             patch("subprocess.run", side_effect=AssertionError("No browser or commands")):
            empty = optimize_periods(prices, ideal(max_charge_kw=6), T)
            full = optimize_periods(prices, ideal(max_charge_kw=6, initial_soc_percent=90), T)
        self.assertAlmostEqual(empty["summary"]["charge_from_grid_kwh"], 8)
        self.assertAlmostEqual(full["summary"]["charge_from_grid_kwh"], 0)

    def test_dst_horizon_is_elapsed_24h_and_windows_cross_midnight(self):
        for day in (datetime(2026, 3, 28, 23, tzinfo=UTC), datetime(2026, 10, 24, 23, tzinfo=UTC)):
            p = optimize_periods(curve([10] * 16 + [400] * 84, day), ideal(max_charge_kw=6), day)
            self.assertEqual(datetime.fromisoformat(p["slots"][-1]["end"]) - day, timedelta(hours=24))
            local = day.astimezone(ZoneInfo("Europe/Riga"))
            other = optimize_periods(curve([10] * 16 + [400] * 84, day), ideal(max_charge_kw=6), local)
            self.assertEqual(p, other)

    def test_validation_and_zero_power(self):
        for limits in (PeriodLimits(horizon_hours=0), PeriodLimits(horizon_hours=25),
                       PeriodLimits(max_charge_windows=3), PeriodLimits(max_charge_windows=True),
                       PeriodLimits(max_discharge_windows=-1), PeriodLimits(max_grid_charge_kw=7),
                       PeriodLimits(max_grid_charge_kw=float("nan"))):
            with self.subTest(limits=limits), self.assertRaises(ValueError):
                optimize_periods(curve([1]), Battery(), T, limits)
        p = optimize_periods(curve([-100, 400]), ideal(), T, PeriodLimits(max_grid_charge_kw=0))
        self.assertTrue(all(s["action"] == "HOLD" for s in p["slots"]))
        with self.assertRaises(ValueError):
            optimize_periods(curve([1]), replace(Battery(), min_soc_percent=10, max_soc_percent=10.9), T)

    def test_equal_prices_do_not_create_gratuitous_cycles(self):
        p = optimize_periods(curve([100] * 96), ideal(max_charge_kw=6), T)
        self.assertTrue(all(s["action"] == "HOLD" for s in p["slots"]))

    def test_midnight_does_not_split_one_contiguous_charge_window(self):
        start = datetime(2026, 10, 27, 21, 30, tzinfo=UTC)  # Riga 23:30.
        p = optimize_periods(curve([10] * 6 + [400] * 8, start), ideal(max_charge_kw=6), start)
        windows = [w for w in p["windows"] if w["action"] == "CHARGE"]
        self.assertEqual(len(windows), 1)
        local = ZoneInfo("Europe/Riga")
        self.assertNotEqual(datetime.fromisoformat(windows[0]["start"]).astimezone(local).date(),
                            datetime.fromisoformat(windows[0]["end"]).astimezone(local).date())

    def test_prices_after_horizon_do_not_change_decisions_or_terminal_median(self):
        prices = curve([100] * 96)
        b = replace(Battery(), max_soc_percent=15)
        first = optimize_periods(prices, b, T)
        extended = optimize_periods(prices + curve([10000] * 96, T + timedelta(days=1)), b, T)
        self.assertEqual(first, extended)

    def test_generated_optimality_against_exhaustive_search(self):
        rng = random.Random(73109)
        for case in range(100):
            b = replace(Battery(), min_soc_percent=10, max_soc_percent=13,
                        initial_soc_percent=rng.randint(10, 13), capacity_kwh=rng.choice([1, 10]),
                        max_charge_kw=rng.choice([0, .08, 6]), max_discharge_kw=rng.choice([0, .08, 5]),
                        round_trip_efficiency=rng.choice([.64, .85, 1]),
                        degradation_eur_kwh=rng.choice([0, .03, .2]),
                        stored_energy_value_eur_kwh=rng.choice([None, 0, .05]),
                        max_cycles_per_day=rng.choice([0, 1, 6]))
            limits = PeriodLimits(max_charge_windows=rng.randint(0, 2), max_discharge_windows=rng.randint(0, 2))
            prices = curve([rng.choice([-100, 0, 20, 200, 400]) for _ in range(rng.randint(1, 5))])
            with self.subTest(case=case, battery=b, limits=limits, prices=prices):
                p = optimize_periods(prices, b, T, limits)
                self.assertAlmostEqual(p["summary"]["objective_benefit_eur"], oracle(prices, b, limits), places=9)

    def test_generated_energy_windows_and_relaxation_properties(self):
        rng = random.Random(93671)
        for case in range(80):
            b = replace(Battery(), min_soc_percent=10, max_soc_percent=18,
                        initial_soc_percent=rng.randint(10, 18), max_charge_kw=6,
                        round_trip_efficiency=rng.uniform(.6, 1), degradation_eur_kwh=rng.uniform(0, .1),
                        stored_energy_value_eur_kwh=rng.choice([None, 0, .03]),
                        max_cycles_per_day=rng.choice([0, 1, 6]))
            prices = curve([rng.uniform(-100, 400) for _ in range(12)])
            p = optimize_periods(prices, b, T)
            with self.subTest(case=case):
                runs = [action for action, _ in itertools.groupby(s["action"] for s in p["slots"])]
                self.assertEqual(runs, [w["action"] for w in p["windows"]])
                for action in ("CHARGE", "DISCHARGE"):
                    self.assertLessEqual(runs.count(action), 2)
                for key in ("charge_from_grid_kwh", "potential_use_kwh"):
                    self.assertAlmostEqual(sum(w[key] for w in p["windows"]), p["summary"][key])
                eta = math.sqrt(b.round_trip_efficiency)
                previous = p["initial_soc_grid_percent"]
                for slot in p["slots"]:
                    self.assertEqual(slot["soc_start_percent"], previous)
                    previous = slot["soc_end_percent"]
                    self.assertTrue(10 <= previous <= 18)
                    delta = (slot["soc_end_percent"] - slot["soc_start_percent"]) * b.capacity_kwh / 100
                    self.assertAlmostEqual(delta, slot["charge_from_grid_kwh"] * eta - slot["potential_use_kwh"] / eta)
                    self.assertLessEqual(slot["charge_from_grid_kwh"], 1.5 + 1e-9)
                restricted = optimize_periods(prices, b, T, PeriodLimits(max_charge_windows=1, max_discharge_windows=1))
                self.assertGreaterEqual(p["summary"]["objective_benefit_eur"] + 1e-9,
                                        restricted["summary"]["objective_benefit_eur"])
                unlimited = optimize(prices, b, T)
                self.assertLessEqual(p["summary"]["objective_benefit_eur"],
                                     unlimited["summary"]["objective_benefit_eur"] + 1e-7)


if __name__ == "__main__":
    unittest.main()
