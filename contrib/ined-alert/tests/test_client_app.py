"""TC-950: the client (SSE, reconnect, errors) and the program's loop with
fakes: alerts end up as mail and in the state file."""

import http.server
import json
import os
import tempfile
import threading
import unittest

from ined import APIError, Client, Event, parse_sse
from ined_alert.main import App, load_state

from .common import MAPS, VIEWS, config
from .fakes import Clock, FakeClient, host, logger


class Server:
    """Serves /api/v1/events: each connection gets the next script of lines,
    and the Last-Event-ID it sent is recorded."""

    def __init__(self, scripts, status=200):
        self.scripts, self.seen = list(scripts), []
        outer = self

        class H(http.server.BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_GET(self):
                outer.seen.append((self.path, self.headers.get("Last-Event-ID"), self.headers.get("Authorization")))
                if status != 200:
                    body = json.dumps({"error": {"code": "unauthenticated", "message": "missing or invalid bearer token"}}).encode()
                    self.send_response(status)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(body)))
                    self.end_headers()
                    self.wfile.write(body)
                    return
                if self.path == "/api/v1/hosts":
                    body = b'[{"id": 1}]'
                    self.send_response(200)
                    self.send_header("Content-Length", str(len(body)))
                    self.end_headers()
                    self.wfile.write(body)
                    return
                lines = outer.scripts.pop(0) if outer.scripts else []
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write("".join(lines).encode())
                self.wfile.flush()

        self.srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), H)
        self.srv.daemon_threads = True
        self.url = f"http://127.0.0.1:{self.srv.server_address[1]}"
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()

    def close(self):
        self.srv.shutdown()
        self.srv.server_close()


class TestClient(unittest.TestCase):
    def test_parse(self):
        lines = [b": keepalive\n", b"id: r-1\n", b"event: host.status\n", b'data: {"id": 1,\n', b'data: "status": "down"}\n', b"\n",
                 b"event: resync\r\n", b"data:\r\n", b"\r\n"]
        self.assertEqual(list(parse_sse(lines)), [Event("r-1", "host.status", {"id": 1, "status": "down"}), Event("", "resync", None)])

    def test_events_reconnect_and_resume(self):
        srv = Server([["id: run-1\nevent: host.status\ndata: {\"id\": 1}\n\n"], ["id: run-2\nevent: host.deleted\ndata: {\"id\": 2}\n\n"]])
        self.addCleanup(srv.close)
        stop = threading.Event()
        states = []
        c = Client(srv.url + "/", token="reader")
        got = []
        for ev in c.events(stop=stop, on_state=lambda s, r: states.append(s)):
            got.append((ev.id, ev.type))
            if len(got) == 2:
                stop.set()
        self.assertEqual(got, [("run-1", "host.status"), ("run-2", "host.deleted")])
        self.assertEqual([(p, l) for p, l, _ in srv.seen], [("/api/v1/events", None), ("/api/v1/events", "run-1")])
        self.assertEqual(srv.seen[0][2], "Bearer reader")
        self.assertEqual(states, ["connected", "disconnected", "connected"])
        self.assertEqual(c.hosts(), [{"id": 1}])

    def test_unauthorized(self):
        srv = Server([], status=401)
        self.addCleanup(srv.close)
        c = Client(srv.url, token="wrong")
        with self.assertRaises(APIError) as cm:
            next(c.events())
        self.assertEqual((cm.exception.status, cm.exception.code), (401, "unauthenticated"))
        with self.assertRaises(APIError):
            c.hosts()


class TestApp(unittest.TestCase):
    def rig(self, hosts, state_file=None, **cfg):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        cfg.setdefault("state_file", state_file or os.path.join(td.name, "state.json"))
        c = config(**cfg)
        client = FakeClient(hosts, VIEWS, MAPS)
        log, buf = logger()
        sent = []
        clock = Clock()
        app = App(c, log, client=client, send=sent.append, clock=clock)
        return app, client, sent, clock, buf

    def test_down_mail_and_state(self):
        app, client, sent, clock, buf = self.rig([host(1, "192.0.2.1", name="web"), host(3, "192.0.2.3")], startup_summary=True)
        self.assertEqual(app.check_token(), "read")
        app.handle(("conn", "connected", ""), clock())
        app.step(clock())
        self.assertTrue(app.loaded)
        client.hosts_[1].update(status="down")
        app.handle(("event", Event("r-1", "host.status", dict(client.hosts_[1]))), clock.add(1))
        for _ in range(4):
            app.step(clock.add(40))  # 161 s: alerted at 121, mailed 30 s later
        subjects = sorted((m["To"], m["Subject"]) for m in sent)
        self.assertEqual(subjects, [
            ("admin@example.com", "[ined] DOWN web (192.0.2.1)"),
            ("admin@example.com", "[ined] ined-alert started: 0 hosts down"),  # sent 30 s after the start
            ("ops@example.com", "[ined] DOWN web (192.0.2.1)"),
            ("pay@example.com", "[ined] DOWN web (192.0.2.1)"),
        ])
        st = load_state(app.cfg.state_file, app.log)
        self.assertEqual(list(st["open"]), ["1"])
        # Recovery goes to the same people.
        sent.clear()
        client.hosts_[1].update(status="up")
        app.handle(("event", Event("r-2", "host.status", dict(client.hosts_[1]))), clock.add(1))
        app.step(clock.add(31))
        self.assertEqual(sorted(m["To"] for m in sent), ["admin@example.com", "ops@example.com", "pay@example.com"])
        self.assertTrue(all(m["Subject"].startswith("[ined] UP web") for m in sent))
        self.assertEqual(load_state(app.cfg.state_file, app.log)["open"], {})
        self.assertIn("event=alert_queued kind=down host=web", buf.getvalue())

    def test_service_mail(self):
        # TC-957: a service.status event ends up as a mail, and in the state file.
        ws = [{"name": "https", "port": 443, "status": "up", "last_change": None, "last_ok": None}]
        app, client, sent, clock, buf = self.rig([host(1, "192.0.2.1", name="web", watch=ws)])
        app.step(clock())
        ws[0].update(status="down", last_change="2027-01-15T08:00:00Z")
        ev = {"host_id": 1, "display_name": "web", "ip": "192.0.2.1", "name": "https", "port": 443,
              "status": "down", "last_change": "2027-01-15T08:00:00Z"}
        app.handle(("event", Event("r-1", "service.status", ev)), clock.add(1))
        for _ in range(4):
            app.step(clock.add(40))
        self.assertEqual(sorted((m["To"], m["Subject"]) for m in sent), [
            ("admin@example.com", "[ined] DOWN https on web (192.0.2.1)"),
            ("ops@example.com", "[ined] DOWN https on web (192.0.2.1)"),
            ("pay@example.com", "[ined] DOWN https on web (192.0.2.1)"),
        ])
        st = load_state(app.cfg.state_file, app.log)
        self.assertEqual((st["version"], list(st["services"])), (2, ["1/https"]))
        self.assertIn("event=alert_queued kind=down host=web service=https", buf.getvalue())

    def test_unreachable_and_routes_reload(self):
        app, client, sent, clock, buf = self.rig([host(1, "192.0.2.1")])
        app.step(clock())
        app.handle(("conn", "disconnected", "connection refused"), clock())
        client.down = True
        for _ in range(12):
            app.step(clock.add(30))
        self.assertEqual([(m["To"], m["Subject"]) for m in sent], [("admin@example.com", "[ined] ined-server unreachable")])
        client.down = False
        app.handle(("conn", "connected", ""), clock())
        app.step(clock.add(31))
        self.assertEqual(sent[-1]["Subject"], "[ined] ined-server reachable again")
        # A group change reloads the routes a few seconds later.
        client.maps_ = {1: {"items": [{"type": "host", "id": 1, "parent_id": None}]}, 2: {"items": []}}
        app.handle(("event", Event("r-3", "group.deleted", {"id": 11})), clock())
        app.step(clock.add(1))
        self.assertEqual(app.routes.groups_of(1), ["Networks/dc/lan", "Payroll/app servers"])
        app.step(clock.add(5))
        self.assertEqual(app.routes.groups_of(1), [])

    def test_bad_state_file(self):
        d = tempfile.mkdtemp()
        p = os.path.join(d, "state.json")
        with open(p, "w") as f:
            f.write("{nope")
        log, buf = logger()
        self.assertEqual(load_state(p, log), {})
        self.assertTrue(os.path.exists(p + ".bad"))
        self.assertIn("event=state_unreadable", buf.getvalue())
        os.remove(p + ".bad")
        os.rmdir(d)

    def test_state_file_of_the_wrong_shape(self):
        # C1 of crosscheck 2026-11-17-01: valid JSON, wrong shape or a newer
        # version: set aside, and the program starts empty.
        d = tempfile.mkdtemp()
        p = os.path.join(d, "state.json")
        for st in ({"open": []}, {"server": None}, {"services": {"x": {}}}, {"version": 3}):
            with open(p, "w") as f:
                json.dump(st, f)
            app, _, _, _, buf = self.rig([], state_file=p)
            self.assertEqual((app.engine.open, app.engine.svc_open), ({}, {}))
            self.assertTrue(os.path.exists(p + ".bad"), st)
            self.assertIn("event=state_unreadable", buf.getvalue())
            os.remove(p + ".bad")
        os.rmdir(d)

    def test_wrong_token_warning(self):
        # L9 of crosscheck 2026-11-17-01: without the authenticated fields
        # the token wasn't accepted; with them but no access, an older server.
        app, client, _, _, buf = self.rig([])
        client.access = ""
        self.assertEqual(app.check_token(), "")
        self.assertIn('warning="the server doesn\'t accept the token"', buf.getvalue())
        client.health_["db"] = "ok"
        app.check_token()
        self.assertIn("an older server?", buf.getvalue())

    def test_fatal_token(self):
        app, client, sent, clock, buf = self.rig([])
        stop = threading.Event()
        app.q.put(("fatal", APIError(401, "unauthenticated", "missing or invalid bearer token")))
        self.assertEqual(app.run(stop), 1)
        self.assertIn("event=stopped", buf.getvalue())


if __name__ == "__main__":
    unittest.main()
