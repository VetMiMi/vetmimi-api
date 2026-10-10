# GitHub Actions pushes images with short-lived credentials from OIDC, so no
# AWS key is stored in GitHub. An account holds one provider per URL: if
# another project in the account (AU-Van) already has one, import it instead
# of creating a second (deploy/README.md).

# The immutable subject prefixes, from
# gh api repos/<owner>/<repo>/actions/oidc/customization/sub.
locals {
  github_subjects = {
    vetmimi-api  = "VetMiMi@322521937/vetmimi-api@1405159702"
    vetmimi-next = "VetMiMi@322521937/vetmimi-next@1377213269"
  }
}

resource "aws_iam_openid_connect_provider" "github" {
  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
}

# Only workflows running on main may push. The release jobs that push use no
# GitHub environment, so the subject is the branch ref, never
# :environment:production. GitHub now issues immutable subjects that carry the
# owner and repository ids (repo:Owner@id/name@id:...); the repositories here
# use them, and the name-only form stays for any that do not.
data "aws_iam_policy_document" "assume_github_release" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github.arn]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values = concat(
        [for repo in values(local.images) : "repo:${repo}:ref:refs/heads/main"],
        [for subject in values(local.github_subjects) : "repo:${subject}:ref:refs/heads/main"],
      )
    }
  }
}

resource "aws_iam_role" "github_release" {
  name                 = "${var.project_name}-github-release"
  assume_role_policy   = data.aws_iam_policy_document.assume_github_release.json
  max_session_duration = 3600
}

data "aws_iam_policy_document" "github_release" {
  statement {
    sid       = "RegistryLogin"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }

  statement {
    sid = "PushImages"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:BatchGetImage",
      "ecr:CompleteLayerUpload",
      "ecr:InitiateLayerUpload",
      "ecr:PutImage",
      "ecr:UploadLayerPart",
    ]
    resources = [for repo in aws_ecr_repository.image : repo.arn]
  }
}

resource "aws_iam_role_policy" "github_release" {
  name   = "push-images"
  role   = aws_iam_role.github_release.id
  policy = data.aws_iam_policy_document.github_release.json
}
