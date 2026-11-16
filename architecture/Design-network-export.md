# Design: export and import of a network

Status: **accepted** (2026-11-16, "OK, all"): N1–N11 as recommended. Not built yet. The order is server/API, then `inedctl`, then the GUI.

Notes for the build, from reading the code:
- **Update existing (N8):** a matching group is updated where it is and is not moved (its other contents would be left behind); the file's items below it go into it. Only matching hosts move, into their group from the file.
- **CIDR nesting (SPEC-113):** a network group that would not fit inside its enclosing network group is an error even with "update existing", not a conflict.
- **Duplicates:** an IP address or CIDR that appears twice in the file is an error in the file.
- **How to build it:**
  - `inventory/transfer.go`: `ExportGroup` reads under the read lock. `Import` works on a copy of the model (factor `clone()` out of `Snapshot`), applies one `Changes` and then switches the model over.
  - API: `POST /import` needs its own body limit; `auth` applies 1 MiB to every request today.
  - Positions in the model are absolute map coordinates. The export subtracts the top group's position; custom-view positions stay absolute.

User request: "should we add a cidr export/import option to allow import/export of a net with all its groups and stuff?"

## Today

- The only way to move data is to copy the whole database file (`storage.db_path`). That moves everything, needs the server stopped, and can't merge into another server.
- "Show in view" copies a group into a custom view on the same server (SPEC-729), but nothing goes between servers or into a file.
- A Tkined `.tki` import is listed as "later" (Spec §13).

## Proposal

Export a network group, with its subgroups and hosts, to one JSON file. Import such a file into the network view of any server, below a group of your choice, in one step that either fully succeeds or changes nothing. A dry run shows first what would happen.

Uses: moving a network to another server, keeping a copy of part of the map, setting up a lab or test server.

## Decisions (recommended)

- **N1 What is exported.**
  - A group of the network view, with everything below it. Usually that is a network group (one with a CIDR), but a plain group works the same way, so a site group with several networks can be exported too.
  - The whole map, several groups at once and a custom view as such are left for later (N11).
- **N2 What comes along.**
  - Each group: name, CIDR, description, note, host label mode (SPEC-725), bar colour (SPEC-727), collapsed, position.
  - Each host: IP address, manual name, description, note, monitored, watch list (names only, SPEC-310), position.
  - Positions are stored relative to the exported group, so the network keeps its layout wherever it lands.
- **N3 What stays behind.** Everything the server finds out again by itself:
  - status, round-trip time, loss, last seen and last change;
  - PTR names;
  - the 6-hour service check (`services`);
  - the status of watched services (they start `unknown`);
  - IDs and created/updated times.
  
  After an import the hosts are new to the server. They are pinged, looked up and checked at once, as hosts created by hand are.
- **N4 Custom views.**
  - For each exported host, its placements in custom views are kept by **view name and group path** in that view, for example `Payroll` › `app servers`, with the position.
  - The groups on such a path are exported with their name, description, note, host labels, bar colour and collapsed state, so they can be made again.
  - On import:
    - a view that doesn't exist is skipped, and the dry run says so; views are not created;
    - a group on the path that is missing is created;
    - a host already placed in that view (with N8's update option) keeps its placement unless the file places it.
- **N5 The file.** JSON, indented, with a format name and version so later versions can read it or refuse it with a clear message:

  ```json
  {
   "format": "go-ined-network",
   "version": 1,
   "exported_at": "2026-11-16T16:40:00Z",
   "server_version": "v0.5.0-45-g1234567",
   "from": "Networks › dc › lan",
   "group": {
    "name": "lan", "cidr": "10.0.1.0/24", "description": "", "note": "",
    "labels": "", "color": "", "collapsed": false, "x": 0, "y": 0,
    "groups": [ { "name": "printers", "cidr": "", "...": "...", "groups": [], "hosts": [] } ],
    "hosts": [
     { "ip": "10.0.1.10", "name": "web", "description": "", "note": "",
       "monitored": true, "watch": ["ssh", "https"], "x": 40, "y": 60,
       "views": [ { "view": "Payroll", "path": ["app servers"], "x": 120, "y": 80 } ] }
    ]
   },
   "view_groups": [
    { "view": "Payroll", "path": ["app servers"], "description": "", "note": "",
      "labels": "", "color": "", "collapsed": false, "x": 300, "y": 100 }
   ]
  }
  ```

  The file is not trusted. Everything in it is checked as API input is:
  - names, CIDRs, IP addresses, watch lists, and notes up to 64 KiB;
  - at most 10,000 hosts and groups, at most 32 levels deep;
  - unknown keys are an error, so a typo is not silently lost.
- **N6 Where it lands.**
  - The exported group becomes a child of a chosen group of the network view, or of the top level.
  - Its position is given (the GUI uses where you clicked). Without one, it is placed as a new group without a position is.
  - Everything below it keeps its place relative to it.
  - Hosts go where the file puts them, not into "the most specific network group" (SPEC-107). Their `outside_cidr` is worked out as usual.
  - The dry run warns when another network group of the server would be a more specific home for the imported CIDR, for example importing 10.0.1.0/24 at the top level while 10.0.0.0/16 exists. This is a warning, not a conflict.
- **N7 Dry run first.** The import can be asked to only report. The report lists:
  - what would be created: groups, hosts, custom-view groups, placements;
  - what would be updated (with N8's option);
  - the conflicts, the views that don't exist, and the warnings.
  
  The GUI always shows this report and asks before importing. `inedctl import` has `--dry-run`.
- **N8 Conflicts.**
  - What counts as the same item:
    - a host with the same IP address;
    - a network group with the same CIDR anywhere in the network view;
    - a plain group with the same name under the same parent.
  - **By default an import with any conflict is refused** and nothing changes. The error lists the conflicts, the first 50 with a count of the rest.
  - **With "update existing"**, matching items are updated from the file instead:
    - fields, positions and watch lists are set;
    - matching hosts are moved into the imported group;
    - nothing on the server that isn't in the file is deleted or moved.
- **N9 One step, normal events.**
  - The import is one database transaction: all of it or nothing.
  - It emits the usual events for what it created or changed (`group.created`, `host.created`, `host.updated`, `placement.updated`), as discovery and "Show in view" do.
    - Connected clients and `ined-alert` follow them as now.
    - A client that was disconnected during a large import falls back to `resync`, as for any large change.
  - The import is logged with its counts.
- **N10 Where you use it.**
  - **API:**
    - `GET /groups/{id}/export` returns the file. It works with the read-only token too: the export holds notes, so it is readable by anyone who can read the map anyway.
    - `POST /import?parent_id=&x=&y=&dry_run=true&update=true` takes the file and returns the report: 200 when done or a dry run, 409 `conflict` with the report when refused.
    - Bodies up to 32 MiB for this path only (others stay at 1 MiB).
    - The read-only token gets 403. OpenAPI and the API examples are updated.
  - **`inedctl`:**
    - `group export <id> [-o FILE]` (default: stdout);
    - `import FILE [--parent ID] [--dry-run] [--update]`, which prints the report and exits 1 when refused.
  - **GUI:**
    - a group's menu in the network view gets "Export…", with a file dialog;
    - the menu of empty space and of a group in the network view gets "Import here…": pick a file, see the report, then "Import" or "Cancel";
    - with conflicts, the report offers "Update existing and import".
- **N11 Later.**
  - Importing into another address range, such as 10.1.0.0/24 to 10.2.0.0/24 with host addresses shifted, for copies with new addresses.
  - Exporting several groups, the whole map or a custom view.
  - The Tkined `.tki` import. It would turn a `.tki` file into this format and then use the same import.
  - Creating missing custom views on import.

## Example

```sh
inedctl group export 12 -o lan.json            # on server A
inedctl --url https://b:8470 import lan.json --parent 3 --dry-run
Would create: 4 groups, 57 hosts, 1 custom-view group, 12 placements in "Payroll".
Skipped: view "Lab" doesn't exist (3 placements).
Warning: 10.0.0.0/16 (Networks › dc) would be a more specific home than "sites" (id 3).
Conflicts: none.
inedctl --url https://b:8470 import lan.json --parent 3
Created: 4 groups, 57 hosts, 1 custom-view group, 12 placements.
```

## Changes

- **Server:**
  - `internal/inventory`: build the export tree; check and apply an import in one transaction, with the report;
  - API: `GET /groups/{id}/export`, `POST /import` with its own body limit; OpenAPI and the API examples.
- **inedctl:** `group export` and `import`.
- **GUI:** "Export…" and "Import here…" with the report dialog; Fyne's file dialogs.
- **SPEC:** new items for the file format, export, import and conflicts (server and API), `inedctl` and the GUI.
- **TCs:**
  - export: fields, relative positions, custom-view paths, what is left out;
  - round trip: export, import into an empty server, export again gives the same file apart from `exported_at`;
  - the dry run; each kind of conflict refused; update existing;
  - bad files: unknown key, wrong version, a bad CIDR or IP, too many items, too deep;
  - a skipped view, a missing custom-view group created, the read-only token;
  - `inedctl`; the GUI dialogs.
- **Order of work:**
  1. Server and API.
  2. `inedctl`.
  3. The GUI.
  
  With the first two, a network can be moved between servers from the command line.
