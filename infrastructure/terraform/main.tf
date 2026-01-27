# VRSky Infrastructure on ServeTheWorld (Norway)
# This Terraform configuration provisions VPS instances for K3s cluster

terraform {
  required_version = ">= 1.5.0"

  required_providers {
    # Note: ServeTheWorld doesn't have an official Terraform provider
    # We'll use null_resource with SSH provisioning
    # For actual VPS ordering, you'll use ServeTheWorld web portal
    null = {
      source  = "hashicorp/null"
      version = "~> 3.2"
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 4.0"
    }
  }

  backend "local" {
    path = "terraform.tfstate"
  }
}

# Variables
variable "cluster_name" {
  description = "Name of the K3s cluster"
  type        = string
  default     = "vrsky-prod"
}

variable "node_count" {
  description = "Number of K3s nodes (1 master + workers)"
  type        = number
  default     = 3
}

variable "master_node_ip" {
  description = "IP address of the K3s master node"
  type        = string
}

variable "worker_node_ips" {
  description = "IP addresses of K3s worker nodes"
  type        = list(string)
}

variable "ssh_private_key_path" {
  description = "Path to SSH private key for node access"
  type        = string
  default     = "~/.ssh/id_rsa"
}

variable "ssh_user" {
  description = "SSH user for node access"
  type        = string
  default     = "root"
}

variable "cloudflare_api_token" {
  description = "Cloudflare API token for DNS management"
  type        = string
  sensitive   = true
}

variable "cloudflare_zone_id" {
  description = "Cloudflare zone ID for your domain"
  type        = string
}

variable "domain_name" {
  description = "Domain name for VRSky platform (e.g., vrsky.example.com)"
  type        = string
}

# Locals
locals {
  all_node_ips = concat([var.master_node_ip], var.worker_node_ips)

  common_ports = {
    ssh          = 22
    http         = 80
    https        = 443
    k3s_api      = 6443
    nats_client  = 4222
    nats_monitor = 8222
  }
}

# Outputs
output "master_node_ip" {
  description = "IP address of the K3s master node"
  value       = var.master_node_ip
}

output "worker_node_ips" {
  description = "IP addresses of K3s worker nodes"
  value       = var.worker_node_ips
}

output "kubeconfig_command" {
  description = "Command to get kubeconfig from master node"
  value       = "scp ${var.ssh_user}@${var.master_node_ip}:/etc/rancher/k3s/k3s.yaml ./kubeconfig && sed -i 's/127.0.0.1/${var.master_node_ip}/g' ./kubeconfig"
}

output "dns_records" {
  description = "DNS records configured in Cloudflare"
  value = {
    api     = "${var.domain_name} -> ${var.master_node_ip}"
    workers = "Cloudflare load balancer -> ${jsonencode(local.all_node_ips)}"
  }
}
