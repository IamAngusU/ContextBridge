#!/usr/bin/env python3
"""Render the live ContextBridge public CI proof badge.

The visual template is exported from IamAngusU/Badges and committed into this
public repository so the separate public CI mirror can render evidence without
depending on a private repository or secret cross-repository token.

Only run data is injected here: metric, per-job status rail, accessible title
and machine-readable proof metadata. The badge design itself stays owned by the
badge system.
"""

from __future__ import annotations

import html
import json
import os
import re
import sys
import urllib.request
from pathlib import Path

BADGE_DESIGN_SOURCE = "IamAngusU/Badges"
BADGE_DESIGN_SOURCE_COMMIT = "000735f88f3258b183db7221eb46213941a2bc52"
TEMPLATE_PATH = (
    Path(__file__).resolve().parents[1]
    / "docs"
    / "assets"
    / "readme"
    / "public-proof-template.svg"
)


def required(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise SystemExit(f"missing {name}")
    return value


def github_json(url: str) -> dict:
    headers = {
        "Accept": "application/vnd.github+json",
        "User-Agent": "ContextBridge-public-proof",
        "X-GitHub-Api-Version": "2022-11-28",
    }
    token = os.environ.get("GITHUB_TOKEN", "").strip()
    if token:
        headers["Authorization"] = f"Bearer {token}"
    request = urllib.request.Request(url, headers=headers)
    with urllib.request.urlopen(request, timeout=20) as response:
        return json.load(response)


def load_jobs(repository: str, run_id: str) -> list[dict]:
    api = os.environ.get("GITHUB_API_URL", "https://api.github.com").rstrip("/")
    jobs: list[dict] = []
    page = 1
    while True:
        payload = github_json(
            f"{api}/repos/{repository}/actions/runs/{run_id}/jobs"
            f"?per_page=100&page={page}"
        )
        batch = payload.get("jobs", [])
        if not isinstance(batch, list):
            raise SystemExit("GitHub jobs response is malformed")
        jobs.extend(item for item in batch if isinstance(item, dict))
        if len(batch) < 100:
            break
        page += 1
        if page > 20:
            raise SystemExit("refusing to paginate more than 2000 CI jobs")
    jobs.sort(key=lambda job: (int(job.get("id") or 0), str(job.get("name") or "")))
    return jobs


def proof_status(conclusion: str) -> str:
    value = conclusion.lower().strip()
    if value == "success":
        return "success"
    if value in {"failure", "timed_out", "action_required", "startup_failure"}:
        return "failure"
    if value in {"cancelled", "stale"}:
        return "warning"
    return "neutral"


def render_segments(statuses: list[str]) -> str:
    if not statuses:
        statuses = ["neutral"]
    left = 101.0
    right = 456.0
    gap = 4.0
    segment = (right - left - gap * (len(statuses) - 1)) / len(statuses)
    if segment < 3:
        gap = 1.5
        segment = (right - left - gap * (len(statuses) - 1)) / len(statuses)
    if segment <= 0:
        raise SystemExit("too many CI jobs for the public proof rail")

    lines: list[str] = []
    x = left
    for status in statuses:
        end = x + segment
        lines.append(
            f'<line class="proof-{status}" x1="{x:.2f}" y1="63" '
            f'x2="{end:.2f}" y2="63"/>'
        )
        x = end + gap
    return "\n    ".join(lines)


def render_svg(jobs: list[dict], sha: str, run_number: str, event: str) -> str:
    template = TEMPLATE_PATH.read_text(encoding="utf-8")
    if (
        'data-badge-system="IamAngusU/Badges"' not in template
        or 'id="proof-metric"' not in template
        or 'id="proof-segments"' not in template
    ):
        raise SystemExit("public proof template is missing badge-system anchors")

    total = len(jobs)
    passed = sum(1 for job in jobs if job.get("conclusion") == "success")
    metric = f"{passed}/{total}" if total else "0/0"
    statuses = [proof_status(str(job.get("conclusion") or "")) for job in jobs]
    title = (
        "ContextBridge public CI mirror: "
        f"{metric} jobs passed for {sha[:7]}, run #{run_number} ({event}); "
        "separate GitHub account, same maintainer, not a third-party audit."
    )
    escaped_title = html.escape(title, quote=True)

    svg = re.sub(
        r'aria-label="[^"]*"',
        f'aria-label="{escaped_title}"',
        template,
        count=1,
    )
    svg = re.sub(
        r"<title>.*?</title>",
        f"<title>{escaped_title}</title>",
        svg,
        count=1,
        flags=re.DOTALL,
    )
    svg = re.sub(
        r'(<text id="proof-metric"[^>]*>).*?(</text>)',
        rf"\g<1>{html.escape(metric)}\g<2>",
        svg,
        count=1,
        flags=re.DOTALL,
    )
    svg = re.sub(
        r'(<g id="proof-segments">).*?(</g>)',
        rf"\g<1>\n    {render_segments(statuses)}\n  \g<2>",
        svg,
        count=1,
        flags=re.DOTALL,
    )
    return svg


def main() -> None:
    if len(sys.argv) != 3:
        raise SystemExit(
            "usage: render-public-proof-badge.py OUTPUT.svg OUTPUT.json"
        )

    repository = required("PROOF_REPOSITORY")
    run_id = required("PROOF_RUN_ID")
    run_number = required("PROOF_RUN_NUMBER")
    event = required("PROOF_RUN_EVENT")
    run_url = required("PROOF_RUN_URL")
    head_sha = required("PROOF_HEAD_SHA")
    jobs = load_jobs(repository, run_id)

    svg_path = Path(sys.argv[1])
    json_path = Path(sys.argv[2])
    svg_path.write_text(
        render_svg(jobs, head_sha, run_number, event),
        encoding="utf-8",
    )

    metadata = {
        "claim": "separate-account CI mirror; same maintainer; not a third-party audit",
        "badge_design_source": BADGE_DESIGN_SOURCE,
        "badge_design_source_commit": BADGE_DESIGN_SOURCE_COMMIT,
        "repository": repository,
        "run_id": int(run_id),
        "run_number": int(run_number),
        "event": event,
        "run_url": run_url,
        "head_sha": head_sha,
        "jobs_total": len(jobs),
        "jobs_success": sum(
            1 for job in jobs if job.get("conclusion") == "success"
        ),
        "jobs": [
            {
                "name": job.get("name"),
                "status": job.get("status"),
                "conclusion": job.get("conclusion"),
            }
            for job in jobs
        ],
    }
    json_path.write_text(
        json.dumps(metadata, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )


if __name__ == "__main__":
    main()
