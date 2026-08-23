#!/bin/bash
# Hardens a fresh Ubuntu Lightsail instance and installs Docker. Run once,
# as a user with sudo (the default `ubuntu` user), right after first boot.
#
#   scp deploy/bootstrap.sh ubuntu@<instance-ip>:~
#   ssh ubuntu@<instance-ip> 'chmod +x bootstrap.sh && ./bootstrap.sh'
set -euo pipefail

echo "==> updating packages"
sudo apt-get update
sudo apt-get upgrade -y

echo "==> installing Docker (official repo, not the distro package)"
sudo apt-get install -y ca-certificates curl gnupg
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
sudo chmod a+r /etc/apt/keyrings/docker.gpg
echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu \
  $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | \
  sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin

echo "==> letting the current user run docker without sudo"
sudo usermod -aG docker "$USER"

echo "==> firewall: allow only SSH, HTTP, HTTPS"
sudo apt-get install -y ufw
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw allow OpenSSH
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw --force enable

echo "==> automatic security updates"
sudo apt-get install -y unattended-upgrades
sudo dpkg-reconfigure -f noninteractive unattended-upgrades

echo "==> fail2ban (SSH brute-force protection)"
sudo apt-get install -y fail2ban
sudo systemctl enable --now fail2ban

cat <<'EOF'

==> done. Before deploying, double-check manually (not automated here —
    editing sshd_config remotely risks locking yourself out if something's
    misconfigured):

  1. Confirm you can already log in with an SSH key (Lightsail sets this
     up by default). Only then, in /etc/ssh/sshd_config, confirm/set:
       PasswordAuthentication no
       PermitRootLogin no
     then: sudo systemctl restart sshd
     — and test a NEW connection in a second terminal before closing
     this one.

  2. Log out and back in (or `newgrp docker`) for the docker group change
     to take effect, then verify: docker run hello-world

EOF
