"""TC-954 (batching and mail) and TC-955 (logging and syslog)."""

import socket
import unittest

from ined_alert import engine as E
from ined_alert.logs import kv, setup
from ined_alert.mail import Batcher, Outbox, compose, smtp_sender, span, subject

from .fakes import SMTPServer, logger

T0 = 1_800_000_000.0
WEB = {"id": 1, "name": "web", "ip": "192.0.2.10", "last_seen": T0 - 30}


def down(hid, name, at=T0 + 120, to=None):
    return E.Notice(E.DOWN, at, to=to or {"ops@example.com": ["Networks/dc"], "admin@example.com": []},
                    host={"id": hid, "name": name, "ip": f"192.0.2.{hid}", "last_seen": None}, since=T0,
                    groups=["Networks/dc/lan"])


class TestMail(unittest.TestCase):
    def test_span(self):
        for s, want in [(5, "5 s"), (120, "2 min"), (3600, "1 h"), (3900, "1 h 5 min"), (86400 * 3 + 7200, "3 d 2 h")]:
            self.assertEqual(span(s), want)

    def test_subjects(self):
        n = E.Notice(E.DOWN, T0 + 120, to={}, host=WEB, since=T0, groups=["Networks/dc/lan"])
        self.assertEqual(subject([(n, [])]), "[ined] DOWN web (192.0.2.10)")
        u = E.Notice(E.UP, T0 + 1080, to={}, host=WEB, since=T0)
        self.assertEqual(subject([(u, [])]), "[ined] UP web (192.0.2.10) after 18 min")
        ip_only = E.Notice(E.DOWN, T0, to={}, host={"id": 2, "name": "192.0.2.2", "ip": "192.0.2.2"}, since=T0)
        self.assertEqual(subject([(ip_only, [])]), "[ined] DOWN 192.0.2.2")
        self.assertEqual(subject([(E.Notice(E.SERVER_DOWN, T0, to={}), [])]), "[ined] ined-server unreachable")
        many = [(down(i, f"h{i}"), ["Networks/dc"]) for i in range(1, 41)]
        self.assertEqual(subject(many), "[ined] 40 hosts down in Networks/dc")
        self.assertEqual(subject([(down(1, "a"), ["Networks/dc"]), (down(2, "b"), [])]), "[ined] 2 hosts down")
        self.assertEqual(subject([(down(1, "a"), []), (u, []), (E.Notice(E.SERVER_UP, T0, to={}), [])]),
                         "[ined] 1 down, 1 up again, 1 server message")
        summary = E.Notice(E.SUMMARY, T0, to={})
        self.assertEqual(subject([(summary, []), (down(1, "a"), [])]), "[ined] 1 down, ined-alert started")

    def test_batch_and_compose(self):
        b = Batcher(30)
        b.add(down(1, "a"), T0)
        b.add(down(2, "b", to={"ops@example.com": ["Networks/dc"]}), T0 + 10)
        self.assertEqual(b.due(T0 + 29), [])
        due = dict(b.due(T0 + 30))
        self.assertEqual(sorted(due), ["admin@example.com", "ops@example.com"])
        self.assertEqual(len(due["ops@example.com"]), 2)
        self.assertEqual(len(b), 0)
        m = compose("alert@example.com", "ops@example.com", due["ops@example.com"], "http://ined:8470", T0 + 30)
        self.assertEqual((m["From"], m["To"], m["Subject"], m["Auto-Submitted"]),
                         ("alert@example.com", "ops@example.com", "[ined] 2 hosts down in Networks/dc", "auto-generated"))
        text = m.get_content()
        self.assertIn("DOWN       a (192.0.2.1) since", text)
        self.assertIn("groups: Networks/dc/lan", text)
        self.assertIn("Server: http://ined:8470", text)
        # A window of 0 sends at once; everything=True empties the queue.
        b0 = Batcher(0)
        b0.add(down(1, "a"), T0)
        self.assertEqual(len(b0.due(T0)), 2)
        b.add(down(1, "a"), T0)
        self.assertEqual(len(b.due(T0, everything=True)), 2)

    def test_outbox_retries(self):
        log, buf = logger()
        tries = []

        def send(msg):
            tries.append(msg["To"])
            raise ConnectionRefusedError("refused")

        o = Outbox(send, log)
        o.put(compose("a@example.com", "x@example.com", [(down(1, "a"), [])], "u", T0), T0)
        for t in (0, 9, 10, 69, 70, 369, 370):
            o.tick(T0 + t)
        self.assertEqual(len(tries), 4)  # the first try and 3 retries
        self.assertEqual(o.letters, [])
        self.assertEqual(buf.getvalue().count("event=mail_failed"), 3)
        self.assertIn("event=mail_dropped to=x@example.com", buf.getvalue())

    def test_smtp(self):
        srv = SMTPServer()
        self.addCleanup(srv.close)
        log, buf = logger()
        o = Outbox(smtp_sender("127.0.0.1", srv.port), log)
        o.put(compose("alert@example.com", "ops@example.com", [(down(1, "web"), [])], "u", T0), T0)
        o.tick(T0)
        self.assertEqual(len(srv.messages), 1)
        rcpts, msg = srv.messages[0]
        self.assertEqual((rcpts, msg["Subject"]), (["ops@example.com"], "[ined] DOWN web (192.0.2.1)"))
        self.assertIn("event=mail_sent to=ops@example.com", buf.getvalue())


class TestLogs(unittest.TestCase):
    def test_kv(self):
        self.assertEqual(kv(event="down", host="web", groups=["Networks/dc", "Payroll/app servers"], n=3, e=""),
                         'event=down host=web groups="Networks/dc,Payroll/app servers" n=3 e=""')
        self.assertEqual(kv(error='say "hi"\nnow'), 'error="say \\"hi\\"\\nnow"')

    def test_syslog_udp(self):
        rx = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        rx.bind(("127.0.0.1", 0))
        rx.settimeout(5)
        self.addCleanup(rx.close)
        import io
        log = setup("info", f"udp://127.0.0.1:{rx.getsockname()[1]}", "local0", stream=io.StringIO())
        log.info(kv(event="down", host="web"))
        data = rx.recv(4096).decode()
        # <134> = local0 (16) * 8 + info (6)
        self.assertEqual(data.rstrip("\x00"), "<134>ined-alert: event=down host=web")
        for h in log.handlers[1:]:
            h.close()

    def test_bad_syslog_address(self):
        import io
        buf = io.StringIO()
        log = setup("info", "udp://nohostport", "local0", stream=buf)
        self.assertEqual(len(log.handlers), 1)
        self.assertIn("event=syslog_failed", buf.getvalue())


if __name__ == "__main__":
    unittest.main()
