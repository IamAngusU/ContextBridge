# ContextBridge extension UI V10

WIP design assets for the browser extension. These files intentionally do **not** replace `extension/src` yet.

## Files

- `popup-preview.html` - compact, browsable design preview for the current operator surface.
- `setup-wizard.html` - first-run extension wizard prototype.
- `assets/chatgpt.svg` and `assets/gemini.svg` - bundled provider marks used only by the design preview.

The full executable prototype with the complete 49-state matrix is kept as the V10 design artifact outside the runtime extension until the UI is approved.

## V10 changes

- The alpha `a` is styled as inline orange text, not a badge or chip. Hover/focus opens the alpha information popover.
- Pairing-token Show/Hide reveals the actual input value. The preview embeds only a non-secret demo token.
- Attached/free/working/heartbeat counters transition when values change.
- Model and reasoning values use the same short ghost/reveal motion when authoritative provider evidence changes.
- Reduced motion disables optional motion and the restrained logo glint.
- Session behavior stays separate from the main status surface.

## Wizard

The current repository no longer uses the old license-key / username / folder-scan onboarding model. The wizard here follows the current product semantics instead:

1. language and explicit-scope explanation
2. local loopback service and pairing token
3. explicit first AI tab
4. browser session policy and safety preferences
5. review and explicit connect

The existing installer remains responsible for choosing the local target and device role (`ollama|managed|browser|later` and `local|relay|worker|all`).

## Integration

After approval, port the presentation layer into `extension/src/popup.html`, `popup.css`, and `popup.js` while preserving the current background, permission, session, lease, and fail-closed contracts.
