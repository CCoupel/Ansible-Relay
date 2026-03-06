# relay-inventory — Spécifications techniques

> Référence pour le binaire relay-inventory GO (Phase 9).
> Source canonique : `DOC/common/ARCHITECTURE.md` §14
> Plugin Python : `DOC/plugins/PLUGINS_SPEC.md` §4

---

## 1. Rôle

`relay-inventory` est un binaire GO standalone compatible avec le protocole
d'inventaire externe Ansible (`--list` / `--host`).

Il interroge `GET /api/inventory` sur le relay-server et formate la réponse
en JSON Ansible standard.

---

## 2. Usage

```bash
# Inventaire complet (utilisé par Ansible)
relay-inventory --list

# Vars d'un hôte spécifique
relay-inventory --host my-host
```

---

## 3. Configuration

```bash
RELAY_SERVER_URL=https://relay.example.com    # défaut: https://localhost:7770
RELAY_TOKEN=relay_plugin_xxxxx                # Bearer token (PLUGIN_TOKEN)
RELAY_CA_BUNDLE=/path/to/ca.pem               # CA custom (optionnel)
RELAY_INSECURE_TLS=false                      # true = désactiver vérif TLS (TESTS)
RELAY_ONLY_CONNECTED=false                    # true = hôtes connectés uniquement
```

---

## 4. Format de sortie

### `--list`

```json
{
  "all": {
    "hosts": ["host-A", "host-B", "host-C"]
  },
  "_meta": {
    "hostvars": {
      "host-A": {
        "ansible_connection": "relay",
        "ansible_host": "host-A",
        "relay_status": "connected",
        "relay_last_seen": "2026-03-06T10:00:00Z"
      },
      "host-B": {
        "ansible_connection": "relay",
        "ansible_host": "host-B",
        "relay_status": "disconnected",
        "relay_last_seen": "2026-03-05T08:00:00Z"
      }
    }
  }
}
```

### `--host <hostname>`

```json
{
  "ansible_connection": "relay",
  "ansible_host": "host-A",
  "relay_status": "connected",
  "relay_last_seen": "2026-03-06T10:00:00Z"
}
```

---

## 5. Intégration Ansible

```ini
# ansible.cfg
[defaults]
inventory = /usr/local/bin/relay-inventory
```

Ou en ligne de commande :
```bash
ansible-playbook -i relay-inventory site.yml
```

---

## 6. Endpoint serveur

```
GET /api/inventory?only_connected=false
Authorization: Bearer <PLUGIN_TOKEN>

→ voir DOC/server/SERVER_SPEC.md §3 pour le format de réponse complet
```

Les agents `relay_status: disconnected` sont inclus par défaut.
Ansible les marquera UNREACHABLE lors de l'exécution (HTTP 503 → `AnsibleConnectionError`).

---

## 7. Code source

```
GO/cmd/inventory/
├── main.go              — parsing args, config, appel HTTP, formatage JSON
└── inventory_test.go    — 19 tests (mock HTTP, formats, filtres)
```
