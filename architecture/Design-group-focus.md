# Design note: a group as a view (focus)

Status: **implemented** (2026-11-13): F1–F9 as recommended (user: "OK, all"), Spec SPEC-726 v0.39, TC-730..734. One detail added while building: the trail's parts are buttons styled like the toolbar's, so they look clickable.
Origin: the user's idea (log 2026-11-13, entry 7): "every group also becomes a view … select the view we want to see not as notebook tab but as a dropdown list". The user chose option 1 (the same map, narrowed to one group; nothing new stored) and to keep the tabs and add the dropdown (entry 8).

## 1. Goal

- Any group can be shown on its own, as if it were a view: the map shows only that group's contents, at the positions they already have.
- A **dropdown list** next to the tabs chooses what to show: a whole view or one of its groups.
- The tabs stay, one per real view (SPEC-715).
- **Nothing new is stored on the server.** No API change, no new events, and no new positions. Moving, editing and deleting work as on the full map, and every other GUI sees the change.

## 2. Behaviour

- **The focus:** each tab has a focus: the whole view (as today) or one of its groups. With a group in focus:
  - the map shows that group's subtree laid out as if the group were the root;
  - the group itself isn't drawn, because the trail above the map names it;
  - its subgroups are drawn as usual, collapsed or expanded.
- **The trail** above the map shows where the focus is, for example `Networks › lan › servers`. Each part can be clicked to go there; the first part is the whole view.
- **The dropdown** ("Show") lists every view and, under each one, its groups as an indented tree, in name order. Choosing an entry switches to that view's tab and sets the focus.

## 3. Decisions (recommendation first)

| # | Question | Recommendation | Alternative |
|---|----------|----------------|-------------|
| F1 | What the dropdown lists | **Every view with its groups, indented.** Choosing a group of another view switches tab. | Only the groups of the current tab |
| F2 | A group in focus that is **collapsed** on the shared map | **Its contents are shown anyway.** Only this GUI shows them; the `collapsed` flag doesn't change, so other GUIs are unaffected. | Expand it on the server first (changes it for everyone) |
| F3 | Going back | **The trail, the dropdown, and Backspace** (one level up; at the top it does nothing). | Trail and dropdown only |
| F4 | Remembering the focus | **Per tab, while the GUI runs.** A restart opens every tab on the whole view. | Saved in `gui.toml` |
| F5 | Zoom and pan | **Entering a focus fits its contents; leaving it restores the zoom and pan the level above had.** | Keep the zoom and pan as they are |
| F6 | Actions inside a focus | **As on the full map, with the focus group as the root.** "New group here…", "New host here…" and the key **g** put new items into the focus group; "Reflow view…" reflows the focus group's members; "Fit all" fits the focus. An item moved out of the focus group disappears from the map, and the status bar says where it went. | — |
| F7 | The focus group is deleted, or a host is found outside the focus | **Deleted:** the focus moves to the nearest group above that still exists (or the whole view), and the status bar says so. **"Find host"** with a host outside the focus goes to the whole view first. | — |
| F8 | How to focus a group from the map | **"Show only this group" in a group's context menu, and the key o** (open) on the primary selected group. Double-click stays expand/collapse (SPEC-703). | Menu only |
| F9 | Many groups | **The dropdown filters as you type** (a combo box: typing narrows the list to names that contain the text; Enter takes the first match). **Ctrl+L** puts the cursor in it. | A plain list, no filter |

## 4. Spec and tests (after the OK)

- **SPEC-726** SHOULD (v0.39, user request): the focus, the trail and the dropdown as above. SPEC-715 and 717 get a reference to it.
- **Tests**, as unit and integration tests in `internal/gui` and `mapmodel`:
  - the layout of a focus, with the group itself not drawn and a collapsed focus group shown open;
  - the trail and its clicks; the dropdown's entries, filter, and switching tab;
  - Backspace and the zoom being restored;
  - new items, **g** and Reflow inside a focus;
  - a focus group deleted; find host outside the focus;
  - the key **o** and the menu item.
- No server, API, store or `inedctl` change.
