#!/bin/bash
# Script de test des plugins Ansible (connection + inventory)
#
# Pré-conditions:
# - Server relay sur 192.168.1.218:7770
# - Agents connectés (qualif-host-01, 02, 03)
# - Python 3 et ansible installés localement
# - Ansible playbook dans playbooks/test_relay_plugins.yml

set -e

RELAY_SERVER="192.168.1.218:7770"
JWT_SECRET_KEY="dev-secret-key-for-qualification-only-change-in-prod"
TOKEN_FILE="/tmp/relay_token.jwt"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "[*] AnsibleRelay Plugin Test"
echo "[*] Server: http://$RELAY_SERVER"
echo ""

# 1. Generate JWT plugin token
echo "[*] Step 1: Generating plugin JWT token..."
python3 << 'PYEOF'
import json, uuid, base64, hmac, hashlib
from datetime import datetime, timezone

jwt_secret = "dev-secret-key-for-qualification-only-change-in-prod"

# Header
header_b64 = base64.urlsafe_b64encode(json.dumps({"alg": "HS256", "typ": "JWT"}).encode()).rstrip(b'=')

# Payload
jti = str(uuid.uuid4())
now = int(datetime.now(timezone.utc).timestamp())
payload = {
    "sub": "ansible-plugin-test",
    "role": "plugin",
    "jti": jti,
    "iat": now,
    "exp": now + 3600,
}
payload_b64 = base64.urlsafe_b64encode(json.dumps(payload).encode()).rstrip(b'=')

# Signature
message = header_b64 + b'.' + payload_b64
signature = hmac.new(jwt_secret.encode(), message, hashlib.sha256).digest()
signature_b64 = base64.urlsafe_b64encode(signature).rstrip(b'=')

token = (header_b64 + b'.' + payload_b64 + b'.' + signature_b64).decode()
print(token)
PYEOF > "$TOKEN_FILE"

echo "✅ Token saved to $TOKEN_FILE"
echo ""

# 2. Verify server is reachable
echo "[*] Step 2: Verifying relay server connectivity..."
if curl -s "http://$RELAY_SERVER/health" | grep -q "ok"; then
    echo "✅ Server is healthy"
else
    echo "❌ Server is not responding"
    exit 1
fi
echo ""

# 3. Test inventory plugin
echo "[*] Step 3: Testing inventory plugin (GET /api/inventory)..."
INVENTORY=$(curl -s "http://$RELAY_SERVER/api/inventory" \
  -H "Authorization: Bearer $(cat $TOKEN_FILE)")

echo "📦 Inventory:"
echo "$INVENTORY" | python3 -m json.tool 2>/dev/null || echo "$INVENTORY"
echo ""

# 4. Check that agents are in inventory
echo "[*] Step 4: Verifying agents in inventory..."
if echo "$INVENTORY" | grep -q "qualif-host"; then
    echo "✅ Agents found in inventory"
else
    echo "❌ No agents found in inventory"
    exit 1
fi
echo ""

# 5. Run Ansible playbook test
echo "[*] Step 5: Running Ansible playbook with relay connection plugin..."
cd "$SCRIPT_DIR"

export RELAY_TOKEN_FILE="$TOKEN_FILE"
export ANSIBLE_LIBRARY="./ansible_plugins"
export ANSIBLE_CONFIG="./ansible.cfg"

echo "[*] Command: ansible-playbook playbooks/test_relay_plugins.yml -i relay_inventory -v"
echo ""

ansible-playbook \
  playbooks/test_relay_plugins.yml \
  -i relay_inventory \
  -e "relay_server_url=http://$RELAY_SERVER" \
  -v

echo ""
echo "✅ All tests completed successfully!"
echo ""
echo "Token file: $TOKEN_FILE (valid for 1 hour)"
