"""Who gets an alert for a host (Design-alerting.md A5, SPEC-953).

A route names a group by its path, view first ("Networks/dc/lan",
"Payroll/app servers"), or a whole view ("Payroll"). It covers the group and
everything below it. A host's recipients are those of every route that
covers it in any view; the admins get every alert.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from .config import Route


@dataclass
class Routes:
    routes: list[Route]
    admins: list[str]
    warnings: list[str] = field(default_factory=list)
    # host id -> {recipient: [route paths that send it there]}
    _to: dict[int, dict[str, list[str]]] = field(default_factory=dict)
    # host id -> paths of the groups the host is directly in, one per view
    _groups: dict[int, list[str]] = field(default_factory=dict)

    def load(self, views: list[dict], maps: dict[int, dict]) -> None:
        """Resolves the routes against the views and their maps (GET /views,
        GET /map?view=N)."""
        self.warnings, self._to, self._groups = [], {}, {}
        names = {v["id"]: v["name"] for v in views}
        matched = {r.group: 0 for r in self.routes}
        for vid, m in sorted(maps.items()):
            vname = names.get(vid, str(vid))
            groups = {it["id"]: it for it in m.get("items", []) if it["type"] == "group"}
            path_of: dict[int, str] = {}

            def path(gid: int, depth: int = 0) -> str:
                if gid in path_of:
                    return path_of[gid]
                g = groups[gid]
                p = g.get("parent_id")
                up = path(p, depth + 1) if p in groups and depth < 1000 else vname
                path_of[gid] = up + "/" + g["name"].strip()
                return path_of[gid]

            for gid in groups:
                path(gid)
            # Which routes cover each group of this view (with its subtree).
            covers: dict[int, list[Route]] = {gid: [] for gid in groups}
            view_routes = [r for r in self.routes if r.group == vname]
            if vname in matched:
                matched[vname] += 1
            for gid, p in path_of.items():
                if p in matched:
                    matched[p] += 1
                    covers[gid] = [r for r in self.routes if r.group == p]
            for it in m.get("items", []):
                if it["type"] != "host":
                    continue
                hid = it["id"]
                to = self._to.setdefault(hid, {})
                rs = list(view_routes)
                gid, n = it.get("parent_id"), 0
                if gid in path_of:
                    self._groups.setdefault(hid, []).append(path_of[gid])
                while gid in groups and n < 1000:
                    rs.extend(covers[gid])
                    gid, n = groups[gid].get("parent_id"), n + 1
                for r in rs:
                    for a in r.to:
                        paths = to.setdefault(a, [])
                        if r.group not in paths:
                            paths.append(r.group)
        for p, n in matched.items():
            if n == 0:
                self.warnings.append(f'route "{p}" matches no view or group')
            elif n > 1:
                self.warnings.append(f'route "{p}" matches {n} groups; it covers all of them')

    def recipients(self, host_id: int) -> dict[str, list[str]]:
        """The recipients of an alert for a host, each with the route paths
        that send it there; admins have an empty list (they get it all)."""
        out = {a: list(p) for a, p in self._to.get(host_id, {}).items()}
        for a in self.admins:
            out.setdefault(a, [])
        return out

    def groups_of(self, host_id: int) -> list[str]:
        """The paths of the groups a host is directly in, in every view."""
        return list(self._groups.get(host_id, []))
