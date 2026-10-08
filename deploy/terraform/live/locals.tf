locals {
  name = "${var.project_name}-${var.environment}"

  tags = {
    Project     = var.project_name
    Environment = var.environment
    ManagedBy   = "terraform"
  }

  # Free host names that need no DNS until a real domain exists: sslip.io
  # answers <anything>-<ip with dashes>.sslip.io with that address.
  ip_dashes   = replace(aws_eip.live.public_ip, ".", "-")
  site_domain = "${local.ip_dashes}.sslip.io"
  api_domain  = "api-${local.ip_dashes}.sslip.io"
}
