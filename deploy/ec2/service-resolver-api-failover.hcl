# Service resolver for vm-dc api — fails over to ocp-dc catalog
# (catalog is the closest equivalent: returns product/data responses)
#
# Apply on EC2:
#   export CONSUL_HTTP_TOKEN=REDACTED_BOOTSTRAP_TOKEN
#   export CONSUL_HTTP_ADDR=http://127.0.0.1:8500
#   consul config write deploy/ec2/service-resolver-api-failover.hcl

Kind = "service-resolver"
Name = "api"

Failover = {
  "*" = {
    Targets = [
      {
        Peer    = "ocp-dc"
        Service = "catalog"
      }
    ]
  }
}
