#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: scripts/restore_data.sh <backup.tar.gz>" >&2
  exit 1
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

backup="$1"
if [[ ! -f "$backup" ]]; then
  echo "backup not found: $backup" >&2
  exit 1
fi

mkdir -p data
tar -xzf "$backup" -C "$ROOT_DIR"

echo "restored $backup"
