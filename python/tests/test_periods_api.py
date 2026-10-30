"""Offline HTTP-source → snapshot → limited-period API integration."""
import json
import tempfile
import unittest
from datetime import date, datetime, timedelta
from pathlib import Path
from unittest.mock import patch

import httpx

from solax_optimizer.api import create_app
from tests.test_e2e import source_transport
from tests.test_optimizer import T, curve
from tests.test_prices import payload


class PeriodApiTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.path = Path(self.directory.name) / "prices.json"
        self.client = httpx.AsyncClient(transport=httpx.ASGITransport(app=create_app(self.path)), base_url="http://test")

    async def asyncTearDown(self):
        await self.client.aclose()
        self.directory.cleanup()

    async def test_supplied_prices_replan_with_new_soc_without_cloud(self):
        rows = [dict(start=p.start.isoformat(), end=p.end.isoformat(), eur_mwh=p.eur_mwh)
                for p in curve([10] * 12 + [400] * 12)]
        body = dict(prices=rows, now=T.isoformat(), battery=dict(max_charge_kw=6, stored_energy_value_eur_kwh=0))
        with patch.dict("os.environ", {"SOLAX_CONTROL_ENABLED": "1", "SOLAX_SERIAL": "not-a-real-device"}), \
             patch("subprocess.run", side_effect=AssertionError("No equipment/browser commands")), \
             patch("socket.create_connection", side_effect=AssertionError("No network")):
            empty = await self.client.post("/v1/plan/periods", json=body)
            body["battery"]["initial_soc_percent"] = 90
            full = await self.client.post("/v1/plan/periods", json=body)
        self.assertEqual(empty.status_code, 200, empty.text)
        self.assertEqual(full.status_code, 200, full.text)
        self.assertGreater(empty.json()["summary"]["charge_from_grid_kwh"], 8)
        self.assertEqual(full.json()["summary"]["charge_from_grid_kwh"], 0)
        self.assertFalse(empty.json()["executable"])
        self.assertFalse(self.path.exists())  # Inline prices do not create a source snapshot.

    async def test_mock_nordpool_snapshot_restart_and_period_plan(self):
        def source(request):
            data = payload(date.fromisoformat(request.url.params["date"]))
            for i, entry in enumerate(data["multiAreaEntries"]):
                entry["entryPerArea"]["LV"] = 10 if i < 32 else 400
            return httpx.Response(200, json=data)
        with source_transport(source), patch("solax_optimizer.service.datetime", wraps=datetime) as clock:
            clock.now.return_value = T
            response = await self.client.post("/v1/prices/refresh")
        self.assertEqual(response.status_code, 200, response.text)
        snapshot = json.loads(self.path.read_text())
        original = self.path.read_bytes()
        async with httpx.AsyncClient(transport=httpx.ASGITransport(app=create_app(self.path)), base_url="http://test") as restarted:
            response = await restarted.post("/v1/plan/periods", json={"now": T.isoformat()})
        self.assertEqual(response.status_code, 200, response.text)
        p = response.json()
        self.assertEqual(self.path.read_bytes(), original)
        self.assertEqual(p["price_source"]["fetched_at"], snapshot["fetched_at"])
        self.assertEqual(p["planning_constraints"]["max_grid_charge_kw"], 6)
        self.assertEqual(datetime.fromisoformat(p["slots"][-1]["end"]) - T, timedelta(hours=24))
        source_prices = {row["end"]: row["eur_mwh"] for row in snapshot["prices"]}
        for slot in p["slots"]:
            self.assertEqual(slot["price_eur_mwh"], source_prices[slot["end"]])
        for action in ("CHARGE", "DISCHARGE"):
            self.assertLessEqual(sum(w["action"] == action for w in p["windows"]), 2)

    async def test_no_snapshot_and_invalid_soc_fail_without_cloud(self):
        self.assertEqual((await self.client.post("/v1/plan/periods", json={})).status_code, 503)
        rows = [dict(start=p.start.isoformat(), end=p.end.isoformat(), eur_mwh=p.eur_mwh) for p in curve([10, 400])]
        body = dict(prices=rows, now=T.isoformat(), battery={"initial_soc_percent": None})
        self.assertEqual((await self.client.post("/v1/plan/periods", json=body)).status_code, 422)
        body["battery"]["initial_soc_percent"] = 95
        self.assertEqual((await self.client.post("/v1/plan/periods", json=body)).status_code, 422)

