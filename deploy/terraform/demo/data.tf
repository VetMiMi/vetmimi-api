data "aws_availability_zones" "available" {
  state = "available"
}

# Owned by ../live; the demo only pulls from them.
data "aws_ecr_repository" "api" {
  name = "vetmimi-api"
}

data "aws_ecr_repository" "web" {
  name = "vetmimi-next"
}

data "aws_ec2_managed_prefix_list" "cloudfront" {
  name = "com.amazonaws.global.cloudfront.origin-facing"
}

data "aws_cloudfront_cache_policy" "disabled" {
  name = "Managed-CachingDisabled"
}

data "aws_cloudfront_origin_request_policy" "all_viewer" {
  name = "Managed-AllViewer"
}
