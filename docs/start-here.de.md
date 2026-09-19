# ContextBridge: Aus deinen KI-Seiten werden ausführende Worker

Du hast einen PC mit einem Browser, vielleicht einen zweiten Rechner mit GPU und vielleicht einen VPS. Normalerweise leben diese Dinge nebeneinander: Du kopierst Prompts, wartest, lädst Dateien herunter und schiebst sie von Hand weiter. **ContextBridge verbindet sie zu einem kontrollierbaren Arbeitsfluss.** Eine App oder ein Terminal gibt einen Auftrag ab; ContextBridge wählt einen passenden Worker, beobachtet das Ergebnis und bringt es zurück.

> **🎬 Video direkt hier einfügen:** 20–30 Sekunden aus dem [kurzen Demo-Skript](demo-video-de-en.md). Zu sehen: VPS steuert zuerst ein lokales Ollama-Textmodell → ChatGPT-Tab auf dem Windows-PC erzeugt ein Bild → echte Bilddatei erscheint auf dem VPS → dieselbe Datei geht an das lokale Vision-Modell und danach an Gemini → beide Antworten erscheinen wieder im VPS-Terminal. Keine Tokens, privaten Chats oder Kontodaten im Bild. Ein Standbild aus dem Video dient als Vorschaubild; Beschriftung: „Ein VPS. Eigene Modelle und KI-Tabs. Eine verifizierte Datei.“

## Das Problem – und was ContextBridge anders macht

Ein offener KI-Tab ist für sich genommen noch kein verlässlicher Worker. Eine sichtbare Antwort kann unfertig sein. „Ich habe ein Bild erstellt“ ist keine Bilddatei. Ein Stop-Knopf kann hängen bleiben, ein Upload im inaktiven Tab warten, ein Anbieter ein Limit anzeigen. Gleichzeitig liegen eigene Modelle und GPUs oft auf anderen Geräten als die App, die sie braucht.

ContextBridge legt dazwischen eine kleine, überprüfbare Schicht:

1. **Du bestimmst die Quellen und Ziele.** Ein lokaler Job, eine App, ein Ordner oder später ein VPS kann Arbeit einreichen. Browser-Tabs werden nur durch ausdrückliches Anhängen Teil des Pools.
2. **Der Router prüft Fähigkeiten.** Text, Bildverständnis, Embeddings, Modellwahl, freie Slots, RAM und VRAM helfen bei der Auswahl. Eine harte VRAM-Anforderung wird nicht stillschweigend ignoriert.
3. **Der Worker liefert ein überprüfbares Ergebnis.** Text wird auf das gewünschte Ausgabeformat begrenzt. Bei angeforderten Bildern und Dateien zählen übertragene Bytes mit Hash, nicht die Behauptung des Modells.
4. **Fehler bleiben Fehler.** Ein sichtbares Rate Limit, eine unklare Browser-Situation oder ein fehlendes Artefakt wird nicht als Erfolg ausgegeben.

> **🖼️ Bild direkt nach dieser Liste:** einfache, breite Grafik `docs/assets/flow-overview.png`: links „App / Ordner / VPS“, in der Mitte „ContextBridge: Auftrag → Auswahl → Prüfung“, rechts drei klar getrennte Ziele „ChatGPT-Tab“, „Gemini-Tab“, „eigener PC mit Ollama/llama.cpp“. Ein Rückpfeil führt „Text / JSON / verifizierte Datei“ zur Quelle. Keine künstliche Cloud um lokale Modelle zeichnen: Bei ChatGPT/Gemini läuft die Inferenz weiterhin beim jeweiligen Anbieter.

Falls dir die Begriffe neu sind: Ein **Auftrag** ist ein Prompt mit Regeln für das Ergebnis. Ein **Worker** ist ein Computer, der ihn bearbeitet. Ein **Slot** ist ein gleichzeitig freier Arbeitsplatz auf diesem Worker. Ein **Pool** ist die Gruppe auswählbarer Worker. Ein **Relay** ist die gemeinsame Vermittlungsstelle zwischen deinem VPS und mehreren privaten Computern. Ein **Artefakt** ist eine tatsächlich zurückgegebene Datei – zum Beispiel ein PNG –, nicht bloß ein Satz, der eine Datei behauptet.

### Warum das interessant ist

- **Deine Browser-KI wird fernsteuerbar, ohne einen Anbieter-API-Schlüssel vorzutäuschen.** Du nutzt den tatsächlich angemeldeten, ausdrücklich gewählten Tab. Der Anbieter sieht die dorthin gesendeten Inhalte weiterhin und seine Nutzungsgrenzen gelten weiter.
- **Eine echte Bilddatei kann den Provider wechseln.** Der ChatGPT→VPS→lokales Ollama-Vision-Modell→Gemini-Weg wurde mit derselben Datei live getestet. Ein Artefakt muss als Datei zurückkommen und verifiziert werden.
- **Ein PC ist nicht die Grenze.** Private Worker verbinden sich ausgehend mit einem HTTPS-Relay; du musst zu Hause keinen eingehenden Router-Port für jeden PC öffnen.
- **Deine Hardware ist sichtbar.** Lokale Modelle, CPU-/GPU-Zustand, freie Speicherressourcen und belegte Slots werden im Terminal und Dashboard angezeigt. Ein erreichbares Ollama ohne geladenes Modell ist ausdrücklich nicht dasselbe wie ein bereiter Modell-Worker.
- **Sicheres Nichtwissen ist erlaubt.** Unbekannte Browser-Modelle bleiben `unknown`; ein unklarer, möglicherweise schon gesendeter Auftrag wird nicht blind erneut verschickt.

## In wenigen Minuten zum ersten Erfolg: ein Windows-PC und ein KI-Tab

Dieser Weg braucht **keinen VPS und keine eigene GPU**. Du brauchst Windows, Opera/Chrome/Edge oder einen anderen Chromium-Browser, einen angemeldeten ChatGPT- oder Gemini-Tab und die Berechtigung, dort Aufträge zu senden. Der Test schickt einen kurzen Prompt an diesen Anbieter.

> **Versionshinweis, bitte lesen:** Der Installer lädt die [neueste veröffentlichte GitHub-Release](https://github.com/IamAngusU/ContextBridge/releases/latest), nicht automatisch den neuesten Commit auf `main`. Dieser Guide wird für **v0.5.77** vorbereitet. Der einfache Texttest unten funktioniert mit älteren 0.5.x-Ständen; das vollständige Demo-Runbook mit frischen Chats, verifizierter Bildübergabe und den aktuellen Sicherheitsgrenzen benötigt mindestens **0.5.66**. Prüfe vor der Demo mit `contextbridge version`, was wirklich installiert ist. Falls die verlinkte neueste Release noch darunter liegt, ist 0.5.77 noch nicht öffentlich veröffentlicht – ein Commit oder lokal geladener Ordner ist keine Release.

### Schritt 1: Installieren

Öffne **PowerShell**, füge genau diesen Befehl ein und drücke Enter:

```powershell
irm https://angusu.de/contextbridge/install.ps1 | iex
```

Der Installer fragt nach zwei Entscheidungen. Für den einfachsten Browser-Start wähle **3 – browser tab** als erstes Ziel und **1 – local bridge only** für dieses Gerät. Er legt die Dateien standardmäßig unter `%LOCALAPPDATA%\ContextBridge` ab, erzeugt eine private Konfiguration, richtet den lokalen Dienst ein und zeigt den Pfad zur Erweiterung. Er prüft die Prüfsumme des heruntergeladenen Release-Archivs. Der Befehl führt ein Skript aus dem Internet aus: Nutze ihn nur, wenn du der angegebenen Domain vertraust. Wer Skripte vorher lesen möchte, findet [Installer und Releases im öffentlichen Repository](https://github.com/IamAngusU/ContextBridge).

> **🖼️ Screenshot hier:** die beiden **aktuellen** Installer-Fragen mit markierter Auswahl `3` und `1`; keine Konfiguration, Tokens oder persönlichen Pfade zeigen. Dateivorschlag `docs/assets/install-windows-choices.png`.

Auf Linux oder macOS lautet der öffentliche Installer-Befehl:

```bash
curl -fsSL https://angusu.de/contextbridge/install.sh | sh
```

Auch dort für den ersten lokalen Browser-Test Ziel **3** und Gerätemodus **1** wählen. Die folgenden Klicks beschreiben Opera; bei anderen Browsern heißen die Menüs ähnlich.

Linux ist ein nativer Relay-, Worker- und CLI-Einsatz mit AMD64/ARM64-Builds,
systemd-Setup und Ubuntu-CI – nicht nur ein Fernsteuer-Terminal für Windows.
macOS hat native Builds, LaunchAgent, Metal-Erkennung und eine macOS-CI-Matrix;
die finale Prüfung von v0.5.70 umfasst dort nur Cross-Builds, keine Ausführung
auf einem Mac. Es gibt noch keine gepflegte physische Mac-E2E-Matrix
für Metal, Ollama und alle Browser-Lifecycle-Fälle. Safari wird von der
Erweiterung derzeit nicht beansprucht. Details stehen im
[Plattformabschnitt des README](../README.md#platform-support-and-evidence).
Für unbeaufsichtigte Linux-/macOS-Worker gibt es außerdem eine
[nichtinteraktive Installation mit expliziten Umgebungsvariablen](headless-worker-install.md);
auch dort bleibt die Freigabe am Relay ein eigener Schritt.

### Schritt 2: Die Erweiterung laden

1. Öffne in Opera `opera://extensions`.
2. Aktiviere **Developer mode / Entwicklermodus**.
3. Klicke **Load unpacked / Entpackte Erweiterung laden**.
4. Wähle den **Chromium extension**-Ordner, den der Installer am Ende ausgegeben hat. Beim Standard-Windows-Pfad ist das `%LOCALAPPDATA%\ContextBridge\extension\chromium` – der Ordner selbst, nicht eine einzelne Datei darin.
5. Öffne einen **neuen, leeren** ChatGPT- oder Gemini-Chat. Klicke das ContextBridge-Symbol und dann **Connect this AI page**. Wenn du zuerst mehrere Seiten vorbereiten möchtest: auf jeder Seite **Attach this page**, am Ende einmal **Connect**.

Der **Windows-Installer** legt beim Browser-Setup den lokalen Pairing-Token in die Zwischenablage. Fragt das Popup danach, füge ihn unter **Advanced setup and diagnostics** ein. Auf Linux/macOS liest du ihn bei Bedarf aus der privaten `config.yml`, deren Pfad der Installer ausgibt. Das ist ein Geheimnis für die Verbindung zum **lokalen Dienst**, kein ChatGPT- oder Gemini-Passwort. Zeige ihn nicht in Screenshots, Videos, Issues oder Logs. **Connected** muss eine frische, bestätigte Verbindung bedeuten; nur ein geöffnetes Popup ist noch keine Verbindung.

> **🖼️ Zwei aktuelle Screenshots nebeneinander:** (A) Opera-Erweiterungsseite mit „Load unpacked“ und sichtbar ausgewähltem `chromium`-Ordner; (B) ContextBridge-Popup auf einer **leeren** KI-Seite mit „Connect this AI page“ beziehungsweise nachher „Connected“. Keine alte v0.2-Popup-Grafik verwenden. Dateivorschläge `docs/assets/install-opera-unpacked.png` und `docs/assets/popup-connected-current.png`.

### Schritt 3: Prüfen, ob alles lebt

Öffne unter Windows eine **neue** PowerShell, damit der vom Installer ergänzte `PATH` aktiv ist. Unter Linux/macOS öffne ein neues Terminal; falls `contextbridge` dort noch nicht gefunden wird, beachte den vom Installer ausgegebenen Hinweis zu `~/.local/bin` im `PATH`. Diese drei Befehle ändern keine KI-Unterhaltung:

```powershell
contextbridge version
contextbridge doctor
contextbridge status
```

`version` zeigt die wirklich installierte Version. `doctor` sagt dir, welcher Setup-Schritt fehlt. `status` zeigt Dienst, Browser und Ressourcen. Für die lebendige Terminalansicht:

```powershell
contextbridge console
```

Der Installer richtet außerdem den kurzen, identischen Befehl `cb` ein, sofern
dieser Name nicht bereits einem anderen Programm gehört. Er überschreibt nie
einen fremden `cb`-Befehl. In einer neu geöffneten PowerShell, Bash oder Zsh
ergänzt Tab die bekannten Befehle, Unterbefehle und Optionen; die statischen,
prüfbaren Skripte lassen sich mit
`contextbridge completion powershell|bash|zsh` auch nur ausgeben.

Tippe dort `exit` und Enter: **nur die Ansicht** schließt sich, der verwaltete Dienst läuft weiter. Soll der normale lokale Dienst wirklich beendet werden, nutze `contextbridge stop`; der authentifizierte lokale Befehl verweigert das Stoppen, solange lokale, Relay- oder Worker-Arbeit läuft. Nur `contextbridge stop --force` unterbricht ausdrücklich laufende Arbeit. Ein eigenständig gestarteter `relay`- oder `worker`-Prozess ohne lokalen Bridge-Dienst wird weiter über seinen Service-Manager oder Ctrl+C beendet. `contextbridge run` startet einen Dienst im Vordergrund; starte ihn nicht zusätzlich, nur um das Terminal zu sehen. Für die grafische Ansicht öffnet `contextbridge dashboard` das lokale Dashboard.

Im Panel sind lange GPU- und Modelllisten pro Worker zunächst eingeklappt.
`details 1` blendet beides für Node 1 ein, `gpus 1` oder `models 1` nur den
jeweiligen Teil. Mit `all` statt `1` gilt der Toggle für alle angezeigten
Nodes. `help` zeigt die Befehle lesbar auf mehreren Zeilen, `clear` leert nur die sichtbare
Sitzungshistorie und `exit` beendet ausschließlich diese Ansicht.

> **🖼️ Bild hier:** aktueller Terminalausschnitt mit Online-Dienst, einem angebundenen Tab, belegten/freien Slots und getrenntem Verlauf. Einen leeren/„unknown“-Modellstatus nicht retuschieren. Dateivorschlag `docs/assets/first-connected-console.png`.

### Schritt 4: Dein erstes echtes „Hallo“

Bleib in **PowerShell**. Kopiere diesen ganzen Block einschließlich `@'` und `'@`:

```powershell
@'
{"source":"first-test","route":"default","task":"generation","prompt":"Reply with exactly BRIDGE-OK and no other words.","output":{"mode":"text","max_bytes":1024}}
'@ | contextbridge submit --file -
```

Auf Linux/macOS ist derselbe Test im Terminal:

```bash
contextbridge submit --file - <<'JSON'
{"source":"first-test","route":"default","task":"generation","prompt":"Reply with exactly BRIDGE-OK and no other words.","output":{"mode":"text","max_bytes":1024}}
JSON
```

ContextBridge liest das JSON über die Standardeingabe der PowerShell-Pipeline, schickt es über die konfigurierte Standardroute und druckt das Ergebnis. Bei der Browser-Auswahl aus Schritt 1 sollte der **angehängte** KI-Tab den Prompt erhalten. Die genaue JSON-Hülle der Antwort kann je nach Ausgabeweg variieren; suche darin nach `BRIDGE-OK`. Wenn der Anbieter andere Worte schreibt, wird das als dessen Antwort sichtbar – ContextBridge verspricht keine perfekte Befolgung eines Prompts. Wenn gar kein Tab bereit ist, prüfe `contextbridge doctor` und den Popup-Status, statt den Befehl mehrfach unkontrolliert zu senden.

**Geschafft:** Du hast einen strukturierten Auftrag über den lokalen Dienst an einen ausdrücklich verbundenen Browser-Worker geschickt und eine Antwort zurückbekommen. Mehr ist für den ersten Erfolg nicht nötig.

### Noch ein sicherer Copy-Paste-Test

Dieser zweite Auftrag enthält nur einen erfundenen Satz und fordert keine Datei, Recherche oder Aktion außerhalb des Chats an. Er demonstriert, dass dein Terminal nicht auf einen einzigen fest eingebauten Testprompt beschränkt ist:

```powershell
@'
{"source":"guide-mini-test","route":"default","task":"generation","prompt":"Fasse diesen erfundenen Satz auf Deutsch in höchstens sieben Wörtern zusammen: Ein grüner Roboter trägt drei rote Äpfel über eine Brücke.","output":{"mode":"text","max_bytes":2048}}
'@ | contextbridge submit --file -
```

Auch dieser Test ist ein echter Website-Auftrag und kann auf das Kontingent deines KI-Anbieters zählen. Ein Anbieterlimit ist kein Grund, denselben Prompt blind immer wieder abzusenden.

### Wo du den eigentlichen Prompt bearbeitest

`prompt` enthält deine vertrauenswürdige Aufgabe und ist je Job frei
bearbeitbar – in `cluster chat --prompt`, einer Job-JSON oder einem geplanten
Job. Von Endnutzern gelieferter Inhalt gehört in `text`. Der Core setzt davor
immer einen defensiven Safety-Wrapper und ergänzt den gewählten
Ausgabevertrag; die Extension kann diesen Rahmen nicht abschalten und die
visuelle Anlernung ändert nur Bedienelement-Selektoren. Strenges JSON ist
trotzdem möglich, wenn du `output.mode: "json"`, erforderliche Top-Level-Keys
und Typen/Bedeutung im Job-Prompt angibst. Diese Grenze ist Defense in Depth,
kein Versprechen, dass eine einzelne Modellanweisung jede Prompt Injection
verhindert. Ein vollständiges Beispiel steht unter
[Browser-Sessions und Prompt-Verträge](browser-sessions-and-prompts.de-en.md).

## Was du danach ausprobieren kannst

Die folgenden **lesenden** Befehle sind ein guter Rundgang, auch wenn noch keine eigene GPU oder kein Modell installiert ist:

```powershell
contextbridge hardware
contextbridge models
contextbridge status
contextbridge update status
```

`hardware` zeigt das Gerät; `models` trennt gefundene von geladenen Modellen; `status` zeigt den aktuellen Weg und Messwerte; `update status` zeigt, ob verifizierte Updates aktiviert sind. Updates sind standardmäßig **aus** und werden nicht durch einen Dashboard-Besuch eingeschaltet. Ein neuer Release-Ordner für eine entpackte Erweiterung muss im Browser separat neu geladen werden; eine Chat-Seite musst du dafür nicht neu laden.

### Aufträge, die über reinen Text hinausgehen

| Idee | Was ContextBridge daran übernimmt | Voraussetzung |
| --- | --- | --- |
| Eine App schickt kurze Texte oder JSON zur Klassifikation | Route, Ausgabeformat, Begrenzung und protokollierten Fehlerstatus | kompatibler lokaler oder Browser-Provider |
| ChatGPT erstellt ein Bild; eigenes Vision-Modell und Gemini lesen es | tatsächliche Datei zurückholen, Hash prüfen, erneut anhängen | passende Modelle, Anbieterfunktionen und aktuelle Browser-Unterstützung |
| Ein Support-Ordner bekommt `.result.json` neben jedem Job | atomisches Abholen, Ergebnisdatei und nachvollziehbarer Status | konfigurierte Inbox und Produzent |
| Mehrere eigene PCs bearbeiten unabhängige Aufträge | Worker-Pool, Slots, Fähigkeits- und Ressourcenwahl | HTTPS-Relay und gepaarte Worker |
| Ein täglicher Job erzeugt einen Text und ein zweiter prüft ihn | Uhrzeit/Zeitzone, Pause/Fortsetzen, Verlauf, begrenzte Folgeschritte | lokaler [Schedule](schedules.md) und passender Provider |
| Eigene Ollama-/GGUF-Modelle liefern Text, Vision oder Embeddings | lokaler Route- und Hardwarestatus | Modell tatsächlich installiert und bereit |

Wenn **mindestens 0.5.66 installiert** ist und du ein Relay mit ChatGPT- und Gemini-Worker verbunden hast, führt das [copy-paste-fertige Demo-Runbook](demo-de-en.md) die Bild→VPS→Gemini-Kette Schritt für Schritt aus. Das kürzere [Video-Skript](demo-video-de-en.md) zeigt, welche Teile für eine Aufnahme wirklich stark sind. Bild und Text in einem Auftrag können je nach Webanbieter unterschiedlich priorisiert werden; das Runbook trennt Bildgenerierung und OCR deshalb bewusst in zwei Jobs.

> **🎬 Längeres Demo-Video hier einfügen:** ungefähr 3 Minuten, ehrlich geschnitten, mit sichtbarer echter Jobdauer; erst Konsole/Attach und lokaler Ollama-Text, dann ChatGPT-Bilddatei, lokales Vision-Modell und Gemini-OCR, danach Textjob bei minimiertem Opera, zuletzt Dashboard. Tonspur DE oder EN; Untertitel für die andere Sprache. Dateivorschlag `docs/assets/contextbridge-demo.mp4`, Vorschaubild `docs/assets/contextbridge-demo-poster.webp`. In der Video-Beschreibung auf [Befehle und DE/EN-Sprechtext](demo-video-de-en.md) verlinken.

## Wie die Verbindungen funktionieren – ohne Token-Salat

Für einen einzelnen PC ist der Weg kurz:

```text
Deine App / PowerShell
        ↓ Auftrag an 127.0.0.1 mit lokalem Token
ContextBridge-Dienst auf deinem PC
        ↓ ausdrücklich angehängter Tab ODER lokales Modell
Ergebnis zurück an deine App
```

Für einen VPS oder mehrere PCs kommt ein **HTTPS-Relay** hinzu:

```text
App / VPS (Producer-Token)
          ↓ HTTPS
Relay (Queue, Rechte, Auswahl)
          ↑ ausgehende WSS-Verbindungen der privaten Worker
PC A: Browser-Tabs + Ollama      PC B: eigene GPU + Modelle
          ↓
Ergebnis über das Relay zurück zum berechtigten Producer
```

> **🖼️ Diagramm genau hier:** vier kleine, gleich gestaltete Teilbilder `docs/assets/topologies.png`: `1:1` eine App/ein Worker; `N:1` mehrere berechtigte Apps/ein Worker; `1:N` eine App/mehrere Worker; `N:N` mehrere Apps/mehrere Worker. Pfeile von privaten PCs zeigen **ausgehend** zum Relay. Unter dem Relay steht „ein dauerhaftes Koordinationssystem, keine aktive Multi-Relay-Replikation“.

Die drei Schlüsselarten erfüllen verschiedene Aufgaben:

| Schlüssel | Wer hat ihn? | Wofür? |
| --- | --- | --- |
| **Lokaler Pairing-Token** | Dienst und deine Browser-Erweiterung auf demselben PC | Authentifiziert die Verbindung zu `127.0.0.1` und dem lokalen Dashboard. Nicht an andere PCs weitergeben. |
| **Worker-Identität / Node-Token** | ein gepaarter Worker und sein Relay | Erlaubt genau diesem Gerät, sich ausgehend beim Relay zu melden. Für einen zweiten unabhängigen Relay eine eigene Identität verwenden. |
| **Producer-Token** | eine einreichende App oder ein VPS-CLI-Client | Darf Aufträge im erlaubten Scope einstellen und eigene Ergebnisse lesen. Nicht als Browser-Pairing-Token benutzen. |

Beim ersten Cluster-Setup wählst du auf dem Server **Relay** und gibst ihm eine echte HTTPS-Adresse mit Reverse Proxy; auf jedem PC wählst du **Worker**, trägst diese Adresse ein und bestätigst seinen einmaligen Pairing-Code am Relay. Danach zeigt `contextbridge cluster status` die Online-Knoten. **Den lokalen Port 32145 nicht einfach öffentlich ins Internet stellen.** Der [Architektur-Guide](architecture.md), [Sicherheits-Guide](security.md) und die [Mehr-Relay-Anleitung](multiple-servers.md) erklären die produktive Einrichtung und Grenzen.

### Welche Pool-Form passt zu dir?

- **1:1:** ein Programm, ein Worker. Gut für einen privaten PC.
- **N:1:** mehrere berechtigte Programme teilen einen Worker. Sie bekommen getrennte Producer-Rechte; ein einzelner Browser-Tab erledigt dennoch nur **einen UI-Job gleichzeitig**.
- **1:N:** eine App verteilt unabhängige Jobs auf mehrere PCs oder Tabs. Anforderungen an Modell, Aufgabe, Gruppe und gegebenenfalls freien VRAM filtern Kandidaten.
- **N:N:** mehrere Apps nutzen einen Pool mehrerer Worker über **ein** Relay. Jobs und Ergebnisse gehören weiterhin ihrem Producer; ein öffentlicher `session_id` allein berechtigt nicht zum Lesen fremder Gespräche.

Ein einzelner PC darf mehreren **getrennten** Relays beitreten. Dabei laufen derzeit getrennte Worker-Prozesse ohne gemeinsamen hardwareweiten Slot-Zähler; du musst ihre Summen selbst so wählen, dass RAM/VRAM nicht überbucht werden. Zwei Relays sind auch **keine** automatisch replizierte Hochverfügbarkeits-Installation. Innerhalb eines Relays zählt ein PC mit zwei GPUs nicht automatisch als zwei unabhängige Browser-Tabs: Der Scheduler meldet jede GPU, prüft eine harte `min_free_vram_bytes`-Anforderung gegen **eine** ausreichend freie Karte und nutzt sonst VRAM nur als weiche Präferenz. Er reserviert keine nummerierte GPU und addiert VRAM nicht still über Karten hinweg. Ohne harte VRAM-Anforderung bleibt ein kompatibler Zero-GPU-/CPU-Worker zulässig. Browser-Tabs laufen seriell **je Tab**, mehrere Tabs können parallel arbeiten. Lokale Modelle können andere Nebenläufigkeit haben; ein Rack wird als mehrere gepaarte Nodes abgebildet.

CPU-Last, freie RAM-Menge, Temperatur, GPU-Auslastung und freier VRAM sind
Live-Werte des ganzen Geräts. Sie gehören nicht automatisch ContextBridge oder
einem einzelnen Job. Per-Job-Peaks werden nur gezeigt, wenn eine Engine sie
ausdrücklich mit `resource_scope: "job"` als zurechenbar liefert; fehlende
Messungen bleiben unbekannt.

### Sobald ein VPS verbunden ist: ein kleiner Fern-Test

Diese beiden Befehle gehören **auf den VPS**, nicht in den KI-Tab. Voraussetzung: Relay und Worker sind gepaart, und das VPS-Terminal hat eine Producer-Berechtigung (siehe [Cluster-Setup](../README.md#build-a-compute-cluster)). Erst den Pool nur lesen, dann bewusst genau **einen** Textjob senden:

```bash
contextbridge cluster status
contextbridge cluster chat --profile gemini --artifacts off --prompt 'Antworte mit einem kurzen deutschen Satz: Was macht ein Worker-Pool?'
```

Erscheint bei `cluster status` kein Online-Worker oder ist kein Gemini-Tab bereit, **nicht** auf Verdacht absenden: zuerst Pairing, Verbindung und Tabstatus prüfen. `--artifacts off` passt hier, weil der Auftrag nur Text verlangt. Für Bilder muss stattdessen ein echter Speicherordner angegeben werden; die [Bild-Demo](demo-video-de-en.md) zeigt die Befehle und den Speicherort.

## Die Funktionen im Terminal: kleiner Spickzettel

| Befehl | Wann er hilft |
| --- | --- |
| `contextbridge help` | Alle Hauptbefehle anzeigen. |
| `contextbridge completion powershell\|bash\|zsh` | Statische Autovervollständigung ausgeben; der Installer aktiviert sie standardmäßig für neue Shells. |
| `contextbridge doctor` | Eine konkrete fehlende Setup-Voraussetzung finden. |
| `contextbridge console` | Live-Ansicht an den laufenden Dienst anhängen; `exit` schließt nur diese Ansicht. |
| `contextbridge stop` | Normalen lokalen Dienst idle-sicher beenden; `--force` ist der bewusste Unterbrechungs-Override. |
| `contextbridge status` | Einmaliger lokaler Status mit Routen und Messwerten. |
| `contextbridge dashboard` | Lokales Dashboard mit Browser, Modellen, Hardware, Queue, Schedules und Verlauf öffnen. |
| `contextbridge hardware` / `contextbridge models` | Eigene Ressourcen und Modellbereitschaft lesen, ohne ein Modell zu laden. |
| `contextbridge submit --file job.json` | Einen strukturierten lokalen Auftrag abgeben; `--file -` liest Standard-Eingabe. |
| `contextbridge schedule list` | Geplante lokale Jobs und nächste Ausführung sehen. [Details](schedules.md). |
| `contextbridge cluster status` | Nach Relay-Konfiguration alle verbundenen PCs/Slots sehen. |
| `cb selftest` | Begrenzt auf lokale Engine, passende Tabs und freie Slots warten; standardmäßig keine KI-Anfrage senden. `contextbridge cluster selftest` ist die identische Langform. [Details](selftest.md). |
| `contextbridge cluster chat` | Nach Relay-Konfiguration einen Terminal-Chat an einen ausgewählten Provider senden. |
| `contextbridge browser inspect` | Kleine, inhaltsarme Diagnose für einen angehängten Browser-Tab lesen. |
| `contextbridge update status` | Updatezustand lesen; `enable` ist eine eigene bewusste Entscheidung. |

`cluster chat` kann mit `--profile chatgpt` oder `--profile gemini` einen Browser-Provider verlangen, mit `--model` und `--reasoning` eine **sichtbar angebotene** Option anfordern, und mit demselben `--session` Folgeaufträge in derselben reservierten Unterhaltung halten. `--new-chat` verlangt eine neue Unterhaltung; es überschreibt also den Wunsch „nutze genau diesen bereits angehängten Tab“. In der Erweiterung heißt die passende manuelle Einstellung **New browser session → Use an unassigned attached tab**. Wenn mehrere passende Tabs frei sind, ist der gerade sichtbare Tab nicht automatisch fest gepinnt; für einen gezielten Test nur diesen Provider-Tab angehängt lassen.

## Wie schnell ist es wirklich?

ContextBridge beschleunigt **nicht** magisch die Inferenz von ChatGPT, Gemini oder deinem lokalen Modell. Es spart Handarbeit, erlaubt unabhängige Jobs parallel auf passenden Slots und misst, was passiert. In einem einzelnen Live-Durchlauf am 14.09.2026 mit einem Windows-Worker und externen Browseranbietern wurden beobachtet:

| Auftrag | Beobachtete End-to-End-Dauer |
| --- | ---: |
| VPS → lokales Ollama `qwen2.5:latest` → VPS | 4,5 s |
| ChatGPT-Bild bis zur gespeicherten Datei auf dem VPS | 24,9 s |
| VPS-Bild → lokales Ollama-Vision-Modell `qwen2.5vl:7b` → VPS | 18,1 s |
| Gemini-OCR mit dieser angehängten Bilddatei | 38,6 s |
| Gemini-Textantwort bei minimiertem Opera | 22,6 s |

**Das sind einzelne Beispielmessungen, kein Benchmark, Mittelwert, SLA oder Versprechen für deinen Account.** Anbieterlast, Modell, Tarif, Tabzustand, Netzwerk und Dateigröße ändern die Zeit. Das lokale Dashboard zeigt unter anderem Jobanzahl, beobachtete durchschnittliche Latenz und Fehlerrate; Cluster-Status zeigt abgeschlossene/fehlgeschlagene Jobs und kumulierte Rechenzeit. Die Aufschlüsselung nach angefordertem Provider/Modell/Denkstufe ist eine **Konfigurationsmetrik**, kein Beweis, dass ein Webanbieter intern genau dieses Modell benutzt hat. Für faire Messungen: gleiche Aufgaben, gleiche Provider und Modellwahl, mehrere Wiederholungen, auch Fehlschläge mitzählen. Ein leerer oder gestörter Tab ist kein „schneller“ Null-Sekunden-Erfolg.

> **🖼️ Metrik-Bild hier:** aktueller Dashboard-Ausschnitt mit Queue-Zähler, „Average latency“, „Failure rate“ und Recent activity. Neben dem Bild als Bildunterschrift: „Messwerte deiner Installation, keine globale Leistungszusage.“ Vor Aufnahme Tokens, Prompt-Inhalte, Konten und fremde Job-IDs ausblenden. Dateivorschlag `docs/assets/metrics-current.png`.

## Ehrliche Grenzen

- **Browseranbieter bleiben Browseranbieter.** Login, Abonnement, Quoten, Rate Limits, Captchas, Nutzungsbedingungen und UI-Änderungen gelten weiter. ContextBridge ersetzt weder deren Inferenz durch deine GPU noch gibt es unbegrenzte kostenlose Nutzung.
- **Nicht jeder Tab ist sofort ein Worker.** Nur ausdrücklich angehängte und verbundene Seiten sind im Pool. Automatisch angelegte Chats brauchen freie Plätze; bei 16 angehängten Tabs ist Schluss. Ein neuer Hintergrund-Tab kann je nach Browser erst verspätet UI bereitstellen. Bild-Uploads werden deshalb bei neu erzeugten Chats sichtbar gestartet. Ein erfolgreicher minimierter **Text**job beweist keinen minimierten Medien-Upload.
- **Nicht jede Fähigkeit ist verifiziert.** ChatGPT/Gemini sind automatisch erkannt, andere Seiten benötigen ein visuell gelehrtes Profil. Die separate Gemini-Videoerzeugung ist noch ungetestet. Gemini Music hat in früheren Tests echte MP4-Audiodateien geliefert, ein späterer Lauf lieferte aber trotz „Generating your track“ keine verifizierte Datei; fordere `min_media` an und behandle einen Fehlschlag als Fehlschlag.
- **„Connected“ ist keine Fertigmeldung.** Die Erweiterung kann verbunden sein, während ein bestimmter Tab lädt, ein Modell unbekannt ist oder ein Job am Anbieter scheitert. Diagnostics und Status zeigen diese Unterschiede.
- **Geplante Aufträge laufen nicht auf gut Glück doppelt.** Nach einem Neustart kann ein bereits an eine Website gesendeter Lauf `interrupted` sein; er wird nicht blind erneut gesendet. Pause verhindert künftige Starts, stoppt aber keinen schon gesendeten Prompt. Fallback auf ein anderes Browser-Modell passiert nur vor Send, wenn das Menü den verlangten Eintrag ausdrücklich als nicht verfügbar zeigt; es gibt keine universelle automatische „eine Denkstufe tiefer“.
- **Cluster-Ausführung ist at-most-once, nicht exactly-once.** Ein Workerfehler, Disconnect oder Execution-Timeout ist nach möglichem Provider-Send mehrdeutig und führt deshalb zum Fehlschlag statt zur automatischen Neuausführung. Der Provider kann den Auftrag trotzdem beendet haben, obwohl ContextBridge das Ergebnis nicht sicher bekam. Prüfe den Status und reiche bewusst einen neuen Job ein; nur die Zustellung eines bereits erzeugten Completion-Payloads darf wiederholt werden, ohne den Provider erneut auszuführen.
- **Verschlüsselung hat Grenzen.** TLS schützt den Weg zum Relay; optionales E2EE kann dessen Einblick in Prompt/Ergebnis verhindern. Ein ChatGPT- oder Gemini-Job muss trotzdem beim jeweiligen Webanbieter lesbar ankommen. E2EE macht transparentes Failover auf einen anderen Worker unmöglich und deaktiviert Klartext-Live-Streaming.
- **Ein Relay ist kein Hochverfügbarkeits-Cluster.** Eine einzelne BoltDB-Relay-Instanz ist dauerhaft, aber nicht active-active repliziert. Mehrere unabhängige Relays koordinieren aktuell keine gemeinsame Hardware-Budgetgrenze.
- **Fehler sind sichtbar.** Ein Providerfehler, eine nicht sicher zugeordnete Antwort oder eine nicht abgeholte Datei werden nicht zu einem erfolgreichen Ergebnis umetikettiert. Für Moderation wird unsichere/ungültige Ausgabe zu `review`, nicht zu `allow`.

## Wenn etwas nicht klappt

1. **`contextbridge` wird nicht gefunden:** eine neue PowerShell öffnen. Der Installer ergänzt den **Benutzer-PATH**; ein bereits geöffnetes Terminal kennt ihn noch nicht. Alternativ die im Installer ausgegebene `contextbridge.exe` direkt starten.
2. **Erweiterung zeigt „Connect“:** erst den gewünschten KI-Tab **Attach this page**, dann **Connect**. Bei einem Tokenfehler den lokalen Token aus der zugehörigen `config.yml` erneut einsetzen; keinen Producer-Token verwenden. `contextbridge doctor` nennt lokale Blocker.
3. **Tab wird nicht genutzt:** Ist er angehängt, leer/unbelegt und passt `--profile`? `--new-chat` fordert absichtlich einen **anderen, neuen** Chat. Bei mehreren passenden Tabs ist der sichtbare Tab nicht garantiert der gewählte.
4. **Modell steht auf „unknown“:** nicht raten. Tab einmal aktiv anzeigen, Popup-Diagnose oder `contextbridge browser inspect` prüfen. Wenn das Browser-Menü die Wahl nicht sichtbar macht, kann ContextBridge sie nicht sicher bestätigen.
5. **Bild/Datei fehlt:** nur ein echter gespeicherter Pfad ist ein Artefakt. Prüfe `Saved artifact:` und die Datei. Eine URL oder Textbehauptung erfüllt `--image`/`min_images` nicht.
6. **Nach einem Update:** bei einer entpackten Erweiterung im Erweiterungsmenü **Neu laden** klicken, falls die neue Version noch nicht aktiv ist. Eine laufende Chat-Seite dafür nicht blind neu laden; Entwürfe und nicht beendete Jobs prüfen.
7. **Schreibst du einen Bugreport?** Version, Browser, Provider, sichtbare Fehlermeldung und groben Zeitpunkt notieren. Keine vollen Chat-DOMs, Pairing-Tokens, privaten Prompts oder Browser-Cookies veröffentlichen. Die [bekannten Browser-Randfälle](known-browser-edge-cases.md) beschreiben sichere Diagnosen.

## Medien-Briefing für README und Website

| Stelle | Medium | Was genau sichtbar sein soll |
| --- | --- | --- |
| Direkt unter der Überschrift | Hero-Video + Poster | VPS → lokales Ollama-Modell → ChatGPT-Bild → gespeicherte Datei → Ollama-Vision + Gemini-OCR → VPS-Ergebnisse, 20–30-s Kurzfassung. |
| Nach „Problem und Lösung“ | horizontale Flussgrafik | Eine Quelle, Router/Prüfung, Browser- und lokale Ziele, Rückweg der verifizierten Ausgabe. |
| Bei Installation | zwei aktuelle Screenshots | Installer-Auswahl und Opera „Load unpacked“/Popup „Connected“, ohne Token. |
| Bei Pooling | 1:1-/N:1-/1:N-/N:N-Grafik | Mehrere Apps/PCs und **ausgehend** verbundene Worker; kein irreführendes offenes Heimnetz. |
| Bei Geschwindigkeit | echtes Dashboard-Screenshot | Latenz, Fehlerrate, Job-History, leere Queue ehrlich als leer. |
| Nach dem ersten Erfolg | volles Demo-Video | 2–3 Minuten ehrlich geschnitten, DE/EN-Untertitel, Befehle im [Video-Skript](demo-video-de-en.md). |

Bis die neuen Medien aufgenommen sind, sind die markierten Briefings **Platzhalter, keine angeblich vorhandenen Dateien**. Das ältere [Dashboard-Beispielbild](assets/contextbridge-dashboard.png) zeigt nur die Art der Oberfläche; das alte [Popup-Bild](assets/extension-popup.png) ist für die aktuelle 0.5.x-Erweiterung veraltet und soll nicht als Installationsanleitung verwendet werden.

## Ein Satz zum Schluss

**ContextBridge macht aus ausgewählten KI-Tabs und eigenen Computern keine Magie, sondern einen beobachtbaren Pool: Aufträge hinein, passende Worker wählen, echte Ergebnisse prüfen, Fehler ehrlich zeigen.** Starte mit einem Tab und `BRIDGE-OK`; erweitere erst danach auf deine Modelle, GPUs, Rechner und Server.
