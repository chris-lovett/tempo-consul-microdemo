# Consul service definition for the web fake-service on EC2 (vm-dc)
#
# This REPLACES /etc/consul.d/web.hcl on the EC2 instance.
# It adds an upstream for otel-collector (exported from ocp-dc) so the
# local OTel Collector agent can forward spans through the mesh gateway.
#
# Apply:
#   sudo cp web.hcl /etc/consul.d/web.hcl
#   sudo consul reload

service {
  name = "web"
  port = 9090
  tags = ["vm-dc", "fake-service", "tracing-demo"]

  token = "REDACTED_WEB_TOKEN"

  connect {
    sidecar_service {
      proxy {
        upstreams = [
          {
            # api service running locally on this VM
            destination_name = "api"
            local_bind_port  = 9091
          },
          {
            # otel-collector imported from ocp-dc peer
            # The local OTel Collector agent forwards spans to localhost:9317,
            # which Envoy tunnels through the mesh gateway to ocp-dc.
            destination_name  = "otel-collector"
            destination_peer  = "ocp-dc"
            local_bind_port   = 9317
          }
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
