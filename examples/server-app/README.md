<!-- SPDX-License-Identifier: Apache-2.0 -->

# Minimal server-side app clients

Create one scoped producer credential on the relay host without printing the
secret:

```sh
contextbridge integrate relay \
  --subject demo-server-app \
  --lifetime-hours 720 \
  --max-queued-jobs 4 \
  --max-jobs-per-hour 60 \
  --write-env ./contextbridge-producer.env
```

Transfer that file through a secure channel to the server-side application
host and load its two environment variables. Do not put it in a web root,
browser bundle, mobile application, log or repository.

The examples need only Python 3 or Node.js 18+:

```sh
python examples/server-app/python_submit.py
node examples/server-app/javascript-submit.mjs
```

Both examples use one stable idempotency key for one logical submission,
retain the returned job ID, poll a bounded authenticated result, and distinguish
terminal failure from completion. They reject plain HTTP except on loopback.
They deliberately do not implement E2EE or incremental event watching.

## Read-only custom UI

Create an observer credential on the relay host:

```sh
contextbridge integrate ui \
  --subject demo-dashboard \
  --write-env ./contextbridge-ui.env
```

Load the generated environment file into a trusted Node.js process, then run:

```sh
node examples/server-app/javascript-observe.mjs
```

[`contextbridge-ui-client.mjs`](contextbridge-ui-client.mjs) exports a reusable
`ContextBridgeUIClient`. Its `snapshot()` method concurrently reads the
protocol, pool overview, nodes, bounded job history and pipelines; separate
methods expose exact jobs, timing estimates, activity and cursor-based events.
It performs no mutations and rejects non-HTTPS remote relay URLs, oversized
responses, invalid JSON and unsafe identifiers.

An observer token belongs in a private backend environment or trusted desktop
secret store. Never import it into JavaScript delivered to an untrusted web
browser. Public web frontends should call their own authenticated backend,
which can filter the read-only CB data further for the logged-in user.
