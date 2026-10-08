# The default VPC's public subnets: one instance needs no network of its own.
# Only Caddy and coturn listen on the host (deploy/test-compose.sh).

resource "aws_security_group" "live" {
  name        = local.name
  description = "VetMiMi live host: SSH, Caddy and coturn"

  tags = {
    Name = local.name
  }
}

resource "aws_vpc_security_group_ingress_rule" "ssh" {
  for_each          = toset(var.ssh_cidr_blocks)
  security_group_id = aws_security_group.live.id
  description       = "SSH for the owner and the deploy key"
  ip_protocol       = "tcp"
  from_port         = 22
  to_port           = 22
  cidr_ipv4         = each.value
}

resource "aws_vpc_security_group_ingress_rule" "public" {
  for_each = {
    http     = { protocol = "tcp", from = 80, to = 80, description = "Caddy HTTP, redirects and ACME" }
    https    = { protocol = "tcp", from = 443, to = 443, description = "Caddy HTTPS" }
    http3    = { protocol = "udp", from = 443, to = 443, description = "Caddy HTTP/3" }
    turn_tcp = { protocol = "tcp", from = 3478, to = 3478, description = "coturn TURN over TCP" }
    turn_udp = { protocol = "udp", from = 3478, to = 3478, description = "coturn TURN over UDP" }
    turn_relay = {
      protocol    = "udp"
      from        = var.turn_relay_ports.from
      to          = var.turn_relay_ports.to
      description = "coturn relay range"
    }
  }

  security_group_id = aws_security_group.live.id
  description       = each.value.description
  ip_protocol       = each.value.protocol
  from_port         = each.value.from
  to_port           = each.value.to
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_vpc_security_group_egress_rule" "all" {
  security_group_id = aws_security_group.live.id
  description       = "Image pulls, ACME, S3, email and the social APIs"
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}
