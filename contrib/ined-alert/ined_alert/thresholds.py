"""SNMP thresholds (Design-snmp.md N7, N8; SPEC-958): which values of a
host are over a rule's line.

A rule names a metric and a line: `cpu` (the average over the CPUs),
`cpu_max` (the busiest CPU), `memory` and `disk` (each fixed disk); a
value above the line for `for` is alerted HIGH, and OK when it is at or
below it again.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime

METRICS = ("cpu", "cpu_max", "memory", "disk")

# Values older than this are no values: the server keeps a host's last
# values while it doesn't answer (crosscheck 2026-11-18-04 C3); three reads
# at the default interval of 5 min.
STALE_S = 900.0
_LABEL = {"cpu": "CPU", "cpu_max": "busiest CPU", "memory": "memory", "disk": "disk"}


@dataclass
class Threshold:
    metric: str
    above: float
    for_s: float = 300.0


def _at(s) -> float | None:
    try:
        return datetime.fromisoformat(str(s)[:26].rstrip("Z") + "+00:00" if str(s).endswith("Z") else str(s)).timestamp()
    except ValueError:
        return None


def values_of(h: dict, now: float | None = None) -> dict | None:
    """A host's SNMP values, or None: no agent, monitoring off, not read
    since the server started, or older than STALE_S at now."""
    v = ((h.get("snmp") or {}).get("values")) or None
    if not isinstance(v, dict):
        return None
    if now is not None:
        at = _at(v.get("at"))
        if at is not None and now - at > STALE_S:
            return None
    return v


def disk_name(name: str) -> str:
    """ "C:\\ Label:  Serial Number 1234" -> "C:\\" (Windows); others as they are (L6)."""
    return name.split(" Label:")[0].strip() or name


def readings(h: dict, rules: list[Threshold], now: float | None = None) -> dict[tuple[str, str], dict]:
    """The values of a host that the rules look at, by (metric, item):
    {"metric", "item", "label", "value", "above", "for_s"}. item is the
    disk's or memory's name, or "" for the CPU. Empty when the host has no
    SNMP values (values_of). With two rules for one metric, the lower line
    counts."""
    v = values_of(h, now)
    if v is None:
        return {}
    mems = [s for s in v.get("storage") or [] if isinstance(s, dict) and s.get("kind") == "memory"]
    out: dict[tuple[str, str], dict] = {}

    def put(rule: Threshold, item: str, value, label: str) -> None:
        if not isinstance(value, (int, float)) or isinstance(value, bool):
            return
        k = (rule.metric, item)
        if k in out and out[k]["above"] <= rule.above:
            return
        out[k] = {"metric": rule.metric, "item": item, "label": label, "value": value,
                  "above": rule.above, "for_s": rule.for_s}

    for r in rules:
        if r.metric == "cpu":
            put(r, "", v.get("cpu_pct"), "CPU")
        elif r.metric == "cpu_max":
            put(r, "", v.get("cpu_max_pct"), "busiest CPU")
        else:
            for s in v.get("storage") or []:
                if isinstance(s, dict) and s.get("kind") == r.metric:
                    name = str(s.get("name", ""))
                    if r.metric == "disk":
                        label = f"disk {disk_name(name)}"
                    elif len(mems) > 1:  # "Physical memory" and "Virtual memory" (C5)
                        label = f"memory ({name})"
                    else:
                        label = "memory"
                    put(r, name, s.get("used_pct"), label)
    return out


def key_str(k: tuple[int, str, str]) -> str:
    """The state file's key of a threshold alert: "12/disk//data"."""
    return f"{k[0]}/{k[1]}/{k[2]}"


def key_parse(s: str) -> tuple[int, str, str]:
    hid, _, rest = s.partition("/")
    metric, _, item = rest.partition("/")
    if metric not in METRICS:
        raise ValueError(f"unknown metric {metric!r}")
    return int(hid), metric, item
