variable "aws_region" {
  description = "Region of the live host. Sydney, beside Daw Mi's clients."
  type        = string
  default     = "ap-southeast-2"
}

variable "project_name" {
  description = "Project tag and name prefix; the budget module counts cost by it."
  type        = string
  default     = "vetmimi"
}

variable "environment" {
  description = "Environment tag and name suffix."
  type        = string
  default     = "live"
}

variable "instance_type" {
  description = "EC2 instance type. Must be arm64 (Graviton): the images are built for linux/arm64."
  type        = string
  default     = "t4g.small"
}

variable "root_volume_gb" {
  description = "Size of the encrypted gp3 root volume, which holds Docker images, PostgreSQL and the local backups."
  type        = number
  default     = 20
}

variable "termination_protection" {
  description = "Refuse to terminate the instance, whose disk holds the database. Set false only to tear it down."
  type        = bool
  default     = true
}

variable "ssh_public_key" {
  description = "The owner's SSH public key for the ubuntu user. A public key is not a secret, but it stays in the gitignored terraform.tfvars."
  type        = string
}

variable "ssh_cidr_blocks" {
  description = "Sources allowed to reach SSH. GitHub-hosted runners deploy over SSH from changing addresses, so the default is open and the key is the control."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "turn_relay_ports" {
  description = "coturn's relay port range; must match min-port and max-port in deploy/compose/coturn/turnserver.conf."
  type        = object({ from = number, to = number })
  default     = { from = 49160, to = 49200 }
}

variable "backup_retention_days" {
  description = "Days a nightly database dump stays in the backups bucket."
  type        = number
  default     = 30
}

variable "alert_email" {
  description = "Where the CloudWatch alarms are emailed. It stays in the gitignored terraform.tfvars."
  type        = string
}
