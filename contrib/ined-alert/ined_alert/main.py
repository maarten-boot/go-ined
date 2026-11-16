"""ined-alert: the program (Design-alerting.md, SPEC-950..956).

    ined-alert [--config alert.toml] [--check | --test-mail]

Exit codes: 0 stopped by a signal (or --check/--test-mail succeeded),
1 a runtime error such as a refused token, 2 a configuration error.
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import queue
import signal
import smtplib
import sys
import tempfile
import threading
import time
from typing import Callable

from ined import APIError, Client

from . import config as C
from .engine import Engine, Notice
from .logs import kv, setup
from .mail import Batcher, Outbox, compose, smtp_sender, test_message
from .routes import Routes

HEALTH_EVERY = 60.0  # seconds between /health checks
ROUTES_AFTER = 5.0  # reload the routes at most this often after changes

# Events after which the groups (and so the routes) are loaded again.
_ROUTE_EVENTS = {"host.created", "host.updated", "group.created", "group.updated", "group.deleted",
                 "view.created", "view.updated", "view.deleted", "placement.updated", "placement.deleted"}


def load_state(path: str, log: logging.Logger) -> dict:
    try:
        with open(path, encoding="utf-8") as f:
            st = json.load(f)
        if not isinstance(st, dict):
            raise ValueError("not an object")
        return st
    except FileNotFoundError:
        return {}
    except (OSError, ValueError) as e:
        bad = path + ".bad"
        try:
            os.replace(path, bad)
        except OSError:
            bad = ""
        log.error(kv(event="state_unreadable", file=path, error=str(e), moved_to=bad))
        return {}


def save_state(path: str, st: dict) -> None:
    """Writes the state file atomically (mode 0600)."""
    d = os.path.dirname(path) or "."
    os.makedirs(d, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=d, prefix=".state-")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(st, f, indent=1, sort_keys=True)
        os.replace(tmp, path)
    except BaseException:
        os.unlink(tmp)
        raise


class App:
    def __init__(self, cfg: C.Config, log: logging.Logger, client: Client | None = None,
                 send: Callable | None = None, clock: Callable[[], float] = time.time):
        self.cfg, self.log, self.clock = cfg, log, clock
        self.client = client or Client(cfg.url, cfg.token)
        self.routes = Routes(cfg.routes, cfg.admins)
        self.engine = Engine(cfg, self.routes, self._fetch, log, load_state(cfg.state_file, log))
        self.batcher = Batcher(cfg.batch)
        self.outbox = Outbox(send or smtp_sender(cfg.mail_host, cfg.mail_port), log)
        self.q: queue.Queue = queue.Queue()
        self.loaded = False
        self.routes_dirty = 0.0  # when routes changed, 0 = not
        self.next_health = 0.0
        self.last_warnings: list[str] = []
        self.connected = True  # until the stream says otherwise; logs only the first loss

    def _fetch(self, hid: int) -> dict | None:
        try:
            return self.client.host(hid)
        except APIError as e:
            if e.status == 404:
                return None
            raise OSError(str(e)) from None

    # ---- loading ----

    def load_routes(self) -> None:
        views = self.client.views()
        maps = {v["id"]: self.client.map(v["id"]) for v in views}
        self.routes.load(views, maps)
        if self.routes.warnings != self.last_warnings:
            for w in self.routes.warnings:
                self.log.warning(kv(event="config_warning", warning=w))
            self.last_warnings = list(self.routes.warnings)
        self.routes_dirty = 0.0

    def load_all(self, now: float) -> list[Notice]:
        self.load_routes()
        return self.engine.snapshot(self.client.hosts(), now)

    def check_token(self) -> str:
        """Logs what the token may do (A2); returns the access."""
        h = self.client.health()
        acc = h.get("access", "")
        if acc == "full":
            self.log.warning(kv(event="token_warning", warning="the token may change things; use server.read_token"))
        elif acc == "":
            self.log.warning(kv(event="token_warning", warning="the server doesn't say what the token may do (an older server?)"))
        return acc

    # ---- the loop ----

    def notices(self, ns: list[Notice], now: float) -> None:
        for n in ns:
            if not n.to:
                self.log.warning(kv(event="no_recipient", kind=n.kind, **({"host": n.host["name"]} if n.host else {})))
            fields = {"event": "alert_queued", "kind": n.kind} | ({"host": n.host["name"]} if n.host else {})
            self.log.info(kv(**fields, to=sorted(n.to)))
            self.batcher.add(n, now)

    def flush(self, now: float, everything: bool = False) -> None:
        for rcpt, items in self.batcher.due(now, everything):
            self.outbox.put(compose(self.cfg.mail_from, rcpt, items, self.cfg.url, now), now)
        self.outbox.tick(now)
        if self.engine.dirty:
            try:
                save_state(self.cfg.state_file, self.engine.state())
                self.engine.dirty = False
            except OSError as e:
                self.log.error(kv(event="state_not_saved", file=self.cfg.state_file, error=str(e)))

    def handle(self, item, now: float) -> None:
        kind = item[0]
        if kind == "conn":
            _, state, reason = item
            if state == "connected":
                self.log.info(kv(event="connected", url=self.cfg.url))
                self.notices(self.engine.reachable(True, "", now), now)
            else:  # the first loss is a warning, the retries after it debug
                (self.log.warning if self.connected else self.log.debug)(kv(event="disconnected", url=self.cfg.url, reason=reason))
                self.notices(self.engine.reachable(False, reason, now), now)
            self.connected = state == "connected"
            return
        if kind == "fatal":
            raise item[1]
        ev = item[1]
        if not self.loaded:
            return  # the snapshot that follows covers it
        if ev.type in ("host.status", "host.updated", "host.created"):
            self.notices(self.engine.update(ev.data, now), now)
        elif ev.type == "host.deleted":
            self.notices(self.engine.deleted(ev.data["id"], now), now)
        elif ev.type == "resync":
            self.log.info(kv(event="resync", reason=(ev.data or {}).get("reason", "")))
            self.notices(self.load_all(now), now)
        if ev.type in _ROUTE_EVENTS and not self.routes_dirty:
            self.routes_dirty = now

    def step(self, now: float) -> None:
        """What runs about once a second: loading, health, due alerts, mail."""
        try:
            if not self.loaded:
                self.notices(self.load_all(now), now)
                self.loaded = True
                self.log.info(kv(event="loaded", hosts=len(self.engine.hosts), open_alerts=len(self.engine.open),
                                 routes=len(self.cfg.routes)))
                if self.cfg.startup_summary:
                    self.notices([self.engine.summary(now)], now)
            if self.routes_dirty and now - self.routes_dirty >= ROUTES_AFTER:
                self.load_routes()
            if now >= self.next_health:
                self.next_health = now + HEALTH_EVERY
                h = self.client.health()
                reason = "; ".join(x for x in (h.get("icmp_error", ""), h.get("db", "") if h.get("db") not in ("", "ok") else "") if x)
                self.notices(self.engine.health(h.get("status") == "degraded", reason or "status degraded", now), now)
        except APIError as e:
            if e.status in (401, 403):
                raise
            self.log.warning(kv(event="server_error", error=str(e)))
        except OSError as e:
            self.log.debug(kv(event="server_unreachable", error=str(e)))
        self.notices(self.engine.tick(now), now)
        self.flush(now)

    def run(self, stop: threading.Event) -> int:
        def reader():
            try:
                for ev in self.client.events(stop=stop, on_state=lambda s, r: self.q.put(("conn", s, r))):
                    self.q.put(("event", ev))
            except APIError as e:
                self.q.put(("fatal", e))

        threading.Thread(target=reader, name="events", daemon=True).start()
        try:
            while not stop.is_set():
                try:
                    item = self.q.get(timeout=1.0)
                    self.handle(item, self.clock())
                    while True:  # drain what is there
                        self.handle(self.q.get_nowait(), self.clock())
                except queue.Empty:
                    pass
                self.step(self.clock())
        except APIError as e:
            self.log.error(kv(event="stopped", error=str(e), hint="check server.token"))
            return 1
        finally:
            self.flush(self.clock(), everything=True)
            for l in self.outbox.letters:
                self.log.error(kv(event="mail_dropped", to=l.msg["To"], subject=l.msg["Subject"], reason="stopping"))
        self.log.info(kv(event="stopped"))
        return 0


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog="ined-alert", description="Email alerts per group for go-ined.")
    p.add_argument("--config", default=str(C.default_config_path()), help="the config file (default: %(default)s)")
    g = p.add_mutually_exclusive_group()
    g.add_argument("--check", action="store_true", help="check the config, the token and the routes, then exit")
    g.add_argument("--test-mail", action="store_true", help="send a test mail to the admins, then exit")
    a = p.parse_args(argv)
    try:
        cfg = C.load(a.config)
    except C.ConfigError as e:
        print(f"ined-alert: {e}", file=sys.stderr)
        return 2
    log = setup(cfg.log_level, cfg.syslog_address, cfg.syslog_facility)
    app = App(cfg, log)
    if a.test_mail:
        if not cfg.admins:
            print("ined-alert: --test-mail sends to alert.admins, which is empty", file=sys.stderr)
            return 2
        now = time.time()
        for rcpt in cfg.admins:
            m = test_message(cfg.mail_from, rcpt, cfg.url, now)
            try:
                app.outbox.send(m)
            except (OSError, smtplib.SMTPException) as e:
                log.error(kv(event="mail_failed", to=rcpt, error=str(e)))
                return 1
            log.info(kv(event="mail_sent", to=rcpt, subject=m["Subject"]))
        return 0
    log.info(kv(event="started", url=cfg.url, mail=f"{cfg.mail_host}:{cfg.mail_port}", admins=cfg.admins,
                routes=len(cfg.routes), state_file=cfg.state_file, syslog=cfg.syslog_address or "off"))
    if not cfg.admins:
        log.warning(kv(event="config_warning", warning="no alert.admins: server problems are only logged"))
    if a.check:
        try:
            acc = app.check_token()
            app.load_routes()
            hosts = app.client.hosts()
        except (APIError, OSError) as e:
            log.error(kv(event="check_failed", error=str(e)))
            return 1
        print(f"server {cfg.url}: token access {acc or 'not reported'}, {len(hosts)} hosts")
        for r in cfg.routes:
            n = sum(1 for h in hosts if any(r.group in paths for paths in app.routes.recipients(h["id"]).values()))
            print(f'route "{r.group}" -> {", ".join(r.to)}: {n} hosts')
        for w in app.routes.warnings:
            print("warning: " + w)
        return 0 if not app.routes.warnings else 1
    try:
        app.check_token()
    except APIError as e:
        if e.status in (401, 403):
            log.error(kv(event="stopped", error=str(e), hint="check server.token"))
            return 1
    except OSError as e:
        log.warning(kv(event="disconnected", url=cfg.url, reason=str(e)))
    stop = threading.Event()
    for s in (signal.SIGINT, signal.SIGTERM):
        signal.signal(s, lambda *_: stop.set())
    return app.run(stop)
