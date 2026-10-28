"""Atomic JSON price snapshot; no database, secrets or equipment access."""
import asyncio
import json
import logging
import os
import tempfile
from dataclasses import asdict
from datetime import datetime
from pathlib import Path

import httpx

from .optimizer import Price, UTC, validate_prices
from .prices import fetch_prices

log = logging.getLogger(__name__)


class PriceStore:
    def __init__(self, path: Path):
        self.path = path
        self.lock = asyncio.Lock()

    async def refresh(self):
        async with self.lock:
            now = datetime.now(UTC)
            async with httpx.AsyncClient() as client:
                rows, warnings = await fetch_prices(client, now)
            data = dict(fetched_at=now.isoformat(), warnings=warnings,
                        prices=[{**asdict(p), "start":p.start.isoformat(), "end":p.end.isoformat()} for p in rows])
            self.path.parent.mkdir(parents=True, exist_ok=True)
            name = None
            try:
                with tempfile.NamedTemporaryFile(mode="w", dir=self.path.parent, delete=False) as f:
                    name = f.name
                    json.dump(data, f, allow_nan=False)
                    f.flush()
                    os.fsync(f.fileno())
                os.replace(name, self.path)
            finally:
                if name and os.path.exists(name):
                    os.unlink(name)
            log.info("Prices refreshed: %s intervals; warnings=%s", len(rows), warnings)
            return data

    def read(self):
        data = json.loads(self.path.read_text())
        rows = validate_prices([Price(datetime.fromisoformat(p["start"]), datetime.fromisoformat(p["end"]),
                                      p["eur_mwh"]) for p in data["prices"]])
        return rows, data
