# AnsibleRelay — Instructions projet pour Claude Code

## Présentation

**AnsibleRelay** est un système permettant d'exécuter des playbooks Ansible sur des hôtes distants sans connexion SSH entrante. Les agents clients initient eux-mêmes la connexion vers un serveur central (modèle Salt Minion, connexions inversées).

## Documentation de référence

| Fichier | Contenu |
|---|---|
| `DOC/HLD.md` | Architecture haut niveau, schémas composants et flux de messages |
| `DOC/ARCHITECTURE.md` | Spécifications techniques détaillées (protocoles, formats, sécurité, déploiement) |
| `DOC/SECURITY.md` | Modèle de sécurité complet (enrollment, rôles, tokens, rotation) |
| `DOC/BACKLOG.md` | État des phases et tâches |

**Lire ces fichiers avant toute implémentation.**

## Structure du projet

```
ansible-relay/
├── README.md                 # Point d'entrée projet
├── CLAUDE.md                 # Instructions Claude Code
├── DOC/                      # Documentation vivante (specs, architecture, sécurité)
│   ├── ARCHITECTURE.md       # Spécifications techniques v1.1+ (§1-§22)
│   ├── HLD.md                # High-Level Design
│   ├── SECURITY.md           # Modèle de sécurité complet
│   ├── BACKLOG.md            # Phases et tâches
│   ├── DEPLOYMENT.md         # Guide de déploiement
│   └── QUICKSTART.md         # Démarrage rapide
├── RELEASE/                  # Historique d'implémentation (phases, rapports, migrations)
├── GO/                       # Code source GO
│   ├── cmd/server/           # relay-server (API + WS + CLI cobra)
│   ├── cmd/agent/            # relay-agent
│   └── cmd/inventory/        # relay-inventory binary
└── ansible_minion/           # Docker Compose qualif
```

## Stack technique

- **Agent** : GO, gorilla/websocket, subprocess, RSA-4096, JWT
- **Serveur** : GO, net/http, gorilla/websocket, NATS JetStream, SQLite (modernc)
- **Inventory** : GO binary standalone (`relay-inventory`)
- **Plugins Ansible** : Python (contrainte Ansible — ConnectionBase / InventoryModule)
- **Tests** : `JWT_SECRET_KEY=test ADMIN_TOKEN=test go test ./... -v`
- **Déploiement** : systemd (agent), Docker Compose (qualif), Kubernetes (prod)

## Décisions techniques majeures (non négociables)

- Transport : **WSS** obligatoire (TLS sur toutes les connexions)
- Canal agent : **1 WebSocket persistante** par agent, multiplexée par `task_id`
- Bus de messages : **NATS JetStream** (streams `RELAY_TASKS` + `RELAY_RESULTS`)
- Plugin Ansible → serveur : **REST HTTP bloquant**
- Auth : **JWT signé** (rôles `agent` / `plugin` / `admin`), blacklist JTI — voir `DOC/SECURITY.md`
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
