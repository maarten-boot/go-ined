# Design: positions rounded to one decimal

Status: **accepted** (2026-11-18): P1–P3 as recommended ("yes, add it; OK, all three designs").

User question: "i see that in the export the x, y coordinates are float, is more than one digit after the comma helpful at all as coordinate?"

## Why there are long fractions

- At zoom 100 % one map unit is one screen pixel; at the maximum zoom (400 %, `mapmodel.MaxZoom`) one unit is 4 pixels, so 0.1 unit is less than half a pixel.
- A drag at another zoom divides the screen movement by the zoom (37 px at 150 % is 24.666… units). The server stores the result as it comes (`REAL`), and the API and the export (SPEC-125) show every digit.

## Decisions

- **P1 The server rounds every position it saves to one decimal** (half away from zero): hosts, groups and custom-view placements, through every path: create, PATCH, move (also `contents_only`), reflow, join and grouping, copy to a view, import, the free position for a new item. One helper in the inventory, used where a position is checked (`checkPos`) and set.
  - An item moves by at most 0.05 units, which can't be seen; groups and their contents stay aligned, since each position is rounded on its own and a move adds the same rounded offset to all.
- **P2 The export rounds too**, so positions saved before P1 come out short at once; the database isn't migrated, and old values are rounded when they are next saved.
- **P3 Docs:** SPEC-106 (positions) and SPEC-125 (the file) say "one decimal"; API examples regenerated; tests: a drag-sized position (24.666…) is stored and returned as 24.7, an export of an unrounded stored value is rounded, a move of a group keeps its members' offsets.

Not done: rounding in the GUI (the server's rule covers every client, `inedctl` and scripts included).
