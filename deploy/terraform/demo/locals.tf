locals {
  name = "${var.project_name}-${var.environment}"
  azs  = slice(data.aws_availability_zones.available.names, 0, 2)

  # The two services behind the load balancer, each with its own CloudFront
  # distribution, because their paths overlap (/admin exists on both).
  public = {
    api = { port = 8080, health = "/readyz", priority = 10 }
    web = { port = 3000, health = "/api/health", priority = 20 }
  }

  api_url  = "https://${aws_cloudfront_distribution.public["api"].domain_name}"
  site_url = "https://${aws_cloudfront_distribution.public["web"].domain_name}"
}
