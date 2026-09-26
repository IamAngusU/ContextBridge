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

`portable_resources.max_packs` limits returned packs. The independent
`portable_resources.max_scan_candidates` (default 4096, maximum 32768) limits
the total marker/sidecar candidates inspected across all roots. Reaching that
work envelope is an explicit error and yields no partial pack set, because a
later candidate could otherwise reveal an identity collision that changes
which pack is safe to route.

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

A passive local toolbox may additionally describe bounded service metadata:

```json
{
  "id": "control-api",
  "type": "service",
  "url": "http://127.0.0.1:4310",
  "health_path": "/api/status",
  "capability_path": "/api/node/capabilities",
  "execute_path": "/api/node/execute",
  "capabilities": ["tools", "typed-execution", "workflows", "artifact-lineage"]
}
```

The paths must be absolute URL paths without traversal, query, or fragment;
they are accepted only on a loopback `service` endpoint. ContextBridge reports
them as capability metadata but does not invoke an execute path merely because
media advertised one. A separate operator-reviewed integration and normal job
policy remain required.

Inspect detected packs with:

```sh
contextbridge resources --config ./config.yml
contextbridge resources --config ./config.yml --json
```

An encrypted, unmounted, disconnected, invalid, or duplicated pack simply
contributes no routable capacity. Routes should retain an intentional fallback
for that state.
