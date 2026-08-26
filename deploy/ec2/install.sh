#!/usr/bin/env bash
# deploy/ec2/install.sh
#
# Builds and installs the vm-dc Go binaries directly on the EC2 instance.
# Run this ON THE EC2 HOST — no SCP or hostname resolution required.
#
# Prerequisites (installed once):
#   sudo yum install -y golang git   # or: sudo dnf install -y golang git
#
# Usage:
#   cd /path/to/repo
#   bash deploy/ec2/install.sh
#
# What it does:
#   1. Builds vm-web, vm-api, vm-client for linux/amd64
#   2. Installs binaries to /usr/local/bin/
#   3. Copies updated service definitions to /etc/consul.d/
#   4. Reloads Consul agent
#   5. Restarts web and api systemd services
#   6. Restarts vm-client background process

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# ── Build binaries ────────────────────────────────────────────────────────────

echo "[1/4] Building vm-dc binaries..."
cd "$REPO_ROOT"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o /tmp/vm-web    ./cmd/vm-web
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o /tmp/vm-api    ./cmd/vm-api
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o /tmp/vm-client ./cmd/vm-client
echo "      OK"

# ── Install binaries ──────────────────────────────────────────────────────────

echo "[2/4] Installing to /usr/local/bin/..."
sudo mv /tmp/vm-web    /usr/local/bin/vm-web
sudo mv /tmp/vm-api    /usr/local/bin/vm-api
sudo mv /tmp/vm-client /usr/local/bin/vm-client
sudo chmod +x /usr/local/bin/vm-web /usr/local/bin/vm-api /usr/local/bin/vm-client
echo "      OK"

# ── Update Consul service definitions ────────────────────────────────────────

echo "[3/4] Updating Consul service definitions..."
sudo cp "$REPO_ROOT/deploy/ec2/web.hcl"    /etc/consul.d/web.hcl
sudo cp "$REPO_ROOT/deploy/ec2/api.hcl"    /etc/consul.d/api.hcl
sudo cp "$REPO_ROOT/deploy/ec2/client.hcl" /etc/consul.d/client.hcl

if [[ -z "${CONSUL_HTTP_TOKEN:-}" ]]; then
  echo "      WARNING: CONSUL_HTTP_TOKEN not set; skipping consul reload."
  echo "      Run manually: export CONSUL_HTTP_TOKEN=<bootstrap-token> && consul reload"
else
  consul reload
  echo "      OK (consul reloaded)"
fi

# ── Restart services ──────────────────────────────────────────────────────────

echo "[4/4] Restarting services..."
sudo systemctl restart web api
sudo pkill -f vm-client 2>/dev/null || true
sleep 1

SERVICE_NAME=client \
PORT=9080 \
UPSTREAM_URI=http://localhost:9095 \
OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 \
  nohup /usr/local/bin/vm-client > /tmp/vm-client.log 2>&1 &

echo "      OK (vm-client pid=$!)"
echo ""
echo "✓ Install complete."
echo ""
echo "Tail vm-client traffic:"
echo "  tail -f /tmp/vm-client.log"
echo ""
echo "Verify services:"
echo "  sudo systemctl status web api --no-pager | grep Active"
echo "  curl -s http://localhost:9090/health"
echo "  curl -s http://localhost:9092/health"
echo "  curl -s http://localhost:9080/health"
