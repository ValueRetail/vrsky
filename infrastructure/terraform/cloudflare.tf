# Cloudflare DNS Configuration for VRSky
# Provides DNS load balancing and SSL/TLS termination

provider "cloudflare" {
  api_token = var.cloudflare_api_token
}

# A record for API endpoint (points to master node)
resource "cloudflare_record" "api" {
  zone_id = var.cloudflare_zone_id
  name    = "api.${var.domain_name}"
  value   = var.master_node_ip
  type    = "A"
  ttl     = 300
  proxied = true # Enable Cloudflare proxy for DDoS protection
}

# A records for all nodes (for direct access if needed)
resource "cloudflare_record" "master_direct" {
  zone_id = var.cloudflare_zone_id
  name    = "master.${var.domain_name}"
  value   = var.master_node_ip
  type    = "A"
  ttl     = 300
  proxied = false # Direct access, no proxy
}

resource "cloudflare_record" "worker_direct" {
  count   = length(var.worker_node_ips)
  zone_id = var.cloudflare_zone_id
  name    = "worker-${count.index + 1}.${var.domain_name}"
  value   = var.worker_node_ips[count.index]
  type    = "A"
  ttl     = 300
  proxied = false
}

# Load Balancer Pool for all nodes
resource "cloudflare_load_balancer_pool" "vrsky_pool" {
  account_id = var.cloudflare_zone_id
  name       = "${var.cluster_name}-pool"

  dynamic "origins" {
    for_each = local.all_node_ips
    content {
      name    = "node-${origins.key}"
      address = origins.value
      enabled = true
    }
  }

  check_regions = ["WEU"] # Western Europe health checks

  description = "VRSky K3s cluster node pool"
  enabled     = true

  minimum_origins = 1

  notification_email = "ops@example.com" # Update with your email
}

# Load Balancer for main application endpoint
resource "cloudflare_load_balancer" "vrsky" {
  zone_id          = var.cloudflare_zone_id
  name             = var.domain_name
  default_pool_ids = [cloudflare_load_balancer_pool.vrsky_pool.id]
  fallback_pool_id = cloudflare_load_balancer_pool.vrsky_pool.id
  description      = "VRSky platform load balancer"
  ttl              = 30
  proxied          = true

  steering_policy = "random" # Random distribution across healthy nodes

  session_affinity     = "cookie"
  session_affinity_ttl = 3600
}

# DNS records for monitoring endpoints
resource "cloudflare_record" "grafana" {
  zone_id = var.cloudflare_zone_id
  name    = "grafana.${var.domain_name}"
  value   = var.master_node_ip
  type    = "A"
  ttl     = 300
  proxied = true
}

resource "cloudflare_record" "prometheus" {
  zone_id = var.cloudflare_zone_id
  name    = "prometheus.${var.domain_name}"
  value   = var.master_node_ip
  type    = "A"
  ttl     = 300
  proxied = true
}

# Wildcard for tenant-specific endpoints (e.g., tenant1.vrsky.example.com)
resource "cloudflare_record" "wildcard" {
  zone_id = var.cloudflare_zone_id
  name    = "*.${var.domain_name}"
  value   = var.domain_name
  type    = "CNAME"
  ttl     = 300
  proxied = true
}
