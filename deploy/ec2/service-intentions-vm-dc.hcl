# Service intentions for vm-dc — allow web and api sidecar proxies to reach
# the imported otel-collector service from ocp-dc.
#
# Apply on EC2 with the bootstrap or a sufficiently privileged token:
#   export CONSUL_HTTP_TOKEN=<token>
#   export CONSUL_HTTP_ADDR=http://127.0.0.1:8500
#   consul config write service-intentions-vm-dc.hcl

Kind = "service-intentions"
Name = "otel-collector"
Sources = [
  {
    Name   = "web"
    Action = "allow"
  },
  {
    Name   = "api"
    Action = "allow"
  },
]
