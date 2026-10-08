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
