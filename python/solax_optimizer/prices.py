from datetime import datetime, time, timedelta
from zoneinfo import ZoneInfo

import httpx

from .optimizer import Price, UTC, utc, validate_prices

NORDPOOL_URL = "https://dataportal-api.nordpoolgroup.com/api/DayAheadPrices"
# API deliveryDateCET is a trading day, not a UTC calendar day.
MARKET_ZONE = ZoneInfo("Europe/Stockholm")


def parse_day(payload: dict, delivery_date) -> list[Price]:
    entries = payload.get("multiAreaEntries")
    if entries == []:
        return []  # Explicitly unpublished; never invent prices.
    if not isinstance(entries, list):
        raise ValueError("Missing Nord Pool multiAreaEntries")
    if payload.get("currency", "EUR") != "EUR":
        raise ValueError("Expected EUR prices")
    rows = []
    for e in entries:
        rows.append(Price(datetime.fromisoformat(e["deliveryStart"].replace("Z", "+00:00")),
                          datetime.fromisoformat(e["deliveryEnd"].replace("Z", "+00:00")),
                          float(e["entryPerArea"]["LV"])))
    rows = validate_prices(rows)
    start = datetime.combine(delivery_date, time(), MARKET_ZONE).astimezone(UTC)
    end = datetime.combine(delivery_date + timedelta(days=1), time(), MARKET_ZONE).astimezone(UTC)
    if rows[0].start != start or rows[-1].end != end:
        raise ValueError("Incomplete or wrong Nord Pool delivery day")
    return rows


async def fetch_prices(client: httpx.AsyncClient, now: datetime) -> tuple[list[Price], list[str]]:
    today = utc(now).astimezone(MARKET_ZONE).date()
    rows, warnings = [], []
    for offset in range(2):
        day = today + timedelta(days=offset)
        try:
            response = await client.get(NORDPOOL_URL, params=dict(date=day.isoformat(), market="DayAhead",
                                        deliveryArea="LV", currency="EUR"), timeout=15)
            response.raise_for_status()
            result = parse_day(response.json(), day)
            if not result:
                warnings.append(f"No published prices for {day}")
            rows.extend(result)
        except (httpx.HTTPError, ValueError, KeyError, TypeError) as exc:
            if not rows:
                raise ValueError(f"Cannot load Nord Pool day {day}") from exc
            warnings.append(f"Cannot load {day}: {type(exc).__name__}; using available horizon")
    rows = validate_prices(rows)
    if not any(p.end > utc(now) for p in rows):
        raise ValueError("No current or future published prices")
    return rows, warnings
