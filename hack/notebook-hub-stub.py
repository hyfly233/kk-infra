"""Named-server HTTP stub for notebook-e2e.sh, never a real Hub substitute."""
import argparse
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import re

parser = argparse.ArgumentParser()
parser.add_argument("--port", type=int, required=True)
args = parser.parse_args()
users = {}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def handle_request(self):
        if self.headers.get("Authorization") != "token local-hub-test-token":
            self.send_error(401)
            return
        match = re.fullmatch(r"/hub/api/users/(nb-[0-9a-f]{32})(/servers/workspace)?", self.path)
        if not match:
            self.send_error(404)
            return
        name, server = match.groups()
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))) or b"{}")
        status, result = 200, {}
        if self.command == "GET":
            if name not in users:
                status = 404
            else:
                state = users[name]
                servers = {} if state == "absent" else {"workspace": {"ready": state == "running", "pending": state if state in ("spawn", "stop") else ""}}
                result = {"name": name, "servers": servers}
                if state == "spawn":
                    users[name] = "running"
                elif state == "stop":
                    users[name] = "absent"
        elif self.command == "POST" and not server:
            users[name], status = "absent", 201
        elif self.command == "POST" and server and body.get("platform_spawn_token"):
            users[name], status = "spawn", 202
        elif self.command == "DELETE" and server and body.get("remove") is True:
            users[name], status = "stop", 202
        else:
            status = 400
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(result).encode())

    do_GET = do_POST = do_DELETE = handle_request


HTTPServer(("127.0.0.1", args.port), Handler).serve_forever()
