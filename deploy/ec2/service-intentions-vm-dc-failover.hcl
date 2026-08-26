# ServiceIntentions — vm-dc: allow ocp-dc frontend to reach vm-dc web
# as a failover target, and allow web to reach api.
#
# Apply on EC2:
#   export CONSUL_HTTP_TOKEN=REDACTED_BOOTSTRAP_TOKEN
#   export CONSUL_HTTP_ADDR=http://127.0.0.1:8500
#   consul config write deploy/ec2/service-intentions-vm-dc-failover.hcl

Kind = "service-intentions"
Name = "web"
Sources = [
  {
    # Allow local api sidecar to call web (health checks etc.)
    Name   = "api"
    Action = "allow"
  },
  {
    # Allow ocp-dc frontend to reach vm-dc web as failover target
    Name   = "frontend"
    Peer   = "ocp-dc"
    Action = "allow"
  },
]
