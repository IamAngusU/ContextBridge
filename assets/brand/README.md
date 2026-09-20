# ContextBridge brand assets

This directory is the canonical source for ContextBridge branding.

`contextbridge-wordmark.svg` is the source of truth for the full logo lockup.
Keep derived raster exports outside this directory unless a shipped core
surface requires them. After changing the SVG, run:

```sh
go test ./...
go vet ./...
```
