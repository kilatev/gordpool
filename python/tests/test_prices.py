import unittest
from datetime import date, datetime, time, timedelta

import httpx

from solax_optimizer.optimizer import UTC
from solax_optimizer.prices import MARKET_ZONE, fetch_prices, parse_day


def payload(day, price=100):
    start = datetime.combine(day, time(), MARKET_ZONE).astimezone(UTC)
    end = datetime.combine(day+timedelta(days=1), time(), MARKET_ZONE).astimezone(UTC)
    entries = []
    while start < end:
        nxt = start+timedelta(minutes=15)
        entries.append(dict(deliveryStart=start.isoformat(), deliveryEnd=nxt.isoformat(), entryPerArea={"LV":price}))
        start = nxt
    return dict(currency="EUR", multiAreaEntries=entries)


class PriceTests(unittest.TestCase):
    def test_dst_days(self):
        for day,n in [(date(2026,3,29),92),(date(2026,10,25),100),(date(2026,10,27),96)]:
            rows = parse_day(payload(day),day)
            self.assertEqual(len(rows),n)
            self.assertEqual(rows[0].eur_mwh,100)

    def test_invalid(self):
        day = date(2026,10,27)
        for modify in [lambda p:p.update(currency="USD"), lambda p:p["multiAreaEntries"].pop(),
                       lambda p:p["multiAreaEntries"].append(p["multiAreaEntries"][0]),
                       lambda p:p["multiAreaEntries"][0]["entryPerArea"].clear(),
                       lambda p:p.update(multiAreaEntries=None)]:
            p = payload(day)
            modify(p)
            with self.assertRaises((ValueError,KeyError)): parse_day(p,day)
        self.assertEqual(parse_day({"multiAreaEntries":[]},day),[])


class FetchTests(unittest.IsolatedAsyncioTestCase):
    async def test_today_tomorrow_and_request_parameters(self):
        seen=[]
        def handler(request):
            seen.append(request)
            day=date.fromisoformat(request.url.params["date"])
            return httpx.Response(200,json=payload(day))
        async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
            rows,warnings=await fetch_prices(client,datetime(2026,10,27,12,tzinfo=UTC))
        self.assertEqual(len(rows),192)
        self.assertFalse(warnings)
        self.assertEqual(seen[0].url.params["deliveryArea"],"LV")
        self.assertEqual(seen[0].url.params["currency"],"EUR")

    async def test_missing_or_failed_tomorrow_retains_today(self):
        for status in [200,503]:
            def handler(request):
                day=date.fromisoformat(request.url.params["date"])
                if day.day==27: return httpx.Response(200,json=payload(day))
                return httpx.Response(status,json={"multiAreaEntries":[]})
            async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
                rows,warnings=await fetch_prices(client,datetime(2026,10,27,12,tzinfo=UTC))
            self.assertEqual(len(rows),96)
            self.assertTrue(warnings)

    async def test_network_and_empty_errors(self):
        for status in [503,200]:
            async with httpx.AsyncClient(transport=httpx.MockTransport(lambda r:httpx.Response(status,json={"multiAreaEntries":[]}))) as client:
                with self.assertRaises(ValueError):
                    await fetch_prices(client,datetime(2026,10,27,12,tzinfo=UTC))
