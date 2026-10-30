"""Refresh prices and optionally apply the optimizer plan through SolaX CDP."""
import fcntl
import asyncio
import logging
from datetime import datetime
from pathlib import Path
from zoneinfo import ZoneInfo

from .optimizer import UTC, utc
from .service import PriceStore
from .solax_control import apply_best_plan, configured


def refresh_due(now: datetime) -> bool:
    local = utc(now).astimezone(ZoneInfo("Europe/Riga"))
    # Poll for publication, do not assume a fixed guaranteed release minute.
    return (12 <= local.hour < 18) or (local.hour == 0 and local.minute < 15)


def build_scheduler(store: PriceStore, control=None):
    from rocketry import Rocketry
    app = Rocketry(config={"task_execution":"async"})

    @app.task("every 15 minutes")
    async def refresh_prices():
        if refresh_due(datetime.now(UTC)):
            try:
                await store.refresh()
                if control:
                    rows, _ = store.read()
                    await asyncio.to_thread(apply_best_plan, rows, *control)
            except Exception:
                logging.exception("Scheduled refresh/control failed")

    return app


def main():
    logging.basicConfig(level=logging.INFO)
    control = configured()
    if control:
        logging.info("SolaX scheduled control enabled for inverter %s", control[0])
    Path("state").mkdir(exist_ok=True)
    with open("state/scheduler.lock", "w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        store = PriceStore(Path("state/prices.json"))
        try:
            asyncio.run(store.refresh())
            if control:
                rows, _ = store.read()
                asyncio.run(asyncio.to_thread(apply_best_plan, rows, *control))
        except Exception:
            logging.exception("Startup refresh/control failed; scheduler will continue")
        build_scheduler(store, control).run()


if __name__ == "__main__":
    main()
