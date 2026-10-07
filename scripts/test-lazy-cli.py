"""Isolated built-binary proof: no running pool, model, GPU or API credentials."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    environment = {key: value for key, value in os.environ.items() if not key.startswith("CONTEXTBRIDGE_")}
    with tempfile.TemporaryDirectory(prefix="cb-lazy-proof-") as temporary:
        config = str(Path(temporary) / "config.yml")

        def run(*argv, stdin=None, failure=False):
            result = subprocess.run(
                [str(binary), *argv], input=stdin, capture_output=True, text=True,
                encoding="utf-8", errors="replace", cwd=temporary, env=environment,
                timeout=20, creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
            )
            if (result.returncode != 0) != failure:
                raise AssertionError(f"unexpected exit {result.returncode}: {result.stderr}")
            return result.stdout + result.stderr

        run("init", "--config", config)
        for mode in ("lazy", "normal"):
            output = run("do", "--config", config, "--provider", "ollama", "--tools", "auto",
                         "--egress", "local_only", "--mode", mode, "Was ist 10 mal 3 / 30?")
            if "10 * 3 / 30 = 1" not in output or "calculator" not in output:
                raise AssertionError("deterministic arithmetic did not bypass the absent model")
        output = run("cluster", "chat", "--config", config, "--mode", "lazy",
                     stdin="/settings\n/mode normal\n/settings\n/mode ultra\n/settings\n/exit\n")
        for expected in ("mode lazy", "mode normal", "permissions stay unchanged"):
            if expected not in output:
                raise AssertionError(f"interactive state missing {expected}")
        for prefix in (("do",), ("cluster", "agent", "plan"), ("cluster", "agent", "auto")):
            output = run(*prefix, "--config", config, "--mode", "ultra", failure=True)
            if "--mode must be normal or lazy" not in output:
                raise AssertionError("unsupported mode did not fail closed")
        print(json.dumps({"status": "passed", "built_binary_checks": 6,
                          "isolated_config": True, "model_or_pool_started": False,
                          "cases": ["lazy math", "normal math", "interactive mode switch",
                                    "invalid do mode", "invalid plan mode", "invalid auto mode"]}))


if __name__ == "__main__":
    main()
