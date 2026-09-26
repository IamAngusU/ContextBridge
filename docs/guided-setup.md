# Guided CLI setup

`contextbridge guide` is the human-facing path for choosing what an installed
device should do:

```sh
contextbridge guide
```

The guide can configure the device as local-only, a sender/client, a relay, a
worker, or both relay and worker. It resolves each value in this order:

1. an explicit flag;
2. the existing private configuration;
3. a supported environment variable;
4. a safe derived/default value;
5. a bounded prompt when a required value is still missing.

The central role is always a human choice when the root guide is used. Before
writing, CB prints a redacted summary with the source of each value and asks for
confirmation. Declining, EOF, invalid input exhaustion, or an interrupted
terminal leaves the configuration unchanged.

Supported setup environment variables are:

- `CONTEXTBRIDGE_CONFIG` for the configuration path;
- `CONTEXTBRIDGE_RELAY_URL` for a worker/client relay;
- `CONTEXTBRIDGE_PUBLIC_URL` for a relay's public HTTPS origin;
- `CONTEXTBRIDGE_NODE_NAME` for a worker display name.

URLs are validated before the confirmation. The summary prints only their
origin, not credentials, query parameters, fragments, or path components.

## Automation remains deterministic

The guide refuses to run unless both input and output are attached to a real
terminal. It never prompts through pipes, redirected output, CI, MCP, a service,
or another machine-facing interface.

Use explicit flags in those environments:

```sh
contextbridge cluster configure \
  --mode worker \
  --relay-url https://relay.example.net \
  --name build-node-1
```

An existing command can opt into the same bounded completion flow with
`contextbridge cluster configure --interactive ...`. Fully specified commands
without `--interactive` preserve their existing behavior and output contract.

Pairing has the same opt-in human path:

```sh
contextbridge pair --interactive
```

It resolves the relay, identity path, and node name from explicit flags,
configuration, supported environment variables, and safe local defaults. Only
the still-missing relay URL is requested. Before contacting the relay or
writing an identity, CB shows the relay origin (never URL credentials, path,
query, or fragment), node, identity path, and configured group count, then
asks for confirmation. A declined or interrupted flow sends no pairing request
and creates no identity. If the selected identity file already exists, the
summary says so explicitly; it is replaced only after the relay approves the
new pairing.

The guide does not invent or relax credentials, pairing approval, E2EE
reservations, policy boundaries, idempotency keys, destructive confirmations,
or agent approval digests. Secure LAN joining still uses a trusted join bundle
and explicit pairing; discovery never becomes trust.
