#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

scripts/backup_data.sh >/dev/null
git pull --ff-only
go build -o bin/proxylite ./cmd/proxylite

if systemctl --user is-active --quiet proxylite.service; then
  systemctl --user restart proxylite.service
fi

echo "ProxyLiteChecker updated"
