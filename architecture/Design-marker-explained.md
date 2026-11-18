# Design: explaining the service marker (details pane and hover)

Status: **accepted** (2026-11-18): E1, E2a, E3 and E4 as recommended ("OK, all three designs").

User question: "the little red triangle on the bottom right of a host means what? it would be nice to have a explanation in the right details pane when we select the host or a hover ?"

## What the triangle is

It is the **service marker** (SPEC-732, v0.52): the host has at least one **watched service down** (SPEC-310). The host's own glyph still shows its ping status, so a host can be up (●) and still carry the triangle, for example when it answers ping but `https` doesn't accept connections. The details pane already has a "Watched" row ("ssh up, https down since 14:07"), but nothing connects that row to the triangle, and the map has no hover help at all.

## Proposal

- **E1 A problem line in the details pane.** When the selected host has watched services down, the details show a line right below the title, before the other rows:
  ```
  ▲ 1 watched service down: https (443) since 14:07
  ▲ 2 watched services down: https (443) since 14:07, tcp:5432 since 13:50
  ```
  - The triangle is drawn as on the map, in the `down` colour; the text is the normal text colour, so it keeps the contrast of the chosen preset (AA, AAA, Max).
  - The line is selectable and copyable, like the other values (SPEC-708).
  - It follows `service.status` and disappears when nothing is down. The "Watched" row stays as it is.
  - Recommended: yes.
- **E2 Hover help on the map.** Fyne has no tooltips, so the map gets its own small pop-up: resting the pointer about half a second on a host shows a box next to it, gone when the pointer moves away, on a click or a drag.
  - **E2a (recommended):** for every host: name, IP address, status ("up", "down since 14:07", "unknown", "not monitored"), and, when any are down, the line of E1. Hovering the triangle shows the same box. One rule for every host is easier to learn than help on the triangle only.
  - **E2b:** only on the triangle, with the line of E1.
  - Groups get no hover box now (their frame label already shows name and status).
  - Colours from the theme (white box, black text, a border), so every preset keeps its contrast.
  - Nothing is fetched on hover: the map model already has the name, status and watched services.
- **E3 A legend.** A new **Help** menu with **"Map symbols…"**: a small dialog listing the host glyphs (● up, ✕ down, ? unknown, – not monitored, the hollow "stale" sign), the service marker, the dashed frame of a network group, and the bottom bar. Recommended: yes; it answers "what does this mean?" for every symbol, not only the triangle.
- **E4 Docs.** README's GUI section names the triangle; Spec: SPEC-708 (E1), SPEC-732 (E2) and a new SPEC item for the Help menu (E3); tests in Tests.md.

## Not now

- Hover help for groups, the trail or the toolbar.
- Hover on touch screens (there is no hover; E1 and E3 cover them).

## Decisions for the user

- **E1** problem line in the details: yes (recommended) / no.
- **E2** hover: E2a every host (recommended) / E2b the triangle only / none.
- **E3** Help › "Map symbols…": yes (recommended) / no.
