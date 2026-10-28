import itertools
import math
import random
import unittest
from dataclasses import replace
from datetime import datetime, timedelta

from solax_optimizer.optimizer import Battery, Price, UTC, optimize, validate_prices

T = datetime(2026, 10, 27, tzinfo=UTC)


def curve(values, start=T):
    return [Price(start+timedelta(minutes=15*i), start+timedelta(minutes=15*(i+1)), p) for i,p in enumerate(values)]


def ideal(**kw):
    return replace(Battery(), round_trip_efficiency=1, degradation_eur_kwh=0,
                   stored_energy_value_eur_kwh=0, **kw)


class OptimizerTests(unittest.TestCase):
    def test_go_arbitrage_golden(self):
        # Go TestOptimizeBatteryArbitrage uses hourly slots and 10kW power.
        # Repeat each price four times to preserve that physical scenario.
        prices = curve([p*10 for p in [20,2,3,25,30,10] for _ in range(4)])
        plan = optimize(prices, ideal(max_charge_kw=10, max_discharge_kw=10), T)
        s = plan["summary"]
        self.assertAlmostEqual(s["charge_from_grid_kwh"], 8)
        self.assertAlmostEqual(s["potential_use_kwh"], 8)
        self.assertAlmostEqual(s["objective_benefit_eur"], 2.24)
        self.assertEqual({i//4 for i,s in enumerate(plan["slots"]) if s["action"]=="CHARGE"}, {1})
        self.assertEqual({i//4 for i,s in enumerate(plan["slots"]) if s["action"]=="DISCHARGE"}, {4})

    def test_waits_for_cheapest_and_highest(self):
        b = ideal(capacity_kwh=1, max_charge_kw=4, max_discharge_kw=4)
        plan = optimize(curve([100,70,40,10,50,80,150,250]), b, T)
        self.assertEqual([s["action"] for s in plan["slots"]], ["HOLD"]*3+["CHARGE"]+["HOLD"]*3+["DISCHARGE"])

    def test_multiple_cycles(self):
        b = ideal(capacity_kwh=1, max_charge_kw=4, max_discharge_kw=4)
        p = optimize(curve([10,250,10,250]), b, T)
        self.assertAlmostEqual(p["summary"]["equivalent_cycles"], 2)

    def test_no_unprofitable_cycle(self):
        for values, b in [([100]*8, Battery()), ([100,105]*4, Battery()),
                          ([100,120]*4, replace(Battery(), degradation_eur_kwh=.05))]:
            p = optimize(curve(values), replace(b, stored_energy_value_eur_kwh=0), T)
            self.assertTrue(all(s["action"]=="HOLD" for s in p["slots"]))

    def test_power_requires_adjacent_intervals(self):
        p = optimize(curve([10,20,30,40,250,240,230,220]), ideal(), T)
        self.assertGreater(sum(s["action"]=="CHARGE" for s in p["slots"]), 1)
        self.assertTrue(all(abs(s["power_battery_kw"]) <= 5+1e-9 for s in p["slots"]))

    def test_cycles_capped(self):
        b = ideal(capacity_kwh=1, max_charge_kw=4, max_discharge_kw=4, max_cycles_per_day=10)
        p = optimize(curve([10,250,10,250]), b, T)
        self.assertLessEqual(p["summary"]["equivalent_cycles"], 10/24+1e-9)

    def test_current_partial_and_past(self):
        p = optimize(curve([10,250]), ideal(), T+timedelta(minutes=10))
        self.assertEqual(p["slots"][0]["start"], (T+timedelta(minutes=10)).isoformat())
        self.assertLessEqual(p["slots"][0]["charge_from_grid_kwh"], 5/12+1e-8)
        p = optimize(curve([10,250]), ideal(), T+timedelta(minutes=15))
        self.assertEqual(len(p["slots"]), 1)

    def test_empty_past_invalid_and_gap(self):
        for prices in [[], curve([1], T-timedelta(days=1)), curve([1])+curve([2], T+timedelta(hours=1))]:
            with self.assertRaises(ValueError): optimize(prices, Battery(), T)
        for prices in [curve([float("nan")]), curve([1])*2, [Price(T,T+timedelta(hours=1),1)],
                       [Price(T.replace(tzinfo=None), T+timedelta(minutes=15),1)]]:
            with self.assertRaises(ValueError): validate_prices(prices)
        with self.assertRaises(ValueError): optimize(curve([1]*201), Battery(), T)

    def test_battery_validation(self):
        for key,value in [("capacity_kwh",0),("max_charge_kw",-1),("round_trip_efficiency",1.1),
                          ("min_soc_percent",95),("initial_soc_percent",0),("degradation_eur_kwh",-1),
                          ("max_cycles_per_day",-1),("max_discharge_kw",float("inf"))]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                optimize(curve([1,2]), replace(Battery(), **{key:value}), T)
        with self.assertRaises(ValueError):
            optimize(curve([1]), replace(Battery(), min_soc_percent=10.1,max_soc_percent=10.9,initial_soc_percent=10.5), T)

    def test_zero_power_and_negative_prices(self):
        p = optimize(curve([-100,250]), ideal(max_charge_kw=0,max_discharge_kw=0), T)
        self.assertTrue(all(s["action"]=="HOLD" for s in p["slots"]))
        p = optimize(curve([-100,250]), ideal(), T)
        self.assertGreater(p["summary"]["objective_benefit_eur"], 0)
        self.assertFalse(p["executable"])

    def test_energy_conservation_random(self):
        r = random.Random(17)
        for _ in range(25):
            b = replace(Battery(), initial_soc_percent=r.randint(10,90))
            p = optimize(curve([r.uniform(-100,300) for _ in range(12)]), b, T)
            eta = math.sqrt(b.round_trip_efficiency)
            for s in p["slots"]:
                delta = (s["soc_end_percent"]-s["soc_start_percent"])*b.capacity_kwh/100
                self.assertAlmostEqual(delta, s["charge_from_grid_kwh"]*eta-s["potential_use_kwh"]/eta)
                self.assertTrue(10 <= s["soc_end_percent"] <= 90)
                self.assertLessEqual(abs(s["power_battery_kw"]), 5+1e-9)
            self.assertGreaterEqual(p["summary"]["objective_benefit_eur"], -1e-8)

    def test_matches_exhaustive_search(self):
        # Independent enumeration on a narrow SOC window, with loss and wear.
        for values in ([100,10,250], [-100,20,10], [20,20,20]):
            b = replace(Battery(), min_soc_percent=10,max_soc_percent=13, initial_soc_percent=11,
                        stored_energy_value_eur_kwh=.04)
            eta, step = math.sqrt(.85), .1
            best = float("inf")
            for tail in itertools.product(range(10,14), repeat=3):
                path = (11,)+tail
                cost = 0
                for t,price in enumerate(values):
                    e = (path[t+1]-path[t])*step
                    cost += price/1000*(e/eta if e>0 else e*eta)+max(-e,0)*.03
                cost -= (path[-1]-11)*step*.04
                best = min(best,cost)
            result = optimize(curve(values), b, T)
            self.assertAlmostEqual(result["summary"]["objective_benefit_eur"], -best)
