# Alarms for the one instance, emailed through SNS. The memory and disk
# metrics come from the CloudWatch agent that bootstrap.sh installs; until it
# runs, those two alarms show insufficient data.

resource "aws_sns_topic" "alerts" {
  name = "${var.project_name}-alerts"
}

# AWS emails a confirmation link first; nothing is delivered until it is clicked.
resource "aws_sns_topic_subscription" "alerts_email" {
  topic_arn = aws_sns_topic.alerts.arn
  protocol  = "email"
  endpoint  = var.alert_email
}

locals {
  alert_actions = [aws_sns_topic.alerts.arn]
  instance      = { InstanceId = aws_instance.live.id }
}

# AWS hardware or network trouble: move the instance to healthy hardware,
# keeping its id, Elastic IP and disk.
resource "aws_cloudwatch_metric_alarm" "system_status" {
  alarm_name          = "${local.name}-system-status"
  alarm_description   = "AWS host failure; the instance is being recovered to new hardware."
  namespace           = "AWS/EC2"
  metric_name         = "StatusCheckFailed_System"
  dimensions          = local.instance
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = 2
  comparison_operator = "GreaterThanOrEqualToThreshold"
  threshold           = 1
  alarm_actions       = concat(["arn:aws:automate:${var.aws_region}:ec2:recover"], local.alert_actions)
  ok_actions          = local.alert_actions
}

# The operating system stopped answering (out of memory, kernel trouble):
# reboot it after ten minutes.
resource "aws_cloudwatch_metric_alarm" "instance_status" {
  alarm_name          = "${local.name}-instance-status"
  alarm_description   = "The instance failed its status check for 10 minutes; it is being rebooted."
  namespace           = "AWS/EC2"
  metric_name         = "StatusCheckFailed_Instance"
  dimensions          = local.instance
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 2
  datapoints_to_alarm = 2
  comparison_operator = "GreaterThanOrEqualToThreshold"
  threshold           = 1
  alarm_actions       = concat(["arn:aws:automate:${var.aws_region}:ec2:reboot"], local.alert_actions)
  ok_actions          = local.alert_actions
}

# A t4g sustained above its baseline spends CPU credits; this says so early.
resource "aws_cloudwatch_metric_alarm" "cpu" {
  alarm_name          = "${local.name}-cpu"
  alarm_description   = "CPU above 80% for 15 minutes."
  namespace           = "AWS/EC2"
  metric_name         = "CPUUtilization"
  dimensions          = local.instance
  statistic           = "Average"
  period              = 300
  evaluation_periods  = 3
  comparison_operator = "GreaterThanThreshold"
  threshold           = 80
  alarm_actions       = local.alert_actions
  ok_actions          = local.alert_actions
}

resource "aws_cloudwatch_metric_alarm" "memory" {
  alarm_name          = "${local.name}-memory"
  alarm_description   = "Memory above 90% for 10 minutes; check docker stats."
  namespace           = local.metrics_namespace
  metric_name         = "mem_used_percent"
  dimensions          = local.instance
  statistic           = "Average"
  period              = 300
  evaluation_periods  = 2
  comparison_operator = "GreaterThanThreshold"
  threshold           = 90
  alarm_actions       = local.alert_actions
  ok_actions          = local.alert_actions
}

# The dimensions must match what the agent sends exactly: it drops the device
# (bootstrap.sh) and the root of the Ubuntu image is ext4.
resource "aws_cloudwatch_metric_alarm" "disk" {
  alarm_name          = "${local.name}-disk"
  alarm_description   = "Root disk above 85%; prune images or grow root_volume_gb."
  namespace           = local.metrics_namespace
  metric_name         = "disk_used_percent"
  dimensions          = merge(local.instance, { path = "/", fstype = "ext4" })
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 1
  comparison_operator = "GreaterThanThreshold"
  threshold           = 85
  alarm_actions       = local.alert_actions
  ok_actions          = local.alert_actions
}
