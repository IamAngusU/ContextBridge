# ContextBridge brand assets

This directory is the canonical source for ContextBridge branding.

## Format roles

- `contextbridge-mark.svg` is the source of truth for the standalone mark and should be preferred in the README, website, dashboard, documentation, and other scalable UI surfaces.
- `contextbridge-wordmark.svg` is the source of truth for the full logo lockup.
- `*.webp` files are raster exports for large previews and other places where a compact raster asset is preferable.
- `contextbridge-mark-16.png`, `-24.png`, and `-32.png` are exact-size raster exports for small UI/OS use.
- Browser extension manifest icons stay PNG (`16`, `32`, `48`, `128`) under `extension/src/icons/` for broad Chromium/Firefox compatibility.

Do not use WebP as the extension toolbar/manifest icon source. Do not edit packaged copies under `extension/chromium/` or `extension/firefox/` directly.

After changing canonical brand assets, run:

```sh
./scripts/sync-brand-assets.sh
./scripts/package-extensions.sh
./scripts/verify-extensions.sh
```
