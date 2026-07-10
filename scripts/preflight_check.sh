#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

bash -n scripts/start.sh
bash -n scripts/check_version_consistency.sh
bash -n scripts/backup_data.sh
bash -n scripts/restore_data.sh
bash -n scripts/update_local.sh
bash -n scripts/install_systemd.sh
bash -n scripts/install.sh
bash scripts/install.sh --help >/dev/null
scripts/check_version_consistency.sh
go test ./...
go build -o bin/proxylite ./cmd/proxylite
git diff --check
