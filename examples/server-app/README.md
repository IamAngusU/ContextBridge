<!-- SPDX-License-Identifier: Apache-2.0 -->

# Minimal server-side app clients

Create one scoped producer credential on the relay host without printing the
secret:

```sh
contextbridge integrate relay \
  --subject demo-server-app \
  --lifetime-hours 720 \
  --write-env ./contextbridge-producer.env
```

Transfer that file through a secure channel to the server-side application
host and load its two environment variables. Do not put it in a web root,
browser bundle, mobile application, log or repository.

The examples need only Python 3 or Node.js 18+:

```sh
python examples/server-app/python_submit.py
node examples/server-app/javascript-submit.mjs
```

Both examples use one stable idempotency key for one logical submission,
retain the returned job ID, poll a bounded authenticated result, and distinguish
terminal failure from completion. They reject plain HTTP except on loopback.
They deliberately do not implement E2EE or incremental event watching.
