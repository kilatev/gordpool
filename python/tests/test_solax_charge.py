import argparse
import sys
import types
import unittest
from unittest.mock import MagicMock, patch

from solax_optimizer import solax_charge as charge


class ChargeTests(unittest.TestCase):
    def test_times_and_url_selection(self):
        for value in ("00:00", "04:00", "05:00", "23:59"):
            self.assertEqual(charge.time_value(value), value)
        for value in ("4:00", "24:00", "12:60", "04:00\n", "", "-1:00"):
            with self.assertRaises(argparse.ArgumentTypeError):
                charge.time_value(value)
        url = "https://global.solaxcloud.com/green/remoteSetting/#/x3hybridg4basic"
        self.assertTrue(charge.is_settings_url(url))
        self.assertTrue(charge.is_settings_url(url + "?lang=en_US"))
        self.assertFalse(charge.is_settings_url(url.replace("global.solaxcloud.com", "evil.test")))
        self.assertFalse(charge.is_settings_url(url.replace("basic", "set")))

    def test_write_is_verified_before_next_field_and_stops_on_failure(self):
        page = MagicMock()
        events = []
        api = types.ModuleType("playwright.sync_api")
        assertion = MagicMock()
        api.expect = lambda locator: assertion
        assertion.to_have_value.side_effect = lambda value: events.append(("verify", value))
        with patch.dict(sys.modules, {"playwright.sync_api": api}), \
                patch.object(charge, "open_period"), patch.object(charge, "field"), \
                patch.object(charge, "set_time", side_effect=lambda p, label, value: (events.append((label, value)), True)[1]):
            charge.apply_period(page, "test-sn", "04:00", "05:00")
            self.assertEqual(events[:4], [(charge.START, "04:00"), ("verify", "04:00"),
                                          (charge.END, "05:00"), ("verify", "05:00")])
            self.assertEqual(page.reload.call_count, 2)
            events.clear()
            assertion.to_have_value.side_effect = AssertionError("Save did not persist")
            with self.assertRaises(AssertionError):
                charge.apply_period(page, "test-sn", "00:00", "00:00")
            self.assertEqual(events, [(charge.START, "00:00")])


if __name__ == "__main__":
    unittest.main()
