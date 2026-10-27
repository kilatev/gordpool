import asyncio
import importlib.util
import json
import tempfile
import unittest
from dataclasses import asdict
from datetime import datetime
from pathlib import Path
from unittest.mock import AsyncMock, patch

import httpx

from solax_optimizer.api import create_app
from solax_optimizer.optimizer import UTC
from solax_optimizer.scheduler import build_scheduler, refresh_due
from solax_optimizer.service import PriceStore
from tests.test_optimizer import T, curve


class ApiTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.path = Path(self.temp.name)/"prices.json"
        self.app = create_app(self.path)
        self.client = httpx.AsyncClient(transport=httpx.ASGITransport(app=self.app),base_url="http://test")

    async def asyncTearDown(self):
        await self.client.aclose()
        self.temp.cleanup()

    async def test_health_and_missing_snapshot(self):
        self.assertFalse((await self.client.get("/healthz")).json()["equipment_control"])
        self.assertEqual((await self.client.get("/v1/prices")).status_code,503)
        self.assertEqual((await self.client.post("/v1/plan",json={})).status_code,503)

    async def test_plan_supplied_and_validation(self):
        rows=[dict(start=p.start.isoformat(),end=p.end.isoformat(),eur_mwh=p.eur_mwh) for p in curve([10,250])]
        data=dict(prices=rows,now=T.isoformat())
        response=await self.client.post("/v1/plan",json=data)
        self.assertEqual(response.status_code,200,response.text)
        self.assertFalse(response.json()["executable"])
        data["battery"]={"capacity_kwh":-1}
        self.assertEqual((await self.client.post("/v1/plan",json=data)).status_code,422)
        self.assertEqual((await self.client.post("/v1/plan",json={"prices":[]})).status_code,422)
        data.pop("battery")
        data["now"]="2026-10-27T00:00:00"
        self.assertEqual((await self.client.post("/v1/plan",json=data)).status_code,422)

    async def test_refresh_and_cached_plan(self):
        with patch("solax_optimizer.service.fetch_prices",new=AsyncMock(return_value=(curve([10,250]),["tomorrow unavailable"]))):
            r=await self.client.post("/v1/prices/refresh")
        self.assertEqual(r.status_code,200,r.text)
        self.assertEqual((await self.client.get("/v1/prices")).status_code,200)
        r=await self.client.post("/v1/plan",json={"now":T.isoformat()})
        self.assertEqual(r.status_code,200)
        self.assertEqual(r.json()["price_source"]["warnings"],["tomorrow unavailable"])
        original=self.path.read_bytes()
        with patch("solax_optimizer.service.fetch_prices",new=AsyncMock(side_effect=ValueError("offline"))):
            self.assertEqual((await self.client.post("/v1/prices/refresh")).status_code,503)
        self.assertEqual(self.path.read_bytes(),original)
        self.path.write_text("broken")
        self.assertEqual((await self.client.get("/v1/prices")).status_code,503)


class StoreTests(unittest.IsolatedAsyncioTestCase):
    async def test_atomic_failure_cleanup(self):
        with tempfile.TemporaryDirectory() as d:
            path=Path(d)/"prices.json"
            path.write_text("old")
            with patch("solax_optimizer.service.fetch_prices",new=AsyncMock(return_value=(curve([1]),[]))), patch("os.replace",side_effect=OSError("disk")):
                with self.assertRaises(OSError): await PriceStore(path).refresh()
            self.assertEqual(path.read_text(),"old")
            self.assertEqual(list(Path(d).iterdir()),[path])

    async def test_refreshes_do_not_overlap(self):
        active=peak=0
        async def fetch(*args):
            nonlocal active,peak
            active+=1
            peak=max(active,peak)
            await asyncio.sleep(.001)
            active-=1
            return curve([1]),[]
        with tempfile.TemporaryDirectory() as d:
            store=PriceStore(Path(d)/"prices.json")
            with patch("solax_optimizer.service.fetch_prices",new=fetch):
                await asyncio.gather(store.refresh(),store.refresh())
            self.assertEqual(peak,1)


class SchedulerTests(unittest.TestCase):
    @unittest.skipUnless(importlib.util.find_spec("rocketry"), "Rocketry is not installed")
    def test_real_rocketry_registration(self):
        with tempfile.TemporaryDirectory() as d:
            scheduler = build_scheduler(PriceStore(Path(d)/"prices.json"))
            self.assertEqual(len(scheduler.session.tasks), 1)
            self.assertEqual(scheduler.session.config.task_execution, "async")

    def test_riga_windows_in_summer_and_winter(self):
        for value,expected in [("2026-07-01T09:00:00+00:00",True), # Riga 12
                               ("2026-12-01T10:00:00+00:00",True),
                               ("2026-12-01T09:00:00+00:00",False),
                               ("2026-12-01T16:00:00+00:00",False),
                               ("2026-12-01T22:01:00+00:00",True),
                               ("2026-12-01T22:16:00+00:00",False)]:
            self.assertEqual(refresh_due(datetime.fromisoformat(value)),expected)
        with self.assertRaises(ValueError):
            refresh_due(datetime(2026,10,27))
