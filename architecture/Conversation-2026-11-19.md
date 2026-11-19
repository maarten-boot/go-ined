# Conversation log 2026-11-19

1. **User**: status?
   **Claude** (a new session): the release commit `203cc55` is on GitHub, but CI on `main` failed for it and for `a06116a`. The cause is staticcheck ST1018: `TestOneLine` (`internal/snmp/client_test.go`) held the Unicode format characters U+202E, U+200B and U+2066 themselves, not as `\u` escapes; the shell wrote them literally.
   - Fixed on `fix/staticcheck` (merged). staticcheck v0.8.2, CI's version, was run locally over the whole repository and is clean.
   - The tag `v0.8.0` goes on the commit after this fix, once CI is green. The release commit's content is unchanged.
