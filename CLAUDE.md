# go-ined — project rules for Claude

Go project. These rules supplement the global `~/.claude/CLAUDE.md`.

## Documentation
- All planning docs live in `architecture/`. Docs are edited on branch `docs/architecture` and merged into `main`. Code is developed on `feature/*` branches.
  - `Conversation-YYYY-MM-DD.md` — running conversation log, one file per day.
  - `Plan.md` — implementation plan; keep current.
  - `Spec.md` — specification; every requirement has an ID (`SPEC-NNN`).
  - `Tests.md` — test cases, each traced to one or more `SPEC-NNN` IDs.
  - `Crosscheck-YYYY-MM-DD-nn.md` — crosscheck reports (`nn` = 01, 02, … restarting daily).
- `Changes.md` (repo root) records changes between releases.
- After significant changes, suggest a crosscheck.

## Git
- Module path: `github.com/maarten-boot/go-ined`. Remote: `github.com/maarten-boot/go-ined` (public).
- Commits must use the GitHub noreply address `11140490+maarten-boot@users.noreply.github.com` (set in the repo-local git config). Never use a private email.
- Claude has no push access: suggest push and other remote write commands for the user to run.

## Input review
- If a user sentence contains errors or uncertainties, propose corrections and confirm before proceeding.

## Crosscheck ("do a crosscheck" / "crosscheck")
1. Inspect all code and match it against Spec.md and Plan.md.
2. Inspect Spec.md and Plan.md and check them against the code.
3. Verify all examples (docs, `examples/`, Go `Example*` functions) agree with code, Spec and Plan.
4. Check Tests.md against the code and the actual test files.
5. Run the test suite (`go test ./...`) and report pass/fail per test.
6. Add missing examples where justified.
7. Suggest other improvements.
8. For code↔docs mismatches: propose the fix and **wait for the user's OK** before changing either side.
9. Save the report to `architecture/Crosscheck-YYYY-MM-DD-nn.md` and post a short summary in chat.

## CI / GitHub Actions
- Pin every action to its full 40-char commit SHA, with the version as a comment:
  `uses: actions/checkout@<sha> # vX.Y.Z`
- Use the latest workable release; if that is not possible (breaking change, no release, incompatibility), explain why in a comment and in chat.

## GUI / TUI colour and contrast
- Default: white background, black text.
- Every foreground/background pair must reach WCAG contrast ≥ 4.5:1 (AA).
- User can raise contrast interactively and via config file: presets AA (4.5:1), AAA (7:1), Max (21:1, pure black/white), plus a custom minimum ratio.
- Colour pairs are validated automatically by a test.
