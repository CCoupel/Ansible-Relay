# AnsibleRelay — Instructions projet pour Claude Code

## Présentation

**AnsibleRelay** est un système permettant d'exécuter des playbooks Ansible sur des hôtes distants sans connexion SSH entrante. Les agents clients initient eux-mêmes la connexion vers un serveur central (modèle Salt Minion, connexions inversées).

## Documentation de référence

| Fichier | Contenu |
|---|---|
| `HLD.md` | Architecture haut niveau, schémas composants et flux de messages |
| `ARCHITECTURE.md` | Spécifications techniques détaillées (protocoles, formats, sécurité, déploiement) |

**Lire ces deux fichiers avant toute implémentation.**

## Structure du projet

```
ansible-relay/
├── agent/                    # Daemon client (relay-agent) — systemd
│   ├── relay_agent.py        # Point d'entrée principal
│   ├── async_registry.py     # Registre jobs Ansible async
│   ├── facts_collector.py    # Collecte facts système
│   └── relay-agent.service   # Unit file systemd
├── server/                   # Relay server — FastAPI + NATS
│   ├── api/                  # Routes FastAPI
│   └── db/                   # ORM / store SQLite → PostgreSQL
├── ansible_plugins/
│   ├── connection_plugins/relay.py       # Remplace SSH
│   └── inventory_plugins/relay_inventory.py
├── tests/
│   ├── unit/
│   ├── integration/
│   └── robustness/
├── HLD.md                    # High-Level Design
├── ARCHITECTURE.md           # Spécifications techniques v1.1
└── docker-compose.yml        # Déploiement tests/qualif (à créer)
```

## Stack technique

- **Agent** : Python 3.11+, asyncio, websockets, subprocess
- **Serveur** : Python 3.11+, FastAPI, NATS JetStream (nats.py), SQLite/PostgreSQL, JWT
- **Plugins Ansible** : Python, Ansible ConnectionBase / InventoryModule
- **Tests** : pytest, pytest-asyncio, httpx
- **Déploiement** : systemd (agent), Docker Compose (qualif), Kubernetes (prod)

## Décisions techniques majeures (non négociables)

- Transport : **WSS** obligatoire (TLS sur toutes les connexions)
- Canal agent : **1 WebSocket persistante** par agent, multiplexée par `task_id`
- Bus de messages : **NATS JetStream** (streams `RELAY_TASKS` + `RELAY_RESULTS`)
- Plugin Ansible → serveur : **REST HTTP bloquant**
- Auth : **JWT signé** (rôles `agent` / `plugin` / `admin`), blacklist JTI
- `authorized_keys` : **table DB** (pas de fichiers), alimentée par API admin
- Concurrence agent : **subprocess par tâche** (pas de threads)
- Stdout MVP : **buffer 5MB max**, truncation + flag
- Fichiers MVP : **< 500KB**, base64 inline
- Scope v1 : **Linux uniquement**

## Conventions de code

- Python : PEP 8, type hints, docstrings sur les fonctions publiques
- Async : `asyncio` partout côté serveur et agent (pas de `threading`)
- Logs : `structlog` ou `logging` standard, **masquer `stdin` si `become=true`**
- Tests : un fichier de test par module, nommage `test_<module>.py`
- Commits : conventionnel (`feat:`, `fix:`, `docs:`, `test:`, `refactor:`)

## Workflow équipe

- `/start-session` : démarre la team complète AnsibleRelay (8 agents)
- Ordre d'implémentation MVP : `relay-agent` → `relay server` → `plugins Ansible`
- Chaque composant est validé par `qa` avant de passer au suivant
- `security-reviewer` audite chaque PR avant merge
