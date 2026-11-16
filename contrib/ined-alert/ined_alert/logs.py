"""Logging as key=value lines, to stderr and optionally syslog
(Design-alerting.md A8, SPEC-955)."""

from __future__ import annotations

import logging
import logging.handlers
import socket
import sys

TAG = "ined-alert"
_LEVELS = {"debug": logging.DEBUG, "info": logging.INFO, "warn": logging.WARNING, "error": logging.ERROR}


def _quote(v) -> str:
    if isinstance(v, (list, tuple, set)):
        v = ",".join(str(x) for x in v)
    s = str(v)
    if s == "" or any(c in s for c in ' "=\\\t\n'):
        s = '"' + s.replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n").replace("\t", "\\t") + '"'
    return s


def kv(**fields) -> str:
    """event=down host=web groups="Networks/dc/lan" -- for a collector to parse."""
    return " ".join(f"{k}={_quote(v)}" for k, v in fields.items())


def syslog_handler(address: str, facility: str) -> logging.Handler:
    """A handler for "udp://host:port", "tcp://host:port" or a socket path."""
    fac = logging.handlers.SysLogHandler.facility_names[facility]
    if address.startswith(("udp://", "tcp://")):
        hostport = address[6:]
        host, _, port = hostport.rpartition(":")
        if not host or not port.isdigit():
            raise ValueError(f'syslog.address "{address}": host:port expected')
        host = host.strip("[]")
        kind = socket.SOCK_DGRAM if address.startswith("udp") else socket.SOCK_STREAM
        h = logging.handlers.SysLogHandler(address=(host, int(port)), facility=fac, socktype=kind)
    else:
        h = logging.handlers.SysLogHandler(address=address, facility=fac)
    h.setFormatter(logging.Formatter(TAG + ": %(message)s"))
    return h


def setup(level: str = "info", syslog_address: str = "", facility: str = "local0", stream=None) -> logging.Logger:
    """The program's logger. A syslog collector that can't be reached is
    reported on stderr; alerting goes on without it."""
    log = logging.getLogger(TAG)
    log.handlers.clear()
    log.propagate = False
    log.setLevel(_LEVELS[level])
    logging.raiseExceptions = False  # a lost collector must not print a traceback per line
    err = logging.StreamHandler(stream or sys.stderr)
    err.setFormatter(logging.Formatter("%(asctime)s %(levelname)s %(message)s"))
    log.addHandler(err)
    if syslog_address:
        try:
            log.addHandler(syslog_handler(syslog_address, facility))
        except (OSError, ValueError) as e:
            log.error(kv(event="syslog_failed", address=syslog_address, error=str(e)))
    return log
