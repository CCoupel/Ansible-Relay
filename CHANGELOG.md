# Changelog — Ansible-SecAgent

All notable changes to this project will be documented in this file.

---

## [Unreleased] — Phase 11 : Event Hooks Unifiés

### Added
- **Système de hooks unifié** (`internal/hooks/`) — actions déclenchées par événements agent via fichier JSON de configuration (`RELAY_HOOKS_CONFIG`, défaut `/etc/secagent-server/hooks.json`)
  - 4 types d'action : `webhook` (HTTP POST + HMAC-SHA256), `shell` (subprocess + env SECAGENT_*), `file` (append O_CREATE), `api` (méthode configurable)
  - Moteur de template : `{{hostname}}`, `{{event}}`, `{{timestamp}}`, `{{status}}`, `{{enrolled_at}}`
  - Dispatcher asynchrone (queue 1000, non-bloquant) avec hot-reload via `SIGHUP`
- **Table `action_log`** — trace toutes les exécutions d'actions (remplace `webhook_deliveries`)
- **CLI** : `secagent-server hooks status` et `secagent-server hooks log [--limit] [--event] [--hostname] [--format]`
- **Route** `GET /api/admin/hooks/log` — consultation des logs d'action via API admin
- **Dispatch événements** : `host.new` (enrollment), `host.up`/`host.down` (WebSocket), `host.revoked`, `host.deleted`
- **Spec** `DOC/server/HOOKS_SPEC.md` — documentation complète du système

### Removed
- Package `internal/webhooks/` — remplacé par `internal/hooks/`
- Tables SQLite `webhooks` et `webhook_deliveries` — remplacées par `action_log`
- Routes CRUD admin webhooks (`POST/GET/DELETE /api/admin/webhooks/...`)
- Fichiers : `store_webhooks.go`, `admin_webhooks.go`

### Fixed
- **`internal/storage/store.go`** — parsing `DATABASE_URL` avec préfixe `sqlite:///` corrigé (off-by-one → `strings.HasPrefix` longest-first)

### Infrastructure
- `GO/Dockerfile` — chemin build corrigé (`./cmd/server` → `./cmd/secagent-server`)
- `GO/Dockerfile.caddy` — nouveau, Caddyfile baked dans l'image (compatibilité remote Docker)
- `GO/docker-compose.yml` — NATS via args CLI, Caddy build context, DATABASE_URL chemin direct

---

## Phases précédentes

### Phase 10 — Enrollment Token (clôturée)
- Système de tokens d'enrollment pré-signés (issues #87–#96)
- API admin : génération, révocation, liste tokens
- Validation côté agent : phase 0 enrollment

### Phases 1–9 (clôturées)
- Infrastructure serveur, agent minion, plugins Ansible, inventaire, authentification JWT, NATS JetStream, WebSocket multiplexé, CLI cobra, sécurité RSA-4096
