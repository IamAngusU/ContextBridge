# Pool self-test

`cb selftest` is the operator-safe way to prove that a
producer can see the pool before sending work. It runs identically on the
worker PC, a VPS relay, or another configured producer. The long form
`contextbridge cluster selftest` is identical.

## Safe default: readiness only

```bash
cb selftest --config /var/lib/contextbridge/config.yml
```

The command reads live relay/node capabilities and waits for all of these:

- an online Ollama generation model;
- an attached, idle ChatGPT tab;
- an attached, idle Gemini tab;
- free worker capacity.

It reports exactly what is missing while it waits. The default timeout is five
minutes, Ctrl+C cancels immediately, and **no AI prompt is sent by default**.
Use `--providers local`, `--providers chatgpt,gemini`, or a similar subset when
the demonstration deliberately needs fewer targets.

## One-command live text proof

```bash
cb selftest --config /var/lib/contextbridge/config.yml --run
```

`--run` is the explicit provider-cost/rate-limit opt-in. After readiness passes,
the command sends one bounded exact-marker request to each selected target and
verifies the returned text. Every browser request carries the fresh-chat and
fresh-chat-per-job markers. The extension must therefore create an isolated
ContextBridge-owned chat; the self-test never edits an existing personal
conversation. Each result reports wall time and, when the worker supplied
them, queue and compute time; the final line reports total elapsed time.

To test only local compute without contacting a browser provider:

```bash
cb selftest --providers local --run
```

The smallest compatible loaded Ollama generation model is preferred. If none
is loaded, the smallest compatible available model is selected and may load on
demand. Pin an intentional model with `--local-model MODEL`.

## Explicit image proof

Image generation is separate because it can consume more time, quota, or paid
credits:

```bash
mkdir -p /tmp/contextbridge-selftest-artifacts
cb selftest --run --image \
  --artifacts /tmp/contextbridge-selftest-artifacts
```

This adds one ChatGPT request, requires a transferred image with real bytes,
checks the normal artifact contract, and saves it with an exclusive filename.
Existing files are never overwritten. Without `--artifacts`, an isolated
temporary directory is used and removed after verification; add
`--keep-artifacts` to retain that automatically created directory.

Useful controls:

```text
--dry-run                 State readiness intent explicitly; identical to omitting --run
--timeout 10m             Bound how long the operator waits (1s through 30m)
--job-timeout 10m         Bound each opted-in live check separately (5s through 30m)
--poll 2s                 Set status refresh rate (250ms through 30s)
--providers LIST          local, chatgpt, gemini, or a comma-separated subset
--local-model MODEL       Require one named local generation model
--run                     Send fixed, bounded text checks
--image                   Add one real ChatGPT image check; requires --run
--artifacts DIR           Save verified image bytes in this directory
--keep-artifacts          Keep the otherwise temporary image directory
```

The first implementation intentionally does not test music/video, mutate
provider settings, reuse chats, submit user-defined prompts, retry ambiguous
executions, or attach tabs automatically. Those actions need separate explicit
operator intent. A readiness timeout leaves the pool unchanged and names the
next action: start a worker/local engine, attach the requested tab, connect the
extension, wait out a provider cooldown, or narrow `--providers`.

## Deutscher Kurzablauf

Nur prüfen und warten, ohne einen Prompt zu senden:

```bash
cb selftest --config /var/lib/contextbridge/config.yml
```

Danach den isolierten Texttest bewusst starten:

```bash
cb selftest --config /var/lib/contextbridge/config.yml --run
```

Für eine reine lokale Probe ohne ChatGPT/Gemini-Kontingent:

```bash
cb selftest --providers local --run
```

Die Browserproben verlangen je Auftrag einen neuen ContextBridge-Chat. Fehlt ein
passender angehängter Tab, wartet der Befehl begrenzt und erklärt, was der
Bediener verbinden muss. Persönliche Unterhaltungen werden nicht umgeschrieben.
