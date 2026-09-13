<p align="center">
  <img src="extension/assets/contextbridge-mark.svg" width="104" height="104" alt="ContextBridge logo">
</p>

<h1 align="center">ContextBridge</h1>

<p align="center"><strong>Route AI jobs across your apps, private computers, local models, and explicitly taught browser tabs.</strong></p>

<p align="center">
  <a href="https://github.com/IamAngusU/ContextBridge/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/IamAngusU/ContextBridge?display_name=tag&sort=semver&style=flat-square&color=2a9d8f"></a>
  <a href="LICENSE"><img alt="MIT license" src="https://img.shields.io/badge/license-MIT-20231f?style=flat-square"></a>
  <img alt="Go 1.25" src="https://img.shields.io/badge/Go-1.25-00ADD8?style=flat-square&logo=go&logoColor=white">
  <img alt="Windows, Linux, and macOS" src="https://img.shields.io/badge/platform-Windows%20%7C%20Linux%20%7C%20macOS-59636e?style=flat-square">
  <img alt="Chrome, Edge, Opera, and Firefox" src="https://img.shields.io/badge/browsers-Chromium%20%7C%20Firefox-d7422f?style=flat-square">
  <a href="https://github.com/IamAngusU/ContextBridge/stargazers"><img alt="GitHub stars" src="https://img.shields.io/github/stars/IamAngusU/ContextBridge?style=flat-square&color=d9a62e"></a>
</p>

ContextBridge is a local-first runtime and decentralized compute router for structured AI jobs. A source can be a folder, website, database worker, command pipeline, private server, or any application that can send JSON. A destination can be one or many private computers running Ollama, a managed `llama.cpp` engine, or an AI page open in a browser tab.

ChatGPT and Gemini are detected automatically from stable DOM and accessibility anchors. For another web chat, choose a tab and visually teach its prompt field, send control, answer area, and optional image upload. Learned overrides stay in extension storage and can be replaced at any time.

![ContextBridge local dashboard](docs/assets/contextbridge-dashboard.png)

## Why ContextBridge

- **Visual browser teaching:** point at page controls instead of reverse engineering selectors.
- **Progressive browser output:** follow a web-chat answer while it is generated, through the worker and relay, with `cluster submit --stream`.
- **Stateful browser conversations:** pin related turns to one browser worker with `session_id`, or use the streaming `cluster chat` terminal.
- **Parallel web-chat pool:** select several ChatGPT and Gemini tabs in one extension; each becomes an independent serial UI slot. Browser sessions are bound to separate conversations, including when two producers choose the same public `session_id`. A missing ID uses one producer-scoped default session.
- **Provider-aware recovery:** progress percentages, stop/send controls, rate limits, errors, accidental reloads, and stalled image generation are separate states rather than answer text.
- **Images and files back:** opt in to bounded response artifacts, verify their SHA-256, and save them with `--artifacts`.
- **1:1, 1:N, and N:N compute:** connect one app to one worker, distribute one queue across many workers, or share a capability-aware node pool between producers.
- **Outbound worker connections:** workers join through WebSockets without router port forwarding or fixed public worker ports.
- **Durable scheduling:** priority queue, history, bounded retries, disconnect recovery, groups, tags, task requirements, and VRAM-aware placement survive relay restarts.
- **Optional E2EE jobs:** a producer can seal a payload for the selected worker with X25519 and AES-256-GCM so the relay cannot read the payload or result.
- **Bounded model pipelines:** chain extraction, embeddings, retrieval, vision, and generation with fixed steps and explicit loop limits.
- **Explicit scope:** only the chosen page origin and local service are requested.
- **Local models:** route text and images to Ollama without adding another hosted service.
- **Managed runtimes:** install an official `llama.cpp` release, verify its SHA256, and supervise it on localhost.
- **Hardware awareness:** inspect RAM, VRAM, CUDA, ROCm, Metal, loaded models, and CPU fallback from CLI or dashboard.
- **Extraction and embeddings:** use validated JSON extraction and native batch vector output through one protocol.
- **RAG foundation:** ingest and search tenant-isolated vectors through a replaceable vector-store interface.
- **Structured output:** model output is normalized before it reaches the calling application.
- **Fail-safe moderation:** invalid, missing, or timed-out decisions become `review`, never silent approval.
- **Flexible inputs:** HTTP, stdin, folders, SSH pipelines, database workers, and custom adapters use one protocol.
- **Inspectable operation:** a local dashboard shows routes, hardware, providers, models, latency, flags, jobs, and decisions.
- **Portable deployment:** one executable for Windows, Linux, and macOS on AMD64 and ARM64.

## Quick Start

### Windows

Open PowerShell and run:

```powershell
irm https://angusu.de/contextbridge/install.ps1 | iex
```

### Linux or macOS

```bash
curl -fsSL https://angusu.de/contextbridge/install.sh | sh
```

The installer verifies the matching published release checksum, creates a private config, asks whether this device is local-only, a relay, a worker, or both, sets up user autostart where supported, starts the service, and opens the dashboard. It can use an existing Ollama installation, install a managed `llama.cpp` runtime, or pair a browser tab.

## Automatic Updates

Automatic updates are **off by default**. Opt in from the local dashboard, the browser extension's Connection section, `contextbridge update enable`, or `updates.enabled: true` in config. Supported installers may register an operating-system update job, but while disabled it performs no release check or installation. Once enabled, the Go service checks independently of the UI at the configured interval (24 hours by default); installation waits until the local bridge, worker, and relay have no active jobs. An explicit `updates.enabled: false` is an administrator lock that the UI cannot override. The starter config uses `enabled: null`, which starts off but permits the local toggle. A JavaScript or dashboard failure cannot turn updates on.

```bash
contextbridge update status
contextbridge update check
contextbridge update apply
contextbridge update disable
contextbridge update enable
```

An update is downloaded beside the current executable, checked against both `SHA256SUMS` and GitHub asset digests, and started to verify its reported version and compatibility with the current config. The previous executable remains available for rollback. Windows uses a short-lived local PowerShell helper after the running process exits; it verifies the restarted service's versioned health response and restores the previous binary if that fails. Linux and macOS keep a pending rollback until the updated service reports healthy. A failed version is not retried automatically; a newer release or an explicit `update apply` can be tried. Unpacked browser extensions still need their files refreshed with the installer and then reloaded in the browser; the core updater does not silently replace a live extension.

For a systemd relay whose executable is root-owned but service runs as `contextbridge`, install [`deploy/contextbridge-relay.service`](deploy/contextbridge-relay.service), [`deploy/contextbridge-relay-update.service`](deploy/contextbridge-relay-update.service), and [`deploy/contextbridge-relay-update.timer`](deploy/contextbridge-relay-update.timer). Enable the relay service and update timer. The service uses `CONTEXTBRIDGE_UPDATES_EXTERNAL=1`, so only the root-owned timer performs installations; `updates.enabled: false` still disables them. Each hourly timer run checks whether a release check is due and applies a newer release only when the relay has no queued or active jobs, then restarts and verifies the service with rollback if needed. No GitHub Actions are involved.

The public installer endpoint counts total requests and privacy-preserving unique installer starts. Unique values use an HMAC of a masked network prefix and short user-agent family. Raw IP addresses, cookies, prompts, results, device names, and model activity are not collected. Self-hosted installations do not send runtime telemetry to `angusu.de`.

Manual installation is just as small:

```bash
contextbridge init
contextbridge run
contextbridge doctor
contextbridge dashboard
```

`contextbridge doctor` verifies the config, running local service, token, default
route, relay, and worker identity. It prints a concrete fix for every blocking
check; use `--json` in installers and monitoring.

## Build A Compute Cluster

The same protocol covers every topology. Workers always connect outward to a TLS relay, report current capabilities and load, and receive only jobs allowed by their token and group policy.

### Relay

```bash
contextbridge cluster configure --mode relay --public-url https://relay.example.com
contextbridge run
contextbridge cluster dashboard
```

Place Caddy, nginx, or another TLS reverse proxy in front of the relay's localhost listener. No worker port needs to be opened in a home router.

Production templates for a hardened systemd service and an nginx path proxy live in [`deploy/`](deploy/). Update the internal port if the installer selected another one.

### Worker

```bash
contextbridge cluster configure --mode worker --relay-url https://relay.example.com
contextbridge pair
contextbridge run
```

The worker prints a short code. An admin approves it in the relay dashboard or with `contextbridge cluster pairing --approve CODE`. The node token and X25519 private key stay in the owner-only identity file.

### Producer

Create a scoped producer token once:

```bash
contextbridge cluster token --role producer --subject support-api
contextbridge cluster submit --file examples/cluster-job.json --token cb_producer_TOKEN
contextbridge cluster submit --file examples/cluster-job.json --token cb_producer_TOKEN --e2ee
contextbridge cluster chat --token cb_producer_TOKEN
contextbridge cluster chat --token cb_producer_TOKEN --e2ee
```

Set requirements such as `task`, `provider`, `group`, `model`, `vision`, `embedding`, tags, or minimum free VRAM. Use `provider: browser` to require a live, taught web-chat tab, or `provider: ollama` to require a local Ollama engine. The scheduler chooses a compatible online node using live concurrency, queue, RAM, and VRAM data. Measured VRAM demand is a preference, not a hidden requirement, so CPU-only workers remain useful unless `min_free_vram_bytes` is explicitly set. Normal TLS jobs can move to another node after a disconnect. E2EE jobs are bound to the worker key selected during reservation and fail clearly if that worker disappears.

Use the same non-secret `requirements.session_id` for follow-up turns. The relay keeps that producer's conversation on the most recently used compatible worker, while retaining failover when the node is offline. The selected ChatGPT or Gemini tab supplies the actual conversation history. The browser extension reserves one conversation for one internal producer-and-session key; a different key never sends into that chat. By default, a new session needs an unassigned attached tab. Choose **Open a new chat tab automatically** in the extension to create a fresh chat per session, or set `metadata.contextbridge_new_chat: true` on an individual browser job. In manual mode, navigate an attached tab to a new empty chat and click **Use this page for a new session**; returning to the exact saved URL resumes an earlier session. Closing a tab parks its known conversation until reopened and attached. Old tabs whose pre-0.5.20 session histories may have mixed are quarantined until the user opens a new empty chat and explicitly releases them. These are conversation-routing boundaries, not separate browser accounts or a substitute for the provider's own privacy controls. `contextbridge cluster chat` manages the ID automatically and provides a streaming `you ›` / `ai ›` terminal session. A producer token may be passed with `--token`, stored safely from a credential file with `contextbridge cluster login --token-file producer.json`, or supplied through `CONTEXTBRIDGE_CLUSTER_TOKEN`.

This release uses one durable BoltDB file per relay process. It supports many producers and workers through one relay. Active-active relay replication is a separate deployment tier and requires a shared database and message broker rather than copying the BoltDB file.

A PC can join more than one independent relay with one worker process and identity per relay, with per-worker task/provider/model allow-lists. See [multiple-server setup and its shared-hardware limitation](docs/multiple-servers.md). The server never needs the PC's browser credentials or direct access to its local model files.

Choose a web provider and its UI options for the whole terminal session:

```bash
contextbridge cluster chat --provider browser --profile chatgpt --model gpt-6-astra --reasoning high
contextbridge cluster chat --provider browser --profile gemini
```

Inside interactive chat, `/model …`, `/reasoning …`, `/profile …`, `/image on|off`, `/min-images 0…12`, `/music on|off`, `/min-artifacts 0…12`, and `/e2ee on|off` change subsequent turns; `/settings` shows the active choices. E2EE encrypts prompts and final results for one reserved worker; plaintext streaming and transparent failover are intentionally unavailable for that turn. The same `model`, `reasoning`, `browser_profile`, and `session_id` fields can be placed in an individual browser job payload. Choices are matched against the provider's visible localized menu; use the full scanned label (for example, `3.1 Pro` rather than `Pro`). Normal spaces are accepted where a browser label uses non-breaking spaces. An unavailable choice fails clearly instead of silently running a different model.

## Connect A Browser Tab

![ContextBridge visual teaching overlay](docs/assets/visual-teaching.png)

1. Load `extension/chromium` in Chrome, Edge, Opera, Brave, or Vivaldi. Use `extension/firefox` for Firefox.
2. Open the extension on a ChatGPT or Gemini page. It recognizes the provider automatically. Click **Connect this AI page**: with no tabs attached, this explicitly attaches the current page and starts the browser bridge in one flow. To prepare several tabs first, click **Attach this page** on each; the same button becomes **Detach this page**. Existing conversations are never attached merely by viewing them.
3. The local service and saved pairing token are checked during Connect; no separate profile or service test is required. First-time setup may ask you to paste the local pairing token once under **Advanced setup and diagnostics**. Multi-tab filters, fresh-chat auto-attach, profile teaching, and model scanning remain available under Advanced. Each attached tab is one serial browser slot, and detaching prevents future jobs but cannot unsend website work already in progress.
4. For another provider, choose **Customize detection** and click the requested controls directly in the page.

Add `--stream` to `contextbridge cluster submit` to print progressive browser text while the final normalized result remains on stdout. Plaintext progress is deliberately disabled for E2EE jobs.

Set `output.artifacts: true` on a browser text/JSON job to collect up to twelve generated images, media, download links, or code files from the newly completed response. Embedded files share a bounded `max_artifact_bytes` budget (12 MiB maximum), are normalized, and SHA-256 verified. Save them with `contextbridge cluster submit --artifacts ./downloads ...`; provider-hosted resources that the browser cannot read remain HTTPS references in the JSON result. For image generation, run `contextbridge cluster chat --image --artifacts ./downloads --prompt "Create one image of ..."`. This asks through the prompt and requires a transferred image file; it does not change the web chat's tool selection. In a JSON browser job, set `output.min_images: 1` (or more for a series). The optional `metadata.contextbridge_image_tool: true` explicitly selects ChatGPT's visible image tool if desired. For Gemini's visible Music tool, use `contextbridge cluster chat --music --profile gemini --artifacts ./downloads --prompt "Create a short instrumental ..."`; the returned playable file may be MP4 rather than a standalone audio file. JSON jobs can set `metadata.contextbridge_music_tool: true` with `output.min_media: 1`. `output.min_artifacts` and `--min-artifacts` require files of any supported type. A text claim, a code block, a mislabeled file, or an HTTPS link without transferred bytes cannot satisfy `min_images` or `min_media`. The interactive chat saves artifacts automatically under local ContextBridge storage unless `--artifacts off` is used. Use `--attach-image ./picture.png` to send a local picture along with the prompt. The selected web chat must actually provide the requested feature; ContextBridge cannot create media when its provider says that feature is unavailable. See [`browser-image-job.json`](examples/browser-image-job.json) and [`browser-music-job.json`](examples/browser-music-job.json).

For local selector troubleshooting, run `contextbridge browser inspect --config ./config.yml`. The paired extension reports a small, read-only DOM snapshot for each attached tab: prompt and send controls, whether a draft exists and its length, file inputs (including hidden ones), selected tool controls, response-image counts, image progress, and a whitelisted last-failure reason. It does **not** provide full HTML, prompt values, chat text, file contents, or cookies. With multiple tabs, specify `--tab ID`; the snapshot is refreshed roughly every five seconds without reloading any page and is not sent to the relay. Gemini's choices come from its live, lazily rendered mode picker, not a hard-coded global list. Automatic discovery opens and closes that picker only on an idle attached tab with an empty composer, at most once per 30 minutes; the scan button remains available for immediate refresh.

Gemini **video generation is not yet tested**. The live Music run produced an MP4 with a real audio track, but that is not evidence that Gemini's separate video-generation workflow works. We deliberately have not spent paid credits on that test, so ContextBridge makes no verified video-generation claim yet.

See [known browser edge cases](docs/known-browser-edge-cases.md) for observed provider states, recovery boundaries, and cases still awaiting a safe reproduction.
Cross-browser control and a shared multi-relay hardware budget are proposed in the [interoperability roadmap](docs/roadmap.md), not presented as finished features.

Under **Manage other tabs**, the popup lists attached and unattached tabs across all windows **of that browser**, with an All/Attached/Not attached filter and near-live Working/Cooling down states. It updates without reloading provider pages. Each selected tab is one serial UI slot; one extension can run several independent tabs concurrently, including a mix of ChatGPT and Gemini. Add PCs to grow the same pool. Rate-limited tabs enter a five-minute cooldown and their error text is logged but never returned as successful model output. Local Ollama/llama.cpp jobs can use higher worker concurrency and continue to participate without a GPU; a busy browser tab no longer lowers their worker-wide job limit. GPU headroom only influences placement unless a job explicitly requires VRAM. Different browser installations do not yet share a single extension popup or remotely detach each other's tabs.

The Chromium package works with Manifest V3 browsers. The Firefox package has its own background manifest and localhost policy. Local Firefox development installs use `about:debugging`; a permanent consumer install requires a Mozilla-signed package.

See [Visual browser teaching](docs/browser-teaching.md) for exact browser steps and profile behavior.

## Use Ollama

Install Ollama and start ContextBridge:

```bash
contextbridge run
```

With `model: auto`, ContextBridge selects the smallest compatible local model. Image jobs prefer a vision-capable model and embedding jobs require an embedding-capable model. An explicit model name always wins. The default route tries Ollama first and uses the paired browser only when Ollama is unavailable.

Inventory an existing local setup without moving or loading anything:

```bash
contextbridge models
contextbridge models --path D:\AI\models --path E:\shared-models
contextbridge models --json
```

The inventory merges reachable Ollama models with GGUF, ONNX, and SafeTensors files and reports ready/loaded state, likely text/vision/embedding support, parameters, quantization, size, VRAM, and estimated RAM. Declared-only catalog entries remain available with `contextbridge models --discover=false`.

## Use Managed GGUF Models

ContextBridge can install a current official `llama.cpp` binary for the detected platform. The GitHub release digest is verified before the archive is extracted.

```bash
contextbridge runtime install llama.cpp
contextbridge pull jina-v4-retrieval
contextbridge pull nuextract3
```

Set the selected engine to `auto_start: true`. `gpu: prefer` requests full GPU offload and visibly falls back to CPU if startup fails. `gpu: require` reports an error instead. `gpu: off` stays on CPU.

The built-in manifests use [Jina v4 retrieval](https://huggingface.co/jinaai/jina-embeddings-v4-text-retrieval-GGUF) with required mean pooling and [NuExtract3](https://huggingface.co/numind/NuExtract3-GGUF) with its multimodal projector. Model downloads resume from partial files and are checked against Hugging Face LFS SHA256 metadata. Runtime calls follow the official [llama.cpp server API](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md) and [Ollama API](https://github.com/ollama/ollama/blob/main/docs/api.md).

## Send A Job

```bash
contextbridge submit --file examples/job.json
```

Or stream a trusted source through stdin:

```bash
ssh data-host '/opt/export-next-context-job' | contextbridge submit --file -
curl -fsS http://127.0.0.1:9000/next-job | contextbridge submit --file -
```

A folder producer can place the same JSON document in the configured inbox. ContextBridge claims it atomically and writes a neighboring `.result.json` file.

```json
{
  "source": "support-queue",
  "route": "default",
  "kind": "moderation",
  "prompt": "Review this public submission.",
  "text": "Content supplied by the user"
}
```

See the [job protocol](docs/protocol.md) and [integration recipes](docs/integrations.md).

Cluster jobs wrap that local job in routing requirements. See [examples/cluster-job.json](examples/cluster-job.json).

Choose an explicit output contract per job:

| Mode | Result | Typical use |
| --- | --- | --- |
| `decision` | Strict `allow` or `review` object | Moderation and approval gates |
| `json` | Parsed JSON with optional required keys | Extraction, classification, enrichment |
| `text` | Bounded plain text | Summaries, drafts, and transformations |
| `embedding` | Native `[][]float32` with dimensions | Search, clustering, and RAG |
| `rag` | Indexed count or ranked document matches | Tenant-isolated retrieval |

ContextBridge treats every result as data. It never evaluates returned code or runs returned commands.

## Configuration

Generated config locations:

| Platform | Default path |
| --- | --- |
| Windows | `%LOCALAPPDATA%\ContextBridge\config.yml` |
| Linux | `~/.config/contextbridge/config.yml` |
| macOS | `~/.config/contextbridge/config.yml` |

Start from [config.example.yml](config.example.yml). A route declares a primary provider, ordered fallbacks, timeout, and optional browser profile. Visual profiles live in extension storage; YAML profiles remain available for audited and reproducible deployments.

```yaml
routes:
  default:
    provider: ollama
    fallback: [browser]
    timeout_seconds: 180
    browser_profile: ""
  embedding:
    provider: jina
    fallback: [ollama]
    task: embedding

engines:
  jina:
    type: llama_cpp
    model: jina-v4-retrieval
    listen: 127.0.0.1:32147
    auto_start: true
    gpu: prefer
    mode: embedding
    pooling: mean
```

Leave `browser_profile` empty for the profile taught in the extension. Set it to a YAML profile name when the server must require a reviewed selector set.

## Architecture

```mermaid
flowchart LR
  A[Apps and producers] -->|Authenticated job| R[Durable relay]
  W1[Private worker 1] -->|Outbound WSS| R
  W2[Private worker 2] -->|Outbound WSS| R
  R -->|Capability routing| W1
  R -->|Load balancing| W2
  W1 --> O[Ollama or llama.cpp]
  W2 --> B[Explicit browser tab]
  O --> R
  B --> R
  R --> A
```

ContextBridge never executes commands returned by a model. Source credentials stay with the source process. Browser page content is treated as untrusted data and output is reduced to the configured decision vocabulary.

Read [Architecture](docs/architecture.md) and [Security](docs/security.md) before connecting a remote source.

## Repository Layout

| Path | Purpose |
| --- | --- |
| `cmd/contextbridge` | Cross-platform CLI and service entry point |
| `internal/bridge` | Routing, providers, local API, storage, and dashboard |
| `internal/cluster` | Pairing, E2EE, durable queue, scheduler, relay, workers, pipelines, and cluster dashboard |
| `internal/config` | YAML parsing, defaults, and validation |
| `internal/modelregistry` | Verified GGUF model downloads and local registry |
| `internal/systeminfo` | Cross-platform hardware and backend telemetry |
| `internal/vectorstore` | Replaceable RAG storage contract and local backend |
| `extension/src` | Shared browser extension source |
| `extension/chromium` | Ready-to-load Chromium package |
| `extension/firefox` | Ready-to-load Firefox package |
| `scripts` | Reproducible extension and release checks |
| `deploy` | Hardened systemd and nginx relay templates |
| `web` | Public one-file welcome page, cached repository stats, and installer counters |
| `docs` | Protocol, architecture, security, and integrations |

## Development

```bash
go test ./...
go vet ./...
./scripts/package-extensions.sh
./scripts/verify-extensions.sh
```

Releases do not depend on GitHub Actions. From PowerShell, build every supported
platform bundle, both extension archives, and `SHA256SUMS` locally:

```powershell
.\scripts\build-release.ps1 -Version v0.4.0
```

Contributions are welcome. Start with [CONTRIBUTING.md](CONTRIBUTING.md), use a focused issue for behavior changes, and include tests for routing or protocol work.

## Project Status

ContextBridge is usable today for local workflows and single-relay private compute clusters. Browser pages can change their HTML without notice, so visual profiles are testable and automation failures return `review`. Active-active relay HA and signed browser-store distribution remain future deployment tiers.

MIT licensed. Built and maintained by [Angus Uelsmann](https://github.com/IamAngusU).
