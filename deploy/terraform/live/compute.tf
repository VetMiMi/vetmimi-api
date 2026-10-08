# The live host (ADR-010): one Graviton instance running deploy/compose.

data "aws_ssm_parameter" "ubuntu" {
  name = "/aws/service/canonical/ubuntu/server/24.04/stable/current/arm64/hvm/ebs-gp3/ami-id"
}

resource "aws_key_pair" "owner" {
  key_name   = "${local.name}-owner"
  public_key = var.ssh_public_key
}

resource "aws_instance" "live" {
  ami                    = data.aws_ssm_parameter.ubuntu.insecure_value
  instance_type          = var.instance_type
  key_name               = aws_key_pair.owner.key_name
  vpc_security_group_ids = [aws_security_group.live.id]
  iam_instance_profile   = aws_iam_instance_profile.live.name

  disable_api_termination = var.termination_protection

  metadata_options {
    http_endpoint = "enabled"
    http_tokens   = "required"
    # Two hops, so the api and worker containers can reach the instance
    # role through Docker's bridge network (internal/media/store.go).
    http_put_response_hop_limit = 2
  }

  root_block_device {
    volume_type = "gp3"
    volume_size = var.root_volume_gb
    encrypted   = true
    tags        = merge(local.tags, { Name = local.name })
  }

  tags = {
    Name = local.name
  }

  lifecycle {
    # The parameter moves to each new Ubuntu image; replacing the instance
    # would replace the database with it. unattended-upgrades patches it.
    ignore_changes = [ami]
  }
}

# Keeps the address, and so the sslip.io names, across a stop and start.
resource "aws_eip" "live" {
  domain   = "vpc"
  instance = aws_instance.live.id

  tags = {
    Name = local.name
  }
}
