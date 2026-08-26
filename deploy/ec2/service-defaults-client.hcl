# Service defaults for client on vm-dc
#
# Configures passive health check on the web upstream so that Envoy
# ejects the local web endpoint after 1 failure and promotes to the
# peer failover target (ocp-dc frontend) without hitting the panic
# threshold caused by zero EDS endpoints.
#
# MaxEjectionPercent = 100 is required — the default of 0% means no
# endpoints can be ejected, causing ejections_overflow and blocking
# priority promotion even when all endpoints are unhealthy.
#
# Apply on EC2:
#   export CONSUL_HTTP_TOKEN=REDACTED_BOOTSTRAP_TOKEN
#   export CONSUL_HTTP_ADDR=http://127.0.0.1:8500
#   consul config write deploy/ec2/service-defaults-client.hcl

Kind     = "service-defaults"
Name     = "client"
Protocol = "http"

UpstreamConfig {
  Overrides = [
    {
      Name = "web"
      PassiveHealthCheck {
        Interval              = "5s"
        MaxFailures           = 1
        EnforcingConsecutive5xx = 100
        MaxEjectionPercent    = 100
      }
    }
  ]
}
