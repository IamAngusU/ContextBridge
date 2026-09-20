# ContextBridge brand assets

This directory is the canonical source for ContextBridge branding.

`contextbridge-wordmark.svg` is the source of truth for the full logo lockup.
Keep derived raster exports outside this directory unless a shipped core
surface requires them. After changing the SVG, run:

The repository README intentionally embeds this file directly. The compact
dashboard mark at `internal/bridge/dashboard/mark.svg` is the same visual
identity cropped for square UI surfaces; it is not a replacement brand.

```sh
go test ./...
go vet ./...
```
