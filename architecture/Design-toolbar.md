# Design: one Zoom menu, the "Show" list in the toolbar

Status: **proposed** (2026-11-14), waiting for the user's OK. Planned for after 0.5.0.

User request: "best move all the zoom settings in a single top menu and move the group selector to the top menu bar".
Read as: "top menu" = the toolbar row at the top of the window; "group selector" = the "Show" list (SPEC-726).

## Today (SPEC-717)

```
[Zoom −] 100 % [Zoom +] [100 %] [Fit all] | [Find host…] [Discover…] [View ▾] || [x] Wrap names  Contrast: [AA (4.5:1) ▾]
Show: [Networks            ▾]  Networks › lan
```

## Proposal

```
[Zoom 100 % ▾]  Show: [Networks       ▾] | [Find host…] [Discover…] [View ▾] || [x] Wrap names  Contrast: [AA ▾]
Networks › lan                                  (only while a group is shown)
```

## Decisions (recommended)

- **T1 One Zoom menu button.** It replaces the five zoom items. The button's label shows the current zoom ("Zoom 100 % ▾") and updates as you zoom with the wheel or the keys. Its menu has:
  - Zoom in (Ctrl++)
  - Zoom out (Ctrl+−)
  - 100 % (Ctrl+0)
  - Fit all (Ctrl+1)

  The shortcuts and the mouse wheel stay as they are. Zoom in or out is disabled at 400 % or 10 %. Like "View ▾", it is a button in the toolbar, not a window menu bar (see T4).
- **T2 The "Show" list goes into the toolbar**, right after the Zoom button. It keeps its label "Show:", Ctrl+L, typing to filter and Enter. It is 240 units wide instead of 300, so the toolbar fits in about 1100 pixels.
- **T3 The trail** (`Networks › lan`) stays in its own row below the toolbar. That row is hidden while a whole view is shown, so the map gets the room back.
- **T4 No real menu bar** (File / View / … across the top of the window). On macOS, Fyne puts that in the system menu bar, and on Linux and Windows it adds a row. That is a bigger change. Say so if you want it instead.

## Changes

- SPEC-717 (the toolbar's content), SPEC-726 (where the list sits and when the trail row shows), SPEC-713 unchanged.
- GUI tests for the menu (four entries, label follows the zoom, disabled at the limits), the list in the toolbar (Ctrl+L), and the hidden trail row; TC to be numbered.
