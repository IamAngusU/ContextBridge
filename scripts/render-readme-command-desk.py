#!/usr/bin/env python3
"""Render the README command reference without third-party dependencies.

Run from any directory. --check verifies that committed SVGs match this source.
This script renders a reference, not live terminal output; it executes no CB
commands and reads no credentials, configuration, metrics or model responses.
"""
from __future__ import annotations

import argparse
from html import escape
from pathlib import Path
import sys

COMMANDS: tuple[tuple[str, str], ...] = (
    ("contextbridge dashboard", "Open the local control panel"),
    ("contextbridge models", "Find available local models"),
    ("contextbridge cluster status", "See connected pool workers"),
    ("contextbridge route explain --file ./job.json", "Preview placement; no inference"),
    ("contextbridge cluster conformance worker --json", "Check bounded worker evidence"),
    ("contextbridge benchmark --json", "Measure coordination overhead"),
    ("contextbridge uninstall --dry-run", "Preview removal; change nothing"),
)

STYLE = """.bg{fill:#f6f5f1;stroke:#deddd7}.fg{fill:#242421}
.muted{fill:#676a61}.line{stroke:#deddd7}.dot{fill:#a5a89d}
.safe{fill:#edeee7}.sans{font-family:Arial,Helvetica,sans-serif}
.mono{font-family:Consolas,Menlo,monospace}
@media(prefers-color-scheme:dark){.bg{fill:#242622;stroke:#44473f}
.fg{fill:#f1f0e9}.muted{fill:#b5b7ac}.line{stroke:#44473f}
.dot{fill:#858b79}.safe{fill:#30352b}}"""


def panel_path(width: int, height: int) -> str:
    """Continuous-corner frame matching the existing README squircle family."""
    right, bottom = width - 1, height - 1
    return (f"M25 1H{width-25}C{width-2.2} 1 {right} 2.2 {right} 25"
            f"V{height-25}C{right} {height-2.2} {width-2.2} {bottom} {width-25} {bottom}"
            f"H25C2.2 {bottom} 1 {height-2.2} 1 {height-25}V25C1 2.2 2.2 1 25 1Z")


def render(mobile: bool) -> str:
    width, height = (480, 500) if mobile else (800, 306)
    lines = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}" role="img" aria-labelledby="title description">',
        '<title id="title">ContextBridge command desk</title>',
        '<desc id="description">Seven real commands including a non-destructive uninstall preview. This is a static command reference, not captured output. Pool commands require a running relay and authorized credentials. Full copyable text is in the README.</desc>',
        '<style>' + STYLE + '</style>',
        f'<path class="bg" d="{panel_path(width, height)}"/>',
        f'<path class="line" d="M1 48H{width-1}"/>',
        '<g class="dot"><rect x="23" y="20" width="8" height="8" rx="3"/><rect x="38" y="20" width="8" height="8" rx="3"/><rect x="53" y="20" width="8" height="8" rx="3"/></g>',
        '<text class="fg sans" x="80" y="29" font-size="12" font-weight="600">contextbridge / command desk</text>',
    ]
    if not mobile:
        lines.append('<text class="muted sans" x="776" y="28" text-anchor="end" font-size="10" letter-spacing="1.1">INSPECT · CONNECT · REMOVE</text>')
    for i, (command, description) in enumerate(COMMANDS):
        y = 78 + i * (60 if mobile else 30)
        if i == len(COMMANDS) - 1:
            lines.append(f'<rect class="safe" x="12" y="{y-20}" width="{width-24}" height="{51 if mobile else 32}" rx="9"/>')
        lines.append(f'<text class="muted mono" x="{20 if mobile else 26}" y="{y}" font-size="14">›</text>')
        lines.append(f'<text class="fg mono" x="{38 if mobile else 45}" y="{y}" font-size="{14.5 if mobile else 13.5}">{escape(command)}</text>')
        lines.append(f'<text class="muted sans" x="{38 if mobile else 555}" y="{y+21 if mobile else y}" font-size="12">{escape(description)}</text>')
    lines.append(f'<text class="muted sans" x="{38 if mobile else 45}" y="{height-15}" font-size="10">COMMAND REFERENCE · Copyable text below</text>')
    lines.append('</svg>')
    return '\n'.join(lines) + '\n'


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true', help='fail if generated files are missing or stale')
    args = parser.parse_args()
    output = Path(__file__).resolve().parents[1] / 'docs' / 'assets' / 'readme'
    if not args.check:
        output.mkdir(parents=True, exist_ok=True)
    stale: list[str] = []
    for filename, mobile in (('command-desk.svg', False), ('command-desk-mobile.svg', True)):
        target = output / filename
        content = render(mobile).encode('utf-8')
        if args.check:
            if not target.is_file() or target.read_bytes() != content:
                stale.append(filename)
        else:
            target.write_bytes(content)
    if stale:
        print('Missing or stale README assets: ' + ', '.join(stale), file=sys.stderr)
        return 1
    print('README command assets match their source.' if args.check else 'Rendered desktop and mobile command references.')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
