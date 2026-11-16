"""A small client for the go-ined REST and SSE API, standard library only.

    from ined import Client
    c = Client("http://127.0.0.1:8470", token="...")
    for h in c.hosts():
        print(h["display_name"], h["status"])
    for ev in c.events():          # reconnects and resumes on its own
        print(ev.type, ev.data)
"""

from .client import APIError, Client, Event, parse_sse

__all__ = ["APIError", "Client", "Event", "parse_sse"]
