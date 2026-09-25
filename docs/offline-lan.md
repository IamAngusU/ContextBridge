# Secure offline LAN pools

ContextBridge needs a network only when its components are on different
machines. It does not require a ContextBridge cloud account or an Internet
connection to route work between an installed relay, installed workers and
already-present local runtimes/models.

```text
cluster CLI / native client -> pinned LAN relay -> local workers
                                             \-> Ollama / llama.cpp

OpenAI-compatible app -> localhost ContextBridge -> locally configured routes
```

This is different from a deployment whose only relay is on a public VPS. If
the WAN disappears, that remote relay is unreachable. Put the relay on the LAN
when the local pool must survive loss of Internet connectivity.

## Initialize the relay

On the machine that should coordinate the LAN pool:

```sh
contextbridge cluster lan init
contextbridge run
```

If the machine has several private/VPN interfaces, CB refuses to guess which
identity to publish and lists the candidates. Select the address explicitly:

```sh
contextbridge cluster lan init --advertise-host 192.168.1.20
```

The command creates:

- a persistent Ed25519 TLS leaf identity owned by the relay;
- an HTTPS LAN listener (default port `32151`);
- `contextbridge-lan-join.json`, containing only the relay URL, public
  certificate and SHA-256 SPKI fingerprint.

The private key never enters the join bundle. The bundle is not a login token,
but its integrity is critical: transfer it over a trusted local channel such
as a directly controlled USB drive or an authenticated file share. Do not
replace the bundle with data learned only from mDNS, a QR code on an unknown
page, or an unauthenticated download.

The ordinary loopback relay listener stays enabled for administration on the
relay host. CB does not expose the local bridge bearer token to the LAN.

## Join a worker

Copy the join bundle to the other already-installed machine, then run:

```sh
contextbridge cluster lan join --bundle ./contextbridge-lan-join.json --name laptop
```

The worker prints a short-lived pairing code. On the relay host, compare and
approve that exact code:

```sh
contextbridge cluster pairing
contextbridge cluster pairing --approve PAIRING-CODE
```

After approval, restart/start ContextBridge on the worker. Its identity now
contains the relay certificate and SPKI pin. HTTP pairing, WebSocket worker
traffic and later CLI calls fail closed if a different certificate is
presented—even when that certificate is otherwise trusted by the operating
system.

Check the local trust state with:

```sh
contextbridge cluster lan status
contextbridge doctor
contextbridge cluster status
```

To stop contributing compute while retaining the pinned identity and sender
ability, stop CB and switch the device to client mode. Switching back to
worker mode for the same relay reuses the saved identity:

```sh
contextbridge stop
contextbridge cluster configure --mode client
contextbridge run
```

## What remains available without WAN

When configured entirely on the LAN, these paths do not require Internet:

- durable job admission, scheduling, events, receipts and policies;
- worker pairing/reconnection and E2EE payloads;
- already-installed Ollama, llama.cpp and local adapter/resource-pack routes;
- local files, artifacts and local RAG data;
- OpenAI-compatible applications talking to the loopback CB endpoint for
  resources configured on that local service.

Cloud APIs, GitHub updates, package/model downloads, public DNS and a remote
VPS relay naturally remain unavailable without WAN. Their failure must not be
interpreted as failure of an otherwise healthy local pool.

Runtime offline support is not the same as offline installation. Convenience
installers fetch release assets; prepare the signed/checksummed release,
runtime and model files in advance for an air-gapped installation.

## Security and current limitations

- LAN transport is always TLS. CB does not permit generic cleartext
  `http://192.168.x.x` relay URLs.
- Discovery is not implemented yet and will never be an authorization source.
- Native cluster CLI/API clients use the saved pin. A transparent localhost
  OpenAI-to-cluster gateway for generic third-party apps is not implemented
  yet; do not point such apps at the self-signed LAN listener.
- The join bundle is an explicit trust bootstrap, not a secret capability.
- A changed relay key is rejected. Automatic key rotation is intentionally
  absent until a separately authenticated rotation workflow exists.
- The generated certificate is valid for the advertised host. Changing the
  IP/DNS name requires an explicit identity/rotation decision; CB does not
  silently mint a replacement.
- A local firewall must permit the configured LAN TLS port on the intended
  private network.

The protocol advertises this boundary as
`secure_offline_lan_pinning_v1`. That feature proves support for explicit
pinned LAN transport; it is not a claim that mDNS discovery, key rotation or a
multi-relay HA topology exists.
