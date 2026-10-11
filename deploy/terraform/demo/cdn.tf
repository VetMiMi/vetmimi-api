# One distribution per service, for HTTPS on the default *.cloudfront.net
# certificate. Each adds its own secret header, which is how the load balancer
# tells them apart and refuses direct traffic. Nothing is cached, and every
# viewer header (cookies, Authorization, Origin, WebSocket upgrades) reaches
# the origin.

resource "random_password" "origin" {
  for_each = local.public
  length   = 40
  special  = false
}

# The live web image is built with SITE_ENV=production, so its prerendered
# robots.txt allows crawling; this header keeps the demo out of search results.
resource "aws_cloudfront_response_headers_policy" "noindex" {
  name = "${local.name}-noindex"

  custom_headers_config {
    items {
      header   = "X-Robots-Tag"
      value    = "noindex, nofollow"
      override = true
    }
  }
}

resource "aws_cloudfront_distribution" "public" {
  for_each        = local.public
  enabled         = true
  comment         = "${local.name} ${each.key}"
  is_ipv6_enabled = true
  price_class     = "PriceClass_All"

  origin {
    origin_id   = "alb"
    domain_name = aws_lb.main.dns_name

    custom_header {
      name  = "X-Origin-Secret"
      value = random_password.origin[each.key].result
    }

    custom_origin_config {
      http_port              = 80
      https_port             = 443
      origin_protocol_policy = "http-only"
      origin_ssl_protocols   = ["TLSv1.2"]
    }
  }

  default_cache_behavior {
    target_origin_id           = "alb"
    viewer_protocol_policy     = "redirect-to-https"
    allowed_methods            = ["DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT"]
    cached_methods             = ["GET", "HEAD"]
    cache_policy_id            = data.aws_cloudfront_cache_policy.disabled.id
    origin_request_policy_id   = data.aws_cloudfront_origin_request_policy.all_viewer.id
    response_headers_policy_id = each.key == "web" ? aws_cloudfront_response_headers_policy.noindex.id : null
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    cloudfront_default_certificate = true
  }
}
