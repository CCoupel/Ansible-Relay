# Plugins Ansible — Spécifications techniques

> Référence pour les plugins Ansible du projet AnsibleRelay (Python).
> Source canonique : `DOC/common/ARCHITECTURE.md` §6, §8, §11, §12, §13, §14, §16
> Sécurité : `DOC/security/SECURITY.md` §6 (auth plugin tokens)
> **Contrat d'interface** : `DOC/contracts/REST_PLUGIN.md`

---

## 1. Vue d'ensemble

Les plugins tournent sur l'**Ansible Control Node** (machine de confiance).
Ils remplacent SSH par des appels REST HTTPS vers le relay-server.

```
Ansible Control Node
  ├── connection_plugins/relay.py   — remplace SSH (exec, upload, fetch)
  └── inventory_plugins/relay.py   — inventaire dynamique (GET /api/inventory)
          │
          │ HTTPS bloquant (requests)
          ▼
      Relay Server :7770
          │
          │ WebSocket
          ▼
      relay-agent (hôte cible)
```

**Contrainte fondamentale :** `exec_command()` d'Ansible est synchrone.
Les plugins utilisent `requests` (HTTP bloquant), jamais `asyncio`.

---

## 2. Authentification

Les plugins s'authentifient avec un **PLUGIN_TOKEN** statique :

```
Authorization: Bearer $RELAY_PLUGIN_TOKEN
X-Relay-Client-Host: <hostname du control node>  ← optionnel, pour le binding
```

Ce token est créé par l'admin via :
```bash
relay-server tokens create --role plugin --description "ansible-control-prod" \
  --allowed-ips "192.168.1.10/32" --allowed-hostname "ansible-control-prod"
```

> Voir `DOC/security/SECURITY.md` §6 pour le modèle complet (IP binding, hostname binding).

---

## 3. Connection Plugin (`relay.py`)

### Classe et méthodes

```python
class Connection(ConnectionBase):
    transport = 'relay'

    def _connect(self) -> None
    def exec_command(self, cmd: str, in_data=None, sudoable=True) -> tuple[int, bytes, bytes]
    def put_file(self, in_path: str, out_path: str) -> None
    def fetch_file(self, in_path: str, out_path: str) -> None
    def close(self) -> None
```

### `exec_command()`

```python
POST /api/exec/{hostname}
Authorization: Bearer $RELAY_PLUGIN_TOKEN

{
  "task_id": "uuid-v4",           # généré par le plugin
  "cmd": "<commande>",
  "stdin": "<base64|null>",       # pipelining ou become_pass
  "timeout": 30,                  # depuis ansible.cfg ou variable hôte
  "become": bool,
  "become_method": "sudo"
}

# Mapping retour → Ansible
200 { rc, stdout, stderr } → (rc, stdout_bytes, stderr_bytes)
503                        → AnsibleConnectionError("UNREACHABLE: agent offline")
504                        → AnsibleConnectionError("TIMEOUT")
500                        → AnsibleConnectionError("agent_disconnected")
429                        → AnsibleConnectionError("agent_busy")
```

### `put_file()`

```python
POST /api/upload/{hostname}
{
  "task_id": "uuid-v4",
  "dest": out_path,
  "data": base64.b64encode(open(in_path, 'rb').read()).decode(),
  "mode": "0644"
}
```

**Limite : 500KB**. Si `os.path.getsize(in_path) > 500*1024` → lever `AnsibleError`.

### `fetch_file()`

```python
POST /api/fetch/{hostname}
{ "task_id": "uuid-v4", "src": in_path }

# Réponse :
{ "rc": 0, "data": "<base64>" }
# Écrire base64.b64decode(data) → out_path
```

### Pipelining

Si `ANSIBLE_PIPELINING=true`, Ansible injecte le module Python via `stdin` (pas de `put_file`).
Le plugin supporte cela via le champ `stdin` de `exec_command`.

```ini
# ansible.cfg
[defaults]
pipelining = true
```

### Configuration plugin

```ini
# ansible.cfg
[relay_connection]
relay_server_url = https://relay.example.com   # ou var RELAY_SERVER_URL
plugin_token     = <token>                     # ou var RELAY_PLUGIN_TOKEN
ca_bundle        = /etc/ssl/certs/ca.pem       # ou var RELAY_CA_BUNDLE
verify_tls       = true
timeout          = 30
```

Variables hôte (`host_vars/my-host.yml`) :
```yaml
ansible_connection: relay
ansible_relay_server_url: https://relay.example.com
ansible_relay_timeout: 60
```

---

## 4. Inventory Plugin (`relay_inventory.py`)

### Comportement

Interroge `GET /api/inventory` et retourne le format JSON Ansible standard.

```python
class InventoryModule(BaseInventoryPlugin):
    NAME = 'relay'

    def verify_file(self, path: str) -> bool
    def parse(self, inventory, loader, path, cache=True) -> None
```

### Appel API

```python
GET /api/inventory?only_connected={only_connected}
Authorization: Bearer $RELAY_PLUGIN_TOKEN

# Réponse :
{
  "all": { "hosts": ["host-A", "host-B"] },
  "_meta": {
    "hostvars": {
      "host-A": {
        "ansible_connection": "relay",
        "ansible_host": "host-A",
        "relay_status": "connected",
        "relay_last_seen": "2026-03-06T10:00:00Z"
      }
    }
  }
}
```

### Configuration

```ini
# ansible.cfg
[relay_inventory]
relay_server_url = https://relay.example.com
plugin_token     = <token>
only_connected   = false    # true = exclure agents offline
```

Ou via le binaire GO (`relay-inventory`) :
```bash
RELAY_SERVER_URL=https://relay.example.com \
RELAY_TOKEN=$RELAY_PLUGIN_TOKEN \
relay-inventory --list

relay-inventory --host my-host
```

---

## 5. Gestion des erreurs

| Code HTTP | Signification | Exception Ansible |
|---|---|---|
| `503` | Agent offline | `AnsibleConnectionError` (UNREACHABLE) |
| `504` | Timeout | `AnsibleConnectionError` (timeout) |
| `500` | Déconnexion mid-task | `AnsibleConnectionError` |
| `429` | Agent busy | `AnsibleConnectionError` |
| `413` | Fichier > 500KB | `AnsibleError` |
| `401` | Token invalide | `AnsibleAuthenticationFailure` |
| `403` | IP/hostname non autorisé | `AnsibleAuthenticationFailure` |

---

## 6. Flow complet (référence)

Exemple avec `ansible-playbook -i relay_inventory.py site.yml` :

```
1. Inventory plugin → GET /api/inventory → [host-A(connected), host-B(disconnected)]
2. Ansible prépare les workers

Pour host-A :
  gather_facts → POST /api/exec/host-A { cmd: "python3 -c <setup>" }
  task: copy   → POST /api/upload/host-A { dest: "/tmp/module.py" }
               → POST /api/exec/host-A { cmd: "python3 /tmp/module.py" }
  task: shell  → POST /api/exec/host-A { cmd: "sudo systemctl restart x",
                                          stdin: base64(pass), become: true }

Pour host-B :
  POST /api/exec/host-B → 503 agent_offline → UNREACHABLE
```

---

## 7. Installation

```bash
# Dans ansible.cfg
[defaults]
connection_plugins = /usr/lib/ansible-relay/connection_plugins
inventory_plugins  = /usr/lib/ansible-relay/inventory_plugins

# Variables d'environnement du control node
export RELAY_SERVER_URL=https://relay.example.com
export RELAY_PLUGIN_TOKEN=relay_plugin_xxxxx
export RELAY_CA_BUNDLE=/etc/ssl/certs/relay-ca.pem    # si CA custom
```
