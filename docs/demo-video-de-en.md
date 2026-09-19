# ContextBridge: short video script (DE / EN)

This is the **edited roughly 3-minute video**, not a claim that all provider jobs finish within fixed seconds. It requires **ContextBridge 0.5.80 or newer** on the VPS and worker, plus a compatible 0.5.80-or-newer extension. Keep the terminal's real elapsed times visible; cut waiting footage honestly. The full [technical runbook](demo-de-en.md) retains identity research, relativity, safety notes, and troubleshooting. The story starts with **a local Ollama model controlled from the VPS**, then moves a real image from ChatGPT to Gemini, with an optional local-vision step when enough worker resources are free.

The Windows commands intentionally show the recording PC's custom `C:\ContextBridge\config.yml`. The installer default is `%LOCALAPPDATA%\ContextBridge\config.yml`; default installations can omit `--config` or substitute that path. The visible VPS directory remains exactly `/root/contextbridge-demo`; these commands do not clear it.

## Before recording

- Verify the PC service, VPS relay, and Opera extension are connected and compatible. Leave enough capacity: **at most eight attached tabs before attaching the three fresh demo pages**. Those three pages plus three new-chat jobs then stay below the 16-tab safety limit. Close or detach only finished test chats, never personal or active ones.
- Start Ollama on the Windows PC if it is offline. Check `contextbridge models` locally: this recorded setup uses `qwen2.5:latest` for the reliable text step. The smaller 1.5B candidate was transport-stable but copied only 3/12 exact markers in the wrapped stress check, so it is deliberately not the demo default. The optional local-vision step uses `qwen2.5vl:7b`; include it only if that model is installed and enough RAM/VRAM is free, otherwise skip Shot 5. These names are **demo prerequisites for the steps that use them**, not universal bundled models. A model need not remain loaded in VRAM between jobs. `models` reports provider-verified capability evidence where available, parameter count and quantization—not an intelligence score or proof that every image will be read correctly.
- Use a fresh ChatGPT page and two fresh Gemini pages for the on-camera attach sequence. Keep the browser visible for image creation and upload. Do **not** include Gemini music: the latest test did not return verified media bytes.
- Use one VPS shell throughout the recording; shell variables below must persist. Before recording, prepare both the PC dashboard at `http://127.0.0.1:32145` and the relay cluster dashboard in browser tabs, and authenticate privately. Never show or read out either token. Check that Schedules/Recent activity load locally and that Queue/Jobs load on the relay, then switch back to the console.
- If ChatGPT or Gemini shows a rate-limit dialog, wait for it to clear before recording. Never dismiss a provider error and imply that the job succeeded.

## Shot 1 — Windows console, local models, and attached tabs (about 30–45 s on screen)

On the Windows PC, open a new CMD and start the installer-managed background
service, then attach the terminal view:

```cmd
schtasks /Run /TN ContextBridge
cb console --config "C:\ContextBridge\config.yml"
```

The first command starts the scheduled service (or leaves the already-running
instance alone). The second command is the short alias for `contextbridge
console`: it attaches a view and does not start a second service process. If
the console is faster than Windows service startup, wait one moment and repeat
only the second command. In a second CMD, run:

```cmd
contextbridge models --config "C:\ContextBridge\config.yml"
```

Point to `qwen2.5:latest` (text) and, when you include the optional vision segment, `qwen2.5vl:7b` (text+vision). Show the console status line. In Opera, show **Attach this page** on the fresh ChatGPT and Gemini pages, then click **Connect** once and wait for **Connected**.

**DE:** „Mein Windows-PC ist der Worker. Für den sicheren Basistest nutze ich ein kleines lokales Textmodell; bei freien Ressourcen zeige ich zusätzlich ein eigenes Vision-Modell. ChatGPT- und Gemini-Seiten wähle ich bewusst einzeln aus.“

**EN:** “My Windows PC is the worker. I use a small local text model for the reliable baseline; when resources are free, I also show my own vision model. I explicitly attach the ChatGPT and Gemini pages.”

## Shot 2 — VPS sees the worker (about 10 s)

In **one** VPS shell:

```bash
contextbridge version
contextbridge cluster status --config /var/lib/contextbridge/config.yml
cb selftest --config /var/lib/contextbridge/config.yml
contextbridge cluster status --config /var/lib/contextbridge/config.yml --json |
  python3 -c 'import json,sys; d=json.load(sys.stdin); wanted={"qwen2.5:latest","qwen2.5vl:7b"}; rows=[(n,m) for n in d.get("nodes",[]) for m in n.get("capabilities",{}).get("models",[]) if m.get("provider")=="ollama" and m.get("name") in wanted]; [print("{}: {} [{}]".format(n.get("name","node"),m.get("name","unknown"),"+".join(m.get("tasks",[])))) for n,m in rows]'
DEMO_DIR=/root/contextbridge-demo
mkdir -p "$DEMO_DIR"
DEMO_ID=$(date +%Y%m%d-%H%M%S)
printf 'Files: %s\n' "$DEMO_DIR"
```

Show the PC online, its free slots, providers, and hardware. `cb selftest`
waits until local generation plus one idle attached ChatGPT and Gemini page are
genuinely schedulable; its safe default sends no prompt. Mention the actual
displayed slot count rather than assuming four. Because the compact status can
shorten a long model list, the JSON command reliably prints the two demo models
from the complete node inventory; stop if required `qwen2.5:latest` is absent.
The optional vision model may be absent when Shot 5 is skipped. None of these
commands ranks model quality.

**DE:** „Vom VPS aus sehe ich den verbundenen PC, seine Slots und die installierten Modelle. Jetzt steuere ich zuerst ein lokales Modell – noch ganz ohne Browser-KI.“

**EN:** “From the VPS, I can see the connected PC, its slots, and installed models. First, I will control a local model—without any browser AI.”

## Shot 3 — VPS controls a local Ollama text model

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --provider ollama --model qwen2.5:latest --session "demo-$DEMO_ID-local-text" --artifacts off --prompt 'Antworte exakt mit CB-LOCAL-OK und keinen weiteren Zeichen.'
```

Wait for `CB-LOCAL-OK` and `↳ verwendet: ollama · qwen2.5:latest`. The marker alone is not the proof of execution; the **provider/model/node metadata** in the VPS terminal is. The final live rehearsal finished in **2.3 s**, but keep the actual time on camera. Do not confuse the Windows GPU with hosted ChatGPT/Gemini inference.

**DE:** „Der Befehl kommt vom VPS, aber die Rechnung läuft auf meinem Windows-PC. ContextBridge zeigt mir den tatsächlich verwendeten Anbieter, das Modell und den Worker.“

**EN:** “The command comes from my VPS, but the computation runs on my Windows PC. ContextBridge shows the actual provider, model, and worker.”

## Shot 3B — One job through the durable queue

```bash
QUEUE_JOB=$(mktemp /tmp/contextbridge-demo-queue.XXXXXX.json)
cat > "$QUEUE_JOB" <<JSON
{
  "source": "contextbridge-demo-queue",
  "requirements": {
    "task": "generation",
    "provider": "ollama",
    "model": "qwen2.5:latest",
    "session_id": "demo-$DEMO_ID-queue"
  },
  "payload": {
    "source": "contextbridge-demo-queue",
    "route": "default",
    "task": "generation",
    "prompt": "Antworte exakt mit CB-QUEUE-OK und keinen weiteren Zeichen.",
    "output": {"mode": "text", "max_bytes": 4096}
  },
  "max_attempts": 1
}
JSON
if contextbridge cluster submit --config /var/lib/contextbridge/config.yml --file "$QUEUE_JOB"; then
  rm -f -- "$QUEUE_JOB"
  unset QUEUE_JOB
  printf 'Queue demo completed. The Queued job ID remains visible above.\n'
else
  QUEUE_STATUS=$?
  rm -f -- "$QUEUE_JOB"
  unset QUEUE_JOB
  printf 'STOP: the queue demo failed; do not present it as successful.\n' >&2
  (exit "$QUEUE_STATUS")
fi
```

Keep the printed `Queued:` ID on screen. A free worker may claim the job immediately; the durable enqueue and successful result are the happy path. This block removes only its temporary JSON file and returns nonzero on failure without closing the VPS shell.

**DE:** „Diesmal nutze ich die JSON-Schnittstelle. Der Relay speichert den Auftrag zuerst dauerhaft in der Queue, zeigt seine Job-ID und gibt ihn dann an einen kompatiblen freien Worker.“

**EN:** “This time I use the JSON interface. The relay first stores the job durably in its queue, shows its job ID, and then hands it to a compatible free worker.”

## Shot 4 — Hero flow: ChatGPT returns a real image file

```bash
unset IMAGE_FILE IMAGE_SHA256 GEMINI_INPUT_SHA256
ARTIFACT_LOG=$(mktemp /tmp/contextbridge-demo-artifact.XXXXXX.log)
set -o pipefail
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --profile chatgpt --new-chat --foreground-new-chat --session "demo-$DEMO_ID-image" --image --artifacts "$DEMO_DIR" --prompt 'Erstelle genau ein quadratisches Bild: reinweißer Hintergrund, mittig die klare schwarze Aufschrift „IamAngusU“ und direkt darunter deutlich kleiner „ContextBridge“. Keine weiteren Wörter, Symbole oder Verzierungen. Gib nur das Bild aus.' 2>&1 | tee "$ARTIFACT_LOG"
IMAGE_JOB_STATUS=${PIPESTATUS[0]}
mapfile -t IMAGE_PATHS < <(sed -n 's/^Saved artifact: //p' "$ARTIFACT_LOG")
rm -f -- "$ARTIFACT_LOG"
unset ARTIFACT_LOG
if [ "$IMAGE_JOB_STATUS" -eq 0 ] && [ "${#IMAGE_PATHS[@]}" -eq 1 ]; then
  IMAGE_FILE=${IMAGE_PATHS[0]}
fi
unset IMAGE_PATHS IMAGE_JOB_STATUS
case "${IMAGE_FILE:-}" in
  "$DEMO_DIR"/*) ;;
  *) printf 'STOP: this run did not return exactly one artifact inside %s\n' "$DEMO_DIR" >&2; unset IMAGE_FILE ;;
esac
if [ -n "${IMAGE_FILE:-}" ] && [ -f "$IMAGE_FILE" ]; then
  IMAGE_MIME=$(file -b --mime-type -- "$IMAGE_FILE")
  case "$IMAGE_MIME" in
    image/png|image/jpeg|image/webp) file -- "$IMAGE_FILE" ;;
    *) printf 'STOP: returned artifact is not a supported image: %s\n' "$IMAGE_MIME" >&2; unset IMAGE_FILE ;;
  esac
else
  unset IMAGE_FILE
fi
if [ -n "${IMAGE_FILE:-}" ]; then
  printf 'Saved image: %s\n' "$IMAGE_FILE"
  IMAGE_SHA256=$(sha256sum -- "$IMAGE_FILE" | cut -d ' ' -f1)
  if [ "${#IMAGE_SHA256}" -ne 64 ]; then
    printf 'STOP: could not compute a valid SHA-256 for %s\n' "$IMAGE_FILE" >&2
    unset IMAGE_FILE IMAGE_SHA256
  else
    printf 'Artifact SHA-256: %s\n' "$IMAGE_SHA256"
  fi
else
  printf 'STOP: do not continue; no verified image belongs to this run.\n' >&2
  false
fi
if [ -z "${IMAGE_FILE:-}" ] || [ -z "${IMAGE_SHA256:-}" ]; then false; fi
```

Briefly show ChatGPT working on Windows. Return to the VPS for `Saved artifact:`. The temporary `/tmp` log binds the next steps to the artifact path emitted by **this run**; it is deleted immediately. The persistent `/root/contextbridge-demo` directory is neither cleared nor searched for a merely newest file. If `IMAGE_FILE` is empty, the block prints `STOP` and returns nonzero; stop the demo there and never continue with a guessed or stale path.

**DE:** „Der Job gilt erst als erfolgreich, wenn die tatsächliche Bilddatei auf dem VPS gespeichert wurde – nicht bloß, weil ein Modell sagt, es habe ein Bild erstellt. Der Hash ist ihr Fingerabdruck für den nächsten Schritt.“

**EN:** “The job succeeds only when the actual image file is saved on the VPS—not merely because a model says it created one. The hash is its fingerprint for the next step.”

## Shot 5 — Optional: the same file goes to the local vision model

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --provider ollama --model qwen2.5vl:7b --session "demo-$DEMO_ID-local-vision" --attach-image "$IMAGE_FILE" --artifacts off --prompt 'Lies nur den sichtbaren Text im angehängten Bild. Nenne die große und die kleine Zeile, ohne zu raten.'
```

Include this shot only when `qwen2.5vl:7b` is installed and the worker has enough free RAM/VRAM; otherwise continue directly with Gemini. Look for `↳ verwendet: ollama · qwen2.5vl:7b` and check the actual OCR answer. In a measured run the local model read `IamAngusU` and `ContextBridge` in **18.1 s**. This is one tested image, not a general OCR accuracy guarantee. `--attach-image` creates a hard vision requirement, and the named model itself must advertise that capability.

**DE:** „Jetzt bekommt mein eigenes Vision-Modell dieselbe gespeicherte Datei. Der VPS steuert es fern, und das Ergebnis kommt zurück – ohne ChatGPT oder Gemini für diesen Schritt.“

**EN:** “Now my own vision model receives the same saved file. The VPS controls it remotely and gets the result back—without ChatGPT or Gemini for this step.”

**DE, wenn übersprungen:** „Die lokale Bildanalyse ist optional. Gerade sind nicht genug Ressourcen frei, deshalb sende ich dieselbe verifizierte Datei direkt an Gemini.“

**EN, when skipped:** “Local image analysis is optional. There are not enough free resources right now, so I will send the same verified file directly to Gemini.”

## Shot 6 — Same VPS file goes to Gemini for OCR

```bash
GEMINI_INPUT_SHA256=$(sha256sum -- "$IMAGE_FILE" | cut -d ' ' -f1)
if [ "$GEMINI_INPUT_SHA256" != "$IMAGE_SHA256" ]; then
  printf 'STOP: image bytes changed before the Gemini handoff.\n' >&2
  false
else
  printf 'Gemini input SHA-256: %s\n' "$GEMINI_INPUT_SHA256"
  contextbridge cluster chat --config /var/lib/contextbridge/config.yml --profile gemini --new-chat --session "demo-$DEMO_ID-ocr" --attach-image "$IMAGE_FILE" --artifacts off --prompt 'Lies ausschließlich die Schrift im angehängten Bild. Nenne zuerst die große und dann die kleine Aufschrift. Keine Recherche, keine Identitätsprüfung und kein neues Bild.'
fi
```

The upload chat opens visibly, since inactive Opera tabs can stall file uploads. Wait for Gemini's actual answer. Its wording may vary; both `IamAngusU` and `ContextBridge` must be recognized. Show that the answer returns to the VPS.

**DE:** „Der Hash ist noch identisch. Jetzt sende ich genau diese gespeicherte VPS-Datei an Gemini. Gemini liest die Aufschrift, und die Antwort kommt wieder hier auf dem Server an.“

**EN:** “The hash is still identical. Now I send that exact saved VPS file to Gemini. Gemini reads the inscription, and the answer comes back here on the server.”

## Shot 7 — Minimized Opera, text job still returns

While Opera is **still visible**, prepare a separate owned Gemini text chat:

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --profile gemini --new-chat --foreground-new-chat --session "demo-$DEMO_ID-background" --artifacts off --prompt 'Antworte exakt mit CB-DEMO-READY und keinen weiteren Zeichen.'
```

Show or briefly cut the preparation. Minimize Opera **after** `CB-DEMO-READY` returns. Leave it minimized while sending the follow-up:

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --profile gemini --session "demo-$DEMO_ID-background" --artifacts off --prompt 'Antworte exakt mit CB-DEMO-MINIMIZED-OK und keinen weiteren Zeichen.'
```

Keep Opera minimized until the VPS displays `CB-DEMO-MINIMIZED-OK`. This proves the configured **text** follow-up works without browser focus; it does not claim that background image uploads do.

**DE:** „Opera ist minimiert. Der neue Textjob läuft trotzdem über den Browser-Worker, und die Antwort erscheint auf dem VPS.“

**EN:** “Opera is minimized. The new text job still runs through the browser worker, and its answer appears on the VPS.”

## Shot 8 — Dashboard and close (about 10–15 s)

Restore Opera or use another PC browser window. First show the already authenticated **relay cluster dashboard** and match the `Queued:` ID from Shot 3B to its completed job row; its Queue counter will normally be back at zero because the worker claimed the job. Then show [the local dashboard](http://127.0.0.1:32145) for Schedules, local providers, worker/connection state, and Recent activity. The local execution can have a different internal ID from the outer relay job, so do not present those IDs as identical. A live waiting backlog or scheduled occurrence belongs in a separate, rehearsed technical video.

**DE:** „Das Relay-Dashboard zeigt den echten Queue-Auftrag mit seiner Job-ID. Null wartende Jobs heißt nach dem Erfolg: Der Worker hat ihn übernommen. Im lokalen Dashboard sehe ich zusätzlich Provider, Zeitpläne und Verlauf.“

**EN:** “The relay dashboard shows the real queued job by its job ID. After success, zero waiting jobs means the worker claimed it. The local dashboard also shows providers, schedules, and activity.”

Suggested title: **“I turned browser AI tabs and local models into remote compute workers.”**
