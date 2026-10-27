"""Run separately from Uvicorn: one Rocketry process guarded with flock."""
import fcntl
import asyncio
import logging
from datetime import datetime
from pathlib import Path
from zoneinfo import ZoneInfo

from .optimizer import UTC, utc
from .service import PriceStore


def refresh_due(now: datetime) -> bool:
    local = utc(now).astimezone(ZoneInfo("Europe/Riga"))
    # Poll for publication, do not assume a fixed guaranteed release minute.
    return (12 <= local.hour < 18) or (local.hour == 0 and local.minute < 15)


def build_scheduler(store: PriceStore):
    from rocketry import Rocketry
    app = Rocketry(config={"task_execution":"async"})

    @app.task("every 15 minutes")
    async def refresh_prices():
        if refresh_due(datetime.now(UTC)):
            await store.refresh()

    return app


def main():
    logging.basicConfig(level=logging.INFO)
    Path("state").mkdir(exist_ok=True)
    with open("state/scheduler.lock", "w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        store = PriceStore(Path("state/prices.json"))
        try:
            asyncio.run(store.refresh())
        except (OSError, ValueError):
            logging.exception("Startup refresh failed; previous snapshot retained")
        build_scheduler(store).run()


if __name__ == "__main__":
    main()
