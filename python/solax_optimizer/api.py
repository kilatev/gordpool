from dataclasses import fields
from datetime import datetime
from pathlib import Path

from fastapi import FastAPI, HTTPException
from fastapi.responses import HTMLResponse, Response
from pydantic import BaseModel, Field

from .optimizer import Battery, Price, UTC, optimize
from .service import PriceStore


class PriceInput(BaseModel):
    start: datetime
    end: datetime
    eur_mwh: float


class BatteryInput(BaseModel):
    capacity_kwh: float = 10
    max_charge_kw: float = 5
    max_discharge_kw: float = 5
    initial_soc_percent: float = 10
    min_soc_percent: float = 10
    max_soc_percent: float = 90
    round_trip_efficiency: float = .85
    degradation_eur_kwh: float = .03
    stored_energy_value_eur_kwh: float | None = None
    max_cycles_per_day: float = 0


class PlanInput(BaseModel):
    prices: list[PriceInput] | None = None
    battery: BatteryInput = Field(default_factory=BatteryInput)
    now: datetime | None = None


def create_app(path: Path = Path("state/prices.json")):
    app = FastAPI(title="SolaX price opportunities", version="0.1.0")
    store = PriceStore(path)
    app.state.store = store
    static = Path(__file__).parent / "static"
    index = (static / "index.html").read_text()
    assets = {
        "report.js": ((static / "report.js").read_text(), "text/javascript"),
        "report.css": ((static / "report.css").read_text(), "text/css"),
    }

    @app.get("/", include_in_schema=False)
    async def report():
        return HTMLResponse(index)

    @app.get("/assets/{name}", include_in_schema=False)
    async def report_asset(name: str):
        if name not in assets:
            raise HTTPException(404, "Unknown report asset")
        content, media_type = assets[name]
        return Response(content, media_type=media_type)

    @app.get("/healthz")
    async def health():
        return {"status":"ok", "equipment_control":False}

    @app.get("/v1/prices")
    async def prices():
        try:
            _, data = store.read()
            return data
        except (OSError, ValueError, KeyError, TypeError) as exc:
            raise HTTPException(503, "No valid price snapshot; run scheduler or refresh") from exc

    @app.post("/v1/prices/refresh")
    async def refresh():
        try:
            return await store.refresh()
        except (OSError, ValueError) as exc:
            raise HTTPException(503, "Price refresh failed; previous snapshot retained") from exc

    @app.post("/v1/plan")
    async def plan(request: PlanInput):
        try:
            if request.prices is None:
                rows, meta = store.read()
            else:
                rows = [Price(p.start, p.end, p.eur_mwh) for p in request.prices]
                meta = {"source":"request", "warnings":[]}
            b = Battery(**{f.name:getattr(request.battery, f.name) for f in fields(Battery)})
            result = optimize(rows, b, request.now or datetime.now(UTC))
            result["price_source"] = {k:v for k,v in meta.items() if k != "prices"}
            return result
        except OSError as exc:
            raise HTTPException(503, "No price snapshot") from exc
        except (ValueError, KeyError, TypeError) as exc:
            raise HTTPException(422, str(exc)) from exc

    return app


app = create_app()
