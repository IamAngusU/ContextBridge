# ContextBridge: short video script (DE / EN)

This is the **edited roughly 3-minute video**, not a claim that all provider jobs finish within fixed seconds. It requires **ContextBridge 0.5.66 or newer** on the VPS and worker, plus a compatible 0.5.66-or-newer extension. Keep the terminal's real elapsed times visible; cut waiting footage honestly. The full [technical runbook](demo-de-en.md) retains identity research, relativity, safety notes, and troubleshooting. The story starts with **a local Ollama model controlled from the VPS**, then moves a real image from ChatGPT to Gemini, with an optional local-vision step when enough worker resources are free.

The Windows commands intentionally show the recording PC's custom `C:\ContextBridge\config.yml`. The installer default is `%LOCALAPPDATA%\ContextBridge\config.yml`; default installations can omit `--config` or substitute that path. The visible VPS directory remains exactly `/root/contextbridge-demo`; these commands do not clear it.

## Before recording

- Verify the PC service, VPS relay, and Opera extension are connected and compatible. Leave enough capacity: **at most eight attached tabs before attaching the three fresh demo pages**. Those three pages plus three new-chat jobs then stay below the 16-tab safety limit. Close or detach only finished test chats, never personal or active ones.
- Start Ollama on the Windows PC if it is offline. Check `contextbridge models` locally: this recorded setup uses `qwen2.5:1.5b` for the reliable text step. The optional local-vision step uses `qwen2.5vl:7b`; include it only if that model is installed and enough RAM/VRAM is free, otherwise skip Shot 5. These names are **demo prerequisites for the steps that use them**, not universal bundled models. A model need not remain loaded in VRAM between jobs. `models` reports advertised/inferred capabilities, parameter count and quantization where available—not an intelligence score or proof that every image will be read correctly.
- Use a fresh ChatGPT page and two fresh Gemini pages for the on-camera attach sequence. Keep the browser visible for image creation and upload. Do **not** include Gemini music: the latest test did not return verified media bytes.
- Use one VPS shell throughout the recording; shell variables below must persist. Open the PC dashboard at `http://127.0.0.1:32145` **before** recording and authenticate privately if it asks for the local token. Never show or read out that token. Check that Queue, Schedules, and Recent activity load, then switch back to the console. An empty queue is an empty queue—do not present it as a live queue demonstration.
- If ChatGPT or Gemini shows a rate-limit dialog, wait for it to clear before recording. Never dismiss a provider error and imply that the job succeeded.

## Shot 1 — Windows console, local models, and attached tabs (about 30–45 s on screen)

On the Windows PC, open a new CMD and run:

```cmd
contextbridge console --config "C:\ContextBridge\config.yml"
```

This attaches to the managed service; it does not start another service process. In a second CMD, run:

```cmd
contextbridge models --config "C:\ContextBridge\config.yml"
```

Point to `qwen2.5:1.5b` (text) and, when you include the optional vision segment, `qwen2.5vl:7b` (text+vision). Show the console status line. In Opera, show **Attach this page** on the fresh ChatGPT and Gemini pages, then click **Connect** once and wait for **Connected**.

**DE:** „Mein Windows-PC ist der Worker. Hier laufen eigene Ollama-Modelle: eines für Text, eines mit Bildverständnis. Zusätzlich wähle ich ChatGPT- und Gemini-Seiten bewusst einzeln aus.“

**EN:** “My Windows PC is the worker. It has my own Ollama models—one for text and one with vision. I also explicitly attach the ChatGPT and Gemini pages.”

## Shot 2 — VPS sees the worker (about 10 s)

In **one** VPS shell:

```bash
contextbridge version
contextbridge cluster status --config /var/lib/contextbridge/config.yml
DEMO_DIR=/root/contextbridge-demo
mkdir -p "$DEMO_DIR"
DEMO_ID=$(date +%Y%m%d-%H%M%S)
printf 'Files: %s\n' "$DEMO_DIR"
```

Show the PC online, its free slots, providers/models, and hardware. Mention the actual displayed slot count rather than assuming four. The ordinary status line lists model names; it does **not** rank model quality. `contextbridge models` on the PC is the easier visual proof of text/vision classification.

**DE:** „Vom VPS aus sehe ich den verbundenen PC, seine Slots und die installierten Modelle. Jetzt steuere ich zuerst ein lokales Modell – noch ganz ohne Browser-KI.“

**EN:** “From the VPS, I can see the connected PC, its slots, and installed models. First, I will control a local model—without any browser AI.”

## Shot 3 — VPS controls a local Ollama text model

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --provider ollama --model qwen2.5:1.5b --session "demo-$DEMO_ID-local-text" --artifacts off --prompt 'Antworte exakt mit CB-LOCAL-OK und keinen weiteren Zeichen.'
```

Wait for `CB-LOCAL-OK` and `↳ verwendet: ollama · qwen2.5:1.5b`. The marker alone is not the proof of execution; the **provider/model/node metadata** in the VPS terminal is. This exact live rehearsal finished in **6.4 s**, but keep the actual time on camera. Do not confuse the Windows GPU with hosted ChatGPT/Gemini inference.

**DE:** „Der Befehl kommt vom VPS, aber die Rechnung läuft auf meinem Windows-PC. ContextBridge zeigt mir den tatsächlich verwendeten Anbieter, das Modell und den Worker.“

**EN:** “The command comes from my VPS, but the computation runs on my Windows PC. ContextBridge shows the actual provider, model, and worker.”

## Shot 4 — Hero flow: ChatGPT returns a real image file

```bash
unset IMAGE_FILE
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
test -n "${IMAGE_FILE:-}" || { printf 'Do not continue; no verified image belongs to this run.\n' >&2; false; }
printf 'Saved image: %s\n' "$IMAGE_FILE"
```

Briefly show ChatGPT working on Windows. Return to the VPS for `Saved artifact:`. The temporary `/tmp` log binds the next steps to the artifact path emitted by **this run**; it is deleted immediately. The persistent `/root/contextbridge-demo` directory is neither cleared nor searched for a merely newest file. If `IMAGE_FILE` is empty, stop the demo here; never continue with a guessed or stale path.

**DE:** „Der Job gilt erst als erfolgreich, wenn die tatsächliche Bilddatei auf dem VPS gespeichert wurde – nicht bloß, weil ein Modell sagt, es habe ein Bild erstellt.“

**EN:** “The job succeeds only when the actual image file is saved on the VPS—not merely because a model says it created one.”

## Shot 5 — Optional: the same file goes to the local vision model

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --provider ollama --model qwen2.5vl:7b --session "demo-$DEMO_ID-local-vision" --attach-image "$IMAGE_FILE" --artifacts off --prompt 'Lies nur den sichtbaren Text im angehängten Bild. Nenne die große und die kleine Zeile, ohne zu raten.'
```

Include this shot only when `qwen2.5vl:7b` is installed and the worker has enough free RAM/VRAM; otherwise continue directly with Gemini. Look for `↳ verwendet: ollama · qwen2.5vl:7b` and check the actual OCR answer. In a measured run the local model read `IamAngusU` and `ContextBridge` in **18.1 s**. This is one tested image, not a general OCR accuracy guarantee. `--attach-image` creates a hard vision requirement, and the named model itself must advertise that capability.

**DE:** „Jetzt bekommt mein eigenes Vision-Modell dieselbe gespeicherte Datei. Der VPS steuert es fern, und das Ergebnis kommt zurück – ohne ChatGPT oder Gemini für diesen Schritt.“

**EN:** “Now my own vision model receives the same saved file. The VPS controls it remotely and gets the result back—without ChatGPT or Gemini for this step.”

## Shot 6 — Same VPS file goes to Gemini for OCR

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --profile gemini --new-chat --session "demo-$DEMO_ID-ocr" --attach-image "$IMAGE_FILE" --artifacts off --prompt 'Lies ausschließlich die Schrift im angehängten Bild. Nenne zuerst die große und dann die kleine Aufschrift. Keine Recherche, keine Identitätsprüfung und kein neues Bild.'
```

The upload chat opens visibly, since inactive Opera tabs can stall file uploads. Wait for Gemini's actual answer. Its wording may vary; both `IamAngusU` and `ContextBridge` must be recognized. Show that the answer returns to the VPS.

**DE:** „Jetzt sende ich genau die gespeicherte VPS-Datei an Gemini. Gemini liest die Aufschrift, und die Antwort kommt wieder hier auf dem Server an.“

**EN:** “Now I send that exact saved VPS file to Gemini. Gemini reads the inscription, and the answer comes back here on the server.”

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

Restore Opera or use another PC browser window and open [the local dashboard](http://127.0.0.1:32145). Show the **actual** Queue metric, Schedules, Worker/connection state, and Recent activity/history. If no schedule or queued job exists, say the view supports them; do not narrate a run that did not happen. A live scheduling or parallel-queue demonstration belongs in a separate, rehearsed technical video; with four slots, two to four simultaneous jobs do not demonstrate waiting in a queue.

**DE:** „Hier sieht man denselben Zustand auch im Dashboard: lokale Modelle, Browser-Worker, Aufträge und Verlauf. Alles läuft durch denselben kontrollierbaren Pool.“

**EN:** “The dashboard shows the same state: local models, browser workers, jobs, and history. They all run through the same controllable pool.”

Suggested title: **“I turned browser AI tabs and local models into remote compute workers.”**
