# ExportedServices — vm-dc exports web and api to ocp-dc peer
# so that ocp-dc ServiceResolvers can fail over to them.
#
# Apply on EC2:
#   export CONSUL_HTTP_TOKEN=REDACTED_BOOTSTRAP_TOKEN
#   export CONSUL_HTTP_ADDR=http://127.0.0.1:8500
#   consul config write deploy/ec2/exported-services-vm-dc.hcl

Kind      = "exported-services"
Name      = "default"
Partition = "default"

Services = [
  {
    Name      = "web"
    Namespace = "default"
    Consumers = [
      { Peer = "ocp-dc" }
    ]
  },
  {
    Name      = "api"
    Namespace = "default"
    Consumers = [
      { Peer = "ocp-dc" }
    ]
  }
]
