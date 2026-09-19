# Portable resource packs and hot-plug runtimes

ContextBridge can discover a removable or fixed-volume resource by a stable
manifest ID instead of a drive letter or mount path. Absence is normal: a
configured engine becomes unavailable and its fallback may run. Reinserting
the same pack makes it eligible again during the regular runtime refresh.

Discovery is intentionally passive. ContextBridge:

- looks only for `.contextbridge-pack.json` at a selected root and its direct
  child directories, plus JSON sidecars in `.contextbridge-resources` at the
  selected volume root;
- reads at most 512 direct entries per root and 64 KiB per manifest;
- does not recursively index a model archive;
- does not follow symlinked markers or directories;
- accepts only loopback HTTP endpoints from a portable manifest;
- ignores unknown fields and invalid manifests by rejecting the entire marker;
- never executes a command, installer, autorun file, or script from the pack.

An encrypted or disconnected drive simply contributes no pack. A drive letter
is never an identity or authorization decision. Use a sidecar when the resource
tree has its own checksums, signatures, or immutable release manifest: discovery
must not make that tree fail its own integrity check.

## Marker example

Place this file at the pack root:

```json
{
  "schema_version": 1,
  "id": "example.modelkit",
  "name": "Example ModelKit",
  "version": "1.0.0",
  "kind": "model-runtime",
  "endpoints": [
    {
      "id": "ollama",
      "type": "ollama",
      "url": "http://127.0.0.1:11436",
      "health_path": "/api/version",
      "capabilities": ["text", "vision", "embedding"]
    }
  ]
}
```

For a sealed tree, leave the tree untouched and place a named JSON file such as
`X:\.contextbridge-resources\offline-arsenal.json` at the volume root. Its
`root_relative_path` is resolved against that same mounted volume, so changing
`X:` to another drive letter does not change the pack identity:

```json
{
  "schema_version": 1,
  "id": "example.offline-arsenal",
  "name": "Offline Arsenal",
  "version": "1.0.0",
  "kind": "local-toolbox",
  "root_relative_path": "offline-arsenal",
  "endpoints": [
    {
      "id": "control-api",
      "type": "service",
      "url": "http://127.0.0.1:4310",
      "health_path": "/api/status",
      "capabilities": ["tools", "retrieval", "vision", "audio", "image", "video"]
    }
  ]
}
```

The relative path must name one direct child of the selected volume and resolve
to a real, non-symlinked directory. Absolute or nested paths, `..` escapes,
missing targets, symlinked targets, unknown fields, and remote endpoints are
rejected.

Endpoint types are `ollama`, `openai_compatible`, and `service`. A `service`
endpoint is inventory metadata; it is not automatically granted job or tool
authority.

Reference a routable endpoint from the host config:

```yaml
portable_resources:
  enabled: true
  scan_roots: []
  max_packs: 32

engines:
  portable_ollama:
    type: ollama
    model: qwen2.5:1.5b
    resource_pack: example.modelkit
    endpoint: ollama
    timeout_seconds: 90

routes:
  portable_local:
    provider: portable_ollama
    fallback: [ollama, browser]
    timeout_seconds: 180
```

List what is physically present without starting it:

```bash
contextbridge resources
contextbridge resources --json
```

The terminal and dashboard also show detected packs. A pack may advertise what
its endpoint type can support, but actual Ollama model selection still uses the
normal per-model capability evidence. The marker is discovery metadata, not a
claim that every stored model is loaded, compatible, healthy, or trusted.

## Startup remains explicit

Hot-plug discovery does not start a runtime. Use the pack's reviewed operator
launcher or managed service explicitly. This separation keeps insertion of a
USB disk from becoming code execution. ContextBridge will observe the endpoint
when it becomes reachable.
