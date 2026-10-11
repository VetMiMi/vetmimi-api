# Plain HTTP on port 80: there is no domain, so no certificate. Browsers get
# HTTPS from CloudFront's default certificate (cdn.tf).

resource "aws_lb" "main" {
  name                       = local.name
  load_balancer_type         = "application"
  security_groups            = [aws_security_group.alb.id]
  subnets                    = aws_subnet.public[*].id
  drop_invalid_header_fields = true
  enable_deletion_protection = false
}

resource "aws_lb_target_group" "public" {
  for_each    = local.public
  name        = "${local.name}-${each.key}"
  port        = each.value.port
  protocol    = "HTTP"
  vpc_id      = aws_vpc.main.id
  target_type = "ip"

  # The default 300 s would hold every replaced task through a rolling deploy.
  deregistration_delay = 30

  health_check {
    path                = each.value.health
    matcher             = "200"
    interval            = 15
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }
}

# Anything that did not come through one of the two distributions is refused,
# including other CloudFront distributions inside the same prefix list.
resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.main.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type = "fixed-response"

    fixed_response {
      content_type = "text/plain"
      message_body = "Forbidden"
      status_code  = "403"
    }
  }
}

resource "aws_lb_listener_rule" "public" {
  for_each     = local.public
  listener_arn = aws_lb_listener.http.arn
  priority     = each.value.priority

  condition {
    http_header {
      http_header_name = "X-Origin-Secret"
      values           = [random_password.origin[each.key].result]
    }
  }

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.public[each.key].arn
  }
}
