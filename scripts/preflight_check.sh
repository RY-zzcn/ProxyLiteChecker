#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

bash -n scripts/start.sh
go test ./...
go build -o bin/proxylite ./cmd/proxylite
git diff --check
