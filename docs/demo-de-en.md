# Technical live-demo runbook: Windows browser, VPS commands

For a short recording, use the separate [2–3 minute video script](demo-video-de-en.md). This document is the complete technical runbook, including optional research and fact-check segments.

This script uses the managed Windows service, an Opera extension, and a VPS relay. Run the Linux commands in **one VPS shell** so `DEMO_DIR`, `DEMO_ID`, and `IMAGE_FILE` remain available. `--new-chat` gives each demo segment its own ContextBridge-owned chat. Do not attach a personal conversation for this demo.

## 1. Open ContextBridge on the Windows PC

In a fresh Windows CMD:

```cmd
contextbridge console --config "C:\ContextBridge\config.yml"
```

This opens the live view of the already managed service; it does **not** start a second service. If the service is not running, start the installed `ContextBridge` scheduled task or the Start-menu shortcut first. Type `exit` and Enter to close only this view; the service and jobs continue.

**DE:** „ContextBridge läuft als lokaler Dienst. Mit diesem Befehl öffne ich seine Live-Ansicht: Verbindung, Tabs, Modelle, Jobs und Verlauf. Das Terminal kann ich später schließen, ohne laufende Jobs zu stoppen.“

**EN:** “ContextBridge runs as a local service. This command opens its live view: connection, tabs, models, jobs, and history. I can close the terminal later without stopping jobs.”

## 2. Attach the AI pages, then connect

Open a fresh ChatGPT page and two fresh Gemini pages in Opera. On each page, open ContextBridge and choose **Attach this page**. After all pages are attached, click **Connect** once. Wait for **Connected** and for the tab count to settle. Keep personal chats detached. For new demo jobs, `--new-chat` below opens ContextBridge-owned chats; do not manually reuse a chat with old messages.

Before recording, close or detach finished **test** chats you no longer need. Automatic new chats have a 16-attached-tab safety limit. Do not close a personal or active tab to make room. The optional **Close finished ContextBridge-created per-job tabs when idle** switch helps only for eligible tabs when enabled; it is not a blanket cleanup of every old session.

**DE:** „Ich wähle die Browserseiten bewusst einzeln aus. Erst wenn die gewünschten ChatGPT- und Gemini-Tabs im Pool sind, verbinde ich die Erweiterung. Andere Seiten bleiben außen vor.“

**EN:** “I explicitly select the browser pages one by one. Once the ChatGPT and Gemini tabs are in the pool, I connect the extension. Other pages remain outside it.”

## 3. Check the remote pool on the VPS

```bash
contextbridge cluster status --config /var/lib/contextbridge/config.yml
DEMO_DIR=$(mktemp -d /root/contextbridge-demo.XXXXXX)
DEMO_ID=$(date +%Y%m%d-%H%M%S)
printf 'Demo ID: %s\nFiles: %s\n' "$DEMO_ID" "$DEMO_DIR"
```

Check that the PC is online and at least one browser worker is available. The VPS saves returned artifacts in the printed `$DEMO_DIR`, **not** in the Windows inbox. The browser itself runs on the PC; the CLI and saved results run on the VPS.

**DE:** „Jetzt wechsle ich auf meinen VPS. Der Relay sieht den PC, die freien Slots und die Modelle. Die Browserarbeit passiert auf dem PC; die Befehle und Ergebnisdateien liegen hier auf dem Server.“

**EN:** “Now I switch to my VPS. The relay sees the PC, free slots, and models. Browser work happens on the PC; commands and returned files live here on the server.”

## 4. Create and save a real image with ChatGPT

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --profile chatgpt --new-chat --foreground-new-chat --session "demo-$DEMO_ID-image" --image --artifacts "$DEMO_DIR" --prompt 'Erstelle genau ein quadratisches Bild: reinweißer Hintergrund, mittig die klare schwarze Aufschrift „IamAngusU“ und direkt darunter deutlich kleiner „ContextBridge“. Keine weiteren Wörter, Symbole oder Verzierungen. Gib nur das Bild aus.'
```

The job succeeds only if real image bytes are received and saved. Note the `Saved artifact:` line. Find the latest image and verify its file type:

```bash
IMAGE_FILE=$(find "$DEMO_DIR" -maxdepth 1 -type f \( -iname '*.png' -o -iname '*.jpg' -o -iname '*.jpeg' -o -iname '*.webp' \) -printf '%T@ %p\n' | sort -nr | head -n 1 | cut -d' ' -f2-)
test -n "$IMAGE_FILE" && file "$IMAGE_FILE"
printf 'Image for Gemini: %s\n' "$IMAGE_FILE"
```

**DE:** „ChatGPT erstellt jetzt ein schlichtes Bild. ContextBridge akzeptiert nicht bloß die Behauptung ‚Bild erstellt‘: Der Job gilt erst als erfolgreich, wenn die tatsächliche Bilddatei auf dem VPS gespeichert wurde.“

**EN:** “ChatGPT is creating a minimal image. ContextBridge does not accept the claim ‘image created’ alone: the job succeeds only when the actual image file has been saved on the VPS.”

## 5. Give that image to Gemini for OCR only

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --profile gemini --new-chat --session "demo-$DEMO_ID-ocr" --attach-image "$IMAGE_FILE" --artifacts off --prompt 'Lies ausschließlich die Schrift im angehängten Bild. Nenne zuerst die große und dann die kleine Aufschrift. Keine Recherche, keine Identitätsprüfung und kein neues Bild.'
```

`--attach-image` carries the **saved VPS file** into a new Gemini chat. The image job automatically opens its new tab in the foreground because Opera can suspend uploads in inactive tabs. Gemini's wording may vary; verify that it recognizes `IamAngusU` and `ContextBridge`.

**DE:** „Das gespeicherte Bild geht jetzt an Gemini. Der Auftrag ist absichtlich eng: nur die zwei Aufschriften lesen. Recherche und Identitätsabgleich kommen erst im nächsten, getrennten Job.“

**EN:** “The saved image now goes to Gemini. This task is deliberately narrow: read only the two inscriptions. Research and identity checks come in the next, separate job.”

## 6. Research identity in a separate Gemini chat

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --profile gemini --new-chat --foreground-new-chat --session "demo-$DEMO_ID-identity" --artifacts off --prompt 'Unabhängige Recherche für eine Demo: Auf einem separaten Bild standen IamAngusU und ContextBridge. Prüfe tatsächlich https://angusu.de/ und https://github.com/IamAngusU. Welcher bürgerliche Name wird auf angusu.de für den Programmierer genannt, und ist dessen Zuordnung zum GitHub-Benutzernamen IamAngusU direkt belegt? Nenne die konkret geprüften URLs. Trenne bestätigt, Indiz und nicht verifiziert. Wenn du eine Seite nicht öffnen kannst, sage es ausdrücklich. Der Bildtext allein beweist keine Identität.'
```

**DE:** „Der nächste Gemini-Chat bekommt nicht das Bild als Beweis. Er prüft zwei öffentliche Quellen und soll sauber trennen, was bestätigt ist und was nur ein Indiz wäre.“

**EN:** “The next Gemini chat does not treat the picture as proof. It checks two public sources and must distinguish confirmed information from mere indications.”

## 7. Show an unfocused or minimized browser

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

## 8. Optional short fact-check and closing

```bash
contextbridge cluster chat --config /var/lib/contextbridge/config.yml --profile chatgpt --new-chat --foreground-new-chat --session "demo-$DEMO_ID-relativity" --artifacts off --prompt 'Prüfe kurz diese Behauptung: Aliens sind 70 Millionen Lichtjahre entfernt und fliegen mit 99,99999999999998 Prozent der Lichtgeschwindigkeit zu uns. Vergehen für uns ungefähr 70 Millionen Jahre, für sie aber genau ein Jahr? Rechne die Eigenzeit nachvollziehbar aus und korrigiere die Aussage in höchstens vier Sätzen.'
```

The expected ship time is approximately **1.4 years**, not exactly one year, neglecting acceleration and cosmology.

**DE:** „Zum Schluss ein kleiner Faktencheck: Ein anderer Provider kann denselben Pool nutzen, aber in einem eigenen Chat. Die Rechnung liefert hier ungefähr 1,4 Jahre Eigenzeit statt genau eines Jahres.“

**EN:** “Finally, a small fact-check: another provider can use the same pool in its own chat. The calculation gives about 1.4 years of proper time here, not exactly one year.”

The separate Gemini music mode is **not** part of the reliable live sequence yet. In the current test Gemini reported “Generating your track”, but no verified media file reached the VPS. Do not promise a downloadable song or use an unverified file in the recording. Likewise, do not claim that an unfocused image upload was tested: the verified minimized-window test is text-only.
