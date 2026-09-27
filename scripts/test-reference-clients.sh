#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
python3 "$root/examples/server-app/python_submit_test.py"
node "$root/examples/server-app/contextbridge-ui-client_test.mjs"
node --check "$root/examples/server-app/javascript-submit.mjs"
node "$root/examples/external-workflow/contextbridge-workflow-client_test.mjs"
node --check "$root/examples/external-workflow/run.mjs"
node --test "$root/examples/adapter-v2/reference-adapter_test.mjs"
sh "$root/scripts/test-reference-adapter-e2e.sh"
php "$root/examples/php/client_test.php"
