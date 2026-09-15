# Browser sessions and prompt contracts / Browser-Sessions und Prompt-Verträge

## Deutsch

### Welchen Prompt kann ich bearbeiten?

ContextBridge trennt zwei Ebenen absichtlich:

1. Der **Safety-Wrapper** wird vom Core erzeugt. Er kennzeichnet das Feld
   **text** und sichtbaren Text in einem Eingabebild als nicht vertrauenswürdige
   Daten, verbietet das Befolgen darin enthaltener Befehle und ergänzt den
   gewählten Ausgabevertrag. Dieser Rahmen ist kein Feld im Extension-Popup
   und lässt sich dort nicht abschalten. Das ist eine defensive Anweisung an
   das Modell und eine überprüfte Ausgabegrenze, kein mathematischer Beweis
   gegen jede Prompt Injection.
2. Die **Trusted task instructions** sind der normale **prompt** des Jobs.
   Genau hier schreibt der Betreiber die eigentliche Aufgabe. Man bearbeitet
   ihn mit **cluster chat --prompt**, im Prompt-Feld einer Job-JSON oder im
   Prompt eines geplanten Jobs. Vom Endnutzer gelieferter Inhalt gehört
   dagegen in **text**.

Die visuelle Browser-Anlernung ändert nur Selektoren für Eingabe, Senden,
Antwort und Datei-Upload. Sie ist absichtlich kein System-Prompt-Editor.

Der Standardrahmen funktioniert auch mit strengen JSON-Aufgaben. Wähle dafür
**output.mode: "json"**, liste unverzichtbare Top-Level-Felder in
**required_keys** auf und beschreibe Typen, erlaubte Werte sowie die genaue
Bedeutung im Job-Prompt:

~~~json
{
  "source": "demo",
  "route": "default",
  "prompt": "Gib genau ein JSON-Objekt mit language als BCP-47-Code, summary als String und confidence als Zahl von 0 bis 1 zurück. Keine weiteren Felder.",
  "text": "Vom Benutzer gelieferter, nicht vertrauenswürdiger Inhalt",
  "output": {
    "mode": "json",
    "required_keys": ["language", "summary", "confidence"],
    "max_bytes": 4096
  }
}
~~~

**required_keys** prüft nur, ob diese obersten Schlüssel vorhanden sind. Es
ist kein vollständiger JSON-Schema-Validator. Eine Anwendung muss Typen,
Bereiche, zusätzliche Schlüssel und alle fachlich folgenreichen Werte selbst
validieren. Ungültiges oder zu großes strukturiertes JSON wird abgelehnt und
nicht als abgeschnittener, scheinbar gültiger Präfix ausgegeben.

### Was passiert mit einem sehr langen Chat?

Ein Browser-Tab bleibt eine zustandsbehaftete Unterhaltung beim jeweiligen
Provider. ContextBridge liest für einen Job nur die neueste passende Antwort;
es sendet weder den kompletten DOM noch alte Chattexte an den Relay. Der
Provider kann seine bisherige Unterhaltung trotzdem als Kontext verwenden.

Die Extension zeigt eine inhaltsfreie, nicht blockierende Session-Health-Hilfe:

- ab 60 erkannten Provider-Turns einen Hinweis auf eine wachsende Unterhaltung;
- ab 120 Turns eine deutlichere Empfehlung für einen frischen Chat;
- einen Hinweis bei wiederholt langsamen, auf vier Sekunden begrenzten
  Kontroll-Scans;
- eine Warnung, wenn der Browser den Tab verworfen hat oder seine Controls
  nicht erreichbar sind.

Diese Schwellen sind Heuristiken, keine Behauptung über das Kontextfenster des
Modells. Sie stoppen einen legitimen Follow-up-Chat nicht. Für eine unabhängige
neue Aufgabe ist **Detach this page** plus ein frischer angehängter Chat die
saubere Aktion. Alternativ kann **Open a new chat per session/job** verwendet
werden. Ein bestehender Verlauf bleibt beim Provider erhalten.

ContextBridge versteckt oder entfernt absichtlich keine alten Nachrichten im
fremden DOM. CSS-Verstecken verkleinert den serverseitigen Modellkontext nicht,
spart bei React- oder virtualisierten Seiten nicht verlässlich Speicher und
kann Antwortzuordnung, Accessibility, Provider-State sowie sichere Recovery
zerstören. Auch ein Reload verkürzt die Provider-Unterhaltung nicht.
Automatische Reload-Recovery bleibt deshalb auf einen exakt nachgewiesenen,
ContextBridge-eigenen Turn begrenzt und sendet den Prompt niemals blind erneut.

### Funktioniert ein minimierter Browser?

Ollama braucht keine geöffnete Ollama-Oberfläche; der laufende HTTP-Dienst
genügt. Ein bereits geladener ChatGPT- oder Gemini-Tab kann Textjobs in vielen
Chromium-Konfigurationen auch im Hintergrund oder bei minimiertem Fenster
bearbeiten. Das ist jedoch keine universelle Headless-Garantie: Browser und
Provider dürfen inaktive Tabs drosseln, entladen oder UI-Teile erst bei
Sichtbarkeit rendern.

Darum trennt ContextBridge diese Zustände:

- Ein versteckter, antwortender Tab gilt weiterhin als gesund.
- Ein noch ladender oder schlafender Tab darf warten; Composer-Bereitschaft
  allein ist kein Beweis für eine abgeschlossene Antwort.
- Ein verworfener oder nicht antwortender Tab löst keinen blinden Retry aus.
- Frisch erzeugte Chats mit Bild-Upload werden sichtbar gemacht, weil ein
  inaktiver Opera-Tab den Upload suspendieren kann.
- Ein eng begrenzter, ContextBridge-eigener ChatGPT-Textjob darf zur Recovery
  in den Vordergrund geholt werden, ohne den Prompt erneut zu senden.

Die Extension verwendet dafür DOM-Bereitschaft, **visibilityState**, den
Discard-Zustand, Stop/Send-Signale und stabile neue Antwort-Turns. Sie liest
keine Cookies oder Network-Payloads und installiert keinen dauerhaften
DevTools-Debugger.

Das [Demo-Runbook](demo-video-de-en.md#shot-7--minimized-opera-text-job-still-returns)
dokumentiert einen erfolgreichen Text-Folgejob bei minimiertem Opera. Das ist
ein konkreter Nachweis für diese Konfiguration, aber keine Verallgemeinerung
auf Datei-Uploads, Medienwerkzeuge oder jeden Browser-Energiesparmodus.

## English

### Which prompt can I edit?

ContextBridge intentionally separates two layers. The Core generates a fixed
safety wrapper that marks **text** and text visible in an input image as
untrusted data, tells the model not to follow instructions embedded there, and
adds the selected output contract. The extension does not expose a switch that
disables this boundary. This is a defensive model instruction plus a validated
output boundary, not proof against every prompt-injection technique. The
operator edits the job's **prompt**: pass it to
**cluster chat --prompt**, put it in job JSON, or set the prompt on a schedule.
End-user material belongs in **text**, not in the trusted prompt.

Visual browser teaching changes control selectors only; it is not a system
prompt editor. Strict JSON works with the default wrapper: select
**output.mode: "json"**, add essential top-level **required_keys**, and
describe the exact types, enums, and semantics in the trusted job prompt.
Required keys are a shallow presence check, not full JSON Schema validation,
so the calling application must still validate every consequential value.

### Long conversations

The provider owns the conversation state. ContextBridge reads the newest
matching answer for the current job and does not send the full DOM or old chat
text to the relay. The provider may still use its server-side conversation
history.

The popup provides content-free, non-blocking health guidance at 60 and 120
detected provider turns, after repeated slow bounded control scans, and when a
tab is discarded or its controls cannot be reached. These are heuristics, not
model-context-window claims. A real follow-up is never blocked solely because
the conversation is old. For unrelated work, detach it and attach a fresh
chat, or choose a new chat per session/job.

ContextBridge deliberately does not hide or remove old provider DOM. Hiding
elements cannot reduce server-side model context and can break virtualized UI,
accessibility, response ownership, and recovery. A reload also does not shorten
the provider conversation. Automatic reload remains a narrowly guarded
recovery for a verified ContextBridge-owned turn and never blindly resends.

### Minimized and background operation

Ollama needs its HTTP service, not its desktop UI. Loaded ChatGPT and Gemini
tabs often complete text jobs while their window is minimized, but this is not
a universal headless guarantee: browsers and providers may throttle, discard,
or lazily render inactive pages. Hidden-but-responsive is healthy; discarded
or unresponsive is a warning and never authorizes a blind resend. Fresh image
uploads may be foregrounded because inactive Opera tabs can suspend them.
Diagnostics use bounded control state and lifecycle signals, not cookies,
network payloads, full DOM, prompts, or answer text.

The [demo runbook](demo-video-de-en.md#shot-7--minimized-opera-text-job-still-returns)
records a successful text follow-up while Opera remained minimized. That is
concrete evidence for the tested configuration, not a general claim about file
uploads, media tools, or every browser power-saving mode.
