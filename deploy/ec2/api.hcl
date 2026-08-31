# Consul service definition for the api fake-service on EC2 (vm-dc)
#
# This REPLACES /etc/consul.d/api.hcl on the EC2 instance.
#
# Apply:
#   sudo cp api.hcl /etc/consul.d/api.hcl
#   sudo consul reload

service {
  name    = "api"
  address = "10.0.0.28"
  port    = 9092
  tags    = ["vm-dc", "fake-service", "tracing-demo"]

  token = "REDACTED_API_TOKEN"

  connect {
    sidecar_service {
      proxy {
        upstreams = []
      }
    }
  }

  check {
    http     = "http://localhost:9092/health"
    interval = "10s"
    timeout  = "3s"
  }
}
