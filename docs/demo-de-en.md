# Technical live-demo runbook: Windows browser, VPS commands

For a short recording, use the separate [2–3 minute video script](demo-video-de-en.md). This document is the complete technical runbook, including optional research and fact-check segments.

This script requires **ContextBridge 0.5.66 or newer** on the VPS and worker, plus a compatible 0.5.66-or-newer browser extension. Run the Linux commands in **one Bash VPS shell** so `DEMO_DIR`, `DEMO_ID`, and `IMAGE_FILE` remain available. It uses the managed Windows service, local Ollama models, an Opera extension, and a VPS relay. `--new-chat` gives each browser demo segment its own ContextBridge-owned chat. Do not attach a personal conversation for this demo. The short video begins with local Ollama text, then shows ChatGPT creating a real file, optionally lets local Ollama vision read it when enough resources are free, and gives the same file to Gemini.

The Windows commands intentionally use this recording machine's **custom** config path, `C:\ContextBridge\config.yml`. A default Windows installer uses `%LOCALAPPDATA%\ContextBridge\config.yml`; on that installation either omit `--config` or replace the demonstrated path with the default. The VPS demo directory stays exactly `/root/contextbridge-demo` and is never cleared by this runbook.

## 1. Open ContextBridge on the Windows PC

In a fresh Windows CMD:

```cmd
contextbridge console --config "C:\ContextBridge\config.yml"
```

This opens the live view of the already managed service; it does **not** start a second service. If the service is not running, start the installed `ContextBridge` scheduled task or the Start-menu shortcut first. Start the Ollama Windows app if it is offline, then check its installed and ready models in a second CMD:

```cmd
contextbridge models --config "C:\ContextBridge\config.yml"
```

For the reliable local text command below, `qwen2.5:1.5b` must be present. The optional local-vision step uses `qwen2.5vl:7b` and should run only when the worker has enough free RAM/VRAM; otherwise skip that step and continue with Gemini OCR. Those models belong to this demo PC; they are **not** bundled with every ContextBridge installation. `models` shows Ollama-advertised modalities, parameters, quantization and loaded state where available, with model-name inference only as a compatibility fallback for older runtimes. It does not measure intelligence or guarantee OCR accuracy. Type `exit` and Enter to close only the console view; the service and jobs continue.

**DE:** „ContextBridge läuft als lokaler Dienst. Für den sicheren Basistest nutze ich ein kleines lokales Textmodell. Wenn genug Ressourcen frei sind, zeige ich zusätzlich ein eigenes Vision-Modell. Das Terminal kann ich später schließen, ohne Jobs zu stoppen.“

**EN:** “ContextBridge runs as a local service. I use a small local text model for the reliable baseline. If enough resources are free, I will also show my own vision model. I can close the terminal later without stopping jobs.”

## 2. Attach the AI pages, then connect

Open a fresh ChatGPT page and two fresh Gemini pages in Opera. On each page, open ContextBridge and choose **Attach this page**. After all pages are attached, click **Connect** once. Wait for **Connected** and for the tab count to settle. Keep personal chats detached. For new demo jobs, `--new-chat` below opens ContextBridge-owned chats; do not manually reuse a chat with old messages.

Before recording, close or detach finished **test** chats you no longer need. Automatic new chats have a 16-attached-tab safety limit. Do not close a personal or active tab to make room. The optional **Close finished ContextBridge-created per-job tabs when idle** switch helps only for eligible tabs when enabled; it is not a blanket cleanup of every old session.

**DE:** „Ich wähle die Browserseiten bewusst einzeln aus. Erst wenn die gewünschten ChatGPT- und Gemini-Tabs im Pool sind, verbinde ich die Erweiterung. Andere Seiten bleiben außen vor.“

**EN:** “I explicitly select the browser pages one by one. Once the ChatGPT and Gemini tabs are in the pool, I connect the extension. Other pages remain outside it.”

## 3. Check the remote pool on the VPS

```bash
contextbridge version
contextbridge cluster status --config /var/lib/contextbridge/config.yml
cb selftest --config /var/lib/contextbridge/config.yml
contextbridge cluster status --config /var/lib/contextbridge/config.yml --json |
  python3 -c 'import json,sys; d=json.load(sys.stdin); wanted={"qwen2.5:1.5b","qwen2.5vl:7b"}; rows=[(n,m) for n in d.get("nodes",[]) for m in n.get("capabilities",{}).get("models",[]) if m.get("provider")=="ollama" and m.get("name") in wanted]; [print("{}: {} [{}]".format(n.get("name","node"),m.get("name","unknown"),"+".join(m.get("tasks",[])))) for n,m in rows]'
DEMO_DIR=/root/contextbridge-demo
mkdir -p "$DEMO_DIR"
DEMO_ID=$(date +%Y%m%d-%H%M%S)
printf 'Demo ID: %s\nFiles: %s\n' "$DEMO_ID" "$DEMO_DIR"
```

Check that the PC is online. `cb selftest` waits for a schedulable local
generation model, an idle attached ChatGPT page, an idle attached Gemini page,
and a free worker slot. It reports what is missing and sends **no AI prompt**
unless `--run` is added; do not add it for this preflight. The compact status
view may shorten a long model list, so the JSON command prints the two demo
models explicitly; no output for the required `qwen2.5:1.5b` means stop and fix
the worker before recording. The optional `qwen2.5vl:7b` line may be absent
when you skip local vision. Neither view ranks model quality. The VPS saves
returned artifacts in the printed `$DEMO_DIR`, **not** in the Windows inbox.
The models and browser run on the PC; the CLI and saved results run on the VPS.

**DE:** „Jetzt wechsle ich auf meinen VPS. Der Relay sieht den PC, die freien Slots und die Modelle. Zuerst spreche ich ein lokales Ollama-Modell auf dem PC an – ganz ohne Browser-KI.“

**EN:** “Now I switch to my VPS. The relay sees the PC, free slots, and models. First I call a local Ollama model on the PC—without browser AI.”

## 4. Control a local text model from the VPS

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --provider ollama --model qwen2.5:1.5b --session "demo-$DEMO_ID-local-text" --artifacts off --prompt 'Antworte exakt mit CB-LOCAL-OK und keinen weiteren Zeichen.'
```

Check the VPS output for `CB-LOCAL-OK`, `↳ verwendet: ollama · qwen2.5:1.5b`, and the worker node ID. **Those route facts, not the model's prose, prove where the job ran.** This exact live rehearsal returned in 6.4 seconds; do not promise the same latency to viewers.

**DE:** „Ich gebe den Auftrag auf dem VPS ein; mein Windows-PC rechnet ihn mit dem lokalen Modell. Im Ergebnis stehen der tatsächlich verwendete Anbieter und das Modell.“

**EN:** “I submit from the VPS; my Windows PC computes with its local model. The result names the provider and model actually used.”

## 4A. Put a copy-paste JSON job through the durable queue

This second small local-model job uses the lower-level submission path. It prints a real `Queued:` job ID before waiting for the worker and returning the verified result. A free slot may claim it almost immediately; that is a healthy fast queue, not evidence that the queue was bypassed.

```bash
QUEUE_JOB=$(mktemp /tmp/contextbridge-demo-queue.XXXXXX.json)
cat > "$QUEUE_JOB" <<JSON
{
  "source": "contextbridge-demo-queue",
  "requirements": {
    "task": "generation",
    "provider": "ollama",
    "model": "qwen2.5:1.5b",
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

The final command status remains nonzero on failure without enabling global `set -e` or closing the VPS shell. The authenticated **relay cluster dashboard** can now be matched to the printed outer job ID. The Windows local dashboard may show a separate local execution ID for the same work; do not claim that those two IDs are identical.

**DE:** „Hier sende ich denselben kleinen Test über die niedrigere JSON-Schnittstelle. Der Relay legt ihn dauerhaft in die Queue, gibt eine Job-ID aus und ein kompatibler freier Worker übernimmt ihn.“

**EN:** “Here I submit the same small test through the lower-level JSON interface. The relay durably queues it, prints a job ID, and a compatible free worker claims it.”

## 5. Create and save a real image with ChatGPT

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
  printf 'Image for Gemini: %s\n' "$IMAGE_FILE"
  IMAGE_SHA256=$(sha256sum -- "$IMAGE_FILE" | cut -d ' ' -f1)
  if [ "${#IMAGE_SHA256}" -ne 64 ]; then
    printf 'STOP: could not compute a valid SHA-256 for %s\n' "$IMAGE_FILE" >&2
    unset IMAGE_FILE IMAGE_SHA256
  else
    printf 'Artifact SHA-256: %s\n' "$IMAGE_SHA256"
  fi
else
  printf 'STOP: do not run the OCR steps; no verified image belongs to this run.\n' >&2
  false
fi
if [ -z "${IMAGE_FILE:-}" ] || [ -z "${IMAGE_SHA256:-}" ]; then false; fi
```

The job succeeds only if real image bytes are received and saved. This block captures the `Saved artifact:` path emitted by **this command** in a temporary `/tmp` log, requires exactly one supported image under the persistent demo directory, and then deletes only the temporary log. It never chooses the newest pre-existing file from `/root/contextbridge-demo`; on any ambiguity it unsets `IMAGE_FILE`, prints `STOP`, and leaves the block with a nonzero status. Do not paste the next block after that failure.

**DE:** „ChatGPT erstellt jetzt ein schlichtes Bild. ContextBridge akzeptiert nicht bloß die Behauptung ‚Bild erstellt‘: Der Job gilt erst als erfolgreich, wenn die tatsächliche Bilddatei auf dem VPS gespeichert wurde.“

**EN:** “ChatGPT is creating a minimal image. ContextBridge does not accept the claim ‘image created’ alone: the job succeeds only when the actual image file has been saved on the VPS.”

## 6. Optional: give that same file to the local vision model

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --provider ollama --model qwen2.5vl:7b --session "demo-$DEMO_ID-local-vision" --attach-image "$IMAGE_FILE" --artifacts off --prompt 'Lies nur den sichtbaren Text im angehängten Bild. Nenne die große und die kleine Zeile, ohne zu raten.'
```

Run this optional step only when `qwen2.5vl:7b` is installed and the worker has enough free RAM/VRAM; otherwise skip directly to Gemini OCR. Check for `↳ verwendet: ollama · qwen2.5vl:7b` and the **actual** answer. In one live test the model read `IamAngusU` and `ContextBridge` from the saved PNG in 18.1 seconds. `--attach-image` adds a hard vision requirement: when a model is named, that exact model—not merely another model on the same PC—must advertise vision. ContextBridge detects modality for placement, but it does not measure model intelligence or OCR reliability.

**DE:** „Dasselbe Bild geht jetzt erst an mein eigenes Vision-Modell. Auch das steuere ich vom VPS; für diesen Schritt geht kein Prompt an ChatGPT oder Gemini.“

**EN:** “The same picture now goes first to my own vision model. I control that from the VPS too; this step sends no prompt to ChatGPT or Gemini.”

**DE, wenn übersprungen:** „Die lokale Bildanalyse ist optional. Auf diesem Rechner sind gerade nicht genug Ressourcen frei, deshalb gehe ich mit derselben verifizierten Datei direkt zu Gemini weiter.“

**EN, when skipped:** “Local image analysis is optional. This machine does not have enough free resources right now, so I will take the same verified file directly to Gemini.”

## 7. Give that image to Gemini for OCR only

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

`--attach-image` carries the **saved VPS file** into a new Gemini chat. The image job automatically opens its new tab in the foreground because Opera can suspend uploads in inactive tabs. Gemini's wording may vary; verify that it recognizes `IamAngusU` and `ContextBridge`.

**DE:** „Der Hash ist noch identisch. Genau diese gespeicherte Datei geht jetzt an Gemini. Der Auftrag ist absichtlich eng: nur die zwei Aufschriften lesen. Recherche und Identitätsabgleich kommen erst im nächsten, getrennten Job.“

**EN:** “The hash is still identical. That exact saved file now goes to Gemini. This task is deliberately narrow: read only the two inscriptions. Research and identity checks come in the next, separate job.”

## 8. Research identity in a separate Gemini chat

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --profile gemini --new-chat --foreground-new-chat --session "demo-$DEMO_ID-identity" --artifacts off --prompt 'Unabhängige Recherche für eine Demo: Auf einem separaten Bild standen IamAngusU und ContextBridge. Prüfe tatsächlich https://angusu.de/ und https://github.com/IamAngusU. Welcher bürgerliche Name wird auf angusu.de für den Programmierer genannt, und ist dessen Zuordnung zum GitHub-Benutzernamen IamAngusU direkt belegt? Nenne die konkret geprüften URLs. Trenne bestätigt, Indiz und nicht verifiziert. Wenn du eine Seite nicht öffnen kannst, sage es ausdrücklich. Der Bildtext allein beweist keine Identität.'
```

**DE:** „Der nächste Gemini-Chat bekommt nicht das Bild als Beweis. Er prüft zwei öffentliche Quellen und soll sauber trennen, was bestätigt ist und was nur ein Indiz wäre.“

**EN:** “The next Gemini chat does not treat the picture as proof. It checks two public sources and must distinguish confirmed information from mere indications.”

## 9. Show an unfocused or minimized browser

First prepare an owned Gemini chat while Opera is visible:

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --profile gemini --new-chat --foreground-new-chat --session "demo-$DEMO_ID-minimized" --artifacts off --prompt 'Antworte exakt mit CB-DEMO-READY und keinen weiteren Zeichen.'
```

Now minimize Opera and send a **follow-up to that same session**. Do not restore Opera until the result appears on the VPS:

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --profile gemini --session "demo-$DEMO_ID-minimized" --artifacts off --prompt 'Antworte exakt mit CB-DEMO-MINIMIZED-OK und keinen weiteren Zeichen.'
```

This is a **text** test. It proves that the browser window need not be focused for this configured PC and Gemini text path. It does not prove that background media uploads work the same way.

**DE:** „Opera ist jetzt minimiert. Trotzdem sende ich vom VPS einen neuen Textjob. Die Antwort erscheint hier im Terminal, ohne dass ich den Browser in den Vordergrund hole.“

**EN:** “Opera is minimized now. I am still sending a new text job from the VPS. The answer appears here in the terminal without bringing the browser to the foreground.”

## 10. Optional short fact-check and closing

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --profile chatgpt --new-chat --foreground-new-chat --session "demo-$DEMO_ID-relativity" --artifacts off --prompt 'Prüfe kurz diese Behauptung: Aliens sind 70 Millionen Lichtjahre entfernt und fliegen mit 99,99999999999998 Prozent der Lichtgeschwindigkeit zu uns. Vergehen für uns ungefähr 70 Millionen Jahre, für sie aber genau ein Jahr? Rechne die Eigenzeit nachvollziehbar aus und korrigiere die Aussage in höchstens vier Sätzen.'
```

The expected ship time is approximately **1.4 years**, not exactly one year, neglecting acceleration and cosmology.

**DE:** „Zum Schluss ein kleiner Faktencheck: Ein anderer Provider kann denselben Pool nutzen, aber in einem eigenen Chat. Die Rechnung liefert hier ungefähr 1,4 Jahre Eigenzeit statt genau eines Jahres.“

**EN:** “Finally, a small fact-check: another provider can use the same pool in its own chat. The calculation gives about 1.4 years of proper time here, not exactly one year.”

## 11. Close with the queue and dashboards

Open the authenticated relay cluster dashboard in a browser tab prepared off camera. Never reveal its admin token. Match the `Queued:` ID from step 4A to the completed job row; its Queue counter will normally be back at zero because the worker already claimed it. Then show the Windows local dashboard at `http://127.0.0.1:32145` for its local providers, Schedules, and Recent activity. A local execution may have a different internal ID from its outer relay job.

**DE:** „Im Relay-Dashboard sehe ich denselben Queue-Auftrag mit seiner Job-ID. Null wartende Jobs bedeutet nach dem Erfolg: Der Worker hat ihn übernommen. Das lokale Dashboard zeigt zusätzlich Provider, Zeitpläne und den Verlauf auf diesem PC.“

**EN:** “The relay dashboard shows the same queued job by its job ID. After success, zero waiting jobs means the worker claimed it. The local dashboard also shows providers, schedules, and activity on this PC.”

The separate Gemini music mode is **not** part of the reliable live sequence yet. In the current test Gemini reported “Generating your track”, but no verified media file reached the VPS. Do not promise a downloadable song or use an unverified file in the recording. Likewise, do not claim that an unfocused image upload was tested: the verified minimized-window test is text-only.
