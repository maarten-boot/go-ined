# Spec — go-ined

Status: **draft** — functional scope pending the user's scenario description.
Every requirement has a stable ID (`SPEC-NNN`). Tests in `Tests.md` reference these IDs.

## 1. Purpose
_TBD — awaiting scenario._

## 2. Functional requirements
_TBD — awaiting scenario._

## 3. Cross-cutting requirements

### UI colour and contrast (applies to any GUI/TUI component)
- **SPEC-001** Default theme is a white background with black text.
- **SPEC-002** Every foreground/background colour pair has a WCAG 2.x contrast ratio ≥ 4.5:1.
- **SPEC-003** Contrast presets: `AA` (≥ 4.5:1, default), `AAA` (≥ 7:1), `Max` (21:1, pure black on pure white), and `custom` (user-given minimum ratio, ≥ 4.5).
- **SPEC-004** The user can change the contrast preset interactively at runtime.
- **SPEC-005** The contrast preset / custom ratio can be set in a config file; a runtime change takes effect without restart.

### Build / CI
- **SPEC-006** Every CI action (GitHub Actions or similar) is pinned to a full 40-character commit SHA, with the version in a trailing comment, using the latest workable release. Exceptions are documented inline with the reason.
