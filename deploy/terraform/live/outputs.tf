output "public_ip" {
  description = "The Elastic IP: TURN_EXTERNAL_IP, and the address SSH and the sslip.io names use."
  value       = aws_eip.live.public_ip
}

output "site_domain" {
  description = "SITE_DOMAIN until a real domain exists."
  value       = local.site_domain
}

output "api_domain" {
  description = "API_DOMAIN until a real domain exists."
  value       = local.api_domain
}

output "ssh" {
  description = "How the owner reaches the host."
  value       = "ssh ubuntu@${aws_eip.live.public_ip}"
}

output "media_s3_endpoint" {
  description = "MEDIA_S3_ENDPOINT in api.env."
  value       = "https://s3.${var.aws_region}.amazonaws.com"
}

output "media_bucket" {
  description = "MEDIA_S3_BUCKET in api.env."
  value       = aws_s3_bucket.media.bucket
}

output "media_public_url" {
  description = "MEDIA_PUBLIC_URL in api.env: where the public/ prefix is read from."
  value       = "https://${aws_s3_bucket.media.bucket_regional_domain_name}/public"
}

output "backups_bucket" {
  description = "BACKUP_BUCKET in deploy/compose/.env."
  value       = aws_s3_bucket.backups.bucket
}

output "ecr_registry" {
  description = "ECR_REGISTRY in deploy/compose/.env: the registry host both images live in."
  value       = split("/", aws_ecr_repository.image["vetmimi-api"].repository_url)[0]
}

output "api_repository_url" {
  description = "Where release.yml in vetmimi-api pushes the API image."
  value       = aws_ecr_repository.image["vetmimi-api"].repository_url
}

output "web_repository_url" {
  description = "Where release.yml in vetmimi-next pushes the website image."
  value       = aws_ecr_repository.image["vetmimi-next"].repository_url
}

output "release_role_arn" {
  description = "AWS_RELEASE_ROLE_ARN, the repository variable in vetmimi-api and vetmimi-next."
  value       = aws_iam_role.github_release.arn
}
