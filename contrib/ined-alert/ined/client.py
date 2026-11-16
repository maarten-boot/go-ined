"""The go-ined API client (SPEC-501..506), standard library only."""

from __future__ import annotations

import json
import threading
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import Any, Callable, Iterable, Iterator

PREFIX = "/api/v1"


class APIError(Exception):
    """An error answer of the API: HTTP status, code and message (SPEC-504)."""

    def __init__(self, status: int, code: str, message: str):
        super().__init__(f"{status} {code}: {message}")
        self.status, self.code, self.message = status, code, message


def _api_error(e: urllib.error.HTTPError) -> APIError:
    try:
        body = json.loads(e.read() or b"{}")["error"]
        return APIError(e.code, body.get("code", ""), body.get("message", ""))
    except (ValueError, KeyError, TypeError, OSError):
        return APIError(e.code, "", e.reason if isinstance(e.reason, str) else str(e))
    finally:
        e.close()


@dataclass
class Event:
    """One server-sent event: its id ("<run>-<seq>"), type and JSON data."""

    id: str
    type: str
    data: Any


def parse_sse(lines: Iterable[bytes]) -> Iterator[Event]:
    """Parses an event stream (text/event-stream) into events. Comments,
    such as the keepalive, are skipped."""
    ev_id, ev_type, data = "", "", []
    for raw in lines:
        line = raw.decode("utf-8", "replace").rstrip("\r\n")
        if line == "":
            if data or ev_type:
                text = "\n".join(data)
                yield Event(ev_id, ev_type or "message", json.loads(text) if text else None)
            ev_id, ev_type, data = "", "", []
            continue
        if line.startswith(":"):
            continue
        field, _, value = line.partition(":")
        value = value[1:] if value.startswith(" ") else value
        if field == "id":
            ev_id = value
        elif field == "event":
            ev_type = value
        elif field == "data":
            data.append(value)


class Client:
    """Reads from a go-ined server. Every call raises APIError for an error
    answer and OSError (urllib.error.URLError, TimeoutError) when the server
    can't be reached."""

    def __init__(self, url: str, token: str = "", timeout: float = 10.0):
        self.url = url.rstrip("/")
        self.token = token
        self.timeout = timeout

    def _open(self, path: str, timeout: float, headers: dict[str, str] | None = None):
        req = urllib.request.Request(self.url + PREFIX + path, method="GET")
        if self.token:
            req.add_header("Authorization", "Bearer " + self.token)
        for k, v in (headers or {}).items():
            req.add_header(k, v)
        try:
            return urllib.request.urlopen(req, timeout=timeout)
        except urllib.error.HTTPError as e:
            raise _api_error(e) from None

    def get(self, path: str) -> Any:
        with self._open(path, self.timeout, {"Accept": "application/json"}) as r:
            return json.load(r)

    def health(self) -> dict:
        return self.get("/health")

    def hosts(self) -> list[dict]:
        return self.get("/hosts")

    def host(self, host_id: int) -> dict:
        return self.get(f"/hosts/{int(host_id)}")

    def groups(self) -> list[dict]:
        return self.get("/groups")

    def views(self) -> list[dict]:
        return self.get("/views")

    def map(self, view: int = 1) -> dict:
        return self.get("/map?" + urllib.parse.urlencode({"view": int(view)}))

    def events(
        self,
        last_id: str = "",
        stop: threading.Event | None = None,
        on_state: Callable[[str, str], None] | None = None,
        idle: float = 75.0,
    ) -> Iterator[Event]:
        """Yields the server's events for as long as stop isn't set. It
        reconnects (after 1 s, doubling up to 30 s) and resumes with
        Last-Event-ID; "resync" is passed on (SPEC-503, 506). on_state gets
        ("connected", "") and ("disconnected", reason). A 4xx answer, such
        as a wrong token, raises APIError."""
        delay = 1.0
        while not (stop and stop.is_set()):
            headers = {"Accept": "text/event-stream"}
            if last_id:
                headers["Last-Event-ID"] = last_id
            try:
                with self._open("/events", idle, headers) as r:
                    if on_state:
                        on_state("connected", "")
                    delay = 1.0
                    for ev in parse_sse(r):
                        if ev.id:
                            last_id = ev.id
                        yield ev
                        if stop and stop.is_set():
                            return
                reason = "the server ended the stream"
            except APIError as e:
                if 400 <= e.status < 500:
                    raise
                reason = str(e)
            except (OSError, ValueError) as e:
                reason = str(e) or type(e).__name__
            if on_state:
                on_state("disconnected", reason)
            if stop:
                if stop.wait(delay):
                    return
            else:
                threading.Event().wait(delay)
            delay = min(delay * 2, 30.0)
