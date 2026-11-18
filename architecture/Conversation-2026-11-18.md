# Conversation 2026-11-18

1. **User**: status?
   **Claude**: all pushed (`main` and `docs/architecture` at `36a194c`), CI green on both, v0.6.0 published and checked, nothing waiting. Open: the user's visual checks, the flaky TestDiscoveryDialog, the SNMP items.
2. **User**: the little red triangle on the bottom right of a host means what? it would be nice to have a explanation in the right details pane when we select the host or a hover ?
   **Claude** explained the service marker (a watched service is down; the glyph still shows the ping status) and wrote `Design-marker-explained.md`: E1 a line in the details pane ("▲ 1 watched service down: https (443) since 14:07"), E2 a hover box on the map (E2a every host, recommended; E2b the triangle only), E3 a Help menu with "Map symbols…", E4 docs. Waiting for the user's OK.
