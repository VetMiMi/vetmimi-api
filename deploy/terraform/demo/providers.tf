provider "aws" {
  region = var.aws_region

  # The budget (../budget) counts cost by Project; Environment tells the demo
  # apart from the live host.
  default_tags {
    tags = {
      Project     = var.project_name
      Environment = var.environment
      ManagedBy   = "terraform"
    }
  }
}
