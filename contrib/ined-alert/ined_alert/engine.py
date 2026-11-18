"""What is alerted and when (Design-alerting.md A3, A4, A9;
Design-service-alerts.md S8; SPEC-952, 956).

The engine does no I/O of its own: it gets hosts and events, and the time,
and returns notices. Before a "down" goes out it asks the server once more
(fetch), so an old event can never cause a false alert.

Watched services (the hosts' "watch" lists) are alerted like hosts, with
one rule more: while a host is down, or waiting to be alerted as down, the
alerts for its services wait; the host's alert says enough. When the host
is up again, a service still down is alerted down_after later.
"""

from __future__ import annotations

import logging
import re
from dataclasses import dataclass, field
from datetime import datetime
from typing import Callable

from .config import Config
from .logs import kv
from .routes import Routes

# Notice kinds.
DOWN, UP, STILL_DOWN = "down", "up", "still_down"
SERVER_DOWN, SERVER_UP, DEGRADED, PINGS_AGAIN, SUMMARY = "server_down", "server_up", "degraded", "pings_again", "summary"
NEW = "new"  # a host was added (SPEC-957)


@dataclass
class Notice:
    kind: str
    at: float
    to: dict[str, list[str]]  # recipient -> the route paths that send it there
    host: dict | None = None  # {"id", "name", "ip", "last_seen"}
    since: float = 0.0  # down (or unreachable) since
    groups: list[str] = field(default_factory=list)
    text: str = ""  # the reason, for server notices
    hosts: list[dict] = field(default_factory=list)  # summary: the hosts down
    service: dict | None = None  # {"name", "port", "last_ok"} for a service alert
    services: list[dict] = field(default_factory=list)  # summary: the services down


_FRACTION = re.compile(r"(\.\d{6})\d+")


def parse_time(s: str | None) -> float | None:
    """An RFC 3339 time from the API as seconds since the epoch."""
    if not s:
        return None
    try:
        return datetime.fromisoformat(_FRACTION.sub(r"\1", s.replace("Z", "+00:00"))).timestamp()
    except ValueError:
        return None


def brief(h: dict) -> dict:
    return {"id": h["id"], "name": h.get("display_name") or h.get("ip", ""), "ip": h.get("ip", ""),
            "last_seen": parse_time(h.get("last_seen"))}


def svc_brief(w: dict) -> dict:
    return {"name": w["name"], "port": w.get("port"), "last_ok": parse_time(w.get("last_ok"))}


def watched(h: dict) -> dict[str, dict] | None:
    """A host's watched services by name; None when the host has no
    "watch" (a server from before watched services)."""
    if "watch" not in h:
        return None
    return {w["name"]: w for w in h["watch"] or []}


def svc_key(k: tuple[int, str]) -> str:
    return f"{k[0]}/{k[1]}"


STATE_VERSION = 2


def _number(x) -> bool:
    return isinstance(x, (int, float)) and not isinstance(x, bool)


def _alert(v, service: bool) -> dict:
    """An open alert from the state file, checked."""
    if not isinstance(v, dict):
        raise ValueError("an alert is not an object")
    h = v.get("host")
    if not isinstance(h, dict) or not isinstance(h.get("name"), str) or not isinstance(h.get("ip"), str):
        raise ValueError("an alert without its host")
    if not _number(v.get("since")) or not _number(v.get("sent")):
        raise ValueError("an alert without since or sent")
    to = v.get("to")
    if not isinstance(to, dict) or not all(isinstance(a, str) and isinstance(p, list) for a, p in to.items()):
        raise ValueError("an alert without its recipients")
    if service and (not isinstance(v.get("service"), dict) or not isinstance(v["service"].get("name"), str)):
        raise ValueError("a service alert without its service")
    return v


def parse_state(st) -> dict:
    """Checks what a state file holds (SPEC-956) and returns it as the
    engine keeps it: {"open", "services", "unreachable", "degraded"}.
    Raises ValueError when it isn't a state file of version 1 or 2."""
    if not isinstance(st, dict):
        raise ValueError("not an object")
    ver = st.get("version", 1)
    if not isinstance(ver, int) or isinstance(ver, bool) or not 1 <= ver <= STATE_VERSION:
        raise ValueError(f"version {ver!r}; this ined-alert reads versions 1 and {STATE_VERSION}")
    parts = {}
    for name in ("open", "services", "server"):
        parts[name] = st.get(name, {})
        if not isinstance(parts[name], dict):
            raise ValueError(f'"{name}" is not an object')
    try:
        hosts = {int(k): _alert(v, False) for k, v in parts["open"].items()}
    except ValueError as e:
        raise ValueError(f'"open": {e}') from None
    services: dict[tuple[int, str], dict] = {}
    for k, v in parts["services"].items():
        hid, _, name = k.partition("/")
        try:
            if not name:
                raise ValueError("not host id/name")
            services[(int(hid), name)] = _alert(v, True)
        except ValueError as e:
            raise ValueError(f'"services" {k!r}: {e}') from None
    srv = parts["server"]
    return {"open": hosts, "services": services, "unreachable": bool(srv.get("unreachable")),
            "degraded": bool(srv.get("degraded"))}


class Engine:
    def __init__(self, cfg: Config, routes: Routes, fetch: Callable[[int], dict | None],
                 log: logging.Logger, state: dict | None = None):
        self.cfg, self.routes, self.fetch, self.log = cfg, routes, fetch, log
        st = parse_state(state or {})
        # Hosts with a down mail out: id -> {"host": brief, "since", "to", "sent"}.
        self.open: dict[int, dict] = st["open"]
        # Services with a down mail out: (host id, name) -> {"host", "service", "since", "to", "sent"}.
        self.svc_open: dict[tuple[int, str], dict] = st["services"]
        self.server_alerted = st["unreachable"]
        self.degraded_alerted = st["degraded"]
        # Hosts whose recovery the server couldn't confirm: asked again at
        # the next tick, with their watched services.
        self.recheck: set[int] = set()
        self.pending: dict[int, float] = {}  # down, not alerted yet: id -> down since
        self.svc_pending: dict[tuple[int, str], float] = {}  # the same for services
        self.svc_held: set[tuple[int, str]] = set()  # pending services waiting for their host (logged once)
        self.hosts: dict[int, dict] = {}
        # New hosts to mail (SPEC-957), once the routes know them: id -> added at.
        self.new: dict[int, float] = {}
        self.unreachable_since = 0.0
        self.unreachable_reason = ""
        self.degraded_since = 0.0
        self.degraded_reason = ""
        self.dirty = False

    # ---- state file ----

    def state(self) -> dict:
        return {"version": STATE_VERSION, "open": {str(k): v for k, v in sorted(self.open.items())},
                "services": {svc_key(k): v for k, v in sorted(self.svc_open.items())},
                "server": {"unreachable": self.server_alerted, "degraded": self.degraded_alerted}}

    # ---- hosts ----

    def _since(self, h: dict, now: float) -> float:
        t = parse_time(h.get("last_change"))
        return min(t, now) if t else now

    def snapshot(self, hosts: list[dict], now: float) -> list[Notice]:
        """Takes the full list of hosts (at start and after resync) and
        compares it with what was alerted (A9)."""
        self.hosts = {h["id"]: h for h in hosts}
        out: list[Notice] = []
        for hid in list(self.open):
            if hid not in self.hosts:
                self._close(hid, "deleted")
        for hid in list(self.pending):
            if hid not in self.hosts:
                del self.pending[hid]
        for k in list(self.svc_open):
            if k[0] not in self.hosts:
                self._close_svc(k, "host deleted")
        for k in list(self.svc_pending):
            if k[0] not in self.hosts:
                self._drop_svc(k)
        for h in hosts:
            out += self._apply(h, now, fresh=True)
        return out

    def update(self, h: dict, now: float) -> list[Notice]:
        """A host from an event (host.status, host.updated, host.created)."""
        self.hosts[h["id"]] = h
        return self._apply(h, now, fresh=False)

    def created(self, h: dict, now: float) -> list[Notice]:
        """A host.created event: alerted as any host, and with
        [new_hosts] enabled, mailed as new once the routes are loaded
        again (new_hosts)."""
        if self.cfg.new_hosts:
            self.new[h["id"]] = now
            self.log.info(kv(event="new_host", host=brief(h)["name"], ip=h.get("ip", "")))
        return self.update(h, now)

    def new_hosts(self, now: float) -> list[Notice]:
        """The notices for the new hosts; call it when the routes have been
        loaded after their host.created. A host deleted meanwhile is left out."""
        out = []
        for hid, at in sorted(self.new.items()):
            h = self.hosts.get(hid)
            if h is not None:
                out.append(Notice(NEW, at, to=self.routes.recipients(hid), host=brief(h), groups=self.routes.groups_of(hid)))
        self.new.clear()
        return out

    def deleted(self, hid: int, now: float) -> list[Notice]:
        self.hosts.pop(hid, None)
        self.new.pop(hid, None)
        if self.pending.pop(hid, None) is not None:
            self.log.info(kv(event="cancelled", host_id=hid, reason="host deleted"))
        if hid in self.open:
            self._close(hid, "deleted")
        for k in [k for k in self.svc_pending if k[0] == hid]:
            self._drop_svc(k)
        for k in [k for k in self.svc_open if k[0] == hid]:
            self._close_svc(k, "host deleted")
        return []

    def service(self, e: dict, now: float) -> list[Notice]:
        """A service.status event: {"host_id", "display_name", "ip", "name",
        "port", "status", "last_change"}."""
        hid = e.get("host_id")
        h = self.hosts.get(hid)
        if h is None or watched(h) is None:
            return []  # a host event or the next snapshot brings it
        ws, found = [], False
        for w in h["watch"] or []:
            if w["name"] == e.get("name") and w.get("port") == e.get("port"):
                w = dict(w, status=e.get("status"), last_change=e.get("last_change"))
                found = True
            ws.append(w)
        if not found:
            return []  # not watched by now; host.updated says so
        h = dict(h, watch=ws)
        if e.get("display_name"):
            h["display_name"] = e["display_name"]
        if e.get("ip"):
            h["ip"] = e["ip"]
        self.hosts[hid] = h
        return self._apply_services(h, now, fresh=False)

    def _drop_svc(self, k: tuple[int, str]) -> None:
        self.svc_pending.pop(k, None)
        self.svc_held.discard(k)

    def _close_svc(self, k: tuple[int, str], reason: str) -> None:
        a = self.svc_open.pop(k)
        self.dirty = True
        self.log.info(kv(event="closed", host=a["host"]["name"], ip=a["host"]["ip"], service=k[1], reason=reason,
                         note="no recovery mail"))

    def _close(self, hid: int, reason: str) -> None:
        a = self.open.pop(hid)
        self.dirty = True
        self.log.info(kv(event="closed", host=a["host"]["name"], ip=a["host"]["ip"], reason=reason,
                         note="no recovery mail"))

    def _apply(self, h: dict, now: float, fresh: bool) -> list[Notice]:
        return self._apply_host(h, now, fresh) + self._apply_services(h, now, fresh)

    def _apply_services(self, h: dict, now: float, fresh: bool) -> list[Notice]:
        ws = watched(h)
        hid, b = h["id"], brief(h)
        if ws is None:
            if fresh:  # the server no longer lists watched services (older than v0.50)
                for k in [k for k in self.svc_pending if k[0] == hid]:
                    self._drop_svc(k)
                for k in [k for k in self.svc_open if k[0] == hid]:
                    self._close_svc(k, "the server lists no watched services")
            return []
        mon = h.get("monitored", True)
        if mon:
            self._renamed(hid, b, ws)
        why = "monitoring turned off" if not mon else "no longer watched"
        for k in [k for k in self.svc_pending if k[0] == hid and (not mon or k[1] not in ws)]:
            self._drop_svc(k)
            self.log.info(kv(event="cancelled", host=b["name"], ip=b["ip"], service=k[1], reason=why))
        for k in [k for k in self.svc_open if k[0] == hid and (not mon or k[1] not in ws)]:
            self._close_svc(k, why)
        if not mon:
            return []
        out: list[Notice] = []
        for name, w in ws.items():
            k, status = (hid, name), w.get("status")
            if status == "down":
                if k not in self.svc_open and k not in self.svc_pending:
                    self.svc_pending[k] = self._since(w, now)
                    self.log.info(kv(event="service_down", host=b["name"], ip=b["ip"], service=name,
                                     groups=self.routes.groups_of(hid),
                                     alert_in=f"{int(max(0, self.svc_pending[k] + self.cfg.down_after - now))}s"))
                continue
            if k in self.svc_pending:
                self._drop_svc(k)
                self.log.info(kv(event="cancelled", host=b["name"], ip=b["ip"], service=name,
                                 reason="up again within down_after" if status == "up" else f"status {status}"))
            if status == "unknown" and k in self.svc_open:
                # Nothing checks it now (services turned off, a new IP):
                # the alert ends without a mail, as for a host whose
                # monitoring is turned off (crosscheck 2026-11-18-01 C11).
                self._close_svc(k, "status unknown")
                continue
            if status != "up" or k not in self.svc_open:
                continue
            if not fresh:
                try:  # confirm: an old event must not end an alert
                    h2 = self.fetch(hid)
                except OSError as e:
                    self.log.warning(kv(event="confirm_failed", host=b["name"], service=name, error=str(e),
                                        note="asked again at the next tick"))
                    self.recheck.add(hid)
                    continue
                if h2 is None:
                    self._close_svc(k, "host deleted")
                    continue
                w2 = (watched(h2) or {}).get(name)
                if w2 is None:
                    self._close_svc(k, "no longer watched")
                    continue
                if w2.get("status") != "up":
                    continue
            a = self.svc_open.pop(k)
            self.dirty = True
            self.log.info(kv(event="service_up", host=b["name"], ip=b["ip"], service=name,
                             down_for=f"{int(now - a['since'])}s"))
            out.append(Notice(UP, now, to=a["to"], host=b, service=a["service"], since=a["since"],
                              groups=self.routes.groups_of(hid)))
        return out

    def _renamed(self, hid: int, b: dict, ws: dict[str, dict]) -> None:
        """An alerted service now watched under another name for the same
        port ("ssh" and "tcp:22") keeps its alert, as the server keeps its
        status. (A waiting one needs nothing: the new name is down since the
        same time.)"""
        by_port = {w.get("port"): name for name, w in ws.items()}
        for k in [k for k in self.svc_open if k[0] == hid and k[1] not in ws]:
            new = by_port.get(self.svc_open[k]["service"].get("port"))
            if new is None or (hid, new) in self.svc_open:
                continue
            a = self.svc_open.pop(k)
            a["service"] = dict(a["service"], name=new)
            self.svc_open[(hid, new)] = a
            self._drop_svc((hid, new))
            self.dirty = True
            self.log.info(kv(event="renamed", host=b["name"], ip=b["ip"], service=k[1], to=new))

    def _apply_host(self, h: dict, now: float, fresh: bool) -> list[Notice]:
        hid, status = h["id"], h.get("status")
        b = brief(h)
        if not h.get("monitored", True):
            if self.pending.pop(hid, None) is not None:
                self.log.info(kv(event="cancelled", host=b["name"], ip=b["ip"], reason="monitoring turned off"))
            if hid in self.open:
                self._close(hid, "monitoring turned off")
            return []
        if status == "down":
            if hid not in self.open and hid not in self.pending:
                self.pending[hid] = self._since(h, now)
                self.log.info(kv(event="down", host=b["name"], ip=b["ip"], groups=self.routes.groups_of(hid),
                                 alert_in=f"{int(max(0, self.pending[hid] + self.cfg.down_after - now))}s"))
            return []
        if self.pending.pop(hid, None) is not None:
            self.log.info(kv(event="cancelled", host=b["name"], ip=b["ip"],
                             reason="up again within down_after" if status == "up" else f"status {status}"))
        if status == "up" and hid in self.open:
            if not fresh:
                try:  # confirm: an old event must not end an alert
                    h2 = self.fetch(hid)
                except OSError as e:
                    self.log.warning(kv(event="confirm_failed", host=b["name"], error=str(e),
                                        note="asked again at the next tick"))
                    self.recheck.add(hid)
                    return []
                if h2 is None:
                    self._close(hid, "deleted")
                    return []
                if h2.get("status") != "up":
                    return []
            a = self.open.pop(hid)
            self.dirty = True
            self.log.info(kv(event="up", host=b["name"], ip=b["ip"], down_for=f"{int(now - a['since'])}s"))
            return [Notice(UP, now, to=a["to"], host=b, since=a["since"], groups=self.routes.groups_of(hid))]
        return []

    def tick(self, now: float) -> list[Notice]:
        """Alerts that are due: hosts down for down_after (checked with the
        server first), repeats, and the server's own state."""
        out: list[Notice] = self._recheck(now)
        for hid, since in list(self.pending.items()):
            if now - since < self.cfg.down_after:
                continue
            try:
                h = self.fetch(hid)
            except OSError as e:
                self.log.warning(kv(event="confirm_failed", host_id=hid, error=str(e)))
                continue  # again at the next tick
            del self.pending[hid]
            if h is None:
                continue
            self.hosts[hid] = h
            if h.get("status") != "down" or not h.get("monitored", True):
                self.log.info(kv(event="cancelled", host=h.get("display_name"), reason=f"status {h.get('status')} on the server"))
                continue
            b = brief(h)
            to = self.routes.recipients(hid)
            self.open[hid] = {"host": b, "since": since, "to": to, "sent": now}
            self.dirty = True
            out.append(Notice(DOWN, now, to=to, host=b, since=since, groups=self.routes.groups_of(hid)))
        out += self._tick_services(now)
        if self.cfg.repeat > 0:
            for hid, a in self.open.items():
                if now - a["sent"] >= self.cfg.repeat:
                    a["sent"] = now
                    self.dirty = True
                    out.append(Notice(STILL_DOWN, now, to=a["to"], host=a["host"], since=a["since"],
                                      groups=self.routes.groups_of(hid)))
            for (hid, _), a in self.svc_open.items():
                if now - a["sent"] >= self.cfg.repeat and not self._host_down(hid):
                    a["sent"] = now
                    self.dirty = True
                    out.append(Notice(STILL_DOWN, now, to=a["to"], host=a["host"], service=a["service"],
                                      since=a["since"], groups=self.routes.groups_of(hid)))
        admins = {a: [] for a in self.cfg.admins}
        if self.unreachable_since and not self.server_alerted and now - self.unreachable_since >= self.cfg.server_down_after:
            self.server_alerted, self.dirty = True, True
            out.append(Notice(SERVER_DOWN, now, to=admins, since=self.unreachable_since, text=self.unreachable_reason))
        if self.degraded_since and not self.degraded_alerted and now - self.degraded_since >= self.cfg.server_down_after:
            self.degraded_alerted, self.dirty = True, True
            out.append(Notice(DEGRADED, now, to=admins, since=self.degraded_since, text=self.degraded_reason))
        return out

    def _recheck(self, now: float) -> list[Notice]:
        """Recoveries the server couldn't confirm before: asked again; the
        server's answer counts as a snapshot of that host."""
        out: list[Notice] = []
        for hid in sorted(self.recheck):
            if hid not in self.open and not any(k[0] == hid for k in self.svc_open):
                self.recheck.discard(hid)
                continue
            try:
                h = self.fetch(hid)
            except OSError:
                continue  # logged when it was first asked
            self.recheck.discard(hid)
            if h is None:
                out += self.deleted(hid, now)
                continue
            self.hosts[hid] = h
            out += self._apply(h, now, fresh=True)
        return out

    def _host_down(self, hid: int) -> bool:
        """The host is down, or waiting to be alerted as down: its service
        alerts wait (S8)."""
        h = self.hosts.get(hid)
        return hid in self.pending or (h is not None and h.get("status") == "down")

    def _tick_services(self, now: float) -> list[Notice]:
        out: list[Notice] = []
        fetched: dict[int, dict | None] = {}  # one read per host per tick
        for k, since in list(self.svc_pending.items()):
            hid, name = k
            h = self.hosts.get(hid)
            if self._host_down(hid):
                if k not in self.svc_held:
                    self.svc_held.add(k)
                    self.log.info(kv(event="held", host=(h or {}).get("display_name", hid), service=name,
                                     reason="the host is down"))
                continue
            self.svc_held.discard(k)
            start = since
            if h is not None and h.get("status") == "up":
                start = max(start, parse_time(h.get("last_change")) or 0.0)  # counted from the host's return
            if now - start < self.cfg.down_after:
                continue
            if hid not in fetched:
                try:
                    fetched[hid] = self.fetch(hid)
                except OSError as e:
                    self.log.warning(kv(event="confirm_failed", host_id=hid, service=name, error=str(e)))
                    continue  # again at the next tick
            h = fetched[hid]
            if h is None:
                self._drop_svc(k)
                continue
            self.hosts[hid] = h
            w = (watched(h) or {}).get(name)
            if not h.get("monitored", True) or w is None or w.get("status") != "down":
                self._drop_svc(k)
                self.log.info(kv(event="cancelled", host=h.get("display_name"), service=name,
                                 reason="not down on the server" if w else "no longer watched"))
                continue
            if self._host_down(hid):
                continue  # the host went down meanwhile: held
            self._drop_svc(k)
            b, sv = brief(h), svc_brief(w)
            to = self.routes.recipients(hid)
            self.svc_open[k] = {"host": b, "service": sv, "since": since, "to": to, "sent": now}
            self.dirty = True
            out.append(Notice(DOWN, now, to=to, host=b, service=sv, since=since, groups=self.routes.groups_of(hid)))
        return out

    # ---- the server ----

    def reachable(self, ok: bool, reason: str, now: float) -> list[Notice]:
        """The event stream connected (ok) or was lost."""
        if not ok:
            if not self.unreachable_since:
                self.unreachable_since, self.unreachable_reason = now, reason
            return []
        since, self.unreachable_since = self.unreachable_since, 0.0
        if self.server_alerted:
            self.server_alerted, self.dirty = False, True
            return [Notice(SERVER_UP, now, to={a: [] for a in self.cfg.admins}, since=since)]
        return []

    def health(self, degraded: bool, reason: str, now: float) -> list[Notice]:
        """/health: degraded means the server can't ping (or its database
        fails)."""
        if degraded:
            if not self.degraded_since:
                self.degraded_since, self.degraded_reason = now, reason
                self.log.warning(kv(event="server_degraded", reason=reason))
            return []
        since, self.degraded_since = self.degraded_since, 0.0
        if since:
            self.log.info(kv(event="server_ok"))
        if self.degraded_alerted:
            self.degraded_alerted, self.dirty = False, True
            return [Notice(PINGS_AGAIN, now, to={a: [] for a in self.cfg.admins}, since=since)]
        return []

    def summary(self, now: float) -> Notice:
        """The admins' mail at start (startup_summary): every monitored host
        that is down now, and the watched services down on the others."""
        down = [brief(h) | {"since": self._since(h, now), "groups": self.routes.groups_of(h["id"])}
                for h in self.hosts.values() if h.get("monitored", True) and h.get("status") == "down"]
        down.sort(key=lambda d: d["name"])
        svcs = []
        for h in self.hosts.values():
            if not h.get("monitored", True) or h.get("status") == "down":
                continue  # a host down says enough
            for w in (watched(h) or {}).values():
                if w.get("status") == "down":
                    svcs.append(brief(h) | {"service": svc_brief(w), "since": self._since(w, now),
                                            "groups": self.routes.groups_of(h["id"])})
        svcs.sort(key=lambda d: (d["name"], d["service"]["name"]))
        return Notice(SUMMARY, now, to={a: [] for a in self.cfg.admins}, hosts=down, services=svcs)
