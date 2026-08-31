#!/usr/bin/env bash
# deploy/ec2/terraform/user_data.sh.tpl
#
# Cloud-init script — runs once on first boot.
# Installs and configures Consul Enterprise, Envoy, OTel Collector,
# and fake-service binaries for the vm-dc demo environment.
set -euo pipefail
exec > /var/log/vm-dc-init.log 2>&1

PRIVATE_IP=$(curl -s http://169.254.169.254/latest/meta-data/local-ipv4)
echo "==> vm-dc init started. Private IP: $PRIVATE_IP"

# ── 1. Install Consul Enterprise ─────────────────────────────────────────────
echo "==> Installing Consul Enterprise 2.0.1+ent..."
curl -fsSL https://apt.releases.hashicorp.com/gpg \
  | gpg --dearmor -o /usr/share/keyrings/hashicorp-archive-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/hashicorp-archive-keyring.gpg] \
  https://apt.releases.hashicorp.com $(lsb_release -cs) main" \
  | tee /etc/apt/sources.list.d/hashicorp.list
apt-get update -y -q
apt-get install -y -q consul-enterprise=2.0.1+ent-1

# ── 2. Install Envoy via func-e ───────────────────────────────────────────────
echo "==> Installing Envoy 1.29.9..."
curl -sL 'https://func-e.io/install.sh' | bash -s -- -b /usr/local/bin
func-e use 1.29.9
# func-e installs to the calling user's home; find and copy
ENVOY_BIN=$(find /root /home -name envoy -path "*/1.29.9/*" 2>/dev/null | head -1)
cp "$ENVOY_BIN" /usr/local/bin/envoy
chmod +x /usr/local/bin/envoy

# ── 3. Install OTel Collector ─────────────────────────────────────────────────
echo "==> Installing otelcol-contrib 0.113.0..."
cd /tmp
curl -sLO https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v0.113.0/otelcol-contrib_0.113.0_linux_amd64.deb
dpkg -i otelcol-contrib_0.113.0_linux_amd64.deb
systemctl stop otelcol-contrib 2>/dev/null || true
systemctl disable otelcol-contrib 2>/dev/null || true

# ── 4. Install fake-service binaries ─────────────────────────────────────────
echo "==> Installing fake-service v0.26.2..."
apt-get install -y -q unzip
curl -sLo /tmp/fake-service.zip \
  https://github.com/nicholasjackson/fake-service/releases/download/v0.26.2/fake_service_linux_amd64.zip
cd /tmp && unzip -o fake-service.zip
cp /tmp/fake-service /usr/local/bin/vm-web
cp /tmp/fake-service /usr/local/bin/vm-api
cp /tmp/fake-service /usr/local/bin/vm-client
chmod +x /usr/local/bin/vm-web /usr/local/bin/vm-api /usr/local/bin/vm-client

# ── 5. Consul configuration ───────────────────────────────────────────────────
echo "==> Writing Consul config..."
mkdir -p /etc/consul.d /opt/consul

# License file
cat > /etc/consul.d/consul.lic <<'LICENSE'
${consul_license}
LICENSE

# Main consul.hcl
cat > /etc/consul.d/consul.hcl <<HCL
datacenter  = "vm-dc"
data_dir    = "/opt/consul"
log_level   = "INFO"
node_name   = "$(hostname)"

server           = true
bootstrap_expect = 1

advertise_addr = "$PRIVATE_IP"
bind_addr      = "$PRIVATE_IP"
client_addr    = "127.0.0.1"

license_path = "/etc/consul.d/consul.lic"

ui_config { enabled = true }
connect   { enabled = true }

ports {
  http     = 8500
  https    = 8501
  grpc     = 8502
  grpc_tls = -1
}

acl {
  enabled                  = true
  default_policy           = "deny"
  enable_token_persistence = true

  tokens {
    initial_management = "${bootstrap_token}"
    agent              = "${agent_token}"
  }
}

retry_join = ["${consul_elb}"]
HCL

# Service definitions
cat > /etc/consul.d/web.hcl <<HCL
service {
  name    = "web"
  address = "$PRIVATE_IP"
  port    = 9090
  tags    = ["vm-dc", "tracing-demo"]
  token   = "${web_token}"

  connect {
    sidecar_service {
      proxy {
        upstreams = [
          { destination_name = "api",            local_bind_port = 9091 },
          { destination_name = "frontend",        destination_peer = "ocp-dc", destination_namespace = "tracing-demo", local_bind_port = 9093 },
          { destination_name = "otel-collector",  destination_peer = "ocp-dc", destination_namespace = "tracing-demo", local_bind_port = 9317 }
        ]
      }
    }
  }

  check {
    http     = "http://localhost:9090/health"
    interval = "10s"
    timeout  = "3s"
  }
}
HCL

cat > /etc/consul.d/api.hcl <<HCL
service {
  name    = "api"
  address = "$PRIVATE_IP"
  port    = 9092
  tags    = ["vm-dc", "fake-service", "tracing-demo"]
  token   = "${api_token}"

  connect {
    sidecar_service {}
  }

  check {
    http     = "http://localhost:9092/health"
    interval = "10s"
    timeout  = "3s"
  }
}
HCL

cat > /etc/consul.d/client.hcl <<HCL
service {
  name    = "client"
  address = "$PRIVATE_IP"
  port    = 9080
  tags    = ["vm-dc", "tracing-demo"]
  token   = "${client_token}"

  connect {
    sidecar_service {
      proxy {
        upstreams = [
          { destination_name = "web", local_bind_port = 9095 }
        ]
      }
    }
  }

  check {
    http     = "http://localhost:9080/health"
    interval = "10s"
    timeout  = "3s"
  }
}
HCL

# ── 6. OTel Collector config ──────────────────────────────────────────────────
echo "==> Writing OTel Collector config..."
mkdir -p /etc/otelcol
cat > /etc/otelcol/config.yaml <<'OTEL'
extensions:
  health_check:
    endpoint: "0.0.0.0:13133"

receivers:
  otlp:
    protocols:
      grpc:
        endpoint: "0.0.0.0:4317"
      http:
        endpoint: "0.0.0.0:4318"

processors:
  batch:
    timeout: 5s
    send_batch_size: 512
  memory_limiter:
    check_interval: 1s
    limit_mib: 128
    spike_limit_mib: 32
  resource:
    attributes:
      - key: dc
        value: "vm-dc"
        action: insert

exporters:
  otlphttp:
    endpoint: "${otelcol_route}"
    tls:
      insecure: false

service:
  extensions: [health_check]
  pipelines:
    traces:
      receivers: [otlp]
      processors: [memory_limiter, batch, resource]
      exporters: [otlphttp]
OTEL

# ── 7. Systemd units ──────────────────────────────────────────────────────────
echo "==> Writing systemd units..."

cat > /etc/systemd/system/web.service <<'SVC'
[Unit]
Description=Fake Service - web
After=consul.service
Requires=consul.service
[Service]
Environment=NAME=web
Environment=PORT=9090
Environment=UPSTREAM_URIS=http://localhost:9091
Environment=OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317
ExecStart=/usr/local/bin/vm-web
Restart=on-failure
RestartSec=5s
[Install]
WantedBy=multi-user.target
SVC

cat > /etc/systemd/system/api.service <<'SVC'
[Unit]
Description=Fake Service - api
After=consul.service
Requires=consul.service
[Service]
Environment=NAME=api
Environment=PORT=9092
Environment=OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317
ExecStart=/usr/local/bin/vm-api
Restart=on-failure
RestartSec=5s
[Install]
WantedBy=multi-user.target
SVC

cat > /etc/systemd/system/web-envoy.service <<SVC
[Unit]
Description=Envoy sidecar proxy for web service
After=consul.service
Requires=consul.service
[Service]
ExecStart=/usr/bin/consul connect envoy -sidecar-for web -token=${web_token} -grpc-addr=127.0.0.1:8502 -ignore-envoy-compatibility -admin-bind 127.0.0.1:19000 -- --concurrency 2
Restart=on-failure
RestartSec=5s
[Install]
WantedBy=multi-user.target
SVC

cat > /etc/systemd/system/api-envoy.service <<SVC
[Unit]
Description=Envoy sidecar proxy for api service
After=consul.service
Requires=consul.service
[Service]
ExecStart=/usr/bin/consul connect envoy -sidecar-for api -token=${api_token} -grpc-addr=127.0.0.1:8502 -ignore-envoy-compatibility -admin-bind 127.0.0.1:19001 -- --concurrency 2
Restart=on-failure
RestartSec=5s
[Install]
WantedBy=multi-user.target
SVC

cat > /etc/systemd/system/client-envoy.service <<SVC
[Unit]
Description=Envoy sidecar proxy for client service
After=consul.service
Requires=consul.service
[Service]
ExecStart=/usr/bin/consul connect envoy -sidecar-for client -token=${client_token} -grpc-addr=127.0.0.1:8502 -ignore-envoy-compatibility -admin-bind 127.0.0.1:19002 -- --concurrency 2
Restart=on-failure
RestartSec=5s
[Install]
WantedBy=multi-user.target
SVC

cat > /etc/systemd/system/mesh-gateway.service <<SVC
[Unit]
Description=Consul Mesh Gateway
After=consul.service
Requires=consul.service
[Service]
ExecStart=/usr/bin/consul connect envoy -mesh-gateway -register -address $PRIVATE_IP:21000 -token=${mgw_token} -grpc-addr=127.0.0.1:8502 -ignore-envoy-compatibility -admin-bind 127.0.0.1:19003 -- --concurrency 2
Restart=on-failure
RestartSec=5s
[Install]
WantedBy=multi-user.target
SVC

cat > /etc/systemd/system/otelcol.service <<'SVC'
[Unit]
Description=OpenTelemetry Collector
After=network-online.target
Wants=network-online.target
[Service]
ExecStart=/usr/bin/otelcol-contrib --config=/etc/otelcol/config.yaml
Restart=on-failure
RestartSec=5s
[Install]
WantedBy=multi-user.target
SVC

# ── 8. Start everything ───────────────────────────────────────────────────────
echo "==> Starting services..."
systemctl daemon-reload
systemctl enable --now consul

# Wait for Consul to be ready
echo "==> Waiting for Consul..."
for i in $(seq 1 30); do
  if consul members -token "${bootstrap_token}" > /dev/null 2>&1; then
    echo "Consul ready after $i seconds"
    break
  fi
  sleep 2
done

# Create ACL tokens with fixed secret IDs
export CONSUL_HTTP_TOKEN="${bootstrap_token}"
export CONSUL_HTTP_ADDR=http://127.0.0.1:8500

consul acl policy create -name sidecar-policy \
  -rules 'agent_prefix "" { policy = "read" } service_prefix "" { policy = "write" } operator = "read"' \
  2>/dev/null || true

for SECRET_ID_AND_DESC in \
  "${agent_token}:agent-token" \
  "${web_token}:web-service-token" \
  "${api_token}:api-service-token" \
  "${client_token}:client-service-token"; do
  SECRET_ID="$${SECRET_ID_AND_DESC%%:*}"
  DESC="$${SECRET_ID_AND_DESC##*:}"
  consul acl token create -secret "$SECRET_ID" -description "$DESC" \
    -policy-name sidecar-policy 2>/dev/null || true
done

# Mesh gateway gets a new token each deploy (not fixed)
MGW_TOKEN=$(consul acl token create -description "mesh-gateway-token" \
  -policy-name sidecar-policy -format json | python3 -c "import json,sys; print(json.load(sys.stdin)['SecretID'])")
echo "MGW_TOKEN=$MGW_TOKEN" >> /etc/vm-dc-tokens.env

# Update mesh-gateway unit with actual token
sed -i "s/${mgw_token}/$MGW_TOKEN/" /etc/systemd/system/mesh-gateway.service
systemctl daemon-reload

# Create tracing-demo namespace
curl -sf -X PUT -H "X-Consul-Token: ${bootstrap_token}" \
  http://127.0.0.1:8500/v1/namespace -d '{"Name":"tracing-demo"}' > /dev/null

systemctl enable --now web api otelcol web-envoy api-envoy client-envoy mesh-gateway

echo "==> vm-dc init complete."
echo "==> Next: establish peering from ocp-dc using deploy/consul/peering-setup.md"
