#!/usr/bin/env bash
set -euo pipefail

# Prepares a fresh Ubuntu 24.04 host for deploy.sh; safe to re-run
# (deploy/README.md, "Bootstrap the host").

if [[ $EUID -ne 0 ]]; then
  echo "bootstrap: run as root, for example with sudo" >&2
  exit 1
fi

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y ca-certificates curl git unattended-upgrades unzip

install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
chmod a+r /etc/apt/keyrings/docker.asc
# shellcheck source=/dev/null
. /etc/os-release
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $VERSION_CODENAME stable" \
  > /etc/apt/sources.list.d/docker.list
apt-get update
apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

# Ubuntu 24.04 packages no AWS CLI v2; backup.sh needs it for the bucket.
if ! command -v aws > /dev/null; then
  work=$(mktemp -d)
  curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-$(uname -m).zip" -o "$work/awscli.zip"
  unzip -q "$work/awscli.zip" -d "$work"
  "$work/aws/install"
  rm -rf "$work"
fi

if [[ ! -f /swapfile ]]; then
  fallocate -l 2G /swapfile
  chmod 600 /swapfile
  mkswap /swapfile
fi
swapon --show=NAME --noheadings | grep -qx /swapfile || swapon /swapfile
grep -q '^/swapfile ' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab

printf 'APT::Periodic::Update-Package-Lists "1";\nAPT::Periodic::Unattended-Upgrade "1";\n' \
  > /etc/apt/apt.conf.d/20auto-upgrades

id deploy > /dev/null 2>&1 || useradd --create-home --shell /bin/bash deploy
usermod -aG docker deploy
install -d -m 700 -o deploy -g deploy /home/deploy/.ssh
touch /home/deploy/.ssh/authorized_keys
chown deploy:deploy /home/deploy/.ssh/authorized_keys
chmod 600 /home/deploy/.ssh/authorized_keys

if [[ ! -d /srv/vetmimi/.git ]]; then
  git clone --single-branch --branch main https://github.com/VetMiMi/vetmimi-api.git /srv/vetmimi
fi
install -d -m 700 /srv/vetmimi/backups
chown -R deploy:deploy /srv/vetmimi

# 16:30 UTC is 02:30 in Sydney in winter and 03:30 in summer.
echo '30 16 * * * deploy /srv/vetmimi/deploy/backup.sh 2>&1 | logger -t vetmimi-backup' > /etc/cron.d/vetmimi-backup
