# Service resolver for vm-dc web — fails over to ocp-dc frontend
#
# When the local web service in vm-dc fails its health check, Consul
# automatically routes connections to the frontend service exported
# from ocp-dc through the mesh gateway peering tunnel.
#
# Key details:
#   - Namespace = "tracing-demo" is required because ocp-dc uses namespace
#     mirroring — frontend lives in the tracing-demo namespace there, and
#     is imported into vm-dc under the same namespace.
#   - The old service-resolver-web-failover.hcl used no namespace and broke
#     because the failover target resolved to frontend.default.default.external
#     instead of frontend.tracing-demo.default.external.
#
# Apply on EC2:
#   export CONSUL_HTTP_TOKEN=REDACTED_BOOTSTRAP_TOKEN
#   export CONSUL_HTTP_ADDR=http://127.0.0.1:8500
#   consul config write deploy/ec2/service-resolver-web-failover.hcl

Kind = "service-resolver"
Name = "web"

Failover = {
  "*" = {
    Targets = [
      {
        Peer      = "ocp-dc"
        Service   = "frontend"
        Namespace = "tracing-demo"
      }
    ]
  }
}
