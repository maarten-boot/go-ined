"""The alert.toml settings (Design-alerting.md, SPEC-951)."""

from __future__ import annotations

import os
import re
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

from .thresholds import METRICS, Threshold


class ConfigError(Exception):
    pass


@dataclass
class Route:
    group: str  # "View/group/subgroup"; the view alone covers the whole view
    to: list[str]


@dataclass
class Config:
    url: str = "http://127.0.0.1:8470"
    token: str = ""
    mail_host: str = "localhost"
    mail_port: int = 25
    mail_from: str = ""
    admins: list[str] = field(default_factory=list)
    down_after: float = 120.0
    batch: float = 30.0
    repeat: float = 0.0  # 0 = no repeat
    startup_summary: bool = False
    new_hosts: bool = False  # [new_hosts] enabled: mail every new host (SPEC-957)
    state_file: str = ""
    server_down_after: float = 300.0
    routes: list[Route] = field(default_factory=list)
    thresholds: list[Threshold] = field(default_factory=list)  # [[threshold]] (SPEC-958)
    syslog_address: str = ""  # "" = stderr only
    syslog_facility: str = "local0"
    log_level: str = "info"


def default_config_path() -> Path:
    base = os.environ.get("XDG_CONFIG_HOME") or os.path.join(os.path.expanduser("~"), ".config")
    return Path(base) / "go-ined" / "alert.toml"


def default_state_file() -> str:
    base = os.environ.get("XDG_STATE_HOME") or os.path.join(os.path.expanduser("~"), ".local", "state")
    return os.path.join(base, "ined-alert", "state.json")


_UNIT = {"ms": 0.001, "s": 1, "m": 60, "h": 3600}
_PART = re.compile(r"(\d+(?:\.\d+)?)(ms|s|m|h)")


def parse_duration(s: str, key: str) -> float:
    """Parses a duration as Go writes it ("90s", "2m", "1h30m"); "" or "0"
    is 0. Returns seconds."""
    s = s.strip()
    if s in ("", "0"):
        return 0.0
    pos, total = 0, 0.0
    for m in _PART.finditer(s):
        if m.start() != pos:
            break
        total += float(m.group(1)) * _UNIT[m.group(2)]
        pos = m.end()
    if pos != len(s):
        raise ConfigError(f'{key}: "{s}" is not a duration (for example "30s", "2m", "1h30m")')
    return total


_SECTIONS = {
    "server": {"url", "token"},
    "mail": {"host", "port", "from"},
    "alert": {"admins", "down_after", "batch", "repeat", "startup_summary", "state_file", "server_down_after"},
    "new_hosts": {"enabled"},
    "syslog": {"address", "facility"},
    "log": {"level"},
}
_FACILITIES = {"user", "daemon", "local0", "local1", "local2", "local3", "local4", "local5", "local6", "local7"}


def _addresses(v, key: str) -> list[str]:
    if not isinstance(v, list) or not all(isinstance(a, str) for a in v):
        raise ConfigError(f"{key}: a list of email addresses")
    for a in v:
        if "@" not in a or any(c.isspace() for c in a):
            raise ConfigError(f'{key}: "{a}" is not an email address')
    return list(v)


def load(path: str | os.PathLike, env: dict[str, str] | None = None) -> Config:
    """Reads alert.toml. An unknown key is an error, so a misspelt one is
    noticed. INED_URL and INED_TOKEN override the server settings, as for
    the other clients (SPEC-805)."""
    env = os.environ if env is None else env
    try:
        with open(path, "rb") as f:
            doc = tomllib.load(f)
    except OSError as e:
        raise ConfigError(f"{path}: {e.strerror or e}") from None
    except tomllib.TOMLDecodeError as e:
        raise ConfigError(f"{path}: {e}") from None
    c = Config(state_file=default_state_file())
    for name, value in doc.items():
        if name in ("route", "threshold"):
            continue
        if name not in _SECTIONS or not isinstance(value, dict):
            raise ConfigError(f"unknown section [{name}]")
        for k in value:
            if k not in _SECTIONS[name]:
                raise ConfigError(f"unknown key {name}.{k}")

    def get(section: str, key: str, typ, default):
        v = doc.get(section, {}).get(key, default)
        if typ is float and isinstance(v, int) and not isinstance(v, bool):
            v = float(v)
        if not isinstance(v, typ) or (typ is int and isinstance(v, bool)):
            raise ConfigError(f"{section}.{key}: expected {typ.__name__}")
        return v

    c.url = get("server", "url", str, c.url)
    c.token = get("server", "token", str, c.token)
    c.url = env.get("INED_URL") or c.url
    c.token = env.get("INED_TOKEN") or c.token
    if not c.url.startswith(("http://", "https://")):
        raise ConfigError(f'server.url: "{c.url}" must start with http:// or https://')
    c.mail_host = get("mail", "host", str, c.mail_host)
    c.mail_port = get("mail", "port", int, c.mail_port)
    c.mail_from = get("mail", "from", str, "")
    if not c.mail_from:
        raise ConfigError("mail.from must be set")
    _addresses([c.mail_from], "mail.from")
    c.admins = _addresses(get("alert", "admins", list, []), "alert.admins")
    for key in ("down_after", "batch", "repeat", "server_down_after"):
        default = {"down_after": "2m", "batch": "30s", "repeat": "", "server_down_after": "5m"}[key]
        setattr(c, key, parse_duration(get("alert", key, str, default), "alert." + key))
    c.startup_summary = get("alert", "startup_summary", bool, False)
    c.state_file = get("alert", "state_file", str, c.state_file) or default_state_file()
    c.new_hosts = get("new_hosts", "enabled", bool, False)
    routes = doc.get("route", [])
    if not isinstance(routes, list):
        raise ConfigError("route: use [[route]] tables")
    for i, r in enumerate(routes, 1):
        if not isinstance(r, dict) or set(r) - {"group", "to"}:
            raise ConfigError(f"route {i}: only group and to")
        group = r.get("group", "")
        if not isinstance(group, str) or not group.strip("/ "):
            raise ConfigError(f"route {i}: group must be a path such as \"Networks/lan\"")
        c.routes.append(Route(group=normalize_path(group), to=_addresses(r.get("to", []), f"route {i} ({group}).to")))
    rules = doc.get("threshold", [])
    if not isinstance(rules, list):
        raise ConfigError("threshold: use [[threshold]] tables")
    for i, r in enumerate(rules, 1):
        if not isinstance(r, dict) or set(r) - {"metric", "above", "for"}:
            raise ConfigError(f"threshold {i}: only metric, above and for")
        metric, above = r.get("metric"), r.get("above")
        if metric not in METRICS:
            raise ConfigError(f"threshold {i}: metric must be one of {', '.join(METRICS)}")
        if not isinstance(above, (int, float)) or isinstance(above, bool) or not 0 <= above < 100:
            raise ConfigError(f"threshold {i}: above must be a percentage from 0 to below 100")
        dur = r.get("for", "5m")
        if not isinstance(dur, str):
            raise ConfigError(f"threshold {i}: for must be a duration such as \"10m\"")
        c.thresholds.append(Threshold(metric, float(above), parse_duration(dur, f"threshold {i}.for")))
    if not c.admins and not c.routes:
        raise ConfigError("nobody gets alerts: set alert.admins or a [[route]]")
    c.syslog_address = get("syslog", "address", str, "")
    if c.syslog_address and not c.syslog_address.startswith(("udp://", "tcp://", "/")):
        raise ConfigError(f'syslog.address: "{c.syslog_address}" must be udp://host:port, tcp://host:port or a socket path')
    c.syslog_facility = get("syslog", "facility", str, c.syslog_facility)
    if c.syslog_facility not in _FACILITIES:
        raise ConfigError(f"syslog.facility: one of {', '.join(sorted(_FACILITIES))}")
    c.log_level = get("log", "level", str, c.log_level)
    if c.log_level not in ("debug", "info", "warn", "error"):
        raise ConfigError("log.level: debug, info, warn or error")
    return c


def normalize_path(p: str) -> str:
    """ " Networks / lan/ " -> "Networks/lan" """
    return "/".join(part.strip() for part in p.strip().strip("/").split("/"))
