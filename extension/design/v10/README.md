# Browser extension design WIP v10

This folder is the design/states workbench for the ContextBridge browser extension on the `wip` branch. It is intentionally separate from `extension/src` while the UI is still being reviewed.

Target runtime version: **0.5.71 alpha**.

## Files

- `prototype.html` — compact executable popup design reference.
- `wizard.html` — first-run setup wizard for the current local-first architecture.
- `STATE-MATRIX.md` — the complete 49-state UI coverage contract plus motion rules.

## V10 decisions

- The small `a` next to the version is an **inline text affordance**, not a badge/button-shaped control. It stays orange and opens the alpha popover on hover/focus.
- Pairing-token Show/Hide operates on a real demo token in the prototype, so `Show` visibly reveals the value instead of revealing bullet characters that were stored as the value.
- Counters and authoritative values use a short **ghost reveal** when they change: attached/free/working slots, heartbeat, connection state, provider, model, reasoning/effort, profile state, session policy and pairing/service state.
- Reduced-motion disables ghost transitions, glint, pulses and wizard step movement.
- The ContextBridge mark keeps the restrained left-to-right glint with a long idle interval.
- Light and dark themes stay calm and product-like. Dark mode uses graphite/charcoal surfaces and desaturated semantic colors, not neon or sci-fi treatment.
- Detach remains a direct utility action. It is never hidden only behind the overflow menu.
- Session behavior is treated as routing policy, separate from generic settings.
- Language is modeled as a list-backed selector (`en`, `de`) so more locales can be added without redesigning the header.

## Wizard

The current repository has no extension wizard file, so V10 adds a design reference rather than reviving an older account-style flow. The present ContextBridge product is local-first and does not need cloud account creation, invite keys, usernames or registration.

The wizard therefore follows the current product contract:

1. Language and scope
2. Local loopback service + pairing token
3. Explicit AI-page access (ChatGPT, Gemini, or visual teaching)
4. New-session routing policy
5. Review / ready + optional `cb console`

The wizard is reversible and every choice can be changed later. It does not silently attach personal conversations.

## Integration note

Do not copy this folder mechanically over `extension/src`. Runtime state remains authoritative in the existing background/popup code. The final integration should map those facts to this presentation layer, then keep Chromium and Firefox packaging generated from the common extension source.
