variable "aws_region" {
  description = "Region of the demo, the same as the live host and its image repositories."
  type        = string
  default     = "ap-southeast-2"
}

variable "project_name" {
  description = "Project tag and name prefix; the budget counts cost by it."
  type        = string
  default     = "vetmimi"
}

variable "environment" {
  description = "Environment tag and name suffix."
  type        = string
  default     = "demo"
}

variable "api_image_tag" {
  description = "Commit sha of the vetmimi-api image to run. No default, so every apply names the build it means."
  type        = string
}

variable "web_image_tag" {
  description = "Commit sha of the vetmimi-next image to run."
  type        = string
}

variable "alert_email" {
  description = "Where the alarms are emailed. It stays in the gitignored terraform.tfvars."
  type        = string
}

variable "resend_api_key" {
  description = "Optional Resend key, passed as TF_VAR_resend_api_key. Without it the worker's emails fail and are recorded as failed; nothing else depends on them."
  type        = string
  default     = ""
  sensitive   = true
}

variable "email_from" {
  description = "Sender for the demo's emails; must be a Resend-verified sender when resend_api_key is set."
  type        = string
  default     = "VetMiMi Demo <demo@example.com>"
}
