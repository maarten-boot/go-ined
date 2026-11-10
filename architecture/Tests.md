# Test cases — go-ined

Each test traces to one or more Spec IDs. Status: `planned` / `implemented` / `passing` / `failing`.

| ID | Spec | Description | Go test | Status |
|----|------|-------------|---------|--------|
| TC-001 | SPEC-001 | Default theme is white background (#FFFFFF) / black text (#000000) | _TBD_ | planned |
| TC-002 | SPEC-002 | Every colour pair in every theme has contrast ≥ 4.5:1 (table-driven over all pairs) | _TBD_ | planned |
| TC-003 | SPEC-003 | Each preset enforces its minimum: AA 4.5, AAA 7, Max = 21 exactly | _TBD_ | planned |
| TC-004 | SPEC-003 | `custom` ratio < 4.5 or > 21 is rejected with an error | _TBD_ | planned |
| TC-005 | SPEC-004 | Interactive preset change updates all rendered colour pairs | _TBD_ | planned |
| TC-006 | SPEC-005 | Config file preset is loaded at startup; invalid value falls back to AA with a warning | _TBD_ | planned |
| TC-007 | SPEC-006 | Lint: every `uses:` in `.github/workflows/*.yml` references a 40-hex-char SHA with a version comment | _TBD_ | planned |
