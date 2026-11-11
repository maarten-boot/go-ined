# Conversation log — 2026-11-12

## Session 1 (continued from 2026-11-11)

1. **User** answered the multi-select questions (asked on 2026-11-11): select with Ctrl+click and a Shift+drag rubber band; g asks a name; c, e and Delete act on all selected.
   **Claude** built the two queued requests:
   - **`feature/anchored-expand`** (merged): the API move takes `contents_only`. The GUI keeps a group's top-left corner on collapse (icon to the frame's corner) and on expand (contents shifted first); empty frames start at the icon's corner. Checked under Xvfb, also after dragging the collapsed icon elsewhere. On the way, it fixed a racy test (`TestDiscoveryDialog`, 2 of 10 runs): it released the probes before the cancel had reached the server.
   - **`feature/multi-select`** (merged): a selection set in the map widget (modifier keys from the mouse press); the rubber band; multi-drag and drop; keys g (group the selection: name dialog, common parent, positions kept, corner rule), c/e, Delete (one question), Esc. Checked under Xvfb: Ctrl+click, g, c, the rubber band, Delete.
   - Spec and Plan v0.29 (SPEC-507, 703, 724), TC-725/726. `make all`, staticcheck and govulncheck pass.
