# AnsibleRelay — Spécifications Techniques v1.0

> Document issu de la session de brainstorming architecture.
> Décrit les décisions validées pour le MVP et les axes v2.

---

## Table des matières

1. [Vue d'ensemble](#1-vue-densemble)
2. [Composants du système](#2-composants-du-système)
3. [Architecture réseau et flux de données](#3-architecture-réseau-et-flux-de-données)
4. [Protocole WebSocket](#4-protocole-websocket)
5. [Bus de messages — NATS JetStream](#5-bus-de-messages--nats-jetstream)
6. [API REST du relay server](#6-api-rest-du-relay-server)
7. [Sécurité et authentification](#7-sécurité-et-authentification)
8. [Flow complet d'un playbook](#8-flow-complet-dun-playbook)
9. [Gestion de la concurrence](#9-gestion-de-la-concurrence)
10. [Tâches Ansible async](#10-tâches-ansible-async)
11. [Transfert de fichiers](#11-transfert-de-fichiers)
12. [become (élévation de privilèges)](#12-become-élévation-de-privilèges)
13. [Gestion des erreurs](#13-gestion-des-erreurs)
14. [Inventaire dynamique](#14-inventaire-dynamique)
15. [Haute disponibilité et scalabilité](#15-haute-disponibilité-et-scalabilité)
16. [Configuration](#16-configuration)
17. [Roadmap MVP vs V2](#17-roadmap-mvp-vs-v2)

---

## 1. Vue d'ensemble

AnsibleRelay est un système permettant d'exécuter des playbooks Ansible sur des hôtes distants **sans connexion SSH entrante**. Les agents clients initient eux-mêmes la connexion vers un serveur central, inversant le sens traditionnel du flux de contrôle.

### Principe fondamental

```
Modèle SSH classique :
  Ansible Control Node ──SSH──▶ Hôte cible

Modèle AnsibleRelay :
  Ansible Control Node ──REST──▶ Relay Server ◀──WSS── Agent (hôte cible)
```

### Cas d'usage

- Hôtes derrière NAT/firewall sans port entrant ouvert
- Environnements DMZ, cloud privé, edge computing
- Remplacement de Salt Minion avec compatibilité Ansible native
- Infrastructure > 1000 serveurs avec connexions sortantes uniquement

---

## 2. Composants du système

```
ansible-relay/
├── agent/                    # CLIENT : daemon sur chaque hôte géré
│   ├── relay_agent.py        # Daemon principal (WebSocket + task runner)
│   ├── facts_collector.py    # Collecte des facts Ansible
│   ├── async_registry.py     # Registre des tâches async persisté
│   └── relay-agent.service   # Unité systemd
│
├── server/                   # SERVEUR : relay + broker
│   ├── api/
│   │   ├── main.py           # Application FastAPI
│   │   ├── routes_exec.py    # POST /api/exec/{hostname}
│   │   ├── routes_register.py# POST /api/register
│   │   ├── routes_inventory.py# GET /api/inventory
│   │   └── ws_handler.py     # Handler WebSocket agents
│   ├── broker/
│   │   └── nats_client.py    # Client NATS JetStream
│   └── db/
│       └── agent_store.py    # SQLite : agents, tokens, blacklist
│
├── ansible_plugins/
│   ├── connection_plugins/
│   │   └── relay.py          # Plugin de connexion Ansible
│   └── inventory_plugins/
│       └── relay_inventory.py# Plugin d'inventaire dynamique
│
└── playbooks/
    ├── ansible.cfg           # Configuration Ansible
    └── site.yml              # Playbook de test
```

### Rôles des composants

| Composant | Rôle | Langage |
|---|---|---|
| `relay_agent` | Daemon client, maintient la WS, exécute les tâches | Python |
| `relay server` | Bridge WS↔NATS, expose REST API, gère l'authentification | Python/FastAPI |
| `NATS JetStream` | Bus de messages persistant, routing inter-nodes | Go (binaire) |
| `connection plugin` | Remplace SSH dans Ansible, appels REST bloquants | Python |
| `inventory plugin` | Expose les agents enregistrés à Ansible | Python |

---

## 3. Architecture réseau et flux de données

### Topologie haute disponibilité

```
                    ┌─────────────────────────────┐
                    │       Load Balancer          │
                    │   (HAProxy / nginx / AWS)    │
                    │   sticky session optionnel   │
                    └──────────┬──────┬────────────┘
                               │      │
                ┌──────────────▼──┐  ┌▼──────────────────┐
                │  Relay Server   │  │  Relay Server      │
                │  Node #1        │  │  Node #2           │
                │  WS: host-A,C   │  │  WS: host-B,D      │
                └──────────┬──────┘  └──────┬─────────────┘
                           │                │
                ┌──────────▼────────────────▼───────────────┐
                │              NATS JetStream Cluster        │
                │  Stream: RELAY_TASKS   (tasks.{hostname})  │
                │  Stream: RELAY_RESULTS (results.{task_id}) │
                │  Replicas: 3, Retention: WorkQueue         │
                └────────────────────────────────────────────┘
```

### Les trois connexions du système

| # | Connexion | Initiée par | Vers | Protocole |
|---|---|---|---|---|
| 1 | Session agent | `relay-agent` | Relay Server | WSS (WebSocket over TLS) |
| 2 | Bus messages | Relay Server | NATS Cluster | NATS TCP |
| 3 | Exécution tâche | Connection Plugin | Relay Server API | HTTPS (REST bloquant) |

**L'agent ne connaît pas NATS.** NATS est une infrastructure serveur transparente pour le client.

---

## 4. Protocole WebSocket

### Connexion

- Transport : **WSS** (`wss://`) obligatoire — TLS non négociable
- Authentification : header HTTP à l'upgrade WebSocket
  ```
  Authorization: Bearer <JWT>
  ```
- Une seule connexion WebSocket persistante par agent
- Multiplexage de toutes les tâches via `task_id`

### Format d'enveloppe

Tous les messages (dans les deux sens) suivent cette structure :

```json
{
  "task_id": "uuid-v4",
  "type": "<type>",
  "payload": {}
}
```

### Types de messages Serveur → Agent

#### `exec` — Exécution de commande

```json
{
  "task_id": "t-001",
  "type": "exec",
  "cmd": "python3 /tmp/.ansible/tmp-xyz/module.py",
  "stdin": "<base64|null>",
  "timeout": 30,
  "become": false,
  "become_method": "sudo",
  "expires_at": 1234567890
}
```

| Champ | Description |
|---|---|
| `cmd` | Commande shell à exécuter |
| `stdin` | Données stdin encodées en base64 (pour become_pass, pipelining) |
| `timeout` | Secondes avant kill du subprocess |
| `become` | Élévation de privilèges requise |
| `become_method` | `sudo`, `su`, `pbrun`... |
| `expires_at` | Timestamp UNIX — l'agent refuse si dépassé |

#### `put_file` — Transfert de fichier vers l'agent

```json
{
  "task_id": "t-002-upload",
  "type": "put_file",
  "dest": "/tmp/.ansible/tmp-xyz/module.py",
  "data": "<base64 du contenu>",
  "mode": "0700"
}
```

#### `fetch_file` — Récupération de fichier depuis l'agent

```json
{
  "task_id": "t-003-fetch",
  "type": "fetch_file",
  "src": "/etc/myapp/config.yml"
}
```

#### `cancel` — Annulation de tâche

```json
{
  "task_id": "t-001",
  "type": "cancel"
}
```

L'agent effectue `SIGTERM` sur le subprocess associé au `task_id`.
Le PID n'est jamais exposé au serveur — le mapping `task_id → subprocess` est interne à l'agent.

### Types de messages Agent → Serveur

#### `ack` — Prise en compte de la tâche

```json
{
  "task_id": "t-001",
  "type": "ack",
  "status": "running"
}
```

Envoyé immédiatement après le démarrage du subprocess, avant tout stdout.

#### `stdout` — Sortie standard en streaming

```json
{
  "task_id": "t-001",
  "type": "stdout",
  "data": "ligne de sortie...\n"
}
```

#### `result` — Résultat final de la tâche

```json
{
  "task_id": "t-001",
  "type": "result",
  "rc": 0,
  "stdout": "<stdout complet accumulé>",
  "stderr": "<stderr>",
  "truncated": false
}
```

| `rc` | Signification |
|---|---|
| `0` | Succès |
| `1+` | Erreur applicative |
| `-15` | Tâche annulée (SIGTERM) |
| `-1` | Agent busy (max_concurrent_tasks atteint) |

#### `result` pour `put_file`

```json
{
  "task_id": "t-002-upload",
  "type": "result",
  "rc": 0
}
```

#### `result` pour `fetch_file`

```json
{
  "task_id": "t-003-fetch",
  "type": "result",
  "rc": 0,
  "data": "<base64 du contenu du fichier>"
}
```

### Codes de fermeture WebSocket

| Code | Signification | Comportement agent |
|---|---|---|
| `4000` | Fermeture normale | Reconnexion avec backoff exponentiel |
| `4001` | Token révoqué | Ne pas reconnecter — alerter l'admin |
| `4002` | Token expiré | Refresh token puis reconnecter |
| `4003` | Re-enrollment requis | Clef révoquée, contacter l'admin |
| `4004` | Conflit hostname | Ne pas reconnecter — alerter l'admin |

---

## 5. Bus de messages — NATS JetStream

### Streams

#### RELAY_TASKS

```
Nom         : RELAY_TASKS
Subjects    : tasks.{hostname}
Retention   : WorkQueue (message supprimé après ack)
MaxAge      : 300s (5 minutes)
MaxMsgSize  : 1MB (MVP)
Replicas    : 3
```

**WorkQueue** : chaque message est délivré à exactement un consumer (l'agent du hostname cible). Après ack, le message est supprimé.

#### RELAY_RESULTS

```
Nom         : RELAY_RESULTS
Subjects    : results.{task_id}
Retention   : Limits (message supprimé après consommation ou TTL)
MaxAge      : 60s
MaxMsgSize  : 5MB (MVP — taille max stdout)
Replicas    : 3
```

### Consumer par agent

```
Nom         : relay-agent-{hostname}
Type        : Push (le serveur pousse à l'agent via WS)
AckPolicy   : Explicit
AckWait     : 30s
MaxDeliver  : 1 (pas de retry — Ansible gère le retry au niveau playbook)
```

**MaxDeliver: 1** est un choix délibéré : si l'agent ne peut pas prendre en charge une tâche (crash, reconnexion), le message expire et Ansible reçoit un timeout. L'opérateur relance le playbook. Pas de retry silencieux qui pourrait créer des états incohérents.

### Routage inter-nodes (HA)

```
Problème : Plugin envoie POST à Node #2
           Agent host-A est connecté à Node #1

Solution :
  Node #2 reçoit le POST
    → publie dans NATS tasks.host-A
  Node #1 est subscriber de tasks.host-A
    → reçoit le message NATS
    → le forward à host-A via sa WebSocket
  Agent répond via WS à Node #1
    → Node #1 publie dans NATS results.{task_id}
  Node #2 est subscriber de results.{task_id}
    → reçoit le résultat
    → résout la future() bloquante du POST
    → retourne HTTP 200 au plugin
```

---

## 6. API REST du relay server

### Authentification des appels API

Tous les endpoints (sauf `/api/register`) requièrent :

```
Authorization: Bearer <JWT>
X-Role: plugin   (pour les appels du connection/inventory plugin)
X-Role: agent    (pour les appels internes, si applicable)
```

### Endpoints

#### `POST /api/register` — Enrollment d'un agent

**Seul endpoint accessible sans JWT valide préexistant.**
Requiert TLS. La clef publique doit figurer dans `authorized_keys` côté serveur.

**Requête :**
```json
{
  "hostname": "host-A",
  "public_key_pem": "-----BEGIN PUBLIC KEY-----\n..."
}
```

**Réponse 200 :**
```json
{
  "token_encrypted": "<JWT chiffré avec la clef publique du client (RSAES-OAEP)>",
  "server_public_key_pem": "-----BEGIN PUBLIC KEY-----\n..."
}
```

**Réponse 409 :** hostname déjà enregistré avec une autre clef.

#### `GET /api/inventory` — Inventaire pour Ansible

**Paramètres query :**
- `only_connected=true` — retourne uniquement les agents connectés (optionnel)

**Réponse 200 :**
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
        "relay_last_seen": "2026-03-03T10:00:00Z"
      },
      "host-B": {
        "ansible_connection": "relay",
        "ansible_host": "host-B",
        "relay_status": "disconnected",
        "relay_last_seen": "2026-03-03T08:00:00Z"
      }
    }
  }
}
```

#### `POST /api/exec/{hostname}` — Exécution de commande (bloquant)

**Requête :**
```json
{
  "task_id": "uuid-v4",
  "cmd": "python3 /tmp/.ansible/tmp-xyz/module.py",
  "stdin": "<base64|null>",
  "timeout": 30,
  "become": false,
  "become_method": "sudo"
}
```

**Réponse 200 :**
```json
{
  "rc": 0,
  "stdout": "output...",
  "stderr": "",
  "truncated": false
}
```

**Codes d'erreur :**

| Code HTTP | Signification |
|---|---|
| `503` | Agent offline (`relay_status: disconnected`) |
| `504` | Timeout — agent n'a pas répondu dans le délai imparti |
| `500` | Agent déconnecté pendant l'exécution |
| `429` | Agent busy — `max_concurrent_tasks` atteint |

#### `POST /api/upload/{hostname}` — Transfert de fichier

**Requête :**
```json
{
  "task_id": "uuid-v4",
  "dest": "/tmp/.ansible/tmp-xyz/module.py",
  "data": "<base64>",
  "mode": "0700"
}
```

**Réponse 200 :** `{ "rc": 0 }`

#### `POST /api/fetch/{hostname}` — Récupération de fichier

**Requête :**
```json
{
  "task_id": "uuid-v4",
  "src": "/etc/myapp/config.yml"
}
```

**Réponse 200 :** `{ "rc": 0, "data": "<base64>" }`

#### `WebSocket /ws/agent` — Connexion agent

```
wss://relay-server/ws/agent
Headers: Authorization: Bearer <JWT>
```

Connexion permanente, maintenue par l'agent. Voir section 4 pour le protocole de messages.

---

## 7. Sécurité et authentification

### Modèle de confiance

```
zero-trust sur le transport : TLS obligatoire sur toutes les connexions
trust-on-first-use (TOFU) pour l'enrollment : clef déposée manuellement
JWT signé pour les sessions : vérification à chaque connexion WebSocket
```

### Rôles JWT

| Rôle | `sub` | Permissions |
|---|---|---|
| `agent` | `hostname` | `recv_task`, `send_result` |
| `plugin` | `ansible-controller` | `send_task`, `read_inventory` |

Un token `role: agent` ne peut pas envoyer des tâches (`send_task`).
Un token `role: plugin` ne peut pas ouvrir de WebSocket agent.

### Flow d'enrollment

```
Prérequis : clef publique de l'agent déposée manuellement
            dans server/authorized_keys/{hostname}.pub

1. Agent démarre
   → génère paire RSA-4096 si absente (~/.ansible-relay/id_rsa)
   → POST https://relay-server/api/register
     { hostname: "host-A", public_key_pem: "..." }

2. Relay server
   → vérifie public_key dans authorized_keys/host-A.pub
   → génère JWT : { sub: "host-A", role: "agent",
                    jti: "uuid", iat: now, exp: now+3600 }
   → chiffre JWT avec la clef publique du client (RSAES-OAEP)
   → stocke en DB : (hostname, public_key, jti, enrolled_at)
   → retourne { token_encrypted: "...", server_public_key_pem: "..." }

3. Agent
   → déchiffre token_encrypted avec sa clef privée → JWT
   → stocke JWT localement (~/.ansible-relay/token.jwt)
   → stocke server_public_key (~/.ansible-relay/server.pub)
```

### Flow de reconnexion

```
1. Agent ouvre WSS avec header Authorization: Bearer <JWT>

2. Relay server
   → vérifie signature JWT
   → vérifie jti NOT IN blacklist
   → vérifie hostname == sub
   → si token expiré → close(4002)
   → si jti blacklisté → close(4001)
   → si OK → session ouverte

3. Si close(4002) reçu :
   → Agent appelle POST /api/token/refresh
     { hostname, old_token_encrypted_challenge }
   → Serveur émet un nouveau JWT chiffré

4. Si close(4001) reçu :
   → Agent log l'événement, ne reconnecte pas, alerte admin
```

### Révocation

```
Admin révoque host-A :
  → DB : blacklist.add(jti, reason, revoked_at)
  → Si WS active : close(4001) immédiat
  → Si agent offline : rejet au prochain connect (jti in blacklist)

TTL token : 1h (filet de sécurité — max 1h de survie après révocation)
Blacklist  : nettoyée des entrées expirées (jti.exp dépassé)
```

### Sécurité des logs

**Le champ `stdin` doit être masqué dans les logs quand `become: true`.**

```python
def log_message(msg):
    if msg.get("become") and "stdin" in msg:
        msg = {**msg, "stdin": "***REDACTED***"}
    logger.debug(msg)
```

---

## 8. Flow complet d'un playbook

### Exemple de playbook

```yaml
- hosts: webservers
  gather_facts: true
  tasks:
    - name: create config dir
      file:
        path: /etc/myapp
        state: directory

    - name: deploy config
      copy:
        src: files/myapp.conf
        dest: /etc/myapp/myapp.conf

    - name: restart service
      shell: systemctl restart myapp
      become: true
```

### Séquence complète

```
Phase 0 — Résolution inventaire
  inventory plugin → GET /api/inventory
  → retourne [host-A(connected), host-B(disconnected), host-C(connected)]
  → Ansible prépare 3 workers (forks)

Phase 1 — gather_facts (host-A)
  connection plugin
    → POST /api/exec/host-A { cmd: "python3 -c <setup>", task_id: "t-001" }
  relay server
    → publie dans NATS tasks.host-A
    → subscribe results.t-001 (bloque)
  agent host-A
    → reçoit via WS
    → WS: ack t-001
    → spawn subprocess python3 -c setup
    → WS: stdout {...facts JSON...}
    → WS: result { rc: 0 }
  relay server
    → publie results.t-001
    → HTTP 200 { rc: 0, stdout: "{facts...}" } → plugin
  Ansible parse les facts ✓

Phase 2 — task: file (create directory)
  Ansible génère module file.py
  Étape 1 : put_file
    → POST /api/upload/host-A { dest: "/tmp/.ansible/tmp-xyz/file.py",
                                  data: "<base64>", mode: "0700" }
  Étape 2 : exec_command
    → POST /api/exec/host-A { cmd: "python3 /tmp/.ansible/tmp-xyz/file.py" }
    → agent exécute → result { rc: 0, stdout: '{"changed": true, ...}' }
  Étape 3 : cleanup (envoyé par Ansible core)
    → exec_command("rm -rf /tmp/.ansible/tmp-xyz/")

Phase 3 — task: copy (deploy config)
  Ansible calcule checksum local
  Étape 1 : vérification checksum distant
    → exec_command("python3 -c <checksum /etc/myapp/myapp.conf>")
    → résultat différent → transfert nécessaire
  Étape 2 : put_file
    → POST /api/upload/host-A { dest: "/etc/myapp/myapp.conf",
                                  data: "<base64 myapp.conf>" }
    (MVP : fichier < 1MB)
  Étape 3 : exec_command (chmod, owner...)

Phase 4 — task: shell (systemctl restart)
  become: true → Ansible wraps la commande :
    "sudo -H -S -n -u root systemctl restart myapp"
  → POST /api/exec/host-A {
      cmd: "sudo -H -S -n -u root systemctl restart myapp",
      stdin: "<base64(become_pass+\n)>",  (si sudo avec password)
      become: true,
      timeout: 60
    }
  → agent spawn subprocess avec stdin injecté
  → result { rc: 0 }

Phase 5 — host-B (disconnected)
  POST /api/exec/host-B
  → relay server : ws_connection[host-B] = None
  → HTTP 503 { "error": "agent_offline" }
  → plugin : AnsibleConnectionError
  → Ansible : host-B marqué UNREACHABLE, continue avec host-C
```

---

## 9. Gestion de la concurrence

### Concurrence sur le même host

Plusieurs playbooks peuvent s'exécuter simultanément ciblant le même agent.
Le `task_id` est le mécanisme de démultiplexage.

```
Playbook A : task_id "pb-A-t001" → exec sur host-A
Playbook B : task_id "pb-B-t001" → exec sur host-A (simultané)

WebSocket host-A reçoit les deux tâches.
Agent exécute deux subprocesses en parallèle.
Résultats retournés via la même WS, routés par task_id.
```

### Limite de tâches simultanées

```python
# Configuration agent
MAX_CONCURRENT_TASKS = 10  # configurable

# Si dépassé :
{
  "task_id": "t-xxx",
  "type": "result",
  "rc": -1,
  "error": "agent_busy",
  "running_tasks": 10
}
# HTTP 429 retourné au plugin
```

### Conflits opérationnels

Si deux playbooks modifient le même fichier simultanément sur le même host, c'est une **race condition opérationnelle** hors du périmètre du relay. À documenter comme limitation connue.

---

## 10. Tâches Ansible async

### Comportement Ansible

```yaml
- name: long running task
  shell: ./deploy.sh
  async: 3600   # timeout max en secondes
  poll: 0       # fire-and-forget
  register: job

- name: vérification
  async_status:
    jid: "{{ job.ansible_job_id }}"
  until: result.finished
  retries: 30
  delay: 10
```

### Implémentation côté agent

#### Phase 1 — Lancement (poll: 0)

Le serveur envoie `exec` avec flag `async` et `async_timeout`.

```json
{
  "task_id": "t-async-001",
  "type": "exec",
  "cmd": "./deploy.sh",
  "async": true,
  "async_timeout": 3600
}
```

L'agent **daemonise** le subprocess et répond immédiatement :

```json
{
  "task_id": "t-async-001",
  "type": "result",
  "rc": 0,
  "stdout": "{\"ansible_job_id\": \"jid-uuid\", \"started\": 1, \"finished\": 0}"
}
```

Le job est enregistré dans le registre async persisté sur disque :

```json
// ~/.ansible-relay/async_jobs.json
{
  "jid-uuid": {
    "pid": 4521,
    "cmd": "./deploy.sh",
    "started_at": 1234567890,
    "timeout": 3600,
    "stdout_path": "/tmp/.ansible-relay/jid-uuid.stdout"
  }
}
```

#### Phase 2 — Vérification (async_status)

Ansible envoie `exec` avec la commande `async_status.py --jid jid-uuid`.
L'agent intercepte cette commande et consulte son registre :

```json
// Si en cours :
{ "ansible_job_id": "jid-uuid", "finished": 0,
  "stdout": "<output partiel>" }

// Si terminé :
{ "ansible_job_id": "jid-uuid", "finished": 1,
  "rc": 0, "stdout": "<output complet>" }
```

### Persistance du registre

Le fichier `async_jobs.json` est mis à jour à chaque changement d'état.
Lors du redémarrage de l'agent :
- Si le PID est toujours actif (`/proc/{pid}` existe) : job considéré toujours en cours
- Si le PID est mort : job marqué terminé avec `rc: -1, error: "agent_restarted"`

### Timeout des jobs async

L'agent surveille les jobs async et kill le subprocess si `async_timeout` est dépassé :

```python
if time.time() - job.started_at > job.async_timeout:
    os.kill(job.pid, signal.SIGTERM)
    job.rc = -15
    job.finished = True
```

---

## 11. Transfert de fichiers

### MVP — Fichiers < 1MB

Transfert via base64 inline dans le message WebSocket/NATS.

```
Taille réelle → base64 → overhead x1.33
1MB fichier   → ~1.33MB dans le message
Limite NATS : 1MB par message → limite fichier source : ~750KB effectif
```

**Recommandation MVP : limite à 500KB pour la marge.**

### Pipelining (évite le transfert de fichiers modules)

Si `ANSIBLE_PIPELINING=true` dans `ansible.cfg`, Ansible injecte le module Python via stdin au lieu d'un `put_file`. Plus performant, élimine les fichiers temporaires.

```ini
# ansible.cfg
[defaults]
pipelining = true
```

Le plugin relay supporte le pipelining via le champ `stdin` de `exec`.

### V2 — Chunking (fichiers > 1MB)

À implémenter en v2. Protocole prévu :

```
put_file_chunk:
  { task_id, type: "put_file_chunk", dest, chunk_index, total_chunks, data_base64 }

put_file_complete:
  { task_id, type: "put_file_complete", dest, checksum_sha256 }
```

---

## 12. become (élévation de privilèges)

### Mécanisme

`become` est géré par Ansible core avant d'appeler `exec_command()`.
Le plugin relay reçoit une commande déjà wrappée avec sudo/su/etc.

```
Sans become :
  cmd = "python3 /tmp/module.py"
  stdin = null

Avec become: true, become_method: sudo, sans password :
  cmd = "sudo -H -S -n -u root python3 /tmp/module.py"
  stdin = null

Avec become_pass :
  cmd = "sudo -H -S -n -u root python3 /tmp/module.py"
  stdin = base64("monmotdepasse\n")
  become = true   ← flag pour masquage des logs
```

### Implémentation subprocess côté agent

```python
proc = subprocess.Popen(
    cmd,
    shell=True,
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE
)
if stdin_data:
    stdin_bytes = base64.b64decode(stdin_data)
    proc.stdin.write(stdin_bytes)
proc.stdin.close()
```

### Sécurité

- `stdin` masqué dans les logs si `become: true`
- Le `become_pass` ne doit jamais apparaître en clair dans les logs du relay server, de l'agent, ni de NATS

---

## 13. Gestion des erreurs

### Matrice des erreurs

| Situation | Code WS / HTTP | Comportement Ansible |
|---|---|---|
| Agent offline | HTTP 503 `agent_offline` | UNREACHABLE |
| Agent busy | HTTP 429 `agent_busy` | FAILED |
| Timeout tâche | HTTP 504 `timeout` | FAILED |
| Agent déconnecte pendant tâche | HTTP 500 `agent_disconnected` | FAILED |
| Tâche annulée (cancel) | `rc: -15` | FAILED |
| Fichier trop grand | HTTP 413 `payload_too_large` | FAILED |
| Token révoqué | WS close 4001 | N/A (agent) |
| Token expiré | WS close 4002 | Refresh automatique |

### Timeout en cascade

```
ansible.cfg : timeout = 30
  ↓
connection plugin : POST /api/exec timeout = 30s
  ↓
relay server : asyncio.wait_for(future, 30)
  ↓
si timeout serveur : WS cancel → agent kill subprocess
                     HTTP 504 → plugin → AnsibleConnectionError
```

### Reconnexion de l'agent

```
WS fermée (code 4000 ou erreur réseau)
  → attendre 1s
  → reconnecter
  → échec → attendre 2s → reconnecter
  → échec → attendre 4s → ...
  → backoff exponentiel, max 60s entre tentatives

WS fermée code 4001 (révoqué)
  → NE PAS reconnecter
  → logger l'événement
  → alerter (syslog, email selon config)

WS fermée code 4002 (expiré)
  → appeler POST /api/token/refresh
  → reconnecter avec nouveau token
```

---

## 14. Inventaire dynamique

### Plugin `relay_inventory.py`

Interroge l'API du relay server et retourne le format JSON standard Ansible.

```python
# ansible.cfg
[defaults]
inventory = relay_inventory.py

[relay_inventory]
relay_server = https://relay.example.com
token_file = /etc/ansible/relay_plugin.jwt
only_connected = false   # true pour exclure les agents offline
```

### Réponse incluant tous les agents

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
        "relay_status": "connected"
      }
    }
  }
}
```

Les agents `relay_status: disconnected` sont inclus. Ansible les marquera UNREACHABLE lors de la tentative d'exécution (HTTP 503 → `AnsibleConnectionError`).

---

## 15. Haute disponibilité et scalabilité

### Relay server stateless

Les relay server nodes sont **stateless** grâce à NATS.
Un node peut redémarrer sans perte de tâches en transit (MessageAge < 5min).

### Gestion des connexions WebSocket en HA

```
Agent host-A se connecte à Node #1
  → Node #1 stocke en mémoire : ws_connections["host-A"] = ws_object

Node #1 redémarre
  → Agent détecte WS fermée → reconnecte (backoff expo)
  → Se reconnecte à Node #2 (load balancer)
  → Node #2 maintenant maître de la connexion host-A

Tâche en transit au moment du restart Node #1 :
  → Message dans NATS tasks.host-A (TTL 5min)
  → Node #2 reçoit le message NATS (subscriber)
  → Forward via WS à host-A (maintenant connecté à Node #2)
```

### Capacité estimée

| Composant | Capacité indicative |
|---|---|
| Relay server node (FastAPI async) | ~5000 connexions WS simultanées |
| NATS JetStream | Millions de messages/sec |
| Pour 1000 agents | 1 node suffit, 2-3 nodes pour HA |

### Base de données

SQLite pour le MVP (mono-node).
PostgreSQL pour la production multi-nodes (table `agents` partagée).

---

## 16. Configuration

### Agent (`/etc/ansible-relay/agent.conf`)

```ini
[relay]
server_url = wss://relay.example.com/ws/agent
token_file = /etc/ansible-relay/token.jwt
key_file = /etc/ansible-relay/id_rsa

[agent]
hostname =                    # auto-détecté si vide (socket.gethostname())
max_concurrent_tasks = 10
async_jobs_dir = /var/lib/ansible-relay/async/
stdout_max_bytes = 5242880    # 5MB

[logging]
level = INFO
file = /var/log/ansible-relay/agent.log
mask_become_stdin = true
```

### Relay server (`/etc/ansible-relay/server.conf`)

```ini
[server]
host = 0.0.0.0
port = 8443
tls_cert = /etc/ansible-relay/server.crt
tls_key = /etc/ansible-relay/server.key
authorized_keys_dir = /etc/ansible-relay/authorized_keys/

[nats]
url = nats://nats-cluster:4222
stream_tasks = RELAY_TASKS
stream_results = RELAY_RESULTS
message_ttl = 300

[database]
url = sqlite:///var/lib/ansible-relay/relay.db
# Production : postgresql://user:pass@host/relay

[jwt]
secret_key = <clef secrète HMAC-SHA256>
token_ttl = 3600
```

### Plugin Ansible (`ansible.cfg`)

```ini
[defaults]
inventory = /etc/ansible/relay_inventory.py
connection_plugins = /usr/lib/ansible/plugins/connection
pipelining = true
timeout = 30

[relay_connection]
relay_server = https://relay.example.com
token_file = /etc/ansible/relay_plugin.jwt
key_file = /etc/ansible/relay_plugin_id_rsa

[relay_inventory]
relay_server = https://relay.example.com
token_file = /etc/ansible/relay_plugin.jwt
only_connected = false
```

---

## 17. Roadmap MVP vs V2

### MVP (Phase 1-3)

| Fonctionnalité | Statut |
|---|---|
| relay-agent : WebSocket + exec_command | MVP |
| relay-agent : put_file / fetch_file (< 500KB) | MVP |
| relay-agent : become via stdin | MVP |
| relay-agent : tâches async (registre fichier) | MVP |
| relay-agent : max_concurrent_tasks | MVP |
| relay-agent : reconnexion avec backoff expo | MVP |
| relay server : FastAPI + WebSocket handler | MVP |
| relay server : NATS JetStream (RELAY_TASKS + RELAY_RESULTS) | MVP |
| relay server : REST API exec/upload/fetch | MVP |
| relay server : JWT auth (rôles agent/plugin) | MVP |
| relay server : enrollment + blacklist révocation | MVP |
| relay server : SQLite | MVP |
| connection plugin : exec_command + put_file + fetch_file | MVP |
| connection plugin : pipelining | MVP |
| inventory plugin : tous agents + only_connected | MVP |
| Scope OS | Linux uniquement |

### V2

| Fonctionnalité | Priorité |
|---|---|
| Chunking fichiers > 1MB | Haute |
| Stdout streaming (HTTP chunked) | Haute |
| PostgreSQL (multi-nodes) | Haute |
| mTLS (certificats client) | Moyenne |
| Token rotation automatique (SPIFFE-style) | Moyenne |
| Groupes et tags dynamiques dans l'inventaire | Moyenne |
| K8s Job runner (hybride subprocess/pod) | Basse |
| Support Windows (PowerShell) | Basse |
| Dashboard de monitoring des agents | Basse |

---

*Document généré le 2026-03-03 — Session de brainstorming architecture AnsibleRelay*
