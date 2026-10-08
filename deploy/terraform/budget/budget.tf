# The account's spending guardrail.
#
# A root module of its own, with its own state, so that destroying the live
# host cannot destroy the alarm. A guardrail torn down with the thing it
# guards is only present when it is not needed.
#
# Notifications address the subscriber's email directly; an SNS topic would
# add a topic, a policy and a subscription confirmation for one recipient.

data "aws_caller_identity" "current" {}

resource "aws_budgets_budget" "monthly_cost" {
  account_id   = data.aws_caller_identity.current.account_id
  name         = var.budget_name
  budget_type  = "COST"
  limit_amount = var.monthly_limit_usd
  limit_unit   = "USD"
  time_unit    = "MONTHLY"

  # The account also pays for other projects, so the budget counts only
  # resources tagged Project = vetmimi (the live module's default tags). The
  # tag counts once it is activated as a cost allocation tag (README.md).
  cost_filter {
    name   = "TagKeyValue"
    values = [format("user:Project$%s", var.project_tag)]
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 50
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.notification_email]
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 80
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.notification_email]
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.notification_email]
  }

  # The forecast arrives before the money is spent: it fires when the month
  # is projected to end over the ceiling, not after it has.
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "FORECASTED"
    subscriber_email_addresses = [var.notification_email]
  }
}
