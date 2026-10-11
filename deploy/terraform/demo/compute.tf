resource "aws_ecs_cluster" "main" {
  name = local.name
}

locals {
  api_image = "${data.aws_ecr_repository.api.repository_url}:${var.api_image_tag}"

  # api and worker run the same image with the same configuration.
  api_environment = [for name, value in {
    ENV               = "production"
    PORT              = "8080"
    REDIS_URL         = "redis://${aws_elasticache_cluster.redis.cache_nodes[0].address}:6379/0"
    PUBLIC_API_URL    = local.api_url
    SITE_URL          = local.site_url
    EMAIL_FROM        = var.email_from
    MEDIA_S3_ENDPOINT = "https://s3.${var.aws_region}.amazonaws.com"
    MEDIA_S3_REGION   = var.aws_region
    MEDIA_S3_BUCKET   = aws_s3_bucket.media.bucket
    MEDIA_PUBLIC_URL  = "https://${aws_s3_bucket.media.bucket_regional_domain_name}/public"
    # Production requires a TURN server and Fargate has none; .invalid never
    # resolves, so room tickets work with STUN alone.
    TURN_HOST  = "turn.invalid:3478"
    GOMEMLIMIT = "400MiB"
  } : { name = name, value = value }]

  api_secrets = [for name, p in aws_ssm_parameter.secret : { name = name, valueFrom = p.arn }]
}

resource "aws_ecs_task_definition" "api" {
  family                   = "${local.name}-api"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 256
  memory                   = 512
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.task.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "ARM64"
  }

  container_definitions = jsonencode([{
    name         = "api"
    image        = local.api_image
    essential    = true
    portMappings = [{ containerPort = 8080 }]
    environment  = local.api_environment
    secrets      = local.api_secrets
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.service["api"].name
        awslogs-region        = var.aws_region
        awslogs-stream-prefix = "api"
      }
    }
  }])
}

resource "aws_ecs_task_definition" "worker" {
  family                   = "${local.name}-worker"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 256
  memory                   = 512
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.task.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "ARM64"
  }

  container_definitions = jsonencode([{
    name        = "worker"
    image       = local.api_image
    essential   = true
    command     = ["--mode", "worker"]
    environment = local.api_environment
    secrets     = local.api_secrets
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.service["worker"].name
        awslogs-region        = var.aws_region
        awslogs-stream-prefix = "worker"
      }
    }
  }])
}

resource "aws_ecs_task_definition" "web" {
  family                   = "${local.name}-web"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 256
  memory                   = 512
  execution_role_arn       = aws_iam_role.execution.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "ARM64"
  }

  container_definitions = jsonencode([{
    name         = "web"
    image        = "${data.aws_ecr_repository.web.repository_url}:${var.web_image_tag}"
    essential    = true
    portMappings = [{ containerPort = 3000 }]
    environment = [
      # Not "production": only the live site is crawled or pings IndexNow.
      { name = "SITE_ENV", value = "demo" },
      { name = "SITE_URL", value = local.site_url },
      { name = "API_URL", value = local.api_url },
    ]
    secrets = [
      { name = "API_SERVICE_KEY", valueFrom = aws_ssm_parameter.secret["SERVICE_KEY"].arn },
      { name = "SITE_REVALIDATE_SECRET", valueFrom = aws_ssm_parameter.secret["SITE_REVALIDATE_SECRET"].arn },
    ]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.service["web"].name
        awslogs-region        = var.aws_region
        awslogs-stream-prefix = "web"
      }
    }
  }])
}

# Every service starts a replacement before stopping a task, and rolls back
# by itself if the new tasks never turn healthy. Public addresses are the
# tasks' only route out, since there is no NAT gateway.

resource "aws_ecs_service" "api" {
  name                              = "api"
  cluster                           = aws_ecs_cluster.main.id
  task_definition                   = aws_ecs_task_definition.api.arn
  desired_count                     = 2
  launch_type                       = "FARGATE"
  health_check_grace_period_seconds = 60

  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.tasks.id]
    assign_public_ip = true
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.public["api"].arn
    container_name   = "api"
    container_port   = 8080
  }

  depends_on = [aws_lb_listener_rule.public]

  # The scaler (scaling.tf) owns the count once the service exists.
  lifecycle {
    ignore_changes = [desired_count]
  }
}

resource "aws_ecs_service" "worker" {
  name            = "worker"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.worker.arn
  desired_count   = 1
  launch_type     = "FARGATE"

  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.tasks.id]
    assign_public_ip = true
  }
}

resource "aws_ecs_service" "web" {
  name                              = "web"
  cluster                           = aws_ecs_cluster.main.id
  task_definition                   = aws_ecs_task_definition.web.arn
  desired_count                     = 2
  launch_type                       = "FARGATE"
  health_check_grace_period_seconds = 60

  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.tasks.id]
    assign_public_ip = true
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.public["web"].arn
    container_name   = "web"
    container_port   = 3000
  }

  depends_on = [aws_lb_listener_rule.public]
}
