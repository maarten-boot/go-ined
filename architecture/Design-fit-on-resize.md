# Design: fit all on start and after a resize

Status: **implemented** (2026-11-14): R1–R5 as recommended ("OK, all"), on `feature/fit-on-resize`. Note: "fit all" never zooms above 100 %, so a small map is centred at 100 % after a resize.

User request: "when opening the app with a current view and after a resize or maximize screen also execute a fit-all".
Read as: when the GUI starts, the current view is fitted, and so is the map after the window is resized or maximized.

## Today

- A map is fitted once, on its first layout (`placed`, SPEC-713). At start the window is first laid out at 1200×800. When the window manager then maximizes it, or restores a saved size, the fit is not redone, so the map sits in a corner at the old zoom.
- A resize keeps the zoom and the top-left corner.

## Decisions (recommended)

- **R1 Start.** The current view is fitted at its final size. A maximize by the window manager just after start is a size change, so R2 fits it again; no timer is needed.
- **R2 Resize and maximize.** Whenever the map area's size changes (window resized, maximized or restored), the map of the visible tab fits all. A drag-resize fits continuously while you drag; that is cheap.
- **R3 Also the panel divider.** Dragging the divider between the map and the details panel also changes the map's size, so it fits too. The alternative is to fit only for window resizes, which is harder in Fyne because it has no window-resize event. Say so if you'd rather keep the zoom when moving the divider.
- **R4 Focus and other tabs.** With a group shown on its own (SPEC-726), "fit all" fits that group, as the menu item does. A hidden tab is fitted when it is next shown at a different size.
- **R5 What it costs.** After a resize, your own zoom and pan are replaced by "fit all". Zooming without resizing keeps your zoom as now.

## Changes

- SPEC-713 (when the map fits), test: size change → viewport equals Fit(extent); unchanged size → zoom kept. TC to be numbered.
