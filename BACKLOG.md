# BACKLOG AnsibleRelay — 41 tâches

Date création : 2026-03-03
Status : Phase 0 complète, Phase 1 en attente de confirmation utilisateur

## Vue d'ensemble

- **Phase 1 (relay-agent)** : 13 tâches (#4 à #23)
- **Phase 2 (relay-server)** : 11 tâches (#24 à #34)
- **Phase 3 (plugins Ansible)** : 7 tâches (#35 à #41)
- **Total** : 41 tâches

---

## PHASE 1 — relay-agent (dossier agent/) — 13 tâches

### Prérequis
- Lire ARCHITECTURE.md section "Agent"
- Lire HLD.md schémas agent

### Tâches Phase 1

| # | Tâche | Owner | Status | Bloquée par |
|---|-------|-------|--------|------------|
| #4 | facts_collector.py — collecte facts système | dev-agent | pending | - |
| #6 | relay_agent.py — enrollment POST /api/register + RSA-4096 | dev-agent | pending | #4 |
| #8 | relay_agent.py — connexion WSS + backoff exponentiel (1s→60s) | dev-agent | pending | #6 |
| #9 | relay_agent.py — dispatcher messages WS (exec/put_file/fetch_file/cancel) | dev-agent | pending | #8 |
| #11 | relay_agent.py — exec_command subprocess + stdout streaming + buffer 5MB | dev-agent | pending | #9 |
| #13 | relay_agent.py — put_file (base64, mkdir -p, chmod) | dev-agent | pending | #9 |
| #14 | relay_agent.py — fetch_file (lecture, base64, limite 500KB) | dev-agent | pending | #9 |
| #15 | async_registry.py — registre JSON persisté, reprise redémarrage | dev-agent | pending | #9 |
| #17 | relay-agent.service — unit file systemd (NoNewPrivileges, ProtectSystem) | dev-agent | pending | - |
| #19 | Tests unitaires relay-agent Phase 1 | test-writer | pending | #4, #6, #8, #9, #11, #13, #14, #15, #17 |
| #20 | QA — pytest Phase 1, rapport (nb tests, pass, fail, détails) | qa | pending | #19 |
| #22 | Security review — audit Phase 1 relay-agent | security-reviewer | pending | #20 |
| #23 | Deploy qualif Phase 1 — relay-agent sur 192.168.1.217 | deploy-qualif | pending | #22 |

**Validation Phase 1 → Phase 2** :
- ✓ TOUTES tâches #4-#23 completed
- ✓ qa : 0 test en échec
- ✓ security-reviewer : 0 finding CRITIQUE/HAUT
- ✓ deploy-qualif : OK
- ✓ Confirmation utilisateur

---

## PHASE 2 — relay-server (dossier server/) — 11 tâches

### Prérequis
- Lire ARCHITECTURE.md section "Server"
- Lire HLD.md flux messages et broker
- Phase 1 validée et déployée

### Tâches Phase 2

| # | Tâche | Owner | Status | Bloquée par |
|---|-------|-------|--------|------------|
| #24 | agent_store.py — modèles SQLite (agents, authorized_keys, blacklist) | dev-relay | pending | - |
| #25 | routes_register.py — enrollment + auth JWT + blacklist JTI | dev-relay | pending | #24 |
| #26 | ws_handler.py — connexions WS, futures, on_ws_close | dev-relay | pending | #25 |
| #27 | nats_client.py — NATS JetStream (RELAY_TASKS, RELAY_RESULTS) | dev-relay | pending | #24 |
| #28 | routes_exec.py — endpoints /api/exec, /api/upload, /api/fetch, /api/inventory | dev-relay | pending | #26, #27 |
| #29 | main.py — FastAPI app (lifespan, health check) | dev-relay | pending | #25, #26, #27, #28 |
| #30 | docker-compose.yml + Dockerfile — NATS, relay-api, caddy | dev-relay | pending | #29 |
| #31 | Tests unitaires relay-server Phase 2 | test-writer | pending | #24-#30 |
| #32 | QA — pytest Phase 2, rapport | qa | pending | #31 |
| #33 | Security review — audit Phase 2 relay-server | security-reviewer | pending | #32 |
| #34 | Deploy qualif Phase 2 — relay-server complet sur 192.168.1.217 | deploy-qualif | pending | #33, #23 |

**Validation Phase 2 → Phase 3** :
- ✓ TOUTES tâches #24-#34 completed
- ✓ qa : 0 test en échec
- ✓ security-reviewer : 0 finding CRITIQUE/HAUT
- ✓ deploy-qualif : OK
- ✓ Confirmation utilisateur

---

## PHASE 3 — plugins Ansible (dossier ansible_plugins/) — 7 tâches

### Prérequis
- Lire ARCHITECTURE.md section "Ansible Plugins"
- Lire HLD.md flux plugins
- Phase 2 validée et déployée

### Tâches Phase 3

| # | Tâche | Owner | Status | Bloquée par |
|---|-------|-------|--------|------------|
| #35 | connection_plugins/relay.py — ConnectionBase (exec_command, put_file, fetch_file, pipelining, become) | dev-plugins | pending | #34 |
| #36 | inventory_plugins/relay_inventory.py — InventoryModule (GET /api/inventory) | dev-plugins | pending | #34 |
| #37 | Tests unitaires + E2E plugins Phase 3 | test-writer | pending | #35, #36 |
| #38 | QA — pytest Phase 3 (unitaire + E2E), rapport | qa | pending | #37 |
| #39 | Security review global — audit Phase 3 + revue MVP complète | security-reviewer | pending | #38 |
| #40 | Deploy qualif Phase 3 — test E2E complet sur 192.168.1.217 | deploy-qualif | pending | #39 |
| #41 | Deploy prod — Helm chart Kubernetes (après confirmation utilisateur) | deploy-prod | pending | #40 |

**Validation Phase 3 → Prod** :
- ✓ TOUTES tâches #35-#40 completed
- ✓ qa : 0 test en échec, tests E2E couvrant cas nominaux + erreurs + async
- ✓ security-reviewer : 0 finding CRITIQUE/HAUT, audit global MVP cohérent
- ✓ deploy-qualif : OK
- ✓ Confirmation utilisateur explicite pour #41 (prod)

---

## Dépendances critiques

```
#4 (facts) → #6 (enrollment) → #8 (WSS) → #9 (dispatcher) → #11/#13/#14/#15
#19 (tests) bloque par toutes tâches Phase 1
#20 (QA) → #22 (security) → #23 (deploy-qualif Phase 1)

#24 (DB) → #25 (auth) → #26 (WS) + #27 (NATS)
#26+#27 → #28 (endpoints) → #29 (main)
#31 (tests) bloque par toutes tâches Phase 2
#32 (QA) → #33 (security) → #34 (deploy-qualif Phase 2)

#34 (Phase 2 déployée) → #35 (connection) + #36 (inventory)
#37 (tests E2E) → #38 (QA) → #39 (security global) → #40 (deploy E2E)
#40 (deploy qualif Phase 3) → #41 (prod) [après confirmation utilisateur]
```

---

## Checklist sécurité par phase

### Phase 1 (relay-agent)
- [ ] TLS obligatoire (WSS)
- [ ] JWT signé côté agent
- [ ] Masquage `become_pass` dans logs
- [ ] Validation entrées (command injection)
- [ ] Isolation subprocess (pas de threads)
- [ ] RSA-4096 pour enrollment

### Phase 2 (relay-server)
- [ ] TLS obligatoire (WSS + HTTPS)
- [ ] JWT signé + rôles agent/plugin/admin
- [ ] Blacklist JTI (token revocation)
- [ ] Validation entrées API (injection)
- [ ] Masquage `become_pass` dans logs et stockage
- [ ] Rate limiting

### Phase 3 (plugins Ansible)
- [ ] Validation tokens plugin
- [ ] Pas de fuite credentials dans logs
- [ ] TLS sur appels REST au serveur
- [ ] Audit global bout-en-bout

---

## Métriques de succès

| Phase | Métriques |
|-------|-----------|
| Phase 1 | 0 test en échec, relay-agent enregistré et connecté en WSS |
| Phase 2 | 0 test en échec, relay-server reçoit enregistrement, gère WebSocket |
| Phase 3 | 0 test en échec, playbook Ansible exécuté via plugin relay |
| MVP | E2E : enrollment → playbook exec → résultat, prod déployée Kubernetes |
