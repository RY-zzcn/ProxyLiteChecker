#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

backup_dir="${1:-backups}"
timestamp="$(date +%Y%m%d-%H%M%S)"
target="${backup_dir}/proxylite-data-${timestamp}.tar.gz"

mkdir -p "$backup_dir"
tar -czf "$target" data .env 2>/dev/null || tar -czf "$target" data

echo "$target"
