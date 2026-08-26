# SamenessGroup — vm-dc
#
# Declares that "web" in vm-dc and "frontend" in ocp-dc are the same
# logical service. Consul uses this to generate correct peer failover
# xDS clusters with the right mesh gateway SNI automatically.
#
# When web's health check fails, Consul routes traffic from any service
# that has web as an upstream directly to ocp-dc frontend through the
# mesh gateway — with zero app code changes.
#
# Apply on EC2:
#   export CONSUL_HTTP_TOKEN=REDACTED_BOOTSTRAP_TOKEN
#   export CONSUL_HTTP_ADDR=http://127.0.0.1:8500
#   consul config write deploy/ec2/sameness-group-web.hcl

Kind        = "sameness-group"
Name        = "web-frontend-group"
Partition   = "default"
DefaultForFailover = true
IncludeLocal = true

Members = [
  {
    # Peer service in ocp-dc — local partition is covered by IncludeLocal = true
    Peer = "ocp-dc"
  }
]
