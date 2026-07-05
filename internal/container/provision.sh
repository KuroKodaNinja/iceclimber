#!/bin/sh
# Provision the iceclimber sandbox base image (decision #111). Runs inside a build
# container on the DEFAULT (internet) network, then committed; the real sandbox then
# runs from the commit on the INTERNAL (no-NAT) network. Installs sshd, a NON-SUDO
# `agent` workload user, and host keys — NO toolchain (runtimes are a separate
# `iceclimber install` concern), so the image stays thin.
set -eu
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends openssh-server ca-certificates >/dev/null
mkdir -p /run/sshd /root/.ssh

# The `ssh -R` reverse-tunnel egress (how the air-gapped box reaches Popo's proxy) needs
# TCP forwarding ENABLED. Distros vary (Alpine defaults it OFF); set it explicitly.
sed -i 's/^#\?AllowTcpForwarding.*/AllowTcpForwarding yes/' /etc/ssh/sshd_config
grep -q '^AllowTcpForwarding yes' /etc/ssh/sshd_config || printf '\nAllowTcpForwarding yes\n' >>/etc/ssh/sshd_config

# Popo dials in as the non-sudo `agent` user and the workload runs as it (Claude Code
# refuses --dangerously-skip-permissions as uid 0, decision #110; the air-gap is enforced
# OUTSIDE the container so it doesn't depend on an in-container non-root user).
sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config
id agent >/dev/null 2>&1 || useradd --create-home --shell /bin/bash agent
deluser agent sudo  >/dev/null 2>&1 || true
deluser agent admin >/dev/null 2>&1 || true

# A container has no session-tracking backend, so pam_loginuid (marked `required` in
# /etc/pam.d/sshd) fails pam_open_session for a non-root login — the SSH exec then aborts
# with exit 254 (auth succeeds, but the session can't open). Make it optional so the
# `agent` user's SSH sessions/commands run.
sed -i 's/^session\s\+required\s\+pam_loginuid.so/session optional pam_loginuid.so/' /etc/pam.d/sshd

ssh-keygen -A
