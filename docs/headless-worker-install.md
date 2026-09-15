# Unattended Linux or macOS worker install

The Unix installer can configure a worker without reading from the terminal. Set every choice explicitly:

```sh
curl -fsSL https://angusu.de/contextbridge/install.sh | \
  CONTEXTBRIDGE_PROVIDER=later \
  CONTEXTBRIDGE_CLUSTER_MODE=worker \
  CONTEXTBRIDGE_RELAY_URL=https://relay.example.com \
  CONTEXTBRIDGE_WORKER_NAME=render-node-01 \
  CONTEXTBRIDGE_NONINTERACTIVE=1 \
  CONTEXTBRIDGE_NO_DASHBOARD=1 \
  sh
```

`CONTEXTBRIDGE_NONINTERACTIVE=1` disables every `/dev/tty` question. `CONTEXTBRIDGE_RELAY_URL` is required in worker mode and must be HTTPS, except for a relay on localhost. `CONTEXTBRIDGE_WORKER_NAME` is optional and defaults to the host name. `CONTEXTBRIDGE_PROVIDER` can be `ollama`, `managed`, `browser`, or `later`; `later` avoids downloading a model during installation. `CONTEXTBRIDGE_HOME`, `CONTEXTBRIDGE_BIN_DIR`, and `CONTEXTBRIDGE_CONFIG` can override the installation and config paths. Relay or combined relay/worker installs may set `CONTEXTBRIDGE_PUBLIC_URL` instead of answering the reverse-proxy URL question.

Pairing is intentionally not bypassed. The worker prints a short-lived device code and waits without reading stdin. Approve that code on the relay with an administrator credential:

```sh
contextbridge cluster pairing --approve CODE --config /path/to/relay-config.yml
```

This keeps the relay administrator token off the worker. After approval, the installer stores the worker identity with owner-only permissions and enables the user service when the platform supports it.
