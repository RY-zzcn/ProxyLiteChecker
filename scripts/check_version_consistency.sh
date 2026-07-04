#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

version="$(sed -n 's/.*appVersion[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' cmd/proxylite/main.go)"

if [[ -z "$version" ]]; then
  echo "appVersion not found" >&2
  exit 1
fi

check_contains() {
  local file="$1"
  local needle="$2"
  if ! grep -q "$needle" "$file"; then
    echo "version mismatch: $file does not contain $needle" >&2
    exit 1
  fi
}

check_contains app/web/index.html "v${version}"
check_contains .env.example "APP_VERSION=${version}"
check_contains docker-compose.yml "v${version}"
check_contains CHANGELOG.md "## ${version} -"

echo "version ${version} is consistent"
