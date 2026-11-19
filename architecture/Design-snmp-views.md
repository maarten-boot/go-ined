# Design: SNMP community strings per view

Status: **accepted** (2026-11-19): V1–V6 as recommended ("OK, all as recommended"); being built on `feature/snmp-views`.

User request (2026-11-19), after "SNMP communities has inherit … can I set a community on a group or a view?":
- "as I create new views on hosts with similar capabilities it would be very helpful if I can set communities on a view"
- "and then use inherit"

So: a custom view collects hosts of one kind (switches, printers, …), the view gets the list, and its hosts get it by "Inherit" (no list of their own).

## What exists (Spec v0.67, SPEC-315)

- Lists on a host and on a network-view group, at most 10 strings each, write-only.
- Lookup: the host's own list, else the nearest network group above it with one, else the server's `[snmp] communities`. The most specific list replaces the others (G3a).
- A list someone set is used also outside `snmp.networks`; the server's list only inside (G4a).
- The manager notices a changed list per host at its next tick and looks for the agent again at once.
- Views: the network view (id 1, every host exactly once) and custom views (a host in any number of them, by placement). Views have an `order` (the tab order).

## Proposal

- **V1 Where:** a **custom view** can have a list. The network view can't: for all hosts there is the server's list (`server.toml`). Groups inside custom views still can't (V6).
- **V2 Where it goes in the lookup.** Recommended: **host → custom view → network group → server.**
  - A view made for "hosts with similar capabilities" says more about which community works than where the host sits in the network; so the view's list beats a network group's.
  - Alternative V2b: host → network group → custom view → server (the network place first).
- **V3 A host in several views with a list:** the view **first in the tab order** (lowest `order`) wins. The host's details say which one: "from view switches". Its other views' lists aren't tried (G3a: one list replaces the others).
  - Alternative V3b: try the lists of all its views in tab order, without duplicates, up to 10 strings. More answers, but more strings sent to a host, and the origin is harder to show.
- **V4 `networks`:** a view's list is a list someone set, so it is used also outside `snmp.networks` (as G4a).
- **V5 Changes that make a host look again at once** (no extra code beyond the lookup: the manager compares the list per host):
  - a view's list set or cleared;
  - a host placed in or removed from a view, a view deleted, the tab order changed (V3).
- **V6 Not in this step:** lists on groups inside custom views (one more level, and a host may sit in a group of each view); a GUI editor for the server's own list.

## Interfaces

- **GUI:**
  - The toolbar's **View** menu gets **"SNMP communities…"** for the current custom view (not shown on the network view). It is the same dialog as for hosts and groups: masked fields, Add, Remove, **Inherit** (clears the view's list), Cancel, Save.
  - A host's "SNMP communities" row and its dialog can say **"from view switches"**.
  - The details pane reads the shown host again on `view.updated` and `view.deleted` (as on group events, crosscheck 2026-11-19-02 C1), and already does on placement events.
- **API:** `PATCH /views/{id}` with `"snmp_communities": [...]`; `[]` inherits. 400 for the network view and for the rules of SPEC-315 (10 strings, 1–255 bytes, none twice). View JSON gets `snmp_communities_set`. A host's `snmp_communities_from` can be `"view 5"`. `view.updated` is sent; no per-host events.
- **`inedctl`:** `view snmp-communities <id> LIST|-|--clear`; `view list` / `view show` get a "Communities" column or row ("2 set here", or "-").
- **Store:** schema 11, a column `snmp_communities` on `views` (JSON, as on groups and hosts). A 0.8.x server can't open the database after it.
- **Write-only** as before: never in the API, events, export, log or GUI. The log says "SNMP communities set view=5 count=2".
- **Export/import:** like the group lists: not exported.

## Docs and tests

- Spec: SPEC-315 (lookup, view lists), SPEC-737 (View menu item), SPEC-901 (`inedctl`), the API section; Changes `[Unreleased]`; README.
- Tests: lookup order (host, view, group, server; two views by order; order change), outside `networks`, placement add/remove and view delete cause a new lookup, PATCH rules and 400 on the network view, migration 11, no string in any JSON/event/log, the GUI item and "from view …", `inedctl`.

## Decisions for the user

- **V1** custom views only (recommended).
- **V2** host → view → network group → server (recommended), or V2b.
- **V3** first view in tab order wins (recommended), or V3b.
- **V4–V6** as above (recommended).
