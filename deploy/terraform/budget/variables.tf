variable "aws_region" {
  description = "Region the provider is configured for. Budgets are a global service, so this only decides which endpoint is called."
  type        = string
  default     = "ap-southeast-2"
}

variable "budget_name" {
  description = "Name of the account's monthly cost budget."
  type        = string
  default     = "vetmimi-monthly-cost"
}

variable "monthly_limit_usd" {
  description = "Monthly cost ceiling in USD. The AWS API takes this as a string."
  type        = string
  default     = "25"
}

variable "notification_email" {
  description = "Address that receives every budget notification. Supply it in terraform.tfvars, which is not committed."
  type        = string

  validation {
    condition     = can(regex("^[^@\\s]+@[^@\\s]+\\.[^@\\s]+$", var.notification_email))
    error_message = "notification_email must be a single email address."
  }
}

variable "project_tag" {
  description = "Value of the Project tag whose cost the budget counts; the live module tags every resource with it."
  type        = string
  default     = "vetmimi"
}
