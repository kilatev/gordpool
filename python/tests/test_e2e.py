"""API-to-source integration checks; API requests use ASGI, not a TCP server."""
import json
import os
import tempfile
import unittest
from contextlib import contextmanager
from datetime import date, datetime
from pathlib import Path
from unittest.mock import patch

import httpx

from solax_optimizer.api import create_app
from solax_optimizer.optimizer import UTC
from solax_optimizer.prices import NORDPOOL_URL
from tests.test_prices import payload


@contextmanager
def source_transport(handler):
    original = httpx.AsyncClient
    def client(*args, **kwargs):
        kwargs.setdefault("transport", httpx.MockTransport(handler))
        return original(*args, **kwargs)
    with patch("solax_optimizer.service.httpx.AsyncClient", new=client):
        yield


class PricePipelineTests(unittest.IsolatedAsyncioTestCase):
    async def test_missing_or_invalid_tomorrow_keeps_today_end_to_end(self):
        for tomorrow in (httpx.Response(503), httpx.Response(200, json={"multiAreaEntries": []}),
                         httpx.Response(200, text="not JSON")):
            with self.subTest(response=tomorrow.status_code, body=tomorrow.text):
                calls = []
                def source(request):
                    calls.append(request)
                    if len(calls) == 1:
                        return httpx.Response(200, json=payload(date.fromisoformat(request.url.params["date"])))
                    return tomorrow
                with tempfile.TemporaryDirectory() as directory, source_transport(source):
                    report = {}
                    await self.exercise_pipeline(Path(directory) / "prices.json", report)
                    self.assertTrue(report["refresh_response"]["warnings"])
                    self.assertIn(report["interval_count"], (92, 96, 100))
                self.assertEqual(len(calls), 2)

    async def test_source_failure_preserves_snapshot_and_cached_plan(self):
        fail = False
        def source(request):
            if fail:
                raise httpx.ConnectError("source offline", request=request)
            return httpx.Response(200, json=payload(date.fromisoformat(request.url.params["date"])))
        with tempfile.TemporaryDirectory() as directory, source_transport(source):
            path = Path(directory) / "prices.json"
            report = {}
            await self.exercise_pipeline(path, report)
            original = path.read_bytes()
            fail = True
            async with httpx.AsyncClient(transport=httpx.ASGITransport(app=create_app(path)), base_url="http://test") as client:
                self.assertEqual((await client.post("/v1/prices/refresh")).status_code, 503)
                self.assertEqual(path.read_bytes(), original)
                response = await client.post("/v1/plan", json={"now": report["refresh_response"]["fetched_at"]})
                self.assertEqual(response.status_code, 200, response.text)
                self.assertEqual(response.json()["slots"], report["plan_response"]["slots"])

    async def exercise_pipeline(self, path, report):
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=create_app(path)), base_url="http://test"
        ) as client:
            response = await client.post("/v1/prices/refresh")
            report["refresh_status"] = response.status_code
            report["refresh_response"] = response.json()
            self.assertEqual(response.status_code, 200, response.text)
            snapshot = response.json()
            self.assertTrue(snapshot["prices"])
            self.assertEqual(json.loads(path.read_text()), snapshot)
            cached = await client.get("/v1/prices")
            self.assertEqual(cached.status_code, 200, cached.text)
            self.assertEqual(cached.json(), snapshot)

        # A new application must recover the persisted data without a refresh.
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=create_app(path)), base_url="http://test"
        ) as client:
            cached = await client.get("/v1/prices")
            self.assertEqual(cached.status_code, 200, cached.text)
            self.assertEqual(cached.json(), snapshot)
            response = await client.post("/v1/plan", json={"now": snapshot["fetched_at"]})
            report["plan_status"] = response.status_code
            report["plan_response"] = response.json()
            self.assertEqual(response.status_code, 200, response.text)
            plan = response.json()
            self.assertFalse(plan["executable"])
            self.assertTrue(plan["slots"])
            self.assertEqual(plan["price_source"]["fetched_at"], snapshot["fetched_at"])
            self.assertEqual(plan["price_source"]["warnings"], snapshot["warnings"])
            now = datetime.fromisoformat(snapshot["fetched_at"])
            future = [p for p in snapshot["prices"] if datetime.fromisoformat(p["end"]) > now]
            self.assertEqual(len(plan["slots"]), len(future))
            for slot, price in zip(plan["slots"], future):
                self.assertEqual(slot["price_eur_mwh"], price["eur_mwh"])
                self.assertEqual(slot["end"], price["end"])
            report["interval_count"] = len(snapshot["prices"])
            report["first_interval"] = snapshot["prices"][0]
            report["last_interval"] = snapshot["prices"][-1]
            report["status"] = "passed"

    async def test_source_snapshot_restart_and_plan(self):
        requests = []

        def source(request):
            requests.append(request)
            self.assertEqual(str(request.url).split("?")[0], NORDPOOL_URL)
            self.assertEqual(request.url.params["deliveryArea"], "LV")
            self.assertEqual(request.url.params["currency"], "EUR")
            self.assertEqual(request.url.params["market"], "DayAhead")
            data = payload(date.fromisoformat(request.url.params["date"]))
            for i, entry in enumerate(data["multiAreaEntries"]):
                entry["entryPerArea"]["LV"] = 10 if i % 2 == 0 else 200
            return httpx.Response(200, json=data)

        original_client = httpx.AsyncClient
        def client_with_source(*args, **kwargs):
            # Preserve the ASGI client; replace only outgoing source HTTP.
            kwargs.setdefault("transport", httpx.MockTransport(source))
            return original_client(*args, **kwargs)

        with tempfile.TemporaryDirectory() as directory:
            with patch("solax_optimizer.service.httpx.AsyncClient", new=client_with_source):
                await self.exercise_pipeline(Path(directory) / "prices.json", {})
        self.assertEqual(len(requests), 2)

    @unittest.skipUnless(os.environ.get("GORDPOOL_LIVE_E2E") == "1", "Opt in with GORDPOOL_LIVE_E2E=1")
    async def test_live_nordpool_snapshot_restart_and_plan(self):
        report = {"status": "failed", "source": NORDPOOL_URL,
                  "checked_at": datetime.now(UTC).isoformat(), "mode": "live upstream, ASGI API"}
        report_path = Path("state/e2e-live.json")
        try:
            with tempfile.TemporaryDirectory() as directory:
                await self.exercise_pipeline(Path(directory) / "prices.json", report)
        finally:
            report_path.parent.mkdir(parents=True, exist_ok=True)
            report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2, allow_nan=False))
            print(f"Live E2E: {report['status']}; report: {report_path}", flush=True)
