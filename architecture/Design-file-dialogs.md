# Design: file dialogs that can be moved and resized, and outlined folders

Status: **proposed** (2026-11-18), waiting for the user's OK.

User request: "when doing a export the file dialog is not movable or resizable, can we fix that? and the folders as black is a bit massive, the outline is ok as the symbol is now 40 years part of our culture."

## Why it is so

- `ined-gui` uses Fyne's own file dialog for "Export…" and "Import…" (SPEC-733). It isn't a window: Fyne draws it inside the main window, as an overlay of a fixed size. So it can't be moved or resized, and it doesn't know the desktop's bookmarks or recent folders.
- Fyne opens the desktop's own dialog only in a Flatpak build (through the XDG desktop portal). On Windows and macOS it always uses its own.
- The folders in that dialog are Fyne's folder icon: a filled shape in the text colour, so black on white.

## Proposal

- **F1 The desktop's own file dialog**, a real window that can be moved and resized, with the desktop's bookmarks and recent folders:
  - **F1a (recommended):**
    - **Linux:** through the XDG desktop portal (`org.freedesktop.portal.FileChooser`), as Fyne does under Flatpak. It shows the GNOME or KDE dialog. The package for it, `github.com/rymdport/portal` v0.4.2, is already in `go.sum` through Fyne. The portal is on this machine and is standard on Ubuntu 24.04.
    - **Windows and macOS:** through `github.com/ncruces/zenity`. It is a new dependency: MIT, pure Go, no cgo. On Windows it shows the Windows Save/Open dialog; on macOS the system panel.
    - **Fallback:** when the portal isn't there (a bare window manager, Xvfb in the tests) or the call fails at once, Fyne's dialog is used as now, at 90 % of the window size (F2).
  - **F1b:** the portal on Linux only, Fyne's dialog on Windows and macOS. No new dependency, but the complaint stays there.
  - **F1c:** keep Fyne's dialog, only larger (F2). Still not movable.
- **F2 Fyne's dialog larger.** Where it is still used, it opens at 90 % of the main window instead of its fixed size.
- **F3 Outlined folders.** The GUI's theme gets an outlined folder icon (and open folder) in the text colour, so every contrast preset holds. Shape:
  ```
   ┌───┐
   │   └──────┐
   │          │
   └──────────┘
  ```
  It applies wherever Fyne shows a folder: in the fallback dialog of F1a, and in Fyne's dialog under F1b and F1c. The desktop's own dialog uses the desktop's icons.
- **Unchanged:**
  - the file name offered (`lan.json`) and the `.json` filter;
  - the report dialog after choosing a file (SPEC-733);
  - export and import through `inedctl`.
- **Docs and tests:**
  - SPEC-733 says which dialog is used.
  - Tests:
    - choosing the dialog (portal there or not, a failing call falls back);
    - the outline icon is used, in the text colour of each preset;
    - the export and import tests keep calling `exportTo` and `importFrom` directly, as now.
  - **For the user to check:** the dialog on your desktop, and on Windows or macOS if you use them.

## Decisions for the user

- **F1:** F1a everywhere (recommended), F1b Linux only, or F1c a larger Fyne dialog only.
- **F3:** outlined folders: yes (recommended).
