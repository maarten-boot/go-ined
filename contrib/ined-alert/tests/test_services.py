"""TC-957: alerts for watched services (SPEC-952, 954, 956;
Design-service-alerts.md S8)."""

import unittest

from ined_alert import engine as E

from .fakes import host, svc
from .test_engine import T0, Rig, iso


def web(status="up", last_change=None, **services):
    """web (id 1) watching https and ssh, both up unless given."""
    ws = [services.get("https") or svc("https"), services.get("ssh") or svc("ssh")]
    return host(1, "192.0.2.1", name="web", status=status, last_change=last_change, watch=ws)


def event(r, name, status, at):
    """Sets a service's status on the fake server and returns its
    service.status event."""
    h = r.client.hosts_[1]
    for w in h["watch"]:
        if w["name"] == name:
            w.update(status=status, last_change=iso(at))
            if status == "up":
                w["last_ok"] = iso(at)
            return {"host_id": 1, "display_name": h["display_name"], "ip": h["ip"], "name": name,
                    "port": w["port"], "status": status, "last_change": iso(at)}
    raise KeyError(name)


def kinds(ns):
    return [(n.kind, n.service["name"] if n.service else "host") for n in ns]


class TestServiceAlerts(unittest.TestCase):
    def test_down_after_delay_then_up(self):
        r = Rig([web(), host(3, "192.0.2.3", watch=[])])
        self.assertEqual(r.e.snapshot(r.client.hosts(), r.clock()), [])
        self.assertEqual(r.e.service(event(r, "https", "down", T0), r.clock.add(1)), [])
        self.assertEqual(r.e.tick(r.clock.add(100)), [])
        (n,) = r.e.tick(r.clock.add(20))
        self.assertEqual((n.kind, n.host["name"], n.service["name"], n.service["port"], n.since), (E.DOWN, "web", "https", 443, T0))
        self.assertEqual(sorted(n.to), ["admin@example.com", "ops@example.com", "pay@example.com"])
        self.assertEqual(n.groups, ["Networks/dc/lan", "Payroll/app servers"])
        st = r.e.state()
        self.assertEqual((st["version"], list(st["services"]), st["open"]), (2, ["1/https"], {}))
        (u,) = r.e.service(event(r, "https", "up", T0 + 720), r.clock.add(600))
        self.assertEqual((u.kind, u.service["name"], u.since), (E.UP, "https", T0))
        self.assertEqual(r.e.svc_open, {})
        log = r.buf.getvalue()
        self.assertIn("event=service_down host=web ip=192.0.2.1 service=https", log)
        self.assertIn("event=service_up host=web ip=192.0.2.1 service=https", log)

    def test_short_drop_confirm_and_old_server(self):
        r = Rig([web()])
        r.e.snapshot(r.client.hosts(), r.clock())
        r.e.service(event(r, "ssh", "down", T0), r.clock())
        r.e.service(event(r, "ssh", "up", T0 + 60), r.clock.add(60))
        self.assertIn('service=ssh reason="up again within down_after"', r.buf.getvalue())
        # The event said down, the server says up by now: no mail.
        e = event(r, "https", "down", T0 + 60)
        event(r, "https", "up", T0 + 70)
        r.e.service(e, r.clock())
        self.assertEqual(r.e.tick(r.clock.add(200)), [])
        self.assertIn('service=https reason="not down on the server"', r.buf.getvalue())
        # A server without watched services: its hosts have no "watch", and
        # nothing about services is opened or closed.
        old = Rig([host(1, "192.0.2.1", name="web")])
        old.e.snapshot(old.client.hosts(), old.clock())
        self.assertEqual(old.e.service({"host_id": 1, "name": "ssh", "port": 22, "status": "down"}, old.clock()), [])
        self.assertEqual(old.e.svc_pending, {})

    def test_held_while_the_host_is_down(self):
        # web goes down; its services fail with it. Only the host is mailed.
        r = Rig([web()])
        r.e.snapshot(r.client.hosts(), r.clock())
        r.client.hosts_[1].update(status="down", last_change=iso(T0))
        r.e.update(dict(r.client.hosts_[1]), r.clock())
        r.e.service(event(r, "https", "down", T0 + 10), r.clock.add(10))
        r.e.service(event(r, "ssh", "down", T0 + 10), r.clock())
        self.assertEqual(kinds(r.e.tick(r.clock.add(110))), [(E.DOWN, "host")])
        self.assertEqual(r.e.tick(r.clock.add(3600)), [])
        self.assertIn('event=held host=web service=https reason="the host is down"', r.buf.getvalue())
        self.assertEqual(r.buf.getvalue().count("event=held"), 2, "logged once each")
        # web is back at T1. The server checks its services at once: ssh
        # answers, https doesn't. https is mailed down_after after T1.
        t1 = r.clock.add(10)
        r.client.hosts_[1].update(status="up", last_change=iso(t1))
        self.assertEqual(kinds(r.e.update(dict(r.client.hosts_[1]), t1)), [(E.UP, "host")])
        r.e.service(event(r, "ssh", "up", t1 + 5), r.clock.add(5))
        self.assertEqual(r.e.tick(r.clock.add(110)), [])
        (n,) = r.e.tick(r.clock.add(5))
        self.assertEqual((n.kind, n.service["name"], n.since), (E.DOWN, "https", T0 + 10))
        self.assertEqual(list(r.e.svc_open), [(1, "https")])

    def test_held_while_the_host_waits(self):
        # web goes down but isn't alerted yet; https fails meanwhile: held.
        # web is back within down_after: https is mailed down_after after that.
        r = Rig([web()])
        r.e.snapshot(r.client.hosts(), r.clock())
        r.client.hosts_[1].update(status="down", last_change=iso(T0))
        r.e.update(dict(r.client.hosts_[1]), r.clock())
        r.e.service(event(r, "https", "down", T0 + 10), r.clock.add(10))
        self.assertEqual(r.e.tick(r.clock.add(50)), [])
        self.assertIn('event=held host=web service=https reason="the host is down"', r.buf.getvalue())
        t1 = r.clock.add(30)
        r.client.hosts_[1].update(status="up", last_change=iso(t1))
        self.assertEqual(r.e.update(dict(r.client.hosts_[1]), t1), [])  # cancelled, nothing was mailed
        self.assertEqual(r.e.tick(r.clock.add(119)), [])
        (n,) = r.e.tick(r.clock.add(1))
        self.assertEqual((n.kind, n.service["name"], n.since), (E.DOWN, "https", T0 + 10))

    def test_recovery_waits_for_the_server(self):
        # C2 of crosscheck 2026-11-17-01, for a service.
        r = Rig([web(https=svc("https", "down", iso(T0 - 600)))])
        r.e.snapshot(r.client.hosts(), r.clock())
        self.assertEqual(kinds(r.e.tick(r.clock())), [(E.DOWN, "https")])
        r.client.down = True
        e = event(r, "https", "up", r.clock.add(10))
        self.assertEqual(r.e.service(e, r.clock()), [])
        self.assertEqual(list(r.e.svc_open), [(1, "https")])
        self.assertEqual(r.e.tick(r.clock.add(10)), [])
        r.client.down = False
        self.assertEqual(kinds(r.e.tick(r.clock.add(10))), [(E.UP, "https")])
        self.assertEqual((r.e.svc_open, r.e.recheck), ({}, set()))

    def test_renamed_service_keeps_its_alert(self):
        # L4/P7 of crosscheck 2026-11-17-01: "ssh" watched as "tcp:22" (the
        # same port) keeps its status on the server and its alert here.
        r = Rig([web(ssh=svc("ssh", "down", iso(T0 - 600)))])
        r.e.snapshot(r.client.hosts(), r.clock())
        self.assertEqual(kinds(r.e.tick(r.clock())), [(E.DOWN, "ssh")])
        r.client.hosts_[1]["watch"][1]["name"] = "tcp:22"
        self.assertEqual(r.e.update(dict(r.client.hosts_[1]), r.clock.add(10)), [])
        self.assertEqual(list(r.e.svc_open), [(1, "tcp:22")])
        self.assertEqual(r.e.tick(r.clock.add(600)), [], "no second DOWN")
        self.assertIn("event=renamed host=web ip=192.0.2.1 service=ssh to=tcp:22", r.buf.getvalue())
        (u,) = r.e.service(event(r, "tcp:22", "up", r.clock()), r.clock())
        self.assertEqual((u.kind, u.service["name"]), (E.UP, "tcp:22"))

    def test_renamed_drops_a_held_new_name(self):
        # L5 of crosscheck 2026-11-18-01: the new name's waiting entry goes,
        # also from the "held" set, so its line is logged again later.
        r = Rig([web(ssh=svc("ssh", "down", iso(T0 - 600)))])
        r.e.snapshot(r.client.hosts(), r.clock())
        r.e.tick(r.clock())
        r.e.svc_pending[(1, "tcp:22")] = T0
        r.e.svc_held.add((1, "tcp:22"))
        r.client.hosts_[1]["watch"][1]["name"] = "tcp:22"
        r.e.update(dict(r.client.hosts_[1]), r.clock.add(10))
        self.assertEqual((list(r.e.svc_open), r.e.svc_pending, r.e.svc_held), ([(1, "tcp:22")], {}, set()))

    def test_unknown_closes_an_open_alert(self):
        # C11 of crosscheck 2026-11-18-01: a service that becomes unknown
        # (services turned off, a new IP) ends its alert without a mail and
        # without STILL DOWN repeats.
        r = Rig([web(https=svc("https", "down", iso(T0 - 600)))], repeat=3600)
        r.e.snapshot(r.client.hosts(), r.clock())
        self.assertEqual(kinds(r.e.tick(r.clock())), [(E.DOWN, "https")])
        self.assertEqual(r.e.service(event(r, "https", "unknown", r.clock()), r.clock.add(1)), [])
        self.assertEqual(r.e.svc_open, {})
        self.assertEqual(r.e.tick(r.clock.add(3600)), [], "no STILL DOWN")
        self.assertIn('service=https reason="status unknown" note="no recovery mail"', r.buf.getvalue())
        # The same from a snapshot (a restart with services off).
        r.client.hosts_[1]["watch"][0].update(status="down", last_change=iso(r.clock()))
        r.e.snapshot(r.client.hosts(), r.clock())
        self.assertEqual(kinds(r.e.tick(r.clock.add(600))), [(E.DOWN, "https")])
        r.client.hosts_[1]["watch"][0].update(status="unknown", last_change=iso(r.clock()))
        self.assertEqual(r.e.snapshot(r.client.hosts(), r.clock.add(1)), [])
        self.assertEqual(r.e.svc_open, {})

    def test_older_server_closes_service_alerts(self):
        # L8 of crosscheck 2026-11-17-01: after a downgrade below v0.50 the
        # hosts have no "watch"; a snapshot closes their service alerts.
        r = Rig([web(https=svc("https", "down", iso(T0 - 600)))])
        r.e.snapshot(r.client.hosts(), r.clock())
        r.e.tick(r.clock())
        del r.client.hosts_[1]["watch"]
        self.assertEqual(r.e.update(dict(r.client.hosts_[1]), r.clock.add(1)), [])
        self.assertEqual(list(r.e.svc_open), [(1, "https")], "an event alone doesn't close it")
        self.assertEqual(r.e.snapshot(r.client.hosts(), r.clock.add(1)), [])
        self.assertEqual(r.e.svc_open, {})
        self.assertIn('reason="the server lists no watched services" note="no recovery mail"', r.buf.getvalue())

    def test_open_alert_stays_while_the_host_is_down(self):
        r = Rig([web(https=svc("https", "down", iso(T0)))], repeat=3600)
        r.e.snapshot(r.client.hosts(), r.clock())
        self.assertEqual(kinds(r.e.tick(r.clock.add(120))), [(E.DOWN, "https")])
        # The host goes down: its own alert, no repeat for https meanwhile.
        r.client.hosts_[1].update(status="down", last_change=iso(r.clock()))
        r.e.update(dict(r.client.hosts_[1]), r.clock())
        self.assertEqual(kinds(r.e.tick(r.clock.add(120))), [(E.DOWN, "host")])
        self.assertEqual(kinds(r.e.tick(r.clock.add(3600))), [(E.STILL_DOWN, "host")])
        # Back up, and https with it: both recoveries.
        r.client.hosts_[1].update(status="up", last_change=iso(r.clock()))
        self.assertEqual(kinds(r.e.update(dict(r.client.hosts_[1]), r.clock())), [(E.UP, "host")])
        self.assertEqual(kinds(r.e.service(event(r, "https", "up", r.clock()), r.clock())), [(E.UP, "https")])

    def test_unwatched_monitoring_off_and_deleted(self):
        r = Rig([web(https=svc("https", "down", iso(T0 - 600)), ssh=svc("ssh", "down", iso(T0 - 600))),
                 host(2, "192.0.2.2", watch=[svc("http", "down", iso(T0 - 600))])])
        r.e.snapshot(r.client.hosts(), r.clock())
        self.assertEqual(len(r.e.tick(r.clock())), 3)  # down for 10 min already: at once
        # https no longer watched; then monitoring off (the server makes the
        # rest unknown); host 2 deleted. No mail for any of them.
        r.client.hosts_[1]["watch"] = [r.client.hosts_[1]["watch"][1]]
        self.assertEqual(r.e.update(dict(r.client.hosts_[1]), r.clock()), [])
        self.assertEqual(list(r.e.svc_open), [(1, "ssh"), (2, "http")])
        r.client.hosts_[1].update(monitored=False)
        r.client.hosts_[1]["watch"][0]["status"] = "unknown"
        self.assertEqual(r.e.update(dict(r.client.hosts_[1]), r.clock()), [])
        self.assertEqual(r.e.deleted(2, r.clock()), [])
        self.assertEqual(r.e.svc_open, {})
        log = r.buf.getvalue()
        for reason in ('"no longer watched"', '"monitoring turned off"', '"host deleted"'):
            self.assertIn(f"reason={reason} note=\"no recovery mail\"", log)

    def test_restart(self):
        # Before: https and ssh on web down and alerted, http on db too.
        r = Rig([web(https=svc("https", "down", iso(T0)), ssh=svc("ssh", "down", iso(T0))),
                 host(2, "192.0.2.2", watch=[svc("http", "down", iso(T0))])])
        r.e.snapshot(r.client.hosts(), r.clock())
        self.assertEqual(len(r.e.tick(r.clock.add(120))), 3)
        state = r.e.state()
        self.assertEqual(sorted(state["services"]), ["1/https", "1/ssh", "2/http"])
        # After: https still down (nothing), ssh up (recovery), db deleted
        # (closed), and tcp:5432 down since before the restart (after its delay).
        hosts = [web(https=svc("https", "down", iso(T0)), ssh=svc("ssh", "up", iso(T0 + 150)))]
        hosts[0]["watch"].append(svc("tcp:5432", "down", iso(T0 + 100)))
        r2 = Rig(hosts, state=state)
        r2.clock.t = T0 + 200
        ns = r2.e.snapshot(r2.client.hosts(), r2.clock())
        self.assertEqual(kinds(ns), [(E.UP, "ssh")])
        self.assertEqual(sorted(ns[0].to), ["admin@example.com", "ops@example.com", "pay@example.com"])
        self.assertEqual(r2.e.tick(r2.clock()), [])
        self.assertEqual(kinds(r2.e.tick(r2.clock.add(20))), [(E.DOWN, "tcp:5432")])
        self.assertEqual(sorted(r2.e.svc_open), [(1, "https"), (1, "tcp:5432")])
        # A version 1 state file: hosts only.
        r3 = Rig([web()], state={"version": 1, "open": {"1": state["services"]["1/https"]}})
        self.assertEqual((list(r3.e.open), r3.e.svc_open), ([1], {}))

    def test_summary(self):
        r = Rig([web(https=svc("https", "down", iso(T0 - 60))),
                 host(2, "192.0.2.2", status="down", watch=[svc("http", "down")])])
        r.e.snapshot(r.client.hosts(), r.clock())
        s = r.e.summary(r.clock())
        self.assertEqual([h["name"] for h in s.hosts], ["192.0.2.2"])
        self.assertEqual([(h["name"], h["service"]["name"], h["since"]) for h in s.services], [("web", "https", T0 - 60)])


if __name__ == "__main__":
    unittest.main()
