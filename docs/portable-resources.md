# Portable resources

ContextBridge can discover a fixed or removable resource pack by a stable
manifest ID instead of a drive letter or mount path. Absence is normal: routes
may fall back, while a reinserted pack becomes eligible again after validation.

Discovery deliberately:

- scans only configured roots and filesystem-volume roots;
- reads one bounded marker at a known relative path;
- does not recursively index a model archive;
- rejects symlinked markers and directories;
- accepts only loopback endpoints from a pack manifest;
- never executes an installer, autorun file, launcher, or script from media;
- quarantines every case-insensitive duplicate pack ID instead of selecting a
  first match.

A drive letter is neither identity nor authority. Duplicate identity is
ambiguous, so all colliding packs remain visible as diagnostics but none can be
resolved for execution.

## Example marker

Place `.contextbridge-pack.json` at the volume or pack root:

```json
{
  "schema_version": 1,
  "id": "portable-model-kit",
  "name": "Portable model kit",
  "version": "2026.09",
  "kind": "local-models",
  "endpoints": [
    {
      "id": "ollama-local",
      "type": "ollama",
      "url": "http://127.0.0.1:11434",
      "model": "qwen2.5:1.5b",
      "capabilities": ["text"]
    }
  ]
}
```

The manifest is discovery metadata, not permission to start code. The runtime
must already be running through an operator-reviewed launcher or managed
service. This keeps media insertion from becoming execution.

Inspect detected packs with:

```sh
contextbridge resources --config ./config.yml
contextbridge resources --config ./config.yml --json
```

An encrypted, unmounted, disconnected, invalid, or duplicated pack simply
contributes no routable capacity. Routes should retain an intentional fallback
for that state.
