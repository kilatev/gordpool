"""Reproduce an API plan offline from a JSON request file."""
import json
import sys
from dataclasses import fields
from datetime import datetime
from pathlib import Path

from .api import PlanInput
from .optimizer import Battery, Price, UTC, optimize


def main():
    request = PlanInput(**json.loads(Path(sys.argv[1]).read_text()))
    if request.prices is None or request.now is None:
        raise ValueError("Offline replay requires prices and now")
    result = optimize([Price(p.start,p.end,p.eur_mwh) for p in request.prices],
                      Battery(**{f.name:getattr(request.battery,f.name) for f in fields(Battery)}),request.now)
    print(json.dumps(result,ensure_ascii=False,indent=2,allow_nan=False))


if __name__ == "__main__":
    main()
