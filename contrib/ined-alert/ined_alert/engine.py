"""What is alerted and when (Design-alerting.md A3, A4, A9; SPEC-952, 956).

The engine does no I/O of its own: it gets hosts and events, and the time,
and returns notices. Before a "down" goes out it asks the server once more
(fetch), so an old event can never cause a false alert.
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


class Engine:
    def __init__(self, cfg: Config, routes: Routes, fetch: Callable[[int], dict | None],
                 log: logging.Logger, state: dict | None = None):
        self.cfg, self.routes, self.fetch, self.log = cfg, routes, fetch, log
        state = state or {}
        # Hosts with a down mail out: id -> {"host": brief, "since", "to", "sent"}.
        self.open: dict[int, dict] = {int(k): v for k, v in state.get("open", {}).items()}
        srv = state.get("server", {})
        self.server_alerted = bool(srv.get("unreachable"))
        self.degraded_alerted = bool(srv.get("degraded"))
        self.pending: dict[int, float] = {}  # down, not alerted yet: id -> down since
        self.hosts: dict[int, dict] = {}
        self.unreachable_since = 0.0
        self.unreachable_reason = ""
        self.degraded_since = 0.0
        self.degraded_reason = ""
        self.dirty = False

    # ---- state file ----

    def state(self) -> dict:
        return {"version": 1, "open": {str(k): v for k, v in sorted(self.open.items())},
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
        for h in hosts:
            out += self._apply(h, now, fresh=True)
        return out

    def update(self, h: dict, now: float) -> list[Notice]:
        """A host from an event (host.status, host.updated, host.created)."""
        self.hosts[h["id"]] = h
        return self._apply(h, now, fresh=False)

    def deleted(self, hid: int, now: float) -> list[Notice]:
        self.hosts.pop(hid, None)
        if self.pending.pop(hid, None) is not None:
            self.log.info(kv(event="cancelled", host_id=hid, reason="host deleted"))
        if hid in self.open:
            self._close(hid, "deleted")
        return []

    def _close(self, hid: int, reason: str) -> None:
        a = self.open.pop(hid)
        self.dirty = True
        self.log.info(kv(event="closed", host=a["host"]["name"], ip=a["host"]["ip"], reason=reason,
                         note="no recovery mail"))

    def _apply(self, h: dict, now: float, fresh: bool) -> list[Notice]:
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
                    self.log.warning(kv(event="confirm_failed", host=b["name"], error=str(e)))
                    h2 = h
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
        out: list[Notice] = []
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
        if self.cfg.repeat > 0:
            for hid, a in self.open.items():
                if now - a["sent"] >= self.cfg.repeat:
                    a["sent"] = now
                    self.dirty = True
                    out.append(Notice(STILL_DOWN, now, to=a["to"], host=a["host"], since=a["since"],
                                      groups=self.routes.groups_of(hid)))
        admins = {a: [] for a in self.cfg.admins}
        if self.unreachable_since and not self.server_alerted and now - self.unreachable_since >= self.cfg.server_down_after:
            self.server_alerted, self.dirty = True, True
            out.append(Notice(SERVER_DOWN, now, to=admins, since=self.unreachable_since, text=self.unreachable_reason))
        if self.degraded_since and not self.degraded_alerted and now - self.degraded_since >= self.cfg.server_down_after:
            self.degraded_alerted, self.dirty = True, True
            out.append(Notice(DEGRADED, now, to=admins, since=self.degraded_since, text=self.degraded_reason))
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
        that is down now."""
        down = [brief(h) | {"since": self._since(h, now), "groups": self.routes.groups_of(h["id"])}
                for h in self.hosts.values() if h.get("monitored", True) and h.get("status") == "down"]
        down.sort(key=lambda d: d["name"])
        return Notice(SUMMARY, now, to={a: [] for a in self.cfg.admins}, hosts=down)
