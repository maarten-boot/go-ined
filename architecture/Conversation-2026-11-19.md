# Conversation log 2026-11-19

1. **User**: status?
   **Claude** (a new session): the release commit `203cc55` is on GitHub, but CI on `main` failed for it and for `a06116a`. The cause is staticcheck ST1018: `TestOneLine` (`internal/snmp/client_test.go`) held the Unicode format characters U+202E, U+200B and U+2066 themselves, not as `\u` escapes; the shell wrote them literally.
   - Fixed on `fix/staticcheck` (merged). staticcheck v0.8.2, CI's version, was run locally over the whole repository and is clean.
   - The tag `v0.8.0` goes on the commit after this fix, once CI is green. The release commit's content is unchanged.
2. **User**: pushed (then "pushed the tag").
   **Claude** checked the push (`main` and `docs/architecture` at `9738e02`). CI on `main` was green (run 44502615260), so the annotated tag `v0.8.0` was made on `9738e02` and pushed by the user.
   - The release run 44507479025 was green on the first attempt; its macOS job waited 11 min for a runner. 0.8.0 was published at 11:04 UTC.
   - Checked as before: 11 archives and SHA256SUMS, every checksum OK. The linux/amd64 binaries print `v0.8.0`, the darwin and linux/arm64 GUI binaries are of the right architecture, `ined-gui.exe` is in the Windows zip, and the release text equals the `[0.8.0]` section.
   - Releases and artifacts (the user's rule): 10 releases, no CI artifacts stored, so there is nothing to clean up.
