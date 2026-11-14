# Design: "Show in view ▸" on groups

Status: **proposed** (2026-11-14), waiting for the user's OK.

User request: "also show the 'Show in view' menu item on groups".

## Today

- A **host** can be placed in any number of custom views (SPEC-509, SPEC-721). "Show in view ▸" on a host lists the custom views, ticks those it is in, and places it at a view's root.
- A **group** belongs to exactly one view (`view_id`). A custom view has its own groups ("Payroll ▸ app servers"), and its hosts are placements of the same hosts. So a group can't be "placed" in a second view the way a host can.

## Proposal

"Show in view ▸ <view>" on a group **copies the group's structure into that view**:
- a new group with the same name, description, host labels and bar colour at the view's root;
- the same subgroups below it;
- every host of the group's subtree placed in the matching new group, at the same positions relative to the group.

The hosts stay the same hosts, pinged once, with the same status everywhere. Only the groups are new, and they belong to the target view.

## Decisions (recommended)

- **G1 Copy, not link.** Changes made later to the original group, such as new hosts or a renamed group, don't follow into the copy. A live link would need a new kind of object on the server, which is much more work. Say so if that is what you want.
- **G2 One server call.** `POST /views/{id}/copy-group {"group_id": N}` does it in one transaction with one `map.arranged` event for the target view. It answers the new group's id and the number of hosts placed. `inedctl group copy <id> --view <view>`.
- **G3 Hosts already in the target view** are moved into the copy, since a host has one place per view. The status bar says how many were moved ("lan copied to Payroll: 12 hosts, 3 of them moved from elsewhere in Payroll").
- **G4 The list** has every custom view except the group's own. A view is ticked when every host of the group's subtree is already in it. Choosing a ticked view still copies, because the groups may be missing there. A group without hosts can be copied too, giving empty groups.
- **G5 Where it goes:** the copy's top group is placed at the view's root where new items go (SPEC-106). The details panel and the target tab show it; the current tab stays.
- **G6 Network groups** (with a CIDR) are copied as ordinary groups without a CIDR. In a custom view, a CIDR would only mean "discover into here", which belongs to the network view.

## Changes

- New SPEC-729 (API + GUI + inedctl), OpenAPI, API examples; TCs to be numbered for the server (structure, positions, already-placed hosts, errors such as a group of the same view or an unknown view) and for the GUI menu.
