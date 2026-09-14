# ContextBridge: short video script (DE / EN)

This is the **edited 2–3 minute video**, not a claim that all provider jobs finish within fixed seconds. Keep the terminal's real elapsed times visible; cut waiting footage honestly. The full [technical runbook](demo-de-en.md) retains identity research, relativity, safety notes, and troubleshooting.

## Before recording

- Verify the PC service, VPS relay, and Opera extension are connected and compatible. Leave enough capacity: **at most eight attached tabs before attaching the three fresh demo pages**. Those three pages plus three new-chat jobs then stay below the 16-tab safety limit. Close or detach only finished test chats, never personal or active ones.
- Use a fresh ChatGPT page and two fresh Gemini pages for the on-camera attach sequence. Keep the browser visible for image creation and upload. Do **not** include Gemini music: the latest test did not return verified media bytes.
- Use one VPS shell throughout the recording; shell variables below must persist. Open the PC dashboard at `http://127.0.0.1:32145` **before** recording and authenticate privately if it asks for the local token. Never show or read out that token. Check that Queue, Schedules, and Recent activity load, then switch back to the console. An empty queue is an empty queue—do not present it as a live queue demonstration.
- If ChatGPT or Gemini shows a rate-limit dialog, wait for it to clear before recording. Never dismiss a provider error and imply that the job succeeded.

## Shot 1 — Windows console and attached tabs (about 20–30 s on screen)

On the Windows PC, open a new CMD and run:

```cmd
contextbridge console --config "C:\ContextBridge\config.yml"
```

This attaches to the managed service; it does not start another service process. Show the status line. In Opera, show **Attach this page** on the fresh ChatGPT and Gemini pages, then click **Connect** once and wait for **Connected**.

**DE:** „Mein Windows-PC ist der Worker. Ich wähle die ChatGPT- und Gemini-Seiten einzeln aus und verbinde sie erst danach mit ContextBridge.“

**EN:** “My Windows PC is the worker. I explicitly select the ChatGPT and Gemini pages, then connect them to ContextBridge.”

## Shot 2 — VPS sees the worker (about 10 s)

In **one** VPS shell:

```bash
contextbridge cluster status --config /var/lib/contextbridge/config.yml
DEMO_DIR=$(mktemp -d /root/contextbridge-demo.XXXXXX)
DEMO_ID=$(date +%Y%m%d-%H%M%S)
printf 'Files: %s\n' "$DEMO_DIR"
```

Show the PC online, its free slots, providers/models, and hardware. Mention the actual displayed slot count rather than assuming four.

**DE:** „Vom VPS aus sehe ich den verbundenen PC mit seinen verfügbaren Slots und Modellen. Die Browserarbeit findet dort statt; die Befehle sende ich von hier.“

**EN:** “From the VPS, I can see the connected PC, its available slots, and models. Browser work happens there; I send the commands from here.”

## Shot 3 — Hero flow: ChatGPT returns a real image file

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --profile chatgpt --new-chat --foreground-new-chat --session "demo-$DEMO_ID-image" --image --artifacts "$DEMO_DIR" --prompt 'Erstelle genau ein quadratisches Bild: reinweißer Hintergrund, mittig die klare schwarze Aufschrift „IamAngusU“ und direkt darunter deutlich kleiner „ContextBridge“. Keine weiteren Wörter, Symbole oder Verzierungen. Gib nur das Bild aus.'
```

Briefly show ChatGPT working on Windows. Return to the VPS for `Saved artifact:`; show the returned file and validate that it is an image:

```bash
IMAGE_FILE=$(find "$DEMO_DIR" -maxdepth 1 -type f \( -iname '*.png' -o -iname '*.jpg' -o -iname '*.jpeg' -o -iname '*.webp' \) -printf '%T@ %p\n' | sort -nr | head -n 1 | cut -d' ' -f2-)
test -n "$IMAGE_FILE" && file "$IMAGE_FILE"
printf 'Saved image: %s\n' "$IMAGE_FILE"
```

If `IMAGE_FILE` is empty, stop the demo here; do not continue with a guessed path.

**DE:** „Der Job gilt erst als erfolgreich, wenn die tatsächliche Bilddatei auf dem VPS gespeichert wurde – nicht bloß, weil ein Modell sagt, es habe ein Bild erstellt.“

**EN:** “The job succeeds only when the actual image file is saved on the VPS—not merely because a model says it created one.”

## Shot 4 — Same VPS file goes to Gemini for OCR

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --profile gemini --new-chat --session "demo-$DEMO_ID-ocr" --attach-image "$IMAGE_FILE" --artifacts off --prompt 'Lies ausschließlich die Schrift im angehängten Bild. Nenne zuerst die große und dann die kleine Aufschrift. Keine Recherche, keine Identitätsprüfung und kein neues Bild.'
```

The upload chat opens visibly, since inactive Opera tabs can stall file uploads. Wait for Gemini's actual answer. Its wording may vary; both `IamAngusU` and `ContextBridge` must be recognized. Show that the answer returns to the VPS.

**DE:** „Jetzt sende ich genau die gespeicherte VPS-Datei an Gemini. Gemini liest die Aufschrift, und die Antwort kommt wieder hier auf dem Server an.“

**EN:** “Now I send that exact saved VPS file to Gemini. Gemini reads the inscription, and the answer comes back here on the server.”

## Shot 5 — Minimized Opera, text job still returns

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

## Shot 6 — Dashboard and close (about 10–15 s)

Restore Opera or use another PC browser window and open [the local dashboard](http://127.0.0.1:32145). Show the **actual** Queue metric, Schedules, Worker/connection state, and Recent activity/history. If no schedule or queued job exists, say the view supports them; do not narrate a run that did not happen. A live scheduling or parallel-queue demonstration belongs in a separate, rehearsed technical video; with four slots, two to four simultaneous jobs do not demonstrate waiting in a queue.

**DE:** „Hier sieht man denselben Zustand auch im Dashboard: Worker, Aufträge und Verlauf. Die Browser-Tabs sind für ContextBridge keine separate Spielerei, sondern ausführende Worker.“

**EN:** “The dashboard shows the same state: workers, jobs, and history. To ContextBridge, browser tabs are not a separate trick—they are execution workers.”

Suggested title: **“I turned browser AI tabs into remote compute workers.”**
