# SPDX-License-Identifier: Apache-2.0

import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import python_submit


class _Handler(BaseHTTPRequestHandler):
    target_url = ""
    target_calls = 0

    def do_GET(self):
        if self.path == "/redirect":
            self.send_response(307)
            self.send_header("Location", self.target_url)
            self.end_headers()
            return
        type(self).target_calls += 1
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b'{"ok":true}')

    def log_message(self, *_args):
        pass


class PythonSubmitTests(unittest.TestCase):
    def test_authorization_never_crosses_redirect(self):
        target = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
        redirect = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
        _Handler.target_calls = 0
        _Handler.target_url = f"http://127.0.0.1:{target.server_port}/target"
        threads = [threading.Thread(target=server.serve_forever, daemon=True) for server in (target, redirect)]
        for thread in threads:
            thread.start()
        try:
            with self.assertRaisesRegex(RuntimeError, "HTTP 307"):
                python_submit.request_json("GET", f"http://127.0.0.1:{redirect.server_port}/redirect", "secret")
            self.assertEqual(_Handler.target_calls, 0)
        finally:
            redirect.shutdown()
            target.shutdown()
            redirect.server_close()
            target.server_close()


if __name__ == "__main__":
    unittest.main()
