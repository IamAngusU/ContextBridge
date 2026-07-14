# ContextBridge

ContextBridge moves structured jobs from your own apps, folders, websites, databases, or SSH workflows into a local model or one browser tab you explicitly choose. It returns validated JSON instead of blindly executing model output.

It started as the private review bridge for [InkWall](https://github.com/IamAngusU/InkWall), but the protocol is application-neutral.

## What it supports

- One static executable for Windows, Linux, and macOS
- YAML routes and browser selector profiles
- Ollama text and vision models
- Chrome and Edge extension for a selected AI tab
- Authenticated local HTTP API
- Folder inbox with neighboring result files
- Ordered provider fallback, such as Ollama first and browser second
- Strict `allow` or `review` normalization for moderation jobs
- InkWall job-folder adapter

## Quick start

Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/IamAngusU/ContextBridge/main/install.ps1 | iex
```

Linux or macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/IamAngusU/ContextBridge/main/install.sh | sh
```

Or download a release, then initialize and run it:

```bash
contextbridge init
contextbridge serve
```

The generated config is stored in `%LOCALAPPDATA%\ContextBridge\config.yml` on Windows and `~/.config/contextbridge/config.yml` on Linux and macOS.

## Ollama

Install Ollama, pull the model from your YAML, then keep ContextBridge running:

```bash
ollama pull gemma3:4b
contextbridge serve
```

The default route uses Ollama and hands the job to the paired browser tab only when Ollama is unavailable.

## Browser tab

1. Open `chrome://extensions` or `edge://extensions`.
2. Enable developer mode and load the unpacked `extension` folder from the ContextBridge installation.
3. Open the AI page you want to use.
4. Open the ContextBridge extension, enter the local token from `config.yml`, select the tab and profile, then choose **Pair selected tab**.

Only that origin is requested. Add or adjust a profile in YAML when an AI website changes its HTML.

## Send a job

```bash
contextbridge submit --file examples/job.json
```

You can also write a JSON job into the configured inbox folder or call `POST /v1/jobs`. See [the protocol](docs/protocol.md).

InkWall uses:

```bash
contextbridge review --config /path/to/config.yml --job-dir /path/to/inkwall-job
```

## Configuration

Start from [config.example.yml](config.example.yml). A route chooses the primary provider, ordered fallbacks, timeout, and browser profile. Browser profiles define the allowed URL and selectors for input, image upload, submit, and response elements.

## Boundaries

ContextBridge is an automation transport, not a permission to bypass a provider's rules. Use it only with accounts and services you are allowed to automate. Browser selectors are intentionally local configuration, because providers change independently and different users need different pages.

Read [the security model](docs/security.md) before exposing any source beyond localhost.

## Development

```bash
go test ./...
go build ./cmd/contextbridge
```

MIT licensed.
