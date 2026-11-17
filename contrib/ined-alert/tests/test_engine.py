"""TC-952 (what is alerted, and when) and TC-956 (restart, server state)."""

import unittest

from ined_alert import engine as E
from ined_alert.engine import Engine, parse_time
from ined_alert.routes import Routes

from .common import MAPS, VIEWS, config
from .fakes import Clock, FakeClient, host, logger

T0 = 1_800_000_000.0


def iso(t):
    import datetime
    return datetime.datetime.fromtimestamp(t, datetime.timezone.utc).isoformat().replace("+00:00", "Z")


class Rig:
    def __init__(self, hosts, state=None, **cfg):
        self.cfg = config(**cfg)
        self.client = FakeClient(hosts)
        self.routes = Routes(self.cfg.routes, self.cfg.admins)
        self.routes.load(VIEWS, MAPS)
        self.log, self.buf = logger()
        self.clock = Clock(T0)
        self.e = Engine(self.cfg, self.routes, self._fetch, self.log, state)

    def _fetch(self, hid):
        try:
            return self.client.host(hid)
        except Exception as e:  # APIError 404 -> None, as the app does
            if getattr(e, "status", 0) == 404:
                return None
            raise

    def set(self, hid, **kw):
        self.client.hosts_[hid].update(kw)
        return dict(self.client.hosts_[hid])


class TestEngine(unittest.TestCase):
    def test_parse_time(self):
        self.assertEqual(parse_time("2027-01-15T08:00:00Z"), 1800000000.0)
        self.assertEqual(parse_time("2027-01-15T09:00:00.123456789+01:00"), 1800000000.123456)
        self.assertIsNone(parse_time(None))
        self.assertIsNone(parse_time("yesterday"))

    def test_down_after_delay_then_up(self):
        r = Rig([host(1, "192.0.2.1", name="web"), host(3, "192.0.2.3")])
        self.assertEqual(r.e.snapshot(r.client.hosts(), r.clock()), [])
        self.assertEqual(r.e.update(r.set(1, status="down", last_change=iso(T0)), r.clock.add(1)), [])
        self.assertEqual(r.e.tick(r.clock.add(100)), [])  # 101 s: not yet
        (n,) = r.e.tick(r.clock.add(20))
        self.assertEqual((n.kind, n.host["name"], n.since), (E.DOWN, "web", T0))
        self.assertEqual(sorted(n.to), ["admin@example.com", "ops@example.com", "pay@example.com"])
        self.assertEqual(n.groups, ["Networks/dc/lan", "Payroll/app servers"])
        self.assertTrue(r.e.dirty and 1 in r.e.open)
        self.assertEqual(r.e.tick(r.clock.add(3600)), [], "no repeat by default")
        (u,) = r.e.update(r.set(1, status="up"), r.clock.add(10))
        self.assertEqual((u.kind, u.since), (E.UP, T0))
        self.assertEqual(r.e.open, {})
        self.assertIn("event=down host=web", r.buf.getvalue())
        self.assertIn("event=up host=web", r.buf.getvalue())

    def test_short_drop_and_unknown(self):
        r = Rig([host(1, "192.0.2.1"), host(2, "192.0.2.2")])
        r.e.snapshot(r.client.hosts(), r.clock())
        r.e.update(r.set(1, status="down"), r.clock())
        r.e.update(r.set(1, status="up"), r.clock.add(60))
        r.e.update(r.set(2, status="down"), r.clock())
        r.e.update(r.set(2, status="unknown"), r.clock.add(10))
        self.assertEqual(r.e.tick(r.clock.add(600)), [])
        self.assertIn("reason=\"up again within down_after\"", r.buf.getvalue())
        self.assertIn("reason=\"status unknown\"", r.buf.getvalue())

    def test_confirm_before_alert(self):
        # The event said down, but the server says up by now: no mail.
        r = Rig([host(1, "192.0.2.1")])
        r.e.snapshot(r.client.hosts(), r.clock())
        r.e.update(dict(r.client.hosts_[1], status="down"), r.clock())
        self.assertEqual(r.e.tick(r.clock.add(200)), [])
        self.assertEqual(r.client.fetches, 1)
        # The server can't be reached: asked again at the next tick.
        r.e.update(r.set(1, status="down"), r.clock())
        r.client.down = True
        self.assertEqual(r.e.tick(r.clock.add(200)), [])
        r.client.down = False
        self.assertEqual([n.kind for n in r.e.tick(r.clock.add(1))], [E.DOWN])
        # An old "up" event: the server still says down, so the alert stays.
        self.assertEqual(r.e.update(dict(r.client.hosts_[1], status="up"), r.clock.add(1)), [])
        self.assertIn(1, r.e.open)

    def test_recovery_waits_for_the_server(self):
        # C2 of crosscheck 2026-11-17-01: an "up" event while the server
        # can't be asked keeps the alert; the next tick asks again.
        r = Rig([host(1, "192.0.2.1", name="web", status="down", last_change=iso(T0 - 600)), host(2, "192.0.2.2")])
        r.e.snapshot(r.client.hosts(), r.clock())
        self.assertEqual([n.kind for n in r.e.tick(r.clock())], [E.DOWN])
        r.client.down = True
        self.assertEqual(r.e.update(dict(r.client.hosts_[1], status="up"), r.clock.add(10)), [])
        self.assertIn(1, r.e.open)
        self.assertIn('event=confirm_failed host=web', r.buf.getvalue())
        self.assertEqual(r.e.tick(r.clock.add(10)), [])  # still unreachable: still open
        self.assertIn(1, r.e.open)
        # The server is back and says down: the event was old; the alert stays.
        r.client.down = False
        self.assertEqual(r.e.tick(r.clock.add(10)), [])
        self.assertEqual((list(r.e.open), r.e.recheck), ([1], set()))
        # Again, and this time the server says up: the recovery is mailed.
        r.client.down = True
        r.e.update(dict(r.client.hosts_[1], status="up"), r.clock.add(10))
        r.client.down = False
        r.set(1, status="up")
        (u,) = r.e.tick(r.clock.add(10))
        self.assertEqual((u.kind, u.host["name"]), (E.UP, "web"))
        self.assertEqual((r.e.open, r.e.recheck), ({}, set()))

    def test_bad_state(self):
        # C1 of crosscheck 2026-11-17-01: a state file of the wrong shape or
        # a newer version is refused (the program sets it aside).
        for st in ({"open": []}, {"server": None}, {"services": {"x": {}}}, {"services": {"1": {}}},
                   {"open": {"1": {}}}, {"open": {"x": {}}}, {"version": 3}, {"version": "2"}, []):
            with self.assertRaises(ValueError, msg=repr(st)):
                E.parse_state(st)
        self.assertEqual(E.parse_state({})["open"], {})

    def test_monitoring_off_and_deleted(self):
        r = Rig([host(1, "192.0.2.1", status="down", last_change=iso(T0 - 600)), host(2, "192.0.2.2", status="down", last_change=iso(T0 - 600))])
        r.e.snapshot(r.client.hosts(), r.clock())
        self.assertEqual(len(r.e.tick(r.clock())), 2)  # down for 10 min already: at once
        self.assertEqual(r.e.update(r.set(1, monitored=False), r.clock()), [])
        self.assertEqual(r.e.deleted(2, r.clock()), [])
        self.assertEqual(r.e.open, {})
        self.assertIn("reason=\"monitoring turned off\"", r.buf.getvalue())
        self.assertIn("reason=deleted", r.buf.getvalue())

    def test_repeat(self):
        r = Rig([host(1, "192.0.2.1", status="down", last_change=iso(T0))], repeat=3600)
        r.e.snapshot(r.client.hosts(), r.clock())
        r.e.tick(r.clock.add(120))
        self.assertEqual(r.e.tick(r.clock.add(3599)), [])
        (n,) = r.e.tick(r.clock.add(1))
        self.assertEqual(n.kind, E.STILL_DOWN)

    def test_restart(self):
        # Before the restart: web down and alerted, db down and alerted.
        r = Rig([host(1, "192.0.2.1", status="down", last_change=iso(T0)), host(2, "192.0.2.2", status="down", last_change=iso(T0)),
                 host(3, "192.0.2.3")])
        r.e.snapshot(r.client.hosts(), r.clock())
        self.assertEqual(len(r.e.tick(r.clock.add(120))), 2)
        state = r.e.state()
        # After it: web still down (nothing new), db up (recovery), lone down
        # since before the restart (alerted once its delay has passed).
        r2 = Rig(list(r.client.hosts_.values()), state=state)
        r2.set(2, status="up")
        r2.set(3, status="down", last_change=iso(T0 + 100))
        r2.clock.t = T0 + 200
        ns = r2.e.snapshot(r2.client.hosts(), r2.clock())
        self.assertEqual([(n.kind, n.host["id"]) for n in ns], [(E.UP, 2)])
        self.assertEqual(sorted(ns[0].to), ["admin@example.com", "ops@example.com"])
        self.assertEqual(r2.e.tick(r2.clock()), [])
        self.assertEqual([(n.kind, n.host["id"]) for n in r2.e.tick(r2.clock.add(20))], [(E.DOWN, 3)])
        # A host alerted before the restart and gone since: closed, no mail.
        r3 = Rig([host(3, "192.0.2.3", status="down")], state=r2.e.state())
        self.assertEqual(r3.e.snapshot(r3.client.hosts(), r3.clock()), [])
        self.assertEqual(list(r3.e.open), [3])

    def test_server_unreachable_and_degraded(self):
        r = Rig([])
        self.assertEqual(r.e.reachable(False, "connection refused", r.clock()), [])
        self.assertEqual(r.e.tick(r.clock.add(299)), [])
        (n,) = r.e.tick(r.clock.add(1))
        self.assertEqual((n.kind, n.text, list(n.to)), (E.SERVER_DOWN, "connection refused", ["admin@example.com"]))
        self.assertEqual(r.e.tick(r.clock.add(600)), [])
        # Alerted state survives a restart: "reachable again" is still sent.
        r2 = Rig([], state=r.e.state())
        (n,) = r2.e.reachable(True, "", r2.clock())
        self.assertEqual(n.kind, E.SERVER_UP)
        # A short outage: nothing.
        r2.e.reachable(False, "x", r2.clock())
        self.assertEqual(r2.e.reachable(True, "", r2.clock.add(60)), [])
        # Degraded.
        r2.e.health(True, "no ICMP mode available", r2.clock())
        (d,) = r2.e.tick(r2.clock.add(300))
        self.assertEqual((d.kind, d.text), (E.DEGRADED, "no ICMP mode available"))
        (ok,) = r2.e.health(False, "", r2.clock.add(10))
        self.assertEqual(ok.kind, E.PINGS_AGAIN)

    def test_summary(self):
        r = Rig([host(1, "192.0.2.1", name="web", status="down", last_change=iso(T0 - 60)), host(3, "192.0.2.3"),
                 host(2, "192.0.2.2", status="down", monitored=False)])
        r.e.snapshot(r.client.hosts(), r.clock())
        s = r.e.summary(r.clock())
        self.assertEqual((s.kind, list(s.to)), (E.SUMMARY, ["admin@example.com"]))
        self.assertEqual([(h["name"], h["since"], h["groups"]) for h in s.hosts],
                         [("web", T0 - 60, ["Networks/dc/lan", "Payroll/app servers"])])


if __name__ == "__main__":
    unittest.main()
