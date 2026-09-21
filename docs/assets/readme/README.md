# README assets

The command desk is a static reference, not a screenshot or a successful test
report. Desktop and small-screen variants share one command list and support
light/dark color preferences. The main README keeps all commands as selectable
text; its image is never the only source of an instruction.

Regenerate with Python 3.9+ (standard library only):

```sh
python3 scripts/render-readme-command-desk.py
python3 scripts/render-readme-command-desk.py --check
```

No CB command runs during rendering. No external fonts, network requests,
credentials, generated metrics or simulated terminal results are used.
The renderer owns only `command-desk.svg` and `command-desk-mobile.svg`.

## Presentation references

- [GitHub responsive pictures](https://docs.github.com/en/get-started/writing-on-github/getting-started-with-writing-and-formatting-on-github/quickstart-for-writing-on-github#adding-an-image-to-suit-your-visitors): theme-aware artwork and useful alternative text.
- [GitHub collapsed sections](https://docs.github.com/en/get-started/writing-on-github/working-with-advanced-formatting/organizing-information-with-collapsed-sections): detailed pool setup stays available without dominating the landing page.
- [uv installation and removal](https://docs.astral.sh/uv/getting-started/installation/#uninstallation): make leaving as discoverable as getting started.
- [Charm VHS](https://github.com/charmbracelet/vhs): a real CLI recording can be regenerated from a versioned recipe rather than hand-written successful output.

These are presentation references, not affiliations or runtime dependencies.

## A future recorded demo

Before adding an ANSIFrame or VHS recording, capture a real, disposable CB
setup and keep its recipe, CB source commit and transcript together. Show one
small sequence: inspect the pool, explain a route, submit one job, inspect the
result. A simulated provider must be labeled as such. Redact credentials,
pairing codes, private paths and content before committing any capture.

Keep a readable static poster and transcript alongside any animation, honor
reduced-motion preferences where supported, and avoid looping decoration.
Neither a prepared scene nor a rendered animation is a benchmark or an
integration test. No recording or ANSIFrame integration is claimed here.
