# Private image registries for the live host (ADR-010). GitHub Actions pushes
# to them through the OIDC role in github.tf; the instance role pulls.

locals {
  # Repository name => the GitHub repository whose main branch pushes to it.
  images = {
    vetmimi-api  = "VetMiMi/vetmimi-api"
    vetmimi-next = "VetMiMi/vetmimi-next"
  }
}

resource "aws_ecr_repository" "image" {
  for_each = local.images
  name     = each.key

  # Mutable, so :main can move to each release; deploy.sh releases :<sha>.
  image_tag_mutability = "MUTABLE"

  # Every image can be rebuilt from its commit, so a teardown may delete them.
  force_delete = true

  image_scanning_configuration {
    scan_on_push = true
  }
}

resource "aws_ecr_lifecycle_policy" "image" {
  for_each   = aws_ecr_repository.image
  repository = each.value.name

  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep the last 15 images, enough to roll back"
      selection = {
        tagStatus   = "any"
        countType   = "imageCountMoreThan"
        countNumber = 15
      }
      action = { type = "expire" }
    }]
  })
}
