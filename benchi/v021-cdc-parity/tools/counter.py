#!/usr/bin/env python3
"""Tiny HTTP counter for the Debezium Server (standalone) arm.

Debezium Server's built-in HTTP sink POSTs each change event to this service —
one HTTP request per event (the sink's batch mode is left disabled). For every
POST we append exactly one line to /sink/out.txt on the shared ground-truth
volume, so the benchmark's existing `wc -l` summing hook counts delivered
events — the SAME ground-truth metric as every other arm. No engine metric is
read anywhere.

GET / is a health check and does not count. Stdlib only, no framework: this is
a few lines of boring Python on purpose.
"""
import http.server
import sys
import threading
import traceback

SINK = "/sink/out.txt"
LOCK = threading.Lock()


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"  # keep-alive, so the sink reuses one connection

    def do_POST(self):
        # Drain the request body (one Debezium JSON envelope) so the keep-alive
        # connection stays in sync, then record one line for the event. The
        # body content is irrelevant to the metric (wc -l counts lines, not
        # bytes); draining it is what keeps a single long-lived connection fast
        # instead of reconnect-per-event.
        try:
            length = int(self.headers.get("Content-Length") or 0)
            if length:
                self.rfile.read(length)
            with LOCK:
                with open(SINK, "ab") as f:
                    f.write(b"\n")
            self.send_response(200)
            self.send_header("Content-Length", "0")
            self.end_headers()
        except Exception:  # surface the error in the container log, then 500
            traceback.print_exc()
            raise

    def do_GET(self):
        # Health check endpoint; does not count.
        self.send_response(200)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def log_message(self, *args):
        pass  # suppress the per-request access log at ~20k events/s

    def log_error(self, format, *args):
        # Keep errors visible in the container log (access logs are suppressed
        # above); BaseHTTPRequestHandler routes handle_error() through here.
        sys.stderr.write("%s - %s\n" % (self.address_string(), format % args))

    def send_error(self, code, message=None, explain=None):
        # parse_request() failures (400/431/501) do not go through log_error,
        # so surface them here too.
        sys.stderr.write("send_error %s %s\n" % (code, message))
        super().send_error(code, message, explain)


http.server.ThreadingHTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
