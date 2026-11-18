"""SNMP thresholds (Design-snmp.md N7, N8; SPEC-958): which values of a
host are over a rule's line.

A rule names a metric and a line: `cpu` (the average over the CPUs),
`cpu_max` (the busiest CPU), `memory` and `disk` (each fixed disk); a
value above the line for `for` is alerted HIGH, and OK when it is at or
below it again.
"""

from __future__ import annotations

from dataclasses import dataclass

METRICS = ("cpu", "cpu_max", "memory", "disk")
_LABEL = {"cpu": "CPU", "cpu_max": "busiest CPU", "memory": "memory", "disk": "disk"}


@dataclass
class Threshold:
    metric: str
    above: float
    for_s: float = 300.0


def readings(h: dict, rules: list[Threshold]) -> dict[tuple[str, str], dict]:
    """The values of a host that the rules look at, by (metric, item):
    {"metric", "item", "label", "value", "above", "for_s"}. item is the
    disk's name, or "" for the others. Empty when the host has no SNMP
    values (no agent, monitoring off, or not read since the server
    started). With two rules for one metric, the lower line counts."""
    v = ((h.get("snmp") or {}).get("values")) or None
    if not isinstance(v, dict):
        return {}
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
                    put(r, name, s.get("used_pct"), f"{_LABEL[r.metric]} {name}" if r.metric == "disk" else "memory")
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
