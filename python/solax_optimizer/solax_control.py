"""Apply the optimizer's best contiguous charge and discharge windows to SolaX."""
import os
import subprocess
import sys
from datetime import datetime, timedelta
from pathlib import Path
from zoneinfo import ZoneInfo

from .optimizer import Battery, UTC, optimize


def _quarter(value):
    remainder = value.minute % 15
    if remainder or value.second or value.microsecond:
        value = value.replace(second=0, microsecond=0) + timedelta(minutes=15 - remainder)
    return value


def _blocks(slots, action, zone):
    blocks, block = [], []
    for slot in slots:
        if slot["action"] != action:
            if block:
                blocks.append(block)
                block = []
            continue
        if block and block[-1]["end"] != slot["start"]:
            blocks.append(block)
            block = []
        block.append(slot)
    if block:
        blocks.append(block)

    result = []
    for rows in blocks:
        start = _quarter(datetime.fromisoformat(rows[0]["start"]))
        end = datetime.fromisoformat(rows[-1]["end"])
        if start >= end:
            continue
        quantity = "charge_from_grid_kwh" if action == "CHARGE" else "potential_use_kwh"
        energy = sum(row[quantity] for row in rows)
        if not energy:
            continue
        average_price = sum(row[quantity] * row["price_eur_mwh"] for row in rows) / energy
        result.append(((start.astimezone(zone), end.astimezone(zone)), average_price))
    return result


def best_periods(plan, timezone="Europe/Riga"):
    zone = ZoneInfo(timezone)
    charge = _blocks(plan["slots"], "CHARGE", zone)
    discharge = _blocks(plan["slots"], "DISCHARGE", zone)
    charge_period = min(charge, key=lambda item: item[1])[0] if charge else None
    discharge_period = max(discharge, key=lambda item: item[1])[0] if discharge else None
    fmt = lambda period: tuple(value.strftime("%H:%M") for value in period) if period else ("00:00", "00:00")
    return fmt(charge_period), fmt(discharge_period)


def apply_best_plan(rows, serial, cdp_url="http://127.0.0.1:9222", timezone="Europe/Riga"):
    plan = optimize(rows, Battery(), datetime.now(UTC))
    charge, discharge = best_periods(plan, timezone)
    command = [sys.executable, "-m", "solax_optimizer.solax_charge",
               "--cdp-url", cdp_url, "--serial", serial,
               "--start", charge[0], "--end", charge[1],
               "--discharge-start", discharge[0], "--discharge-end", discharge[1]]
    subprocess.run(command, cwd=Path(__file__).resolve().parents[1], check=True, timeout=180)
    return plan, charge, discharge


def configured():
    if os.getenv("SOLAX_CONTROL_ENABLED") != "1":
        return None
    serial = os.getenv("SOLAX_SERIAL")
    if not serial:
        raise ValueError("SOLAX_SERIAL is required when SOLAX_CONTROL_ENABLED=1")
    timezone = os.getenv("SOLAX_TIMEZONE", "Europe/Riga")
    ZoneInfo(timezone)
    return serial, os.getenv("SOLAX_CDP_URL", "http://127.0.0.1:9222"), timezone
