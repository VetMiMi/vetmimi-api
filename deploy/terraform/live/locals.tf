locals {
  name = "${var.project_name}-${var.environment}"

  # The CloudWatch agent's namespace; bootstrap.sh writes the same name.
  metrics_namespace = "VetMiMi"

  tags = {
    Project     = var.project_name
    Environment = var.environment
    ManagedBy   = "terraform"
  }

  # Free DuckDNS names until a real domain exists. The owner points
  # vetmimi.duckdns.org at the Elastic IP at duckdns.org; DuckDNS answers every
  # name under it with the same address.
  site_domain = "vetmimi.duckdns.org"
  api_domain  = "api.vetmimi.duckdns.org"
}
