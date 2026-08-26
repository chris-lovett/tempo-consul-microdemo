#!/usr/bin/env bash
# deploy/ec2/setup.sh
#
# Applies all Consul config entries for vm-dc in the correct order.
# Run this after the peering token has been established and the
# tracing-demo namespace has been created (see deploy/consul/peering-setup.md).
#
# Usage:
#   export CONSUL_HTTP_TOKEN=REDACTED_BOOTSTRAP_TOKEN
#   export CONSUL_HTTP_ADDR=http://127.0.0.1:8500
#   bash deploy/ec2/setup.sh
#
# Re-running this script is safe — all consul config write operations
# are idempotent.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# ── Preflight ────────────────────────────────────────────────────────────────

if [[ -z "${CONSUL_HTTP_TOKEN:-}" ]]; then
  echo "ERROR: CONSUL_HTTP_TOKEN is not set."
  echo "  export CONSUL_HTTP_TOKEN=REDACTED_BOOTSTRAP_TOKEN"
  exit 1
fi

if [[ -z "${CONSUL_HTTP_ADDR:-}" ]]; then
  export CONSUL_HTTP_ADDR=http://127.0.0.1:8500
fi

echo "→ Consul addr:  $CONSUL_HTTP_ADDR"
echo "→ Repo root:    $REPO_ROOT"
echo ""

# Verify Consul is reachable
if ! consul info > /dev/null 2>&1; then
  echo "ERROR: Cannot reach Consul at $CONSUL_HTTP_ADDR"
  exit 1
fi

# ── Step 1 — Create tracing-demo namespace ───────────────────────────────────
# vm-dc is Consul Enterprise. Services imported from ocp-dc live in the
# tracing-demo namespace there. Without this namespace existing locally,
# vm-dc silently drops the imported peer endpoints.

echo "[1/6] Creating tracing-demo namespace on vm-dc..."
curl -sf -X PUT -H "X-Consul-Token: $CONSUL_HTTP_TOKEN" \
  "$CONSUL_HTTP_ADDR/v1/namespace" \
  -d '{"Name":"tracing-demo"}' > /dev/null \
  && echo "      OK (created or already exists)"

# ── Step 2 — Service definitions ─────────────────────────────────────────────
# Service definitions live in /etc/consul.d/ and are managed by the Consul
# agent directly. Copy them and reload.

echo "[2/6] Copying service definitions to /etc/consul.d/..."
sudo cp "$REPO_ROOT/deploy/ec2/web.hcl"    /etc/consul.d/web.hcl
sudo cp "$REPO_ROOT/deploy/ec2/api.hcl"    /etc/consul.d/api.hcl
sudo cp "$REPO_ROOT/deploy/ec2/client.hcl" /etc/consul.d/client.hcl
echo "      Reloading Consul agent..."
consul reload -token "$CONSUL_HTTP_TOKEN"
echo "      OK"

# ── Step 3 — Exported services ───────────────────────────────────────────────
# Export web and api from vm-dc to the ocp-dc peer so that ocp-dc can
# resolve vm-dc services as failover targets in its own service resolvers.

echo "[3/6] Applying exported-services (web + api → ocp-dc)..."
consul config write "$REPO_ROOT/deploy/ec2/exported-services-vm-dc.hcl"
echo "      OK"

# ── Step 4 — Service intentions ──────────────────────────────────────────────
# Two intention files:
#   service-intentions-vm-dc.hcl          — web + api → otel-collector (ocp-dc peer)
#   service-intentions-vm-dc-failover.hcl — api → web (local), ocp-dc frontend → web

echo "[4/6] Applying service intentions..."
consul config write "$REPO_ROOT/deploy/ec2/service-intentions-vm-dc.hcl"
consul config write "$REPO_ROOT/deploy/ec2/service-intentions-vm-dc-failover.hcl"
echo "      OK"

# ── Step 5 — Service defaults ────────────────────────────────────────────────
# service-defaults-web.hcl:
#   Sets MeshGateway.Mode=none so local failover-target~0 routes directly
#   to web's sidecar rather than through the mesh gateway.
#
# service-defaults-client.hcl:
#   Configures PassiveHealthCheck with MaxEjectionPercent=100 on the web
#   upstream so Envoy can eject the endpoint and promote to the peer
#   failover target when web goes down.

echo "[5/6] Applying service-defaults (web + client)..."
consul config write "$REPO_ROOT/deploy/ec2/service-defaults-web.hcl"
consul config write "$REPO_ROOT/deploy/ec2/service-defaults-client.hcl"
echo "      OK"

# ── Step 6 — Service resolver ────────────────────────────────────────────────
# Explicit failover: web → frontend in ocp-dc tracing-demo namespace.
# Namespace = "tracing-demo" is required because ocp-dc uses namespace
# mirroring, so frontend is imported as default/tracing-demo/frontend on vm-dc.

echo "[6/6] Applying service-resolver (web → ocp-dc frontend)..."
consul config write "$REPO_ROOT/deploy/ec2/service-resolver-web-failover.hcl"
echo "      OK"

# ── Step 7 — Verify ──────────────────────────────────────────────────────────

echo "[7/7] Verifying config entries..."
echo ""

echo "  Namespaces:"
curl -s -H "X-Consul-Token: $CONSUL_HTTP_TOKEN" \
  "$CONSUL_HTTP_ADDR/v1/namespaces" | jq -r '.[].Name' | sed 's/^/    /'

echo ""
echo "  Config entries:"
for kind in sameness-group exported-services service-intentions service-resolver; do
  entries=$(consul config list -kind "$kind" 2>/dev/null || true)
  if [[ -n "$entries" ]]; then
    while IFS= read -r name; do
      echo "    $kind/$name"
    done <<< "$entries"
  fi
done

echo ""
echo "  Peering status:"
curl -s -H "X-Consul-Token: $CONSUL_HTTP_TOKEN" \
  "$CONSUL_HTTP_ADDR/v1/peering/ocp-dc" \
  | jq -r '"    State: \(.State)  Imported: \(.StreamStatus.ImportedServices | join(", "))"' \
  2>/dev/null || echo "    (peering not established yet)"

echo ""
echo "✓ vm-dc setup complete."
echo ""
echo "Next: apply ocp-dc resources from your Mac:"
echo "  kubectl apply -f deploy/consul/exported-services-ocp-dc-failover.yaml -n tracing-demo"
echo "  kubectl apply -f deploy/consul/service-intentions-otel-collector.yaml -n tracing-demo"
echo "  kubectl patch serviceintentions allow-frontend-failover -n tracing-demo \\"
echo "    --type=merge -p '{\"spec\":{\"sources\":["
echo "      {\"action\":\"allow\",\"name\":\"*\"},"
echo "      {\"action\":\"allow\",\"name\":\"web\",\"peer\":\"vm-dc\"},"
echo "      {\"action\":\"allow\",\"name\":\"client\",\"peer\":\"vm-dc\"}"
echo "    ]}}'"
