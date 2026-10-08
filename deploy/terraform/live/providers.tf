provider "aws" {
  region = var.aws_region

  # Tagged at the provider so the budget (../budget) can count this project's
  # cost and a teardown can tell what belongs to it.
  default_tags {
    tags = local.tags
  }
}
