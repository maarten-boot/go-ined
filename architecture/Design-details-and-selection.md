# Design: selectable details, the note below them, and actions on a selection

Status: **proposed** (2026-11-14), waiting for the user's OK.

User request: "text in the left info pane should be selectable, the notes editable text can start directly under the last info item from the inventory. Currently only hosts have 'Remove from this view', that should also apply to groups. When selecting multiple items and executing 'Remove from this view' or 'Show in view', it should apply to all selected items."

How I read it (please confirm):
- "test" → "text".
- "left info pane" → the **details panel**. It is on the right of the map, so I take "left" to be a slip. Say so if you meant something else.

## Today

- The details panel shows rows as plain labels, which you can't select or copy. The note sits at the bottom of the panel, with empty space between the last row and the note.
- "Remove from this view" exists only for a host in a custom view, and acts on that one host even when several items are selected.
- "Show in view ▸" acts on the item you right-clicked: it places a host, or copies a group (SPEC-729).

## Decisions (recommended)

- **D1 Selectable text.**
  - The values in the details panel and its title ("Host nas") can be selected with the mouse and copied with Ctrl+C. Double-click selects a word.
  - The row names ("IP address", …) stay plain labels.
  - The selection colour is the existing light blue (black text on it is about 16:1).
  - Under the **Max** preset, the selection colour is white, as everything there is pure black or white. Selected text is then not highlighted, but copying still works. The alternative is a visible highlight under Max, which needs a grey; say so if you want that.
- **D2 The note right below the rows.**
  - "Note", the note field and "Save note" follow straight after the last row, in the same scroll area as the rows. The title stays fixed at the top.
  - The note field takes the rest of the panel's height, and at least 6 lines. When the rows and the note together are taller than the panel, the whole panel scrolls.
- **D3 "Remove from this view" on groups (custom views only).**
  - This removes the group and its subgroups from this view, and takes its hosts out of this view. The hosts stay in the network view and in any other view they are in.
  - The server already does exactly this when a group of a custom view is deleted with its contents (`DELETE /groups/{id}?children=cascade`, SPEC-120), so the server doesn't change.
  - An empty group goes at once. A group with contents asks first: "Remove group "app servers" (2 subgroups, 12 hosts) from "Payroll"? The hosts stay in the other views."
  - Hosts keep going without a question, as now.
  - In the network view the entry doesn't appear, because removing something there means deleting it ("Delete…").
- **D4 "Remove from this view" on a selection.** With several items selected and one of them right-clicked, the entry acts on the whole selection, like "Move N items to group…" and "Delete N items…" (SPEC-724):
  - The label is "Remove N items from this view".
  - Items inside a selected group go with that group.
  - It asks first when a group with contents is among them.
  - The status bar says what was removed ("Removed 2 groups and 5 hosts from "Payroll"").
- **D5 "Show in view ▸" on a selection.** The same rule applies:
  - Choosing a view places every selected host in it, at that view's root as now, and copies every selected group with everything in it (SPEC-729). Hosts inside a selected group come along with the group's copy and are not placed separately.
  - Selected hosts that are already in the target view stay where they are.
  - The list holds every custom view except the current tab's.
  - A view is ticked when every selected item is already in it, using the same rule as now for groups: the view's map must be loaded in this GUI.
  - The status bar sums it up ("Payroll: placed 3 hosts, copied 1 group (2 groups, 12 hosts)").
- **D6 Keys.** No new keys. Delete still deletes, and it asks first.

## Changes

- GUI only. No server or API changes, no schema change.
- SPEC changes:
  - SPEC-708 for the details panel: selectable values, and where the note sits.
  - SPEC-721 for removing groups from a view.
  - SPEC-724 and SPEC-729 for actions on a selection.
- Tests, with TC numbers to follow:
  - the labels are selectable and the note follows the rows;
  - "Remove from this view" on an empty group, and on a full group with its question; the hosts stay in the network view;
  - "Remove N items from this view";
  - "Show in view" on a selection of hosts and groups, with ticks and the status text.
