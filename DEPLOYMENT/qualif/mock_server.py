"""
mock_server.py — Serveur HTTP minimal pour les tests de qualification Phase 1.

Répond aux tentatives d'enrollment de l'agent :
- POST /api/register → HTTP 403 (key_not_authorized)
- GET  /health       → HTTP 200 (pour le healthcheck Docker)
"""

import http.server
import json
import sys


class EnrollmentMockHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        print(f"[mock-server] {fmt % args}", flush=True)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length)
        print(f"[mock-server] POST {self.path} — {len(body)} bytes", flush=True)

        if self.path == "/api/register":
            resp = json.dumps({"error": "key_not_authorized"}).encode()
            self.send_response(403)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(resp)))
            self.end_headers()
            self.wfile.write(resp)
        else:
            self.send_response(404)
            self.end_headers()

    def do_GET(self):
        if self.path == "/health":
            resp = b"ok"
            self.send_response(200)
            self.send_header("Content-Length", str(len(resp)))
            self.end_headers()
            self.wfile.write(resp)
        else:
            self.send_response(404)
            self.end_headers()


if __name__ == "__main__":
    host = "0.0.0.0"
    port = 8080
    print(f"[mock-server] Démarrage sur {host}:{port}", flush=True)
    srv = http.server.HTTPServer((host, port), EnrollmentMockHandler)
    srv.serve_forever()
