output "api_url" {
  description = "The API over HTTPS through CloudFront."
  value       = local.api_url
}

output "site_url" {
  description = "The website over HTTPS through CloudFront."
  value       = local.site_url
}

output "cluster" {
  description = "The ECS cluster; its services are api, worker and web."
  value       = aws_ecs_cluster.main.name
}

output "db_instance" {
  description = "The RDS instance identifier."
  value       = aws_db_instance.main.identifier
}

output "name" {
  description = "The prefix of the target groups, alarms and scaling policy."
  value       = local.name
}
