# The instance role: the API's media access, backup.sh's uploads, image pulls
# for deploy.sh and the CloudWatch agent's two metrics, and nothing else. Any
# container on the host can reach it (compute.tf), so it grants only these.

data "aws_iam_policy_document" "assume_ec2" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "live" {
  name               = local.name
  assume_role_policy = data.aws_iam_policy_document.assume_ec2.json
}

resource "aws_iam_instance_profile" "live" {
  name = local.name
  role = aws_iam_role.live.name
}

data "aws_iam_policy_document" "live" {
  statement {
    sid       = "MediaObjects"
    actions   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"]
    resources = ["${aws_s3_bucket.media.arn}/*"]
  }

  # Write and read back for a restore; never delete, so a compromised host
  # cannot erase the backups. The lifecycle rule expires them.
  statement {
    sid       = "BackupObjects"
    actions   = ["s3:GetObject", "s3:PutObject"]
    resources = ["${aws_s3_bucket.backups.arn}/postgres/*"]
  }

  statement {
    sid       = "ListBackups"
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.backups.arn]
  }

  statement {
    sid       = "RegistryLogin"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }

  statement {
    sid = "PullImages"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:BatchGetImage",
      "ecr:GetDownloadUrlForLayer",
    ]
    resources = [for repo in aws_ecr_repository.image : repo.arn]
  }

  # The minimal part of CloudWatchAgentServerPolicy the agent's config in
  # bootstrap.sh needs: metrics into the project's namespace, no logs.
  statement {
    sid       = "AgentMetrics"
    actions   = ["cloudwatch:PutMetricData"]
    resources = ["*"]

    condition {
      test     = "StringEquals"
      variable = "cloudwatch:namespace"
      values   = [local.metrics_namespace]
    }
  }
}

resource "aws_iam_role_policy" "live" {
  name   = "host"
  role   = aws_iam_role.live.id
  policy = data.aws_iam_policy_document.live.json
}
