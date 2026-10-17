#!/usr/bin/env bash
# Cloud-init user-data: installs Docker + the benchi harness on a fresh Ubuntu
# instance. The benchmark files themselves are rsync'd in by run-aws.sh.
#
# NOTE: this file is a Terraform templatefile. The only template var is
# benchi_version; shell variable expansions are double-dollar escaped below.
set -euo pipefail

BENCHI_VERSION="${benchi_version}"
BENCHI_ARCH="x86_64"

export DEBIAN_FRONTEND=noninteractive

apt-get update -y
apt-get install -y --no-install-recommends \
  ca-certificates curl git rsync jq wget gnupg lsb-release

# Docker (docker.io + compose plugin is the simplest reproducible path).
apt-get install -y --no-install-recommends docker.io docker-compose-v2
systemctl enable --now docker

# Go (the loader + conduit image build use it).
GO_VERSION="1.26.9"
curl -fsSL "https://go.dev/dl/go$${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tgz
rm -rf /usr/local/go
tar -C /usr/local -xzf /tmp/go.tgz
ln -sf /usr/local/go/bin/go /usr/local/bin/go
ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt

# benchi release binary (GoReleaser). Matches the version the local runner uses.
BENCHI_TARBALL="benchi_$${BENCHI_VERSION}_Linux_$${BENCHI_ARCH}.tar.gz"
BENCHI_URL="https://github.com/ConduitIO/benchi/releases/download/v$${BENCHI_VERSION}/$${BENCHI_TARBALL}"
curl -fsSL "$${BENCHI_URL}" -o "/tmp/$${BENCHI_TARBALL}"
tar -xzf "/tmp/$${BENCHI_TARBALL}" -C /tmp
install -m 0755 /tmp/benchi /usr/local/bin/benchi
benchi --help >/dev/null 2>&1 || true

# The benchmark runner needs docker access as the ubuntu user.
usermod -aG docker ubuntu

echo "benchmark instance ready: benchi $${BENCHI_VERSION}, docker $(docker --version)"
