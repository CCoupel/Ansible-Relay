# -*- coding: utf-8 -*-
# connection_plugins/relay.py
#
# AnsibleRelay — Custom Connection Plugin
#
# Remplace SSH par un canal WebSocket géré par le relay-agent côté client.
# Le serveur FastAPI agit comme broker : il relaie les commandes Ansible
# vers le bon agent via le WebSocket persistant ouvert par le client.
#
# Usage dans l'inventaire :
#   ansible_connection: relay
#   ansible_relay_server: http://localhost:8000   (ou via ansible.cfg)
#
# Protocole broker (JSON) :
#   → { "task_id": "...", "type": "exec", "command": "...", "stdin": "" }
#   ← { "task_id": "...", "rc": 0, "stdout": "...", "stderr": "..." }
#
#   → { "task_id": "...", "type": "put_file", "dst": "...", "data_b64": "..." }
#   ← { "task_id": "...", "rc": 0 }
#
#   → { "task_id": "...", "type": "fetch_file", "src": "..." }
#   ← { "task_id": "...", "rc": 0, "data_b64": "..." }

from __future__ import absolute_import, division, print_function
__metaclass__ = type

DOCUMENTATION = r"""
name: relay
short_description: AnsibleRelay WebSocket connection plugin
description:
  - Connects to hosts via the AnsibleRelay broker instead of SSH.
  - Requires the relay-agent daemon to be running on the target host
    and connected to the relay server.
author: AnsibleRelay Project
version_added: "1.0"
options:
  relay_server:
    description:
      - Base URL of the AnsibleRelay FastAPI server.
    default: http://localhost:8000
    ini:
      - section: relay_connection
        key: server
    env:
      - name: ANSIBLE_RELAY_SERVER
    vars:
      - name: ansible_relay_server
  relay_token:
    description:
      - Bearer token for authenticating with the relay server.
    default: ""
    ini:
      - section: relay_connection
        key: token
    env:
      - name: ANSIBLE_RELAY_TOKEN
    vars:
      - name: ansible_relay_token
  relay_timeout:
    description:
      - Seconds to wait for a task result before timing out.
    default: 30
    type: integer
    ini:
      - section: relay_connection
        key: timeout
    env:
      - name: ANSIBLE_RELAY_TIMEOUT
    vars:
      - name: ansible_relay_timeout
"""

import base64
import json
import os
import uuid

import requests

from ansible.errors import AnsibleConnectionFailure, AnsibleError
from ansible.plugins.connection import ConnectionBase
from ansible.utils.display import Display

display = Display()


class Connection(ConnectionBase):
    """AnsibleRelay connection plugin — routes commands through the relay broker."""

    transport = "relay"
    has_pipelining = False
    has_tty = False

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _relay_server(self):
        return self.get_option("relay_server").rstrip("/")

    def _headers(self):
        token = self.get_option("relay_token")
        h = {"Content-Type": "application/json"}
        if token:
            h["Authorization"] = f"Bearer {token}"
        return h

    def _timeout(self):
        return int(self.get_option("relay_timeout"))

    def _hostname(self):
        return self._play_context.remote_addr

    def _post_task(self, payload: dict) -> dict:
        """POST a task to /api/task/{hostname} and wait for the result."""
        hostname = self._hostname()
        url = f"{self._relay_server()}/api/task/{hostname}"

        display.vvv(f"RELAY: POST {url}  task_id={payload.get('task_id')}", host=hostname)

        try:
            resp = requests.post(
                url,
                headers=self._headers(),
                json=payload,
                timeout=self._timeout(),
            )
        except requests.Timeout:
            raise AnsibleConnectionFailure(
                f"Relay timeout ({self._timeout()}s) waiting for host '{hostname}'"
            )
        except requests.ConnectionError as exc:
            raise AnsibleConnectionFailure(
                f"Cannot reach relay server at '{self._relay_server()}': {exc}"
            )

        if resp.status_code == 404:
            raise AnsibleConnectionFailure(
                f"Host '{hostname}' is not connected to the relay broker "
                f"(HTTP 404 from {url})"
            )

        if resp.status_code != 200:
            raise AnsibleError(
                f"Relay server error {resp.status_code}: {resp.text[:200]}"
            )

        try:
            result = resp.json()
        except ValueError:
            raise AnsibleError(f"Relay server returned non-JSON response: {resp.text[:200]}")

        display.vvv(
            f"RELAY: result rc={result.get('rc', '?')}",
            host=hostname,
        )
        return result

    # ------------------------------------------------------------------
    # ConnectionBase interface
    # ------------------------------------------------------------------

    def _connect(self):
        """Check that the target host is registered with the broker."""
        if self._connected:
            return self

        hostname = self._hostname()
        url = f"{self._relay_server()}/api/hosts/{hostname}/status"

        display.vvv(f"RELAY: checking host status at {url}", host=hostname)

        try:
            resp = requests.get(url, headers=self._headers(), timeout=10)
        except requests.ConnectionError as exc:
            raise AnsibleConnectionFailure(
                f"Cannot reach relay server at '{self._relay_server()}': {exc}"
            )

        if resp.status_code == 404:
            raise AnsibleConnectionFailure(
                f"Host '{hostname}' is not registered with the relay server. "
                "Ensure the relay-agent is running on the target."
            )

        if resp.status_code != 200:
            raise AnsibleConnectionFailure(
                f"Relay server returned {resp.status_code} for host '{hostname}'"
            )

        data = resp.json()
        if not data.get("connected", False):
            raise AnsibleConnectionFailure(
                f"Host '{hostname}' is registered but WebSocket is not active. "
                "The relay-agent may have disconnected."
            )

        self._connected = True
        display.vvv(f"RELAY: host '{hostname}' is connected", host=hostname)
        return self

    def exec_command(self, cmd, in_data=None, sudoable=True):
        """Execute a shell command on the remote host via the relay broker.

        Returns: (return_code, stdout_bytes, stderr_bytes)
        """
        super().exec_command(cmd, in_data=in_data, sudoable=sudoable)

        task_id = str(uuid.uuid4())
        payload = {
            "task_id": task_id,
            "type": "exec",
            "command": cmd,
            "stdin": (in_data or b"").decode("utf-8", errors="replace"),
        }

        result = self._post_task(payload)

        rc = int(result.get("rc", 1))
        stdout = result.get("stdout", "").encode("utf-8")
        stderr = result.get("stderr", "").encode("utf-8")
        return rc, stdout, stderr

    def put_file(self, in_path, out_path):
        """Transfer a local file to the remote host via the relay broker (base64)."""
        super().put_file(in_path, out_path)

        display.vvv(f"RELAY: put_file {in_path} → {out_path}", host=self._hostname())

        if not os.path.exists(in_path):
            raise AnsibleError(f"put_file: local file not found: {in_path}")

        with open(in_path, "rb") as fh:
            data_b64 = base64.b64encode(fh.read()).decode("ascii")

        task_id = str(uuid.uuid4())
        payload = {
            "task_id": task_id,
            "type": "put_file",
            "dst": out_path,
            "data_b64": data_b64,
        }

        result = self._post_task(payload)
        if int(result.get("rc", 1)) != 0:
            raise AnsibleError(
                f"put_file failed on remote host: {result.get('stderr', '')}"
            )

    def fetch_file(self, in_path, out_path):
        """Fetch a remote file via the relay broker (base64) to a local path."""
        super().fetch_file(in_path, out_path)

        display.vvv(f"RELAY: fetch_file {in_path} → {out_path}", host=self._hostname())

        task_id = str(uuid.uuid4())
        payload = {
            "task_id": task_id,
            "type": "fetch_file",
            "src": in_path,
        }

        result = self._post_task(payload)

        if int(result.get("rc", 1)) != 0:
            raise AnsibleError(
                f"fetch_file failed on remote host: {result.get('stderr', '')}"
            )

        data_b64 = result.get("data_b64", "")
        if not data_b64:
            raise AnsibleError(f"fetch_file: relay returned empty data for '{in_path}'")

        os.makedirs(os.path.dirname(os.path.abspath(out_path)), exist_ok=True)
        with open(out_path, "wb") as fh:
            fh.write(base64.b64decode(data_b64))

    def close(self):
        """Nothing persistent to close — HTTP is stateless."""
        self._connected = False
