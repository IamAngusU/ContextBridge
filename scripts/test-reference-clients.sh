#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
python3 "$root/examples/server-app/python_submit_test.py"
node "$root/examples/server-app/contextbridge-ui-client_test.mjs"
node --check "$root/examples/server-app/javascript-submit.mjs"
php "$root/examples/php/client_test.php"
