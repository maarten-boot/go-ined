"""Batching notices per recipient and sending them as mail
(Design-alerting.md A6, A7; Design-service-alerts.md S8; SPEC-954)."""

from __future__ import annotations

import logging
import smtplib
import time
from dataclasses import dataclass, field
from email.message import EmailMessage
from email.utils import formatdate, make_msgid
from typing import Callable

from . import engine as E
from .logs import kv

RETRIES = (10.0, 60.0, 300.0)  # waits before the 2nd, 3rd and 4th attempt

_WORD = {E.DOWN: "DOWN", E.UP: "UP", E.STILL_DOWN: "STILL DOWN"}


def when(t: float, now: float | None = None) -> str:
    """14:02:05 today, else 2026-11-15 14:02:05 (local time)."""
    lt, ln = time.localtime(t), time.localtime(now if now is not None else time.time())
    if lt[:3] == ln[:3]:
        return time.strftime("%H:%M:%S", lt)
    return time.strftime("%Y-%m-%d %H:%M:%S", lt)


def span(sec: float) -> str:
    """45 s, 18 min, 2 h 5 min, 3 d 4 h"""
    s = int(max(0, sec))
    if s < 60:
        return f"{s} s"
    m = s // 60
    if m < 60:
        return f"{m} min"
    h, m = divmod(m, 60)
    if h < 24:
        return f"{h} h {m} min" if m else f"{h} h"
    d, h = divmod(h, 24)
    return f"{d} d {h} h" if h else f"{d} d"


def _host(n: E.Notice) -> str:
    return f"{n.host['name']} ({n.host['ip']})" if n.host["name"] != n.host["ip"] else n.host["ip"]


def _what(n: E.Notice) -> str:
    """The subject's "web (192.0.2.10)" or "https on web (192.0.2.10)"."""
    return f"{n.service['name']} on {_host(n)}" if n.service else _host(n)


def _svc_long(sv: dict) -> str:
    """https (443); tcp:5432 already names its port."""
    return sv["name"] if sv["name"].startswith("tcp:") or not sv.get("port") else f"{sv['name']} ({sv['port']})"


def _what_long(n: E.Notice) -> str:
    return f"{_svc_long(n.service)} on {_host(n)}" if n.service else _host(n)


def _count(c: int, one: str) -> str:
    return f"{c} {one}{'s' if c != 1 else ''}"


def subject(items: list[tuple[E.Notice, list[str]]]) -> str:
    if len(items) == 1:
        n = items[0][0]
        match n.kind:
            case E.DOWN:
                return f"[ined] DOWN {_what(n)}"
            case E.UP:
                return f"[ined] UP {_what(n)} after {span(n.at - n.since)}"
            case E.STILL_DOWN:
                return f"[ined] STILL DOWN {_what(n)} for {span(n.at - n.since)}"
            case E.SERVER_DOWN:
                return "[ined] ined-server unreachable"
            case E.SERVER_UP:
                return "[ined] ined-server reachable again"
            case E.DEGRADED:
                return "[ined] ined-server has a problem: " + n.text
            case E.PINGS_AGAIN:
                return "[ined] ined-server is OK again"
            case E.SUMMARY:
                s = f"[ined] ined-alert started: {_count(len(n.hosts), 'host')} down"
                return s + (f", {_count(len(n.services), 'service')} down" if n.services else "")
    kinds = [n.kind for n, _ in items]
    svcs = any(n.service for n, _ in items)
    if all(k == E.DOWN for k in kinds):
        common = set(items[0][1])
        for _, paths in items[1:]:
            common &= set(paths)
        where = f" in {sorted(common)[0]}" if common else ""
        if not svcs:
            return f"[ined] {len(items)} hosts down{where}"
        nh = sum(1 for n, _ in items if not n.service)
        what = ([_count(nh, "host") + " down"] if nh else []) + [_count(len(items) - nh, "service") + " down"]
        return f"[ined] {', '.join(what)}{where}"
    parts = []
    for k, word in ((E.DOWN, "down"), (E.UP, "up again"), (E.STILL_DOWN, "still down")):
        if not svcs:
            if c := kinds.count(k):
                parts.append(f"{c} {word}")
            continue
        for one, is_svc in (("host", False), ("service", True)):
            if c := sum(1 for n, _ in items if n.kind == k and bool(n.service) == is_svc):
                parts.append(f"{_count(c, one)} {word}")
    if kinds.count(E.SUMMARY):
        parts.append("ined-alert started")
    if other := len(kinds) - sum(kinds.count(k) for k in _WORD) - kinds.count(E.SUMMARY):
        parts.append(f"{other} server message{'s' if other > 1 else ''}")
    return "[ined] " + ", ".join(parts)


def body(items: list[tuple[E.Notice, list[str]]], server_url: str, now: float) -> str:
    out = []
    for n, _ in items:
        match n.kind:
            case E.DOWN | E.STILL_DOWN:
                out.append(f"{_WORD[n.kind]:<10} {_what_long(n)} since {when(n.since, now)}, {span(n.at - n.since)}")
                if n.groups:
                    out.append(f"{'':<10} groups: {', '.join(n.groups)}")
                if n.service:
                    if n.service.get("last_ok"):
                        out.append(f"{'':<10} last answered: {when(n.service['last_ok'], now)}")
                elif n.host.get("last_seen"):
                    out.append(f"{'':<10} last seen: {when(n.host['last_seen'], now)}")
            case E.UP:
                out.append(f"{'UP':<10} {_what_long(n)} at {when(n.at, now)}, down for {span(n.at - n.since)}")
                if n.groups:
                    out.append(f"{'':<10} groups: {', '.join(n.groups)}")
            case E.SERVER_DOWN:
                out.append(f"ined-server can't be reached since {when(n.since, now)}: {n.text}")
                out.append("No host or service alerts are sent until it is back.")
            case E.SERVER_UP:
                out.append(f"ined-server can be reached again (unreachable for {span(n.at - n.since)}).")
            case E.DEGRADED:
                out.append(f"ined-server reports a problem since {when(n.since, now)}: {n.text}")
                out.append("While it can't ping, host statuses don't change.")
            case E.PINGS_AGAIN:
                out.append(f"ined-server is OK again (after {span(n.at - n.since)}).")
            case E.SUMMARY:
                if not n.hosts:
                    out.append("ined-alert started. No monitored host is down.")
                else:
                    out.append("ined-alert started. These hosts are down:")
                    for h in n.hosts:
                        name = f"{h['name']} ({h['ip']})" if h["name"] != h["ip"] else h["ip"]
                        out.append(f"  {name} since {when(h['since'], now)}" + (f"  [{', '.join(h['groups'])}]" if h["groups"] else ""))
                if n.services:
                    out.append("These watched services are down:")
                    for h in n.services:
                        name = f"{h['name']} ({h['ip']})" if h["name"] != h["ip"] else h["ip"]
                        out.append(f"  {_svc_long(h['service'])} on {name} since {when(h['since'], now)}"
                                   + (f"  [{', '.join(h['groups'])}]" if h["groups"] else ""))
        out.append("")
    out.append(f"Server: {server_url}")
    out.append("-- ")
    out.append("ined-alert")
    return "\n".join(out) + "\n"


def compose(sender: str, recipient: str, items: list[tuple[E.Notice, list[str]]], server_url: str, now: float) -> EmailMessage:
    m = EmailMessage()
    m["From"], m["To"], m["Subject"] = sender, recipient, subject(items)
    m["Date"] = formatdate(now, localtime=True)
    m["Message-ID"] = make_msgid("ined-alert")
    m["Auto-Submitted"] = "auto-generated"
    m.set_content(body(items, server_url, now))
    return m


def test_message(sender: str, recipient: str, server_url: str, now: float) -> EmailMessage:
    """The mail of --test-mail."""
    m = EmailMessage()
    m["From"], m["To"], m["Subject"] = sender, recipient, "[ined] test mail from ined-alert"
    m["Date"] = formatdate(now, localtime=True)
    m["Message-ID"] = make_msgid("ined-alert")
    m["Auto-Submitted"] = "auto-generated"
    m.set_content(f"ined-alert can send mail to {recipient}.\n\nServer: {server_url}\n-- \nined-alert\n")
    return m


@dataclass
class Batcher:
    """Collects notices per recipient for `window` seconds (A6)."""

    window: float
    _q: dict[str, tuple[float, list[tuple[E.Notice, list[str]]]]] = field(default_factory=dict)

    def add(self, n: E.Notice, now: float) -> None:
        for rcpt, paths in n.to.items():
            first, items = self._q.setdefault(rcpt, (now, []))
            items.append((n, paths))

    def due(self, now: float, everything: bool = False) -> list[tuple[str, list[tuple[E.Notice, list[str]]]]]:
        out = []
        for rcpt, (first, items) in list(self._q.items()):
            if everything or now - first >= self.window:
                out.append((rcpt, items))
                del self._q[rcpt]
        return out

    def __len__(self) -> int:
        return len(self._q)


@dataclass
class _Letter:
    msg: EmailMessage
    tries: int = 0
    next: float = 0.0


class Outbox:
    """Sends mail, retrying after 10 s, 1 min and 5 min (A7)."""

    def __init__(self, send: Callable[[EmailMessage], None], log: logging.Logger):
        self.send, self.log = send, log
        self.letters: list[_Letter] = []

    def put(self, msg: EmailMessage, now: float) -> None:
        self.letters.append(_Letter(msg, 0, now))

    def tick(self, now: float) -> None:
        for l in list(self.letters):
            if l.next > now:
                continue
            l.tries += 1
            try:
                self.send(l.msg)
            except (OSError, smtplib.SMTPException) as e:
                if l.tries > len(RETRIES):
                    self.letters.remove(l)
                    self.log.error(kv(event="mail_dropped", to=l.msg["To"], subject=l.msg["Subject"], tries=l.tries, error=str(e)))
                else:
                    l.next = now + RETRIES[l.tries - 1]
                    self.log.warning(kv(event="mail_failed", to=l.msg["To"], subject=l.msg["Subject"], tries=l.tries,
                                        retry_in=f"{RETRIES[l.tries - 1]:.0f}s", error=str(e)))
                continue
            self.letters.remove(l)
            self.log.info(kv(event="mail_sent", to=l.msg["To"], subject=l.msg["Subject"], tries=l.tries))


def smtp_sender(host: str, port: int, timeout: float = 10.0) -> Callable[[EmailMessage], None]:
    """Sends through an SMTP relay without login or TLS (A7)."""

    def send(msg: EmailMessage) -> None:
        with smtplib.SMTP(host, port, timeout=timeout) as s:
            s.send_message(msg)

    return send
