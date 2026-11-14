# Design note: a bottom bar to leave a group, and a colour for the top bar

Status: **implemented** (2026-11-14): B1–B8 as recommended (user: "OK, all"), SPEC-704 v0.42 and SPEC-727, TC-735 and 736. Additions while building: the status bar names what moved out, and the details panel has a "Bar colour" row.
Origin: the user's request (log 2026-11-14, entry 1): "give the group also a bottom bar (can be half the size of the top bar); moving an item to the bottom bar makes the item leave the group. Allow the user to select a colour for the top bar of the group, a fixed palette of 16 or so colours and a custom colour selector."

## 1. Today

- An expanded group is a frame with a **top bar** (the label strip, 24 map units high: status glyph, name, status). Dropping an item onto it moves the item **into** the group, at a free position (SPEC-704, 508); while dragging, the bar is marked with a thick border and the word "into".
- Dropping onto empty space only moves the item; its parent stays. Leaving a group takes the context menu ("Move to group…").
- Frames are drawn in one colour (the palette's `Frame`, or `NetFrame` for network groups).

## 2. Proposal

### The bottom bar
- Every expanded frame gets a **bottom bar** of 12 map units (half the top bar), below its members, separated by a thin line. It is empty: no text.
- Dropping an item that lies **inside** the group onto its bottom bar moves it **out** of the group, to the group's parent (or the root). While dragging over it, the bar is marked like the top bar, with the word "out".
- The item lands **just below the frame**, at the x where it was dropped, so it is visibly outside. When the parent is expanded too, the parent's frame grows around it as usual.
- A collapsed group's icon has no bottom bar; dropping onto it still means "into".

### The top bar's colour
- A group's context menu gets **"Bar colour ▸"**: "None" (as today), 16 swatches, and **"Custom…"**, which opens Fyne's colour picker (hue, saturation, RGB and hex input).
- With several groups selected it acts on all of them ("Bar colour (3 groups) ▸"), like "Host labels".
- The colour fills the top bar of an expanded group and the folder tab of a collapsed group's icon. The border keeps its usual colour, so network groups stay recognisable (dashed).

## 3. Decisions (recommendation first)

| # | Question | Recommendation | Alternative |
|---|----------|----------------|-------------|
| B1 | Where does an item go that leaves a group? | **To the group's parent** (one level up; the root for a top-level group) | Always to the root |
| B2 | Where does it land? | **Just below the frame, at the drop's x** (a small gap), so it is clearly out | A free position near the parent (SPEC-508), which can be far away |
| B3 | Which items can leave through a bottom bar? | **Any item inside the group, at any depth**; it goes to the group's parent. For other items the bottom bar is empty space (a plain move, no "out" mark) | Only direct members |
| B4 | Size and look of the bottom bar | **12 units, no text, marked "out" while dragging**; on every expanded frame, network groups included | Shown only while dragging (the frame then jumps by 12 units at the start of a drag) |
| B5 | Where is the colour kept? | **On the server, the same in every GUI**, like the host labels: `color` on groups (`""` or `#rrggbb`), a new column (schema 5), `group.updated` events; `inedctl group color <id> '#rrggbb'\|none` and `group add --color` | Only in this GUI's `gui.toml` |
| B6 | The fixed palette | **16 distinct colours** (below), plus "None" and "Custom…"; a custom colour is stored as its hex value | 8 or 24 colours |
| B7 | Contrast (CLAUDE.md: every text pair ≥ 4.5:1; presets AA, AAA, Max) | **The label is black or white, whichever contrasts more** with the bar; that is always at least 4.58:1, so AA holds for every colour. For AAA or a custom ratio, the bar is lightened or darkened just enough to reach it; under **Max** the bar is drawn uncoloured (pure black on white). The status glyph keeps its own small background square, so its contrast doesn't change | Let the user pick the text colour too |
| B8 | Collapsed groups | **The folder tab of the icon gets the colour** too, so a group is recognisable in both states | Only expanded frames |

Palette (B6), in menu order: red `#E6194B`, orange `#F58231`, yellow `#FFE119`, lime `#BFEF45`, green `#3CB44B`, teal `#469990`, cyan `#42D4F4`, blue `#4363D8`, navy `#000075`, purple `#911EB4`, lavender `#DCBEFF`, magenta `#F032E6`, pink `#FABED4`, maroon `#800000`, brown `#9A6324`, beige `#FFFAC8`.

## 4. Spec and tests (after the OK)

- **SPEC-704** gets the bottom bar (v0.42); a new **SPEC-727** (bar colour, GUI) and **SPEC-1xx/5xx** changes for the `color` field in the data model and the API (OpenAPI, examples), the store (schema 5), and `inedctl`.
- **Tests:**
  - mapmodel: the frame's height and the bottom bar's rectangle; drop target "out" only for items inside; the landing position;
  - GUI: a drag onto the bottom bar moves the item to the parent below the frame; "into" still works; the colour menu (palette, None, Custom…, several groups);
  - contrast: label colour per palette colour ≥ 4.5:1; AAA adjusts the bar; Max drops the colour;
  - store, API and `inedctl`: `color` saved, validated (`#rrggbb` or empty), in events, and the migration.
- Release as **0.5.0** (new features, a new API field and schema).
