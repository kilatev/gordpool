"""Reproduce an API plan offline from a JSON request file."""
import argparse
import json
from dataclasses import fields
from datetime import datetime
from pathlib import Path

from .api import PlanInput, PeriodPlanInput
from .optimizer import Battery, Price, UTC, optimize, optimize_periods


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("request", type=Path)
    parser.add_argument("--periods", action="store_true", help="Limit to 24h and two windows per action, no equipment access")
    args = parser.parse_args()
    model = PeriodPlanInput if args.periods else PlanInput
    request = model(**json.loads(args.request.read_text()))
    if request.prices is None or request.now is None:
        raise ValueError("Offline replay requires prices and now")
    planner = optimize_periods if args.periods else optimize
    result = planner([Price(p.start,p.end,p.eur_mwh) for p in request.prices],
                      Battery(**{f.name:getattr(request.battery,f.name) for f in fields(Battery)}),request.now)
    print(json.dumps(result,ensure_ascii=False,indent=2,allow_nan=False))


if __name__ == "__main__":
    main()
