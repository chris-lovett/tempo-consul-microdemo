# Consul service definition for the web service on EC2 (vm-dc)
#
# This REPLACES /etc/consul.d/web.hcl on the EC2 instance.
#
# Upstreams:
#   api           (9091) — local vm-api for product price lookups
#   frontend      (9093) — ocp-dc frontend via peer, for cart+checkout+payment
#   otel-collector(9317) — ocp-dc OTel Collector via peer, for span export
#
# The 9093 frontend upstream is also the failover target — when web itself
# goes down, the client's 9095 upstream (web) resolves to frontend via the
# ServiceResolver, so traffic never touches this upstream directly during
# failover. During normal operation, web actively calls frontend on 9093
# to run the ocp-dc checkout pipeline, producing a cross-DC trace waterfall.
#
# Apply:
#   sudo cp deploy/ec2/web.hcl /etc/consul.d/web.hcl
#   consul reload

service {
  name    = "web"
  address = "10.0.0.91"
  port    = 9090
  tags    = ["vm-dc", "tracing-demo"]

  token = "REDACTED_WEB_TOKEN"

  connect {
    sidecar_service {
      proxy {
        upstreams = [
          {
            # vm-api running locally — product catalog for price lookups
            destination_name = "api"
            local_bind_port  = 9091
          },
          {
            # ocp-dc frontend via peer mesh gateway
            # vm-web calls this to run cart+checkout+payment on ocp-dc,
            # producing child spans that appear in Tempo under the same trace ID.
            destination_name      = "frontend"
            destination_peer      = "ocp-dc"
            destination_namespace = "tracing-demo"
            local_bind_port       = 9093
          },
          {
            # ocp-dc otel-collector via peer mesh gateway
            # The local OTel Collector agent exports spans to localhost:9317,
            # which Envoy tunnels through the mesh gateway to ocp-dc Tempo.
            destination_name      = "otel-collector"
            destination_peer      = "ocp-dc"
            destination_namespace = "tracing-demo"
            local_bind_port       = 9317
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
