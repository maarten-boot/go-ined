"""A small inventory used by several tests: two views, nested groups."""

from ined_alert.config import Config, Route

# Networks: dc > lan > web; dc > db; lone at the root.
# Payroll: "app servers" > web.
VIEWS = [{"id": 1, "name": "Networks"}, {"id": 2, "name": "Payroll"}]
MAPS = {
    1: {"view_id": 1, "items": [
        {"type": "group", "id": 10, "parent_id": None, "name": "dc"},
        {"type": "group", "id": 11, "parent_id": 10, "name": "lan"},
        {"type": "host", "id": 1, "parent_id": 11, "name": "web"},
        {"type": "host", "id": 2, "parent_id": 10, "name": "db"},
        {"type": "host", "id": 3, "parent_id": None, "name": "lone"},
    ]},
    2: {"view_id": 2, "items": [
        {"type": "group", "id": 20, "parent_id": None, "name": "app servers"},
        {"type": "host", "id": 1, "parent_id": 20, "name": "web"},
    ]},
}
ROUTES = [Route("Networks/dc", ["ops@example.com"]), Route("Payroll/app servers", ["pay@example.com"])]


def config(**kw) -> Config:
    c = Config(mail_from="alert@example.com", admins=["admin@example.com"], routes=list(ROUTES),
               down_after=120, batch=30, server_down_after=300, state_file="")
    for k, v in kw.items():
        setattr(c, k, v)
    return c
