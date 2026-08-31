# deploy/ec2/terraform/main.tf
#
# Provisions the vm-dc EC2 instance for the Consul distributed tracing demo.
# Installs Consul Enterprise, Envoy, OTel Collector, and fake-service binaries
# via user_data, then writes all config files and starts systemd services.
#
# Prerequisites:
#   - AWS credentials via Doormat (export AWS_* env vars before running)
#   - Consul Enterprise license available (set var.consul_license)
#   - All ACL tokens available (set via var.* or terraform.tfvars — see variables.tf)
#
# Usage:
#   cd deploy/ec2/terraform
#   terraform init
#   terraform apply -var-file=terraform.tfvars
#
# After apply, run deploy/ec2/setup.sh on the new VM to apply Consul config entries.
# Then establish peering using deploy/consul/peering-setup.md.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
  required_version = ">= 1.5.0"
}

provider "aws" {
  region = var.aws_region
}

# ── Data sources ──────────────────────────────────────────────────────────────

data "aws_ami" "hc_base" {
  most_recent = true
  owners      = ["888995627335"] # HashiCorp ami-prod account

  filter {
    name   = "name"
    values = ["hc-base-ubuntu-2404-amd64-*"]
  }
  filter {
    name   = "state"
    values = ["available"]
  }
}

data "aws_subnet" "selected" {
  id = var.subnet_id
}

# ── EC2 Instance ──────────────────────────────────────────────────────────────

resource "aws_instance" "vm_dc" {
  ami                         = data.aws_ami.hc_base.id
  instance_type               = var.instance_type
  key_name                    = var.key_name
  subnet_id                   = var.subnet_id
  vpc_security_group_ids      = [var.security_group_id]
  associate_public_ip_address = true

  root_block_device {
    volume_size           = 20
    volume_type           = "gp3"
    delete_on_termination = true
  }

  tags = {
    Name    = "vm-dc-demo"
    Purpose = "consul-tracing-demo"
  }

  user_data = templatefile("${path.module}/user_data.sh.tpl", {
    consul_license          = var.consul_license
    bootstrap_token         = var.bootstrap_token
    agent_token             = var.agent_token
    web_token               = var.web_token
    api_token               = var.api_token
    client_token            = var.client_token
    mgw_token               = var.mgw_token
    consul_elb              = var.consul_expose_servers_elb
    otelcol_route           = var.otelcol_route_url
    private_ip_placeholder  = "" # replaced at runtime via cloud-init
  })
}

# ── Outputs ───────────────────────────────────────────────────────────────────

output "instance_id" {
  value = aws_instance.vm_dc.id
}

output "public_ip" {
  value = aws_instance.vm_dc.public_ip
}

output "private_ip" {
  value = aws_instance.vm_dc.private_ip
}

output "ssh_command" {
  value = "ssh -i ~/.ssh/vm-dc-demo.pem ubuntu@${aws_instance.vm_dc.public_ip}"
}

output "next_steps" {
  value = <<-EOT
    VM is up. Next steps:
    1. Wait ~3 minutes for user_data to finish:
       ssh -i ~/.ssh/vm-dc-demo.pem ubuntu@${aws_instance.vm_dc.public_ip} 'sudo tail -f /var/log/cloud-init-output.log'

    2. Update service HCL address fields if private IP changed from 10.0.0.91:
       Private IP: ${aws_instance.vm_dc.private_ip}

    3. Establish peering from ocp-dc (run on Mac):
       See deploy/consul/peering-setup.md Step 1

    4. Run setup.sh on the VM to apply Consul config entries:
       ssh -i ~/.ssh/vm-dc-demo.pem ubuntu@${aws_instance.vm_dc.public_ip}
       export CONSUL_HTTP_TOKEN=${var.bootstrap_token}
       export CONSUL_HTTP_ADDR=http://127.0.0.1:8500
       bash ~/tempo-consul-microdemo/deploy/ec2/setup.sh
  EOT
}
