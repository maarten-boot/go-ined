"""Test helpers: a small SMTP server, a fake API client and a clock."""

from __future__ import annotations

import io
import socketserver
import threading
from email import message_from_bytes, policy

from ined import APIError
from ined_alert.logs import setup


class SMTPServer:
    """Accepts mail on 127.0.0.1 and keeps it (EHLO, MAIL, RCPT, DATA)."""

    def __init__(self, port: int = 0):
        self.messages = []
        outer = self

        class Handler(socketserver.StreamRequestHandler):
            def handle(self):
                self.wfile.write(b"220 fake ESMTP\r\n")
                rcpts = []
                while line := self.rfile.readline():
                    cmd = line.decode().strip().upper()
                    if cmd.startswith(("EHLO", "HELO")):
                        self.wfile.write(b"250 fake\r\n")
                    elif cmd.startswith("MAIL"):
                        rcpts = []
                        self.wfile.write(b"250 ok\r\n")
                    elif cmd.startswith("RCPT"):
                        rcpts.append(line.decode().split(":", 1)[1].strip().strip("<>"))
                        self.wfile.write(b"250 ok\r\n")
                    elif cmd == "DATA":
                        self.wfile.write(b"354 go on\r\n")
                        data = b""
                        while (l := self.rfile.readline()) not in (b".\r\n", b""):
                            data += l[1:] if l.startswith(b"..") else l
                        outer.messages.append((rcpts, message_from_bytes(data, policy=policy.default)))
                        self.wfile.write(b"250 queued\r\n")
                    elif cmd == "QUIT":
                        self.wfile.write(b"221 bye\r\n")
                        return
                    else:
                        self.wfile.write(b"250 ok\r\n")

        self.srv = socketserver.ThreadingTCPServer(("127.0.0.1", port), Handler)
        self.srv.daemon_threads = True
        self.port = self.srv.server_address[1]
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()

    def close(self):
        self.srv.shutdown()
        self.srv.server_close()


class FakeClient:
    """The parts of ined.Client the alerter uses, from dicts."""

    def __init__(self, hosts=None, views=None, maps=None, access="read"):
        self.hosts_ = {h["id"]: h for h in (hosts or [])}
        self.views_ = views or [{"id": 1, "name": "Networks"}]
        self.maps_ = maps or {1: {"view_id": 1, "items": []}}
        self.access = access
        self.health_ = {"status": "ok", "version": "test"}
        self.down = False  # True: every call fails as if the server were gone
        self.fetches = 0

    def _check(self):
        if self.down:
            raise ConnectionRefusedError("connection refused")

    def health(self):
        self._check()
        return dict(self.health_, access=self.access) if self.access else dict(self.health_)

    def hosts(self):
        self._check()
        return [dict(h) for h in self.hosts_.values()]

    def host(self, hid):
        self._check()
        self.fetches += 1
        if hid not in self.hosts_:
            raise APIError(404, "not_found", "no such host")
        return dict(self.hosts_[hid])

    def views(self):
        self._check()
        return list(self.views_)

    def map(self, view):
        self._check()
        return self.maps_[view]

    def events(self, **_):
        return iter(())


def host(hid, ip, status="up", name="", last_change=None, monitored=True, last_seen=None, watch=None):
    """A host as the API gives it; without watch, as a server from before
    watched services (no "watch" key)."""
    h = {"id": hid, "ip": ip, "display_name": name or ip, "status": status, "monitored": monitored,
         "last_change": last_change, "last_seen": last_seen}
    if watch is not None:
        h["watch"] = watch
    return h


PORTS = {"ssh": 22, "http": 80, "https": 443}


def svc(name, status="up", last_change=None, last_ok=None):
    """A watched service as in a host's "watch" list."""
    port = int(name[4:]) if name.startswith("tcp:") else PORTS[name]
    return {"name": name, "port": port, "status": status, "last_change": last_change, "last_ok": last_ok}


def logger():
    """A logger writing into a StringIO; returns (log, buffer)."""
    buf = io.StringIO()
    return setup("debug", stream=buf), buf


class Clock:
    def __init__(self, t=1_800_000_000.0):
        self.t = t

    def __call__(self):
        return self.t

    def add(self, s):
        self.t += s
        return self.t

