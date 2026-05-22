#!/usr/bin/env bash
# smoke-proxy.sh — Smoke test topologie multi-zones (issue #120)
#
# Vérifie :
#   1. Health des 3 relays (proxy + dmz1 + dmz2)
#   2. Agents enregistrés dans chaque relay DMZ
#   3. relay-proxy agrège les agents des 2 zones via GET /api/agents
#   4. Isolation réseau : relay-dmz1 ne voit pas relay-dmz2
#
# Usage :
#   bash smoke-proxy.sh
#   PROXY_API=http://192.168.1.218:7780 ADMIN_TOKEN=mytoken bash smoke-proxy.sh
#
# Variables d'environnement :
#   PROXY_API    : URL de l'API relay-proxy (défaut: http://192.168.1.218:7780)
#   DMZ1_API     : URL de l'API relay-dmz1  (défaut: http://192.168.1.218:7783)
#   DMZ2_API     : URL de l'API relay-dmz2  (défaut: http://192.168.1.218:7784)
#   ADMIN_TOKEN  : token Bearer admin       (défaut: changeme-admin-token)
#   WAIT_AGENTS  : secondes à attendre avant de compter les agents (défaut: 10)
#   DOCKER_HOST  : remote Docker daemon     (défaut: tcp://192.168.1.218:2375)

set -euo pipefail

PROXY_API="${PROXY_API:-http://192.168.1.218:7780}"
DMZ1_API="${DMZ1_API:-http://192.168.1.218:7783}"
DMZ2_API="${DMZ2_API:-http://192.168.1.218:7784}"
ADMIN_TOKEN="${ADMIN_TOKEN:-changeme-admin-token}"
WAIT_AGENTS="${WAIT_AGENTS:-10}"

PASS=0
FAIL=0

# ─────────────────────────────────────────────────────────────────────────────
# Helpers
# ─────────────────────────────────────────────────────────────────────────────

ok()   { echo "  ✓ $*"; ((PASS++)); }
fail() { echo "  ✗ $*"; ((FAIL++)); }

# count_agents <url> — retourne le nombre d'agents via GET /api/agents
count_agents() {
  local url="$1"
  local raw
  raw=$(curl -sf --max-time 5 \
    -H "Authorization: Bearer ${ADMIN_TOKEN}" \
    "${url}/api/agents" 2>/dev/null) || { echo "0"; return; }

  # Support réponse tableau JSON ou objet {"agents": [...]} ou {"data": [...]}
  echo "$raw" | python3 -c "
import json, sys
data = json.load(sys.stdin)
if isinstance(data, list):
    print(len(data))
elif isinstance(data, dict):
    for key in ('agents', 'data', 'items', 'results'):
        if key in data and isinstance(data[key], list):
            print(len(data[key]))
            sys.exit(0)
    print(0)
else:
    print(0)
" 2>/dev/null || echo "0"
}

# ─────────────────────────────────────────────────────────────────────────────
# Étape 1 — Health checks
# ─────────────────────────────────────────────────────────────────────────────

echo ""
echo "=== Smoke Test — Multi-Zone Proxy (issue #120) ==="
echo ""
echo "── Étape 1/4 : Health checks ──────────────────────────────"

for entry in "relay-proxy:${PROXY_API}" "relay-dmz1:${DMZ1_API}" "relay-dmz2:${DMZ2_API}"; do
  svc="${entry%%:*}"
  url="${entry#*:}"
  if curl -sf --max-time 5 "${url}/health" > /dev/null 2>&1; then
    ok "${svc} /health → OK"
  else
    fail "${svc} /health → ÉCHEC (URL: ${url})"
  fi
done

# ─────────────────────────────────────────────────────────────────────────────
# Étape 2 — Attendre l'enrollment des agents
# ─────────────────────────────────────────────────────────────────────────────

echo ""
echo "── Étape 2/4 : Attente enrollment agents (${WAIT_AGENTS}s) ──────────────"
sleep "${WAIT_AGENTS}"

# ─────────────────────────────────────────────────────────────────────────────
# Étape 3 — Agents par zone
# ─────────────────────────────────────────────────────────────────────────────

echo ""
echo "── Étape 3/4 : Agents par zone ───────────────────────────"

DMZ1_COUNT=$(count_agents "${DMZ1_API}")
DMZ2_COUNT=$(count_agents "${DMZ2_API}")
PROXY_COUNT=$(count_agents "${PROXY_API}")

echo "  relay-dmz1  : ${DMZ1_COUNT} agent(s)"
echo "  relay-dmz2  : ${DMZ2_COUNT} agent(s)"
echo "  relay-proxy : ${PROXY_COUNT} agent(s) agrégés"

[ "${DMZ1_COUNT}" -gt 0 ]  && ok "Agents présents dans DMZ1 (${DMZ1_COUNT})"  || fail "Aucun agent dans DMZ1"
[ "${DMZ2_COUNT}" -gt 0 ]  && ok "Agents présents dans DMZ2 (${DMZ2_COUNT})"  || fail "Aucun agent dans DMZ2"

TOTAL_DMZ=$((DMZ1_COUNT + DMZ2_COUNT))
[ "${PROXY_COUNT}" -ge "${TOTAL_DMZ}" ] && [ "${PROXY_COUNT}" -gt 0 ] \
  && ok "relay-proxy agrège ${PROXY_COUNT}/${TOTAL_DMZ} agents (DMZ1 + DMZ2)" \
  || fail "relay-proxy voit ${PROXY_COUNT} agents — attendu >= ${TOTAL_DMZ}"

# ─────────────────────────────────────────────────────────────────────────────
# Étape 4 — Isolation réseau : relay-dmz1 ne peut pas joindre relay-dmz2
# ─────────────────────────────────────────────────────────────────────────────

echo ""
echo "── Étape 4/4 : Isolation réseau ──────────────────────────"

if command -v docker &>/dev/null; then
  # relay-dmz1 n'est que sur dmz1-net — relay-dmz2 n'est que sur dmz2-net
  # wget avec timeout court : doit échouer (NXDOMAIN ou refus connexion)
  if docker exec relay-dmz1 wget -q --timeout=3 -O- "http://relay-dmz2:7770/health" \
       > /dev/null 2>&1; then
    fail "Isolation DMZ — relay-dmz1 peut joindre relay-dmz2 (attendu: ÉCHEC)"
  else
    ok "Isolation DMZ — relay-dmz1 ne voit pas relay-dmz2 (réseaux isolés)"
  fi
else
  echo "  ~ docker CLI non disponible localement — test isolation ignoré"
  echo "    (Pour tester manuellement : DOCKER_HOST=tcp://192.168.1.218:2375 bash smoke-proxy.sh)"
fi

# ─────────────────────────────────────────────────────────────────────────────
# Bilan
# ─────────────────────────────────────────────────────────────────────────────

echo ""
echo "════════════════════════════════════════════════════════════"
echo "  RÉSULTAT : ${PASS} PASS  /  ${FAIL} FAIL"
echo "════════════════════════════════════════════════════════════"
echo ""

[ "${FAIL}" -eq 0 ] && exit 0 || exit 1
