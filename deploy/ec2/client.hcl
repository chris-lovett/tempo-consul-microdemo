# Consul service definition for vm-client on EC2 (vm-dc)
#
# vm-client calls web via Envoy upstream port 9095.
# When web fails its health check, the SamenessGroup causes Consul to
# transparently reroute that upstream to ocp-dc frontend — no app changes.
#
# Apply:
#   sudo cp deploy/ec2/client.hcl /etc/consul.d/client.hcl
#   sudo consul reload

service {
  name = "client"
  port = 9080
  tags = ["vm-dc", "tracing-demo"]

  token = "REDACTED_WEB_TOKEN"

  connect {
    sidecar_service {
      proxy {
        upstreams = [
          {
            # web service — SamenessGroup will fail this over to ocp-dc frontend
            # when web's health check fails
            destination_name = "web"
            local_bind_port  = 9095
          }
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
