# PHP client for shared hosting

The dependency-free example in [`examples/php`](../examples/php) lets a PHP
8.1+ backend submit and observe jobs through an HTTPS ContextBridge relay. It
is intentionally a small native-protocol client, not a generated SDK.

Use a dedicated producer token with the narrowest group scope your application
needs. Keep it in the hosting provider's secret/environment store—never in
browser JavaScript, HTML, a mobile application, a public repository, or an
error page. Mobile and browser applications should call their own backend,
which validates the user and then invokes ContextBridge.

```bash
export CONTEXTBRIDGE_RELAY_URL='https://relay.example.com'
export CONTEXTBRIDGE_PRODUCER_TOKEN='cb_producer_...'
export CONTEXTBRIDGE_IDEMPOTENCY_KEY='customer-42:operation-193:v1'
php examples/php/submit-demo.php
```

Change the demo's route, provider, model, prompt, and output contract to match
your configured pool. The example targets the measured `modelkit` route; a
different installation may use `default`, `browser`, or another reviewed
route.

## Failure rules

- `submit()` requires an `Idempotency-Key` and sends exactly one POST. If the
  response is lost, inspect by the known job ID when available or repeat the
  exact request with the exact same key. Changed content with that key is a
  conflict.
- safe job reads retry only a small set of transient transport/HTTP failures,
  with bounded exponential backoff.
- `wait()` polls an existing job; it never resubmits it.
- `cancel()` sends once. Cancellation makes the relay record terminal but
  cannot prove that an external provider did not already accept the request.
- `saveArtifacts()` writes only embedded bytes, verifies declared size and
  SHA-256, removes path traversal, refuses overwrites, and never downloads an
  URL-only reference.
- the example accepts remote relays only over HTTPS. Plain HTTP is limited to
  loopback for local development.

The relay's 12 MiB cleartext job and 24 MiB result-envelope bounds still
apply. E2EE reservations, progressive output, pipelines, and browser media
workflows remain available through the native CLI/API but are deliberately
outside this compact example.
