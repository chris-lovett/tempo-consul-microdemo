# deploy/ec2/terraform/variables.tf

variable "aws_region" {
  description = "AWS region for the vm-dc instance"
  type        = string
  default     = "us-east-2"
}

variable "subnet_id" {
  description = "Subnet ID in the ROSA VPC (10.0.0.x range)"
  type        = string
  default     = "subnet-0f6c842e85b144619"
}

variable "security_group_id" {
  description = "Security group ID shared with the ROSA cluster"
  type        = string
  default     = "sg-0b12e1b96649e7a18"
}

variable "instance_type" {
  description = "EC2 instance type"
  type        = string
  default     = "t3.medium"
}

variable "key_name" {
  description = "EC2 key pair name for SSH access"
  type        = string
  default     = "vm-dc-demo"
}

variable "consul_license" {
  description = "Consul Enterprise license string"
  type        = string
  sensitive   = true
}

variable "consul_expose_servers_elb" {
  description = "Hostname of the consul-expose-servers LoadBalancer (for retry_join)"
  type        = string
  default     = "a1b0aca5052764f42aadb61cdcde8c28-514325382.us-east-2.elb.amazonaws.com"
}

variable "otelcol_route_url" {
  description = "OpenShift Route URL for the otel-collector OTLP HTTP endpoint"
  type        = string
  default     = "https://otel-collector-tracing-demo.apps.rosa.cluster2.6cxo.p3.openshiftapps.com"
}

# ── ACL Tokens (sensitive — set via terraform.tfvars or env vars) ─────────────

variable "bootstrap_token" {
  description = "Consul bootstrap/management token for vm-dc"
  type        = string
  sensitive   = true
  default     = "c0f2d09d-a9a7-653b-8ddd-88ca6fa101d8"
}

variable "agent_token" {
  description = "Consul agent token"
  type        = string
  sensitive   = true
  default     = "c03cbdc9-088a-7103-4f76-fec7944e0db1"
}

variable "web_token" {
  description = "ACL token for web service + web-envoy sidecar"
  type        = string
  sensitive   = true
  default     = "66ac6f8d-b6a7-6618-38ce-5d550c0c289a"
}

variable "api_token" {
  description = "ACL token for api service + api-envoy sidecar"
  type        = string
  sensitive   = true
  default     = "e7f01cfe-b9bc-115d-9995-8a5bab55075d"
}

variable "client_token" {
  description = "ACL token for client service + client-envoy sidecar"
  type        = string
  sensitive   = true
  default     = "5b26d95a-a489-30a6-62e2-ff337ed158e4"
}

variable "mgw_token" {
  description = "ACL token for the Consul mesh gateway"
  type        = string
  sensitive   = true
  # Set in terraform.tfvars — generated fresh each VM deployment
}
