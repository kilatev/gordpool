"""Real Chromium + Uvicorn tests; no SolaX account or external prices needed."""
import importlib.util
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import unittest
from datetime import datetime

import httpx

from solax_optimizer.optimizer import UTC
from solax_optimizer.prices import MARKET_ZONE, parse_day
from tests.test_prices import payload


@unittest.skipUnless(importlib.util.find_spec("playwright"), "Install .[browser] and Chromium")
class ReportBrowserTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        from playwright.sync_api import sync_playwright
        cls.directory = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.directory.cleanup)
        root = Path(cls.directory.name)
        now = datetime.now(UTC)
        rows = parse_day(payload(now.astimezone(MARKET_ZONE).date()), now.astimezone(MARKET_ZONE).date())
        (root / "state").mkdir()
        (root / "state/prices.json").write_text(json.dumps({
            "fetched_at": now.isoformat(), "warnings": ["Синтетический снимок для браузерного теста"],
            "prices": [dict(start=p.start.isoformat(), end=p.end.isoformat(), eur_mwh=p.eur_mwh) for p in rows],
        }))
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        cls.url = f"http://127.0.0.1:{port}"
        env = dict(os.environ, PYTHONPATH=str(Path(__file__).resolve().parents[2]))
        cls.log = (root / "server.log").open("w+")
        cls.addClassCleanup(cls.log.close)
        cls.server = subprocess.Popen(
            [sys.executable, "-m", "uvicorn", "solax_optimizer.api:app", "--host", "127.0.0.1", "--port", str(port)],
            cwd=root, env=env, stdout=cls.log, stderr=subprocess.STDOUT,
        )
        cls.addClassCleanup(cls.stop_server)
        with httpx.Client(trust_env=False) as client:
            for _ in range(100):
                try:
                    if client.get(cls.url + "/healthz", timeout=1).status_code == 200:
                        break
                except httpx.HTTPError:
                    pass
                if cls.server.poll() is not None:
                    cls.log.seek(0)
                    raise RuntimeError(cls.log.read())
                time.sleep(.1)
            else:
                raise RuntimeError("Uvicorn did not start within 10 seconds")
        cls.playwright = sync_playwright().start()
        cls.addClassCleanup(cls.playwright.stop)
        options = {"headless": True}
        if os.environ.get("GORDPOOL_CHROMIUM_PATH"):
            options["executable_path"] = os.environ["GORDPOOL_CHROMIUM_PATH"]
        cls.browser = cls.playwright.chromium.launch(**options)
        cls.addClassCleanup(cls.browser.close)

    @classmethod
    def stop_server(cls):
        cls.server.terminate()
        try:
            cls.server.wait(timeout=5)
        except subprocess.TimeoutExpired:
            cls.server.kill()
            cls.server.wait(timeout=5)

    def setUp(self):
        self.context = self.browser.new_context()
        self.addCleanup(self.context.close)
        self.page = self.context.new_page()
        self.errors = []
        self.page.on("pageerror", lambda error: self.errors.append(str(error)))
        self.page.goto(self.url)
        self.page.locator("#report").wait_for(state="visible")
        self.page.get_by_role("button", name="Открыть демо").wait_for()

    def tearDown(self):
        directory = Path("state/browser")
        directory.mkdir(parents=True, exist_ok=True)
        self.page.screenshot(path=str(directory / (self._testMethodName + ".png")), full_page=True)
        self.assertEqual(self.errors, [])

    def test_cached_report_and_period_details(self):
        from playwright.sync_api import expect
        expect(self.page.locator("#source")).to_contain_text("Nord Pool LV")
        expect(self.page.locator("#warnings")).to_contain_text("Синтетический снимок")
        expect(self.page.locator("#chart polyline")).to_have_count(1)
        self.page.locator("#windows summary").first.click()
        expect(self.page.locator("#windows table").first).to_be_visible()
        with self.page.expect_download() as download:
            self.page.get_by_role("button", name="Скачать JSON отчёта").click()
        data = json.loads(Path(download.value.path()).read_text())
        self.assertFalse(data["plan"]["executable"])
        self.assertEqual(data["mode"], "nordpool")

    def test_demo_and_battery_recalculation(self):
        from playwright.sync_api import expect
        self.page.get_by_role("button", name="Открыть демо").click()
        expect(self.page.locator("#source")).to_contain_text("ДЕМО")
        self.page.get_by_text("Параметры виртуальной батареи", exact=True).click()
        self.page.locator('[name="max_charge_kw"]').fill("0")
        self.page.locator('[name="max_discharge_kw"]').fill("0")
        self.page.get_by_role("button", name="Пересчитать план").click()
        expect(self.page.locator("#horizon")).to_contain_text("10% → 10%")
        expect(self.page.locator("#windows summary")).to_have_count(1)
        expect(self.page.locator("#windows summary")).to_contain_text("Ожидание")
        self.page.set_viewport_size({"width":390,"height":844})
        self.assertLessEqual(self.page.evaluate("document.documentElement.scrollWidth"), 390)

    def test_refresh_error_preserves_report(self):
        from playwright.sync_api import expect
        before = self.page.locator("#horizon").inner_text()
        self.page.route("**/v1/prices/refresh", lambda route: route.fulfill(
            status=503, content_type="application/json", body=json.dumps({"detail":"Nord Pool недоступен"}),
        ))
        self.page.get_by_role("button", name="Обновить Nord Pool").click()
        expect(self.page.get_by_role("alert")).to_contain_text("Nord Pool недоступен")
        expect(self.page.locator("#horizon")).to_have_text(before)
        expect(self.page.locator("#status")).to_contain_text("предыдущий отчёт")
        expect(self.page.get_by_role("button", name="Обновить Nord Pool")).to_be_enabled()
