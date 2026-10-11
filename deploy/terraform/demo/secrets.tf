# Every secret is generated here and read by ECS from Parameter Store when a
# task starts. The values sit in the local, gitignored state, which is
# destroyed with the stack.

resource "random_password" "db" {
  length  = 32
  special = false
}

resource "random_password" "secret" {
  for_each = toset(["service_key", "signing_secret", "revalidate_secret", "turn_secret"])
  length   = 48
  special  = false
}

resource "random_id" "totp_key" {
  byte_length = 32
}

locals {
  secrets = {
    DATABASE_URL           = "postgres://vetmimi:${random_password.db.result}@${aws_db_instance.main.endpoint}/vetmimi?sslmode=require"
    SERVICE_KEY            = random_password.secret["service_key"].result
    SIGNING_SECRET         = random_password.secret["signing_secret"].result
    TOTP_ENCRYPTION_KEY    = random_id.totp_key.b64_std
    SITE_REVALIDATE_SECRET = random_password.secret["revalidate_secret"].result
    TURN_SECRET            = random_password.secret["turn_secret"].result
    # Production refuses to start without a key; a placeholder makes each
    # send fail and be recorded, instead of the API refusing to start.
    RESEND_API_KEY = var.resend_api_key != "" ? var.resend_api_key : "re_demo_no_email"
  }
}

resource "aws_ssm_parameter" "secret" {
  for_each = toset(keys(local.secrets))
  name     = "/${var.project_name}/${var.environment}/${each.key}"
  type     = "SecureString"
  value    = local.secrets[each.key]
}
