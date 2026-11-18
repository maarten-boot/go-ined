"""TC-958: the optional mail about new hosts ([new_hosts] enabled, SPEC-957)."""

import copy
import os
import tempfile
import unittest

from ined import Event
from ined_alert import engine as E
from ined_alert.config import ConfigError, load
from ined_alert.mail import compose, subject
from ined_alert.main import App

from .common import MAPS, VIEWS, config
from .fakes import Clock, FakeClient, host, logger

T0 = 1_800_000_000.0


class TestNewHosts(unittest.TestCase):
    def rig(self, **cfg):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        cfg.setdefault("state_file", os.path.join(td.name, "state.json"))
        client = FakeClient([host(1, "192.0.2.1", name="web")], VIEWS, copy.deepcopy(MAPS))
        log, buf = logger()
        sent, clock = [], Clock()
        app = App(config(**cfg), log, client=client, send=sent.append, clock=clock)
        app.step(clock())
        return app, client, sent, clock, buf

    def add(self, app, client, clock, hid, ip, parent, name=""):
        h = host(hid, ip, name=name)
        client.hosts_[hid] = h
        client.maps_[1]["items"].append({"type": "host", "id": hid, "parent_id": parent, "name": name or ip})
        app.handle(("event", Event(f"r-{hid}", "host.created", dict(h))), clock.add(1))

    def test_mailed_after_the_routes_know_the_host(self):
        app, client, sent, clock, buf = self.rig(new_hosts=True)
        self.add(app, client, clock, 4, "192.0.2.4", 11, name="printer-3")  # in Networks/dc/lan
        self.add(app, client, clock, 5, "192.0.2.5", None)  # at the root: admins only
        self.add(app, client, clock, 6, "192.0.2.6", 11)
        del client.hosts_[6]
        app.handle(("event", Event("r-7", "host.deleted", {"id": 6})), clock.add(1))
        app.step(clock.add(1))
        self.assertEqual(app.engine.new.keys(), {4, 5}, "before the routes are loaded again")
        for _ in range(3):
            app.step(clock.add(15))  # routes after 5 s, mail 30 s later
        got = sorted((m["To"], m["Subject"]) for m in sent)
        self.assertEqual(got, [
            ("admin@example.com", "[ined] 2 new hosts"),
            ("ops@example.com", "[ined] NEW printer-3 (192.0.2.4) in Networks/dc/lan"),
        ])
        text = next(m for m in sent if m["To"] == "ops@example.com").get_content()
        self.assertIn("NEW        printer-3 (192.0.2.4) added at ", text)
        self.assertIn("           groups: Networks/dc/lan\n", text)
        self.assertIn("event=new_host host=printer-3 ip=192.0.2.4", buf.getvalue())
        self.assertEqual(app.engine.new, {})

    def test_off_by_default(self):
        app, client, sent, clock, _ = self.rig()
        self.add(app, client, clock, 4, "192.0.2.4", 11)
        for _ in range(4):
            app.step(clock.add(15))
        self.assertEqual(sent, [])
        self.assertIn(4, app.engine.hosts)  # still followed as any host

    def test_subject_alone_and_mixed(self):
        n = E.Notice(E.NEW, T0, to={}, host={"id": 4, "name": "192.0.2.4", "ip": "192.0.2.4"})
        self.assertEqual(subject([(n, [])]), "[ined] NEW 192.0.2.4")
        down = E.Notice(E.DOWN, T0, to={}, host={"id": 1, "name": "web", "ip": "192.0.2.1"}, since=T0)
        self.assertEqual(subject([(down, []), (n, [])]), "[ined] 1 down, 1 new host")
        text = compose("a@example.com", "b@example.com", [(n, [])], "http://ined:8470", T0).get_content()
        self.assertIn("NEW        192.0.2.4 added at ", text)

    def test_config(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        p = os.path.join(td.name, "a.toml")
        base = '[mail]\nfrom = "a@example.com"\n[alert]\nadmins = ["b@example.com"]\n'
        with open(p, "w") as f:
            f.write(base)
        self.assertFalse(load(p, env={}).new_hosts)
        with open(p, "w") as f:
            f.write(base + "[new_hosts]\nenabled = true\n")
        self.assertTrue(load(p, env={}).new_hosts)
        for bad in ("[new_hosts]\nenabled = 1\n", "[new_hosts]\nmail = true\n"):
            with open(p, "w") as f:
                f.write(base + bad)
            with self.assertRaises(ConfigError):
                load(p, env={})


if __name__ == "__main__":
    unittest.main()
