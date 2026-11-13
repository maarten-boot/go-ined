# Conversation log — 2026-11-13

## Session 1 (continued from 2026-11-12)

1. **User**: status?
   **Claude**: all refs pushed (checked with `git ls-remote`), CI green on `main`, `docs/architecture` and `fix/crosscheck-14`. v0.3.1 is the latest release (verified). `[Unreleased]` has the DNS log fix. Dependabot PR #1 is closed. Open: the formal OpenAPI validation, and a visual check of the new GUI features.
2. **User**: do a crosscheck.
   **Claude** ran crosscheck `Crosscheck-2026-11-13-01.md` (the fifteenth) on the crosscheck-14 fix, with a probe of the real binaries under Xvfb `:97` (a free display):
   - The menu on two groups, the Host labels submenu, the details row and "Expand 2 groups" all work as specified; an already expanded group's host didn't move.
   - **C1 (L):** frame labels can run past the frame's right edge: the width leaves out the status glyph, and text is measured regular but drawn bold (since M5).
   - Only this session's processes were stopped, and the scratchpad was emptied.
   - The fix waits for the user's OK.
