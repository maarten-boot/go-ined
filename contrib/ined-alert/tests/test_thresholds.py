"""TC-959: SNMP threshold alerts (SPEC-958; Design-snmp.md N7, N8)."""

import os
import tempfile
import unittest

from ined import Event
from ined_alert import engine as E
from ined_alert.config import ConfigError, load
from ined_alert.mail import compose, subject
from ined_alert.main import App
from ined_alert.thresholds import Threshold, readings

from .common import MAPS, VIEWS, config
from .fakes import Clock, FakeClient, host, logger
from .test_engine import T0, Rig, iso

RULES = [Threshold("disk", 90, 600), Threshold("cpu_max", 95, 300), Threshold("disk", 80, 600)]


def values(cpu=10, busiest=20, disks=(("/", 50), ("/data", 40)), mem=60, at=T0):
    st = [{"name": n, "kind": "disk", "size_bytes": 1, "used_pct": p} for n, p in disks]
    st.append({"name": "Physical memory", "kind": "memory", "size_bytes": 1, "used_pct": mem})
    return {"at": iso(at), "uptime_s": 1, "cpu_pct": cpu, "cpu_max_pct": busiest, "cpus": 8, "storage": st}


def web(**kw):
    h = host(1, "192.0.2.1", name="web")
    h["snmp"] = {"version": "v2c", "name": "web", "values": values(**kw)}
    return h


def ev(r, **kw):
    """Sets web's values, read now, on the fake server and returns the
    snmp.values event."""
    kw.setdefault("at", r.clock())
    r.client.hosts_[1]["snmp"] = {"version": "v2c", "name": "web", "values": values(**kw)}
    return {"host_id": 1, "display_name": "web", "ip": "192.0.2.1", "values": values(**kw)}


class TestReadings(unittest.TestCase):
    def test_readings(self):
        rd = readings(web(disks=(("/", 85),)), RULES)
        self.assertEqual(sorted(rd), [("cpu_max", ""), ("disk", "/")])
        self.assertEqual((rd[("disk", "/")]["above"], rd[("disk", "/")]["label"]), (80, "disk /"))  # the lower line
        self.assertEqual(rd[("cpu_max", "")]["label"], "busiest CPU")
        self.assertEqual(readings(host(2, "192.0.2.2"), RULES), {})
        h = web()
        h["snmp"]["values"] = None
        self.assertEqual(readings(h, RULES), {})


class TestThresholds(unittest.TestCase):
    def rig(self, **kw):
        return Rig([web(**kw)], thresholds=[Threshold("disk", 90, 600), Threshold("cpu_max", 95, 300)], repeat=3600)

    def test_high_after_for_then_ok(self):
        r = self.rig()
        self.assertEqual(r.e.snapshot(r.client.hosts(), r.clock()), [])
        self.assertEqual(r.e.snmp_values(ev(r, disks=(("/", 91), ("/data", 40))), r.clock.add(1)), [])
        self.assertEqual(r.e.tick(r.clock.add(599)), [])
        (n,) = r.e.tick(r.clock.add(1))
        self.assertEqual((n.kind, n.host["name"], n.metric["label"], n.metric["value"], n.metric["above"], n.since),
                         (E.HIGH, "web", "disk /", 91, 90, T0 + 1))
        self.assertEqual(sorted(n.to), ["admin@example.com", "ops@example.com", "pay@example.com"])
        self.assertEqual(list(r.e.state()["thresholds"]), ["1/disk//"])
        # Still high: a repeat after `repeat`, with the newest value.
        r.clock.add(3600)
        r.e.snmp_values(ev(r, disks=(("/", 93),)), r.clock())
        (s,) = r.e.tick(r.clock.add(10))
        self.assertEqual((s.kind, s.metric["value"]), (E.STILL_HIGH, 93))
        # At the line again: OK.
        (o,) = r.e.snmp_values(ev(r, disks=(("/", 90),)), r.clock.add(60))
        self.assertEqual((o.kind, o.metric["value"], o.since), (E.NORMAL, 90, T0 + 1))
        self.assertEqual(r.e.th_open, {})
        log = r.buf.getvalue()
        self.assertIn('event=high host=web ip=192.0.2.1 metric="disk /" value=91 above=90 alert_in=600s', log)
        self.assertIn('event=normal host=web ip=192.0.2.1 metric="disk /" value=90', log)

    def test_short_peak_confirm_and_gone(self):
        r = self.rig()
        r.e.snapshot(r.client.hosts(), r.clock())
        r.e.snmp_values(ev(r, busiest=100), r.clock.add(1))
        r.e.snmp_values(ev(r, busiest=50), r.clock.add(60))
        self.assertIn('reason="below the line again within for"', r.buf.getvalue())
        self.assertEqual(r.e.tick(r.clock.add(400)), [])
        # The event says high, the server doesn't any more: no mail.
        r.e.snmp_values({"host_id": 1, "values": values(busiest=100, at=r.clock())}, r.clock.add(1))
        r.client.hosts_[1]["snmp"]["values"] = values(busiest=40, at=r.clock())
        self.assertEqual(r.e.tick(r.clock.add(300)), [])
        self.assertIn('reason="not above the line on the server"', r.buf.getvalue())
        # Open, then the values go (agent lost, monitoring off): closed without mail.
        r.e.snmp_values(ev(r, busiest=100), r.clock.add(1))
        (n,) = r.e.tick(r.clock.add(300))
        self.assertEqual(n.kind, E.HIGH)
        h = dict(r.client.hosts_[1], snmp={"version": "v2c", "values": None})
        self.assertEqual(r.e.update(h, r.clock.add(1)), [])
        self.assertEqual(r.e.th_open, {})
        self.assertIn('metric="busiest CPU" reason="no values" note="no OK mail"', r.buf.getvalue())
        # Deleted: closed too.
        r.e.snmp_values(ev(r, busiest=100), r.clock.add(1))
        r.e.tick(r.clock.add(300))
        r.e.deleted(1, r.clock.add(1))
        self.assertEqual((r.e.th_open, r.e.th_pending), ({}, {}))

    def test_restart(self):
        r = self.rig(disks=(("/", 95),))
        r.e.snapshot(r.client.hosts(), r.clock())
        (n,) = r.e.tick(r.clock.add(600))
        st = r.e.state()
        self.assertEqual(st["version"], 3)
        # After a restart: still high, nothing; at the line, OK.
        r2 = Rig([web(disks=(("/", 95),))], state=st, thresholds=[Threshold("disk", 90, 600)])
        self.assertEqual(r2.e.snapshot(r2.client.hosts(), r2.clock()), [])
        (o,) = r2.e.snapshot([web(disks=(("/", 70),))], r2.clock.add(60))
        self.assertEqual((o.kind, o.metric["label"], o.since), (E.NORMAL, "disk /", T0))

    def test_mail(self):
        m = {"metric": "disk", "item": "/", "label": "disk /", "value": 91, "above": 90.0}
        b = {"id": 1, "name": "web", "ip": "192.0.2.1"}
        hi = E.Notice(E.HIGH, T0 + 600, to={}, host=b, since=T0, metric=m, groups=["Networks/dc/lan"])
        self.assertEqual(subject([(hi, [])]), "[ined] HIGH disk / 91 % on web (192.0.2.1)")
        ok = E.Notice(E.NORMAL, T0 + 3600, to={}, host=b, since=T0, metric=dict(m, value=78))
        self.assertEqual(subject([(ok, [])]), "[ined] OK disk / 78 % on web (192.0.2.1) after 1 h")
        still = E.Notice(E.STILL_HIGH, T0 + 7200, to={}, host=b, since=T0, metric=m)
        self.assertEqual(subject([(still, [])]), "[ined] STILL HIGH disk / 91 % on web (192.0.2.1) for 2 h")
        down = E.Notice(E.DOWN, T0, to={}, host=b, since=T0)
        self.assertEqual(subject([(down, []), (hi, []), (ok, [])]), "[ined] 1 down, 1 value high, 1 value OK again")
        text = compose("a@example.com", "b@example.com", [(hi, []), (ok, [])], "http://ined:8470", T0 + 3600).get_content()
        self.assertIn("HIGH       disk / 91 % (above 90 %) on web (192.0.2.1) since ", text)
        self.assertIn("           groups: Networks/dc/lan\n", text)
        self.assertIn("OK         disk / 78 % (at or below 90 %) on web (192.0.2.1) at ", text)

    def test_config(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        p = os.path.join(td.name, "a.toml")
        base = '[mail]\nfrom = "a@example.com"\n[alert]\nadmins = ["b@example.com"]\n'

        def cfg(extra):
            with open(p, "w") as f:
                f.write(base + extra)
            return load(p, env={})

        self.assertEqual(cfg("").thresholds, [])
        c = cfg('[[threshold]]\nmetric = "disk"\nabove = 90\n[[threshold]]\nmetric = "cpu"\nabove = 80.5\nfor = "10m"\n')
        self.assertEqual(c.thresholds, [Threshold("disk", 90.0, 300.0), Threshold("cpu", 80.5, 600.0)])
        for bad in ('metric = "load"\nabove = 1\n', 'metric = "cpu"\nabove = 100\n', 'metric = "cpu"\nabove = -1\n',
                    'metric = "cpu"\n', 'metric = "cpu"\nabove = 50\nfor = 5\n', 'metric = "cpu"\nabove = 50\nwhen = "x"\n',
                    'metric = "cpu"\nabove = true\n'):
            with self.assertRaises(ConfigError, msg=bad):
                cfg("[[threshold]]\n" + bad)

    def test_through_the_program(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        c = config(state_file=os.path.join(td.name, "s.json"), thresholds=[Threshold("disk", 90, 60)])
        client = FakeClient([web()], VIEWS, MAPS)
        log, _ = logger()
        sent, clock = [], Clock()
        app = App(c, log, client=client, send=sent.append, clock=clock)
        app.step(clock())
        client.hosts_[1]["snmp"]["values"] = values(disks=(("/", 97),))
        app.handle(("event", Event("r-1", "snmp.values", {"host_id": 1, "display_name": "web", "ip": "192.0.2.1",
                                                           "values": values(disks=(("/", 97),))})), clock.add(1))
        for _ in range(4):
            app.step(clock.add(30))
        self.assertEqual(sorted((m["To"], m["Subject"]) for m in sent), [
            ("admin@example.com", "[ined] HIGH disk / 97 % on web (192.0.2.1)"),
            ("ops@example.com", "[ined] HIGH disk / 97 % on web (192.0.2.1)"),
            ("pay@example.com", "[ined] HIGH disk / 97 % on web (192.0.2.1)"),
        ])


class TestThresholdFixes(unittest.TestCase):
    """Crosscheck 2026-11-18-04 C3, C4, C5, L6, L7, L8."""

    def test_old_values_and_host_down(self):
        r = Rig([web(disks=(("/", 95),))], thresholds=[Threshold("disk", 90, 60)], repeat=600)
        r.e.snapshot(r.client.hosts(), r.clock())
        (n,) = r.e.tick(r.clock.add(60))
        self.assertEqual(n.kind, E.HIGH)
        # The host goes down: no STILL HIGH while it is down.
        h = r.set(1, status="down", last_change=iso(r.clock()))
        r.e.update(h, r.clock.add(1))
        self.assertNotIn(E.STILL_HIGH, [x.kind for x in r.e.tick(r.clock.add(600))])
        # Its values are older than 15 min now: the alert ends without mail.
        self.assertEqual([x for x in r.e.tick(r.clock.add(400)) if x.metric], [])
        self.assertEqual(r.e.th_open, {})
        self.assertIn('metric="disk /" reason="values too old" note="no OK mail"', r.buf.getvalue())
        # A value above the line while the host is down waits for it.
        r2 = Rig([web(disks=(("/", 95),))], thresholds=[Threshold("disk", 90, 60)])
        r2.e.snapshot(r2.client.hosts(), r2.clock())
        r2.e.update(r2.set(1, status="down", last_change=iso(r2.clock())), r2.clock.add(1))
        self.assertEqual([x for x in r2.e.tick(r2.clock.add(120)) if x.metric], [])
        self.assertEqual(len(r2.e.th_pending), 1)

    def test_rule_changed_and_removed(self):
        r = Rig([web(disks=(("/", 95),))], thresholds=[Threshold("disk", 90, 60)])
        r.e.snapshot(r.client.hosts(), r.clock())
        r.e.tick(r.clock.add(60))
        st = r.e.state()
        # The line goes to 93; at 92 the OK says 93.
        r2 = Rig([web(disks=(("/", 92), ), at=T0 + 60)], state=st, thresholds=[Threshold("disk", 93, 60)])
        (o,) = [x for x in r2.e.snapshot(r2.client.hosts(), r2.clock.add(60)) if x.metric]
        self.assertEqual((o.kind, o.metric["above"]), (E.NORMAL, 93))
        # The rule removed: closed, "no rule".
        r3 = Rig([web(disks=(("/", 95),), at=T0 + 60)], state=st, thresholds=[])
        r3.e.snapshot(r3.client.hosts(), r3.clock.add(60))
        self.assertEqual(r3.e.th_open, {})
        self.assertIn('reason="no rule"', r3.buf.getvalue())

    def test_labels(self):
        h = web()
        h["snmp"]["values"]["storage"] += [
            {"name": "Virtual memory", "kind": "memory", "size_bytes": 1, "used_pct": 95},
            {"name": "C:\\ Label:  Serial Number 1234", "kind": "disk", "size_bytes": 1, "used_pct": 92}]
        rd = readings(h, [Threshold("memory", 50, 60), Threshold("disk", 90, 60)])
        self.assertEqual(sorted(v["label"] for v in rd.values()),
                         ["disk /", "disk /data", "disk C:\\", "memory (Physical memory)", "memory (Virtual memory)"])
        self.assertEqual(readings(web(), [Threshold("memory", 50, 60)])[("memory", "Physical memory")]["label"], "memory")

    def test_summary_and_log(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        r = Rig([web(disks=(("/", 95),))], thresholds=[Threshold("disk", 90, 60)])
        r.e.snapshot(r.client.hosts(), r.clock())
        r.e.tick(r.clock.add(60))
        n = r.e.summary(r.clock())
        self.assertEqual([(v["name"], v["metric"]["label"]) for v in n.values], [("web", "disk /")])
        self.assertEqual(subject([(n, [])]), "[ined] ined-alert started: 0 hosts down, 1 value high")
        text = compose("a@example.com", "b@example.com", [(n, [])], "http://ined:8470", r.clock()).get_content()
        self.assertIn("These values are high:\n  disk / 95 % (above 90 %) on web (192.0.2.1) since ", text)
        c = config(state_file=os.path.join(td.name, "s.json"), thresholds=[Threshold("disk", 90, 60)])
        log, buf = logger()
        app = App(c, log, client=FakeClient([web()], VIEWS, MAPS), send=lambda m: None, clock=Clock())
        app.step(app.clock())
        self.assertIn("open_threshold_alerts=0", buf.getvalue())


if __name__ == "__main__":
    unittest.main()
