# Design note: views

Status: **approved**, 2026-11-10 (log item 30): scope option A (views are milestone M4b, after M4 and before the GUI); V1–V6 as recommended. The SPEC items and test cases are written at the start of M4b.
Origin: the user's idea (log item 29): "a notebook where the default view is the CIDR view, but where the user can add new views and rearrange hosts into functional groups (for example, servers used by the same users in the company)".

## 1. Goal

- The GUI gets a notebook: one tab per **view**.
- The first tab is the **network view**: today's map, with network groups by CIDR, discovery and auto-placement.
- The user can add **custom views** and arrange hosts in them into functional groups, such as "Payroll" (app server, database server, file share) or "Lab".
- **The same host can appear in several views,** and its status is the same everywhere, since a host is pinged once.

This also covers most of today's LATER item "host references, so one host can appear in several groups". That item would be replaced by this design.

## 2. Model

| Concept | Today | With views |
|---------|-------|------------|
| View | — (one implicit map) | `id`, `name`, `kind` (`network` or `custom`), `order` (tab position). There is exactly one `network` view, with id 1. It can be renamed but not deleted |
| Group | one tree | Every group belongs to **one view** (`view_id`). Network groups (with `cidr`) exist **only in the network view** |
| Host | `parent_id`, `x`, `y` on the host | The host keeps its own data (IP, names, note, status, `monitored`). **Placement** is per view: `(view_id, host_id) → parent_id, x, y` |
| Placement in the network view | — | Every host has one, as today (auto-placement, SPEC-107) |
| Placement in a custom view | — | Optional: a host is in a custom view only if the user put it there |
| `collapsed` | per group | Per group. Groups belong to one view, so this is automatically per view |
| Group status | worst monitored host in its subtree | The same rule (SPEC-105), evaluated within the group's view |
| Notes | per host and per group | Unchanged. A host's note is the same in every view |

## 3. Open points (recommendation first)

| # | Question | Recommendation |
|---|----------|----------------|
| V1 | Can a host appear more than once in **one** view? | **No: at most one placement per view.** Each view stays a tree, so aggregation, visibility and dragging keep their meaning. A second way of classifying hosts gets its own view |
| V2 | Network groups in custom views? | **No.** CIDR rules (SPEC-113/114), auto-placement and discovery stay in the network view. Custom views have only ordinary groups |
| V3 | What does deleting do in a custom view? | Deleting a group with `children=cascade` removes its subgroups and **placements**, **never hosts**. Hosts are only deleted with `DELETE /hosts/{id}`, which removes all their placements. In the network view, cascade keeps today's meaning (it deletes hosts) |
| V4 | Where do new hosts go? | In the network view only (manual add and discovery). Custom views are filled by hand |
| V5 | Per-user views? | No. Views are shared by all clients, like the map today (D5). There are no users in v0.1 |
| V6 | Ids | **Integers** (decided 2026-11-10). Views get an id sequence that is never reused, like hosts and groups (SPEC-116) |

## 4. Storage (migration 3)

- New table `views (id, name, kind, ord, created_at, updated_at)`, seeded with the network view (id 1, name "Networks").
- `groups` gets `view_id NOT NULL`; all existing groups go into view 1.
- New table `placements (view_id, host_id, parent_id, x, y, PRIMARY KEY (view_id, host_id))`, filled from today's `hosts.parent_id/x/y` for view 1. Then those columns are dropped (SQLite: rebuild the table).
- `meta` gets `next_view_id`.
- The load repair (SPEC-605) is extended: a placement whose parent group is in another view, or is missing, moves to the root of its view, with a warning.

## 5. API (additive: today's calls keep working)

- **Views:** `GET/POST /views`, and `GET/PATCH/DELETE /views/{id}` (deleting view 1 → 409).
- **Map per view:** `GET /map?view=<id>`. The default is the network view, so today's clients see no change.
- **Groups:** `POST /groups` takes an optional `view_id` (default 1). Group JSON gains `view_id`. A `cidr` outside view 1 → 400.
- **Hosts:** the host JSON keeps `parent_id`, `x`, `y`, which mean the **network-view** placement, exactly as today.
- **Placements in custom views:**
  - `PUT /views/{id}/hosts/{host_id}` with `{parent_id, x, y}` adds or moves a host in that view.
  - `DELETE /views/{id}/hosts/{host_id}` takes it out of the view.
  - A `parent_id` from another view → 400.
- **Events:** `view.created|updated|deleted`, and `placement.updated|deleted` (`{view_id, host_id, parent_id, x, y}`). Group events carry `view_id`. `group.status` stays per group.

Because the API only grows, **M4 (client and CLI) can start before views exist**. The client and CLI get the view calls later as additions: `inedctl view list|add|rename|rm`, `inedctl view place|unplace`, and a `--view` flag on `group` commands.

## 6. Effect on the code

| Part | Change |
|------|--------|
| `internal/inventory` | `Model` gets `Views` and `Placements[view][host]`. `Aggregate`, `Visible`, `FreePosition` and the move/cycle checks work per view. Network rules apply only to view 1 |
| `internal/store` | Migration 3, plus load and apply for views and placements |
| `internal/api` | The routes and `?view=` above |
| monitor, discovery, dnsname | **No change.** They work per host, and discovery places hosts in view 1 |
| `pkg/client`, `inedctl` (M4) | Additive calls and commands |
| GUI (M5/M6) | A notebook tab per view. The network view is first and can't be closed. The LATER **Hosts** tab (a sortable list) fits as an extra, non-map tab |
| Spec / Plan | D1 becomes "each item has at most one parent **per view**". New SPEC items for views and placements; about 15 new test cases |

Estimate: about the size of the M1 tree work. It's one milestone, mostly in `inventory`, plus a migration with tests.

## 7. Risks

- The migration rebuilds `hosts`. It needs a tested upgrade from a schema-2 database (as TC-601 does for migration 2).
- Status per group stays cheap. A host counts toward one subtree per view, so `Aggregate` is computed per view, and for 5,000 hosts and a few views that's still milliseconds.
- GUI complexity: dragging a host between views isn't planned. The user adds it to the other view; the original stays.

## 8. Scope: options

| Option | What | When the GUI sees it |
|--------|------|----------------------|
| **A (recommended)** | Approve this model now. M4 starts at once on today's API. Views become a **new milestone M4b before the GUI (M5)** | From the start of M5 |
| B | Views first (M3b), then M4 | From the start of M5; M4 waits about one milestone |
| C | LATER (after v0.1) | Retrofit into the GUI, client and CLI later |
