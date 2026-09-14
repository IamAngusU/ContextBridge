# Local schedules and due queue

ContextBridge stores schedules in the owner-only `schedules.json` file in the configured data directory. Schedules are local to this service and require the same bearer token as `/v1/jobs`. The routine `/v1/status` snapshot includes schedule state but never the prompt or job metadata; full details are available only from `/v1/schedules`.

Create a schedule in the local dashboard, or save this example as `schedule.json` and run `contextbridge schedule add --file schedule.json`:

```json
{
  "name": "Weekday summary",
  "job": {
    "route": "browser-chatgpt",
    "provider": "browser",
    "model": "GPT-5.6 Sol",
    "reasoning": "Sehr hoch",
    "prompt": "Summarize today's open items in five sentences.",
    "output": { "mode": "text" }
  },
  "timing": {
    "type": "weekdays",
    "time": "09:00",
    "timezone": "Europe/Berlin"
  },
  "fallback": {
    "models": ["GPT-5.5"],
    "reasoning": ["Hoch"]
  }
}
```

Use a route that exists in your own `config.yml`; `browser-chatgpt` is only an example. Omit `provider` to use the route's normal provider policy. The optional `fallback` lists are ordered and are tried only in the browser tab's actual menu before the prompt is submitted, and only when a requested choice is explicitly missing or disabled. Missing selectors, unknown model metadata, a timeout, or an ambiguous already-submitted turn never trigger a fallback. The service does not infer a universal "one reasoning level lower" because levels differ by model and provider. For local engines, use explicit route fallbacks in `config.yml`; browser-menu alternatives do not apply.

`timing.type` accepts:

| Type | Fields | Example |
| --- | --- | --- |
| `at` | `at` as RFC 3339 timestamp in the future | `2026-09-15T09:00:00+02:00` |
| `interval` | `interval_seconds` from 60 to 31536000 | `3600` |
| `daily` | `time` (`HH:MM`), `timezone` | `09:00` |
| `weekdays` | `time`, `timezone` | Monday–Friday at `09:00` |
| `weekly` | `time`, `timezone`, `days` | `["mon", "thu"]` |
| `cron` | `cron` (five fields), `timezone` | `0 9 * * 1-5` |

Cron fields are minute, hour, day of month, month, and weekday (`0`/`7` = Sunday). Lists, ranges, and steps are supported. When both day-of-month and weekday are constrained, either may match. Timezones use IANA names. A local time that does not exist during a daylight-saving transition is skipped; a repeated local time may run twice. Intervals use elapsed seconds and do not drift when the service is late.

The due queue has four local execution slots and waits while the requested browser profile is disconnected or busy. It coalesces missed recurring occurrences into one run when resources return; it does not send an unbounded backlog. A one-shot remains due until it can run. Claimed occurrences are persisted before dispatch. If the service dies during a run, that occurrence is marked `interrupted` on restart and is **not** replayed automatically, since the website may already have accepted the prompt. Inspect the tab and job result before using `run` manually.

Commands: `contextbridge schedule list`, `show ID`, `pause ID`, `resume ID`, `run ID`, and `delete ID`. Use `contextbridge result JOB_ID` with the schedule's `last_run_id` to read its saved result. `run` accepts a manual run even when paused, but rejects an occupied schedule or unavailable resources. Deleting a currently running schedule is refused. HTTP equivalents are `GET/POST /v1/schedules`, `GET/DELETE /v1/schedules/{id}`, `POST /v1/schedules/{id}/pause|resume|run`, and `GET /v1/jobs/{job-id}` for the saved result.

Pause disables future automatic claims; it does not cancel a job already sent to an AI page. Resume retains the next due occurrence, including a missed one. Each schedule keeps its latest 50 run records with step outcomes in the owner-only store; the dashboard shows the latest 10 and a live countdown. Full history and errors are available from authenticated `schedule show ID` or `GET /v1/schedules/{id}`. Routine status exposes outcomes and IDs but not prompts, model responses, or error details.

An optional `steps` array adds up to four sequential follow-ups after the base `job`. The next step starts only after the preceding job has a saved successful result. For example:

```json
{
  "name": "Brief then review",
  "job": {
    "route": "browser-chatgpt",
    "prompt": "Write a short brief about today's open items.",
    "output": { "mode": "text" }
  },
  "steps": [
    {
      "name": "Review the brief",
      "job": {
        "prompt": "Review {{previous.text}} for omissions.",
        "output": { "mode": "text" }
      }
    }
  ],
  "timing": { "type": "daily", "time": "09:00", "timezone": "Europe/Berlin" }
}
```

You can also write `{{previous.text}}`, `{{previous.json}}`, or `{{previous.artifact_names}}` in a follow-up prompt. ContextBridge replaces the marker with a reference and puts the actual prior AI output in the next job's **untrusted submitted-content section**, not its trusted instructions. An unknown or empty referenced field fails that step without sending it. An optional `use_previous_artifact: "image"` or `"file"` attaches the first prior artifact with actual bytes and a matching SHA-256 digest to a browser follow-up. The previous step must request `output.artifacts: true`. URL-only or oversized files are not silently fetched or guessed; if a compatible upload control is unavailable, the follow-up fails before prompt submission. A failed or interrupted follow-up never automatically replays preceding steps. The dashboard offers one follow-up; the authenticated API/CLI supports up to four.

The service and cluster dashboards display local wall-clock times, UTC offsets, and signed approximate clock differences. The CLI `status` and `cluster status` show the same comparisons. Each schedule's explicit `timezone` determines its wall-clock time; when omitted, the service PC's local timezone is used. A worker's clock difference is estimated from its last heartbeat receipt, so network transit makes it **approximate**, not a clock-sync guarantee. Set clocks with the host OS time service if precise cron boundaries matter.

By default a recurring browser schedule creates a fresh **foreground** chat on its first run, then reuses that dedicated ContextBridge session. This can switch your active browser tab. It is deliberate: background-only new ChatGPT tabs have sometimes shown a ready composer but stalled after Send. Set `job.metadata.contextbridge_foreground_new_chat: false` to keep new tabs in the background, or `job.metadata.contextbridge_new_chat_per_run: true` to request a separate chat for each run. The extension's attached-tab safety limit still applies. ContextBridge never silently uses a personal conversation as a scheduled session. Unsent drafts remain protected by the extension's existing draft policy.

The failure-rate tables count jobs with an output error and group them by **requested** provider, model, and reasoning (or `unknown` when none was requested). A review verdict is a separate outcome, not automatically an error. The tables describe configuration reliability, not a claim that an unverified browser model actually ran. New breakdowns start collecting in 0.5.39; historical totals remain in the existing overall counters.
