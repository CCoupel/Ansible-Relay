# /marketing — Site GitHub Pages AnsibleRelay

Génère ou met à jour le site marketing AnsibleRelay sur la branche `gh-pages`.

## Instructions

### Étape 1 — Lire les sources du projet

Lis ces fichiers pour extraire les informations nécessaires :
- `C:/Users/cyril/Documents/VScode/Ansible_Agent/DOC/common/ARCHITECTURE.md` — specs techniques, fonctionnalités, sécurité
- `C:/Users/cyril/Documents/VScode/Ansible_Agent/DOC/common/HLD.md` — schémas d'architecture, flux, décisions
- `C:/Users/cyril/Documents/VScode/Ansible_Agent/DOC/common/BACKLOG.md` — phases complètes + phases à venir
- `C:/Users/cyril/Documents/VScode/Ansible_Agent/DOC/security/SECURITY.md` — modèle de sécurité (enrollment, rôles, tokens)

### Étape 2 — Préparer la branche gh-pages

Exécute les commandes suivantes depuis `C:/Users/cyril/Documents/VScode/Ansible_Agent/` :

```bash
cd C:/Users/cyril/Documents/VScode/Ansible_Agent

# Récupère l'URL du remote GitHub
REMOTE_URL=$(git remote get-url origin 2>/dev/null || echo "")

# Vérifie si la branche gh-pages existe (remote)
git fetch origin gh-pages 2>/dev/null || true

# Crée un répertoire temporaire pour le site
SITE_DIR=$(mktemp -d)
```

Si la branche `gh-pages` existe déjà en remote :
```bash
git worktree add "$SITE_DIR" gh-pages 2>/dev/null || git worktree add "$SITE_DIR" origin/gh-pages
```

Si elle n'existe pas :
```bash
git worktree add --orphan -b gh-pages "$SITE_DIR"
```

### Étape 3 — Générer les fichiers du site

Dans le répertoire `$SITE_DIR`, crée les fichiers suivants :

#### `index.html`

Génère une page HTML5 complète avec les sections suivantes :

**Structure de la page :**

```
<nav> — Logo AnsibleRelay + liens de navigation
<section id="hero"> — Tagline + CTA
<section id="problem"> — Le problème Ansible
<section id="solution"> — La solution AnsibleRelay
<section id="security"> — Sécurité by design
<section id="features"> — Fonctionnalités implémentées (par version/phase)
<section id="roadmap"> — Roadmap / fonctionnalités à venir
<section id="architecture"> — Schéma d'architecture ASCII converti en visuel
<section id="quickstart"> — Démarrage rapide
<footer> — Liens GitHub, licence
```

**Contenu à inclure** (extrait des fichiers sources) :

**Hero :**
- Titre : "AnsibleRelay — Ansible sans SSH entrant"
- Sous-titre : "Exécutez vos playbooks sur des hôtes derrière NAT, firewall ou DMZ. Les agents initient eux-mêmes la connexion."
- Badge sécurité : "Zero Trust · TLS Everywhere · JWT + RSA-4096"

**Section problème (extraire de ARCHITECTURE.md §1) :**
- SSH entrant impossible derrière NAT/firewall
- Exposition de port 22 = surface d'attaque
- Salt Minion : alternative lourde, incompatible Ansible natif
- Edge computing, DMZ, cloud privé : cas d'usage réels

**Section solution :**
- Modèle inverse : agent → serveur (jamais l'inverse)
- Compatible Ansible natif (connection plugin + inventory plugin)
- Schéma ASCII simplifié :
  ```
  Ansible Control Node → [HTTPS] → Relay Server ← [WSS] ← Agent
  ```

**Section sécurité (axe principal, extraire de ARCHITECTURE.md §7) :**
- RSA-4096 : chaque agent génère sa paire de clefs au boot
- Enrôlement contrôlé : `authorized_keys` en DB, jamais TOFU
- JWT signé + chiffré RSAES-OAEP : token illisible sans la clef privée de l'agent
- Blacklist JTI : révocation immédiate, connexion WS fermée (code 4001)
- Dual-key JWT : rotation des secrets sans interruption de service (grace period configurable)
- TLS obligatoire sur toutes les connexions (WSS + HTTPS)
- `become_pass` masqué dans tous les logs

**Section fonctionnalités implémentées** (par phase, extraire de BACKLOG.md) :
- v0.1 — Agent Python MVP (enrollment, exec, put_file, fetch_file, become, async)
- v0.2 — Server Python MVP (FastAPI, NATS JetStream, inventaire dynamique)
- v0.3 — Plugins Ansible (connection plugin, inventory plugin)
- v1.0 — Réécriture GO (server 4.65 MiB, agent, inventory binary)
- v1.1 — CLI Management (15 commandes cobra, rotation des clefs, dual-key JWT)

**Section roadmap** (phases suspendues = à venir) :
- Production Kubernetes (Helm chart, StatefulSet NATS, Ingress TLS)
- Documentation & Hardening (rate limiting, audit logs, RBAC)
- Plugin FreeIPA (intégration PKI enterprise, inventory LDAP)

**Quickstart :**
```bash
# Sur le serveur
docker compose up -d

# Autoriser un agent
docker exec relay-api relay-server minions authorize my-host

# Sur l'hôte cible
# relay-agent s'installe via systemd et se connecte automatiquement

# Lancer un playbook Ansible
ansible-playbook -i relay_inventory.py site.yml
```

#### `style.css`

Génère un CSS moderne, sobre et professionnel :
- Palette : fond sombre (#0d1117), accent vert sécurité (#00d4aa), texte clair (#e6edf3)
- Police : system-ui / monospace pour le code
- Layout : sections full-width, max-width 1100px centré
- Hero : gradient sombre avec badge sécurité en accent
- Cards pour les fonctionnalités (grid 3 colonnes)
- Timeline pour la roadmap
- Code blocks avec fond #161b22
- Responsive (mobile-first)
- Pas de framework externe (CSS pur, zéro dépendance)

#### `_config.yml`

```yaml
# GitHub Pages config
theme: null
plugins: []
```

### Étape 4 — Committer et pousser

```bash
cd "$SITE_DIR"

# Ajoute tous les fichiers
git add index.html style.css _config.yml

# Commit avec date
git commit -m "marketing: update GitHub Pages site $(date +%Y-%m-%d)"

# Pousse vers origin gh-pages
git push origin gh-pages

# Nettoie le worktree
cd C:/Users/cyril/Documents/VScode/Ansible_Agent
git worktree remove "$SITE_DIR" --force
```

### Étape 5 — Confirmer à l'utilisateur

Affiche :
```
Site marketing mis à jour sur gh-pages.

URL GitHub Pages : https://<owner>.github.io/<repo>/
(Accessible dans ~2 minutes après le push)

Sections générées :
- Hero + tagline sécurité
- Problème SSH entrant
- Solution AnsibleRelay
- Sécurité by design (RSA-4096, JWT, blacklist, dual-key)
- Fonctionnalités v0.1 → v1.1
- Roadmap (K8s, FreeIPA, hardening)
- Quickstart
```

## Contraintes de génération

- **HTML pur** — zéro dépendance JS externe, zéro CDN (site statique autonome)
- **Contenu précis** — toutes les affirmations sécurité doivent être vérifiées dans ARCHITECTURE.md avant d'être écrites
- **Ton pragmatique** — pas de marketing creux, des faits techniques concrets
- **Axe sécurité prioritaire** — c'est la différenciation principale vs SSH classique
- **Versioning** — les features sont rattachées à leur phase/version réelle du BACKLOG.md
