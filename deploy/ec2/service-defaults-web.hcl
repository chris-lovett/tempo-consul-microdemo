# Service defaults for web on vm-dc
#
# Sets the protocol to http (required for L7 routing and intentions)
# and mesh gateway mode to none for the local service so that
# failover-target~0 (local web) routes directly to the web sidecar
# rather than through the mesh gateway.
#
# MeshGateway.Mode = "none" means:
#   - Local traffic to web goes directly to web's Envoy sidecar
#   - Peer traffic to ocp-dc frontend goes through the mesh gateway
#     (the peer target inherits remote mode from the service-resolver)
#
# Apply on EC2:
#   export CONSUL_HTTP_TOKEN=REDACTED_BOOTSTRAP_TOKEN
#   export CONSUL_HTTP_ADDR=http://127.0.0.1:8500
#   consul config write deploy/ec2/service-defaults-web.hcl

Kind     = "service-defaults"
Name     = "web"
Protocol = "http"

MeshGateway {
  Mode = "none"
}
