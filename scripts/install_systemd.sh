#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVICE_DIR="${HOME}/.config/systemd/user"
SERVICE_FILE="${SERVICE_DIR}/proxylite.service"

mkdir -p "$SERVICE_DIR" "$ROOT_DIR/bin" "$ROOT_DIR/data" "$ROOT_DIR/logs"

if [[ ! -x "$ROOT_DIR/bin/proxylite" ]]; then
  (cd "$ROOT_DIR" && go build -o bin/proxylite ./cmd/proxylite)
fi

cat >"$SERVICE_FILE" <<EOF
[Unit]
Description=ProxyLiteChecker
After=network-online.target

[Service]
Type=simple
WorkingDirectory=${ROOT_DIR}
EnvironmentFile=-${ROOT_DIR}/.env
ExecStart=${ROOT_DIR}/bin/proxylite
Restart=always
RestartSec=3

[Install]
WantedBy=default.target
EOF

systemctl --user daemon-reload
systemctl --user enable proxylite.service

echo "$SERVICE_FILE"
