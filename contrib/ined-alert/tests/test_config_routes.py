"""TC-951 (config) and TC-953 (routes)."""

import os
import tempfile
import unittest

from ined_alert.config import ConfigError, Route, load, normalize_path, parse_duration
from ined_alert.routes import Routes

from .common import MAPS, ROUTES, VIEWS

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def write(text):
    f = tempfile.NamedTemporaryFile("w", suffix=".toml", delete=False)
    f.write(text)
    f.close()
    return f.name


MIN = '[mail]\nfrom = "a@example.com"\n[alert]\nadmins = ["admin@example.com"]\n'


class TestConfig(unittest.TestCase):
    def test_durations(self):
        for s, want in [("", 0), ("0", 0), ("30s", 30), ("2m", 120), ("1h30m", 5400), ("1.5s", 1.5), ("250ms", 0.25)]:
            self.assertEqual(parse_duration(s, "k"), want, s)
        for s in ("2", "m", "2 m", "1x", "1h-1m"):
            with self.assertRaises(ConfigError, msg=s):
                parse_duration(s, "k")

    def test_example_loads(self):
        c = load(os.path.join(HERE, "alert.toml"), env={})
        self.assertEqual((c.url, c.mail_host, c.mail_port, c.down_after, c.batch, c.repeat, c.server_down_after),
                         ("http://127.0.0.1:8470", "localhost", 25, 120, 30, 0, 300))
        self.assertEqual([r.group for r in c.routes], ["Networks/dc", "Payroll/app servers"])
        self.assertFalse(c.startup_summary)
        self.assertTrue(c.state_file.endswith("ined-alert/state.json"))

    def test_defaults_and_env(self):
        c = load(write(MIN), env={"INED_URL": "https://ined:8470", "INED_TOKEN": "r"})
        self.assertEqual((c.url, c.token, c.down_after, c.syslog_address, c.log_level), ("https://ined:8470", "r", 120, "", "info"))

    def test_errors(self):
        cases = {
            '[mail]\nfrom = "a@example.com"\n': "nobody gets alerts",
            MIN + "[alert2]\n": "unknown section [alert2]",
            MIN.replace("[alert]\n", '[alert]\ndown_aftr = "1m"\n'): "unknown key alert.down_aftr",
            '[alert]\nadmins = ["x@example.com"]\n': "mail.from must be set",
            MIN.replace('"admin@example.com"', '"admin"'): '"admin" is not an email address',
            MIN.replace("[alert]\n", '[alert]\nbatch = "soon"\n'): "alert.batch",
            MIN + '[[route]]\ngroup = "/"\nto = ["a@example.com"]\n': "route 1",
            MIN + '[syslog]\naddress = "collector:514"\n': "syslog.address",
            MIN + '[syslog]\nfacility = "kern"\n': "syslog.facility",
            MIN + '[server]\nurl = "ined:8470"\n': "server.url",
            MIN + "[mail]\nport = true\n": "",  # a duplicate table is a TOML error
            "not toml [": "",
        }
        for text, want in cases.items():
            with self.assertRaises(ConfigError, msg=text) as cm:
                load(write(text), env={})
            self.assertIn(want, str(cm.exception), text)

    def test_normalize(self):
        self.assertEqual(normalize_path(" Networks / lan/ "), "Networks/lan")


class TestRoutes(unittest.TestCase):
    def test_recipients(self):
        r = Routes(list(ROUTES), ["admin@example.com"])
        r.load(VIEWS, MAPS)
        self.assertEqual(r.warnings, [])
        # web is in Networks/dc/lan and in Payroll/app servers: both lists and the admin.
        self.assertEqual(r.recipients(1), {"ops@example.com": ["Networks/dc"], "pay@example.com": ["Payroll/app servers"],
                                           "admin@example.com": []})
        self.assertEqual(r.groups_of(1), ["Networks/dc/lan", "Payroll/app servers"])
        self.assertEqual(r.recipients(2), {"ops@example.com": ["Networks/dc"], "admin@example.com": []})
        # A host in no routed group: only the admins.
        self.assertEqual(r.recipients(3), {"admin@example.com": []})
        self.assertEqual(r.groups_of(3), [])
        self.assertEqual(r.recipients(99), {"admin@example.com": []})

    def test_view_route_duplicates_and_warnings(self):
        routes = [Route("Payroll", ["pay@example.com"]), Route("Networks/dc/lan", ["lan@example.com"]),
                  Route("Networks/dc/lan", ["lan2@example.com"]), Route("Networks/nope", ["x@example.com"])]
        r = Routes(routes, [])
        maps = {1: {"items": MAPS[1]["items"] + [{"type": "group", "id": 12, "parent_id": 10, "name": "lan"},
                                                  {"type": "host", "id": 4, "parent_id": 12, "name": "h4"}]},
                2: MAPS[2]}
        r.load(VIEWS, maps)
        self.assertEqual(r.recipients(1), {"pay@example.com": ["Payroll"], "lan@example.com": ["Networks/dc/lan"],
                                           "lan2@example.com": ["Networks/dc/lan"]})
        self.assertEqual(r.recipients(4), {"lan@example.com": ["Networks/dc/lan"], "lan2@example.com": ["Networks/dc/lan"]})
        self.assertEqual(r.warnings, ['route "Networks/dc/lan" matches 2 groups; it covers all of them',
                                      'route "Networks/nope" matches no view or group'])


if __name__ == "__main__":
    unittest.main()
