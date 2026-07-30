# Integration Recipes

Every source produces the same JSON job envelope. Keep source credentials in the source process and pass only the content ContextBridge needs.

## HTTP

```bash
curl -fsS http://127.0.0.1:32145/v1/jobs \
  -H "Authorization: Bearer $CONTEXTBRIDGE_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @examples/job.json
```

## Standard Input

```bash
generate-context-job | contextbridge submit --file -
```

This is useful for scripts, server exports, and database workers that already know how to emit JSON.

## Folder Inbox

Write a complete JSON file to a temporary name, then rename it into the configured inbox with a `.json` suffix. The atomic rename prevents ContextBridge from reading a partially written job.

```bash
cp job.json ~/.config/contextbridge/inbox/job.tmp
mv ~/.config/contextbridge/inbox/job.tmp ~/.config/contextbridge/inbox/job.json
```

The result appears as `job.result.json`.

## SSH

Run the data extraction on the machine that owns the credentials and pipe its JSON to the local bridge:

```bash
ssh private-host '/opt/jobs/export-next' | contextbridge submit --file -
```

For a long-running remote source, expose ContextBridge through an authenticated SSH tunnel. Do not bind the service directly to a public interface.

Use `ExitOnForwardFailure`, server keepalives, strict host-key verification, and a dedicated SSH identity. A reverse tunnel does not require opening the forwarded port in the public firewall when it binds to server localhost.

ContextBridge can display tunnel state when the tunnel supervisor sends `/v1/tunnel/heartbeat`. InkWall's Windows and Linux launchers do this automatically after the server reachability probe succeeds.

## Extraction

Send a JSON output contract to a route backed by NuExtract3 or another structured model:

```bash
contextbridge submit --file examples/extract.json
```

Required keys are validated after inference. Text found in the submitted content or image remains untrusted data and cannot replace the operator prompt.

## Embeddings And RAG

```bash
contextbridge submit --file examples/embedding.json
contextbridge submit --file examples/rag-ingest.json
contextbridge submit --file examples/rag-query.json
```

Use a stable `tenant_id` for each customer or security boundary. The included local backend is deliberately simple. Replace the `vectorstore.Store` implementation for large ANN indexes, replication, or distributed tenancy.

## Website Or API Worker

A website worker should:

1. load the next pending record using its existing database account
2. map it into the ContextBridge job envelope
3. submit it with a short timeout greater than the route timeout
4. persist the decision and provider metadata
5. retry transport failures without converting them to approval

## InkWall

ContextBridge includes an adapter for InkWall review directories:

```bash
contextbridge review --config /path/to/config.yml --job-dir /path/to/inkwall-job
```

It reads `payload.json`, optional text fallbacks, and an optional image, then emits one normalized decision on stdout.

The recommended InkWall transport keeps the PHP receiver on the private computer, sends encrypted and authenticated payloads through an SSH reverse tunnel, and invokes ContextBridge locally. The VPS remains thin and stores no GGUF models.
