"""Apply the optimizer's best contiguous charge and discharge windows to SolaX."""
import os
import subprocess
import sys
from datetime import datetime
from pathlib import Path
from zoneinfo import ZoneInfo

from .optimizer import Battery, UTC, optimize


def best_periods(plan, timezone="Europe/Riga"):
    zone = ZoneInfo(timezone)
    def period(slot):
        return tuple(datetime.fromisoformat(slot[key]).astimezone(zone).strftime("%H:%M")
                     for key in ("start", "end"))

    charge = min((s for s in plan["slots"] if s["action"] == "CHARGE"),
                 key=lambda s: s["price_eur_mwh"], default=None)
    discharge = max((s for s in plan["slots"] if s["action"] == "DISCHARGE"),
                    key=lambda s: s["price_eur_mwh"], default=None)
    return period(charge) if charge else ("00:00", "00:00"), \
        period(discharge) if discharge else ("00:00", "00:00")


def apply_best_plan(rows, serial, cdp_url="http://127.0.0.1:9222", timezone="Europe/Riga"):
    plan = optimize(rows, Battery(), datetime.now(UTC))
    charge, discharge = best_periods(plan, timezone)
    command = [sys.executable, "-m", "solax_optimizer.solax_charge",
               "--login", "--cdp-url", cdp_url, "--serial", serial,
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
