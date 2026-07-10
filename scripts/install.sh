#!/usr/bin/env bash
set -euo pipefail

PROJECT_NAME="ProxyLiteChecker"
GITHUB_REPO="RY-zzcn/ProxyLiteChecker"
INSTALL_DIR="/opt/ProxyLiteChecker"
SERVICE_NAME="proxylitechecker.service"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}"
IMAGE="ghcr.io/ry-zzcn/proxylitechecker"
MODE=""
RELEASE_TAG=""
AUTO_YES=0
TMP_DIR=""
GENERATED_PASSWORD=""

say() {
  printf '%s\n' "$*"
}

die() {
  printf '错误：%s\n' "$*" >&2
  exit 1
}

cleanup() {
  if [[ -n "$TMP_DIR" && -d "$TMP_DIR" ]]; then
    rm -rf "$TMP_DIR"
  fi
}
trap cleanup EXIT

usage() {
  cat <<'EOF'
ProxyLiteChecker 一键部署

用法：
  sudo bash scripts/install.sh
  sudo bash scripts/install.sh --mode binary
  sudo bash scripts/install.sh --mode docker

选项：
  --mode binary|docker  跳过部署方式选择
  --version vX.Y.Z     部署指定正式版本，默认使用 GitHub 最新 Release
  --yes                Docker 缺失时自动同意安装
  -h, --help           显示帮助

默认安装目录：/opt/ProxyLiteChecker
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode)
      [[ $# -ge 2 ]] || die "--mode 缺少参数"
      MODE="$2"
      shift 2
      ;;
    --version)
      [[ $# -ge 2 ]] || die "--version 缺少参数"
      RELEASE_TAG="$2"
      shift 2
      ;;
    --yes)
      AUTO_YES=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "未知参数：$1"
      ;;
  esac
done

[[ "$(uname -s)" == "Linux" ]] || die "一键部署脚本目前只支持 Linux"
[[ "$EUID" -eq 0 ]] || die "请使用 root 运行，例如：curl -fsSL https://raw.githubusercontent.com/${GITHUB_REPO}/main/scripts/install.sh | sudo bash"

for command in curl awk sed grep sha256sum install mktemp od tr; do
  command -v "$command" >/dev/null 2>&1 || die "缺少必要命令：${command}"
done

prompt() {
  local message="$1"
  local answer
  [[ -r /dev/tty ]] || die "当前没有交互终端，请使用 --mode binary 或 --mode docker"
  printf '%s' "$message" >/dev/tty
  IFS= read -r answer </dev/tty
  printf '%s' "$answer"
}

confirm() {
  local message="$1"
  local answer
  if [[ "$AUTO_YES" -eq 1 ]]; then
    return 0
  fi
  answer="$(prompt "${message} [y/N]: ")"
  [[ "$answer" == "y" || "$answer" == "Y" || "$answer" == "yes" || "$answer" == "YES" ]]
}

choose_mode() {
  local choice
  if [[ -n "$MODE" ]]; then
    [[ "$MODE" == "binary" || "$MODE" == "docker" ]] || die "--mode 只能是 binary 或 docker"
    return
  fi
  say "请选择部署方式："
  say "  1. 二进制部署（GitHub Release + systemd）"
  say "  2. Docker 部署（GHCR 镜像）"
  choice="$(prompt "请输入 1 或 2: ")"
  case "$choice" in
    1) MODE="binary" ;;
    2) MODE="docker" ;;
    *) die "无效选择，项目未部署" ;;
  esac
}

resolve_release() {
  local final_url
  if [[ -z "$RELEASE_TAG" ]]; then
    say "正在查询 GitHub 最新正式版本..."
    final_url="$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/${GITHUB_REPO}/releases/latest")" || die "无法查询 GitHub 最新 Release"
    RELEASE_TAG="${final_url##*/}"
  fi
  [[ "$RELEASE_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "无效版本：${RELEASE_TAG}"
}

random_hex() {
  local bytes="$1"
  od -An -N "$bytes" -tx1 /dev/urandom | tr -d ' \n'
}

prepare_install_dir() {
  mkdir -p "$INSTALL_DIR/data"
  chmod 750 "$INSTALL_DIR"
  if [[ ! -f "$INSTALL_DIR/.env" ]]; then
    GENERATED_PASSWORD="$(random_hex 16)"
    cat >"$INSTALL_DIR/.env" <<EOF
TZ=Asia/Shanghai
HOST=0.0.0.0
PORT=8899
DATABASE_URL=sqlite:///./data/proxylite.db
ADMIN_USERNAME=admin
ADMIN_PASSWORD=${GENERATED_PASSWORD}
SECRET_KEY=$(random_hex 32)
ACCESS_TOKEN_MINUTES=1440
PLC_REQUIRE_SECURE=1
PLC_GATEWAY_ENABLED=1
PLC_GATEWAY_HOST=0.0.0.0
PLC_GATEWAY_PORT=18080
PLC_GATEWAY_TARGET_PROFILES=all
PLC_SOCKS5_GATEWAY_ENABLED=1
PLC_SOCKS5_GATEWAY_HOST=0.0.0.0
PLC_SOCKS5_GATEWAY_PORT=18081
EOF
    chmod 600 "$INSTALL_DIR/.env"
  fi
  if grep -q '^APP_VERSION=' "$INSTALL_DIR/.env"; then
    sed -i "s/^APP_VERSION=.*/APP_VERSION=${RELEASE_TAG#v}/" "$INSTALL_DIR/.env"
  else
    printf '\nAPP_VERSION=%s\n' "${RELEASE_TAG#v}" >>"$INSTALL_DIR/.env"
  fi
  chmod 600 "$INSTALL_DIR/.env"
}

wait_for_health() {
  local attempt
  for ((attempt = 1; attempt <= 45; attempt++)); do
    if curl -fsS --max-time 2 http://127.0.0.1:8899/health >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

stop_docker_deployment() {
  if command -v docker >/dev/null 2>&1; then
    docker rm -f proxylitechecker >/dev/null 2>&1 || true
  fi
  rm -f "$INSTALL_DIR/compose.yaml"
}

stop_binary_deployment() {
  if command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now "$SERVICE_NAME" >/dev/null 2>&1 || true
  fi
  rm -f "$SERVICE_FILE" "$INSTALL_DIR/proxylite"
  if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true
  fi
}

install_binary() {
  local machine asset download_base expected actual
  command -v systemctl >/dev/null 2>&1 || die "二进制部署需要 systemd，但未找到 systemctl"
  machine="$(uname -m)"
  case "$machine" in
    x86_64|amd64) asset="proxylite-linux-amd64" ;;
    aarch64|arm64) asset="proxylite-linux-arm64" ;;
    armv7l|armv7) asset="proxylite-linux-armv7" ;;
    *) die "不支持的 Linux 架构：${machine}" ;;
  esac

  TMP_DIR="$(mktemp -d)"
  download_base="https://github.com/${GITHUB_REPO}/releases/download/${RELEASE_TAG}"
  say "正在下载 ${RELEASE_TAG} 二进制和校验文件..."
  curl -fL --retry 3 --connect-timeout 15 -o "$TMP_DIR/$asset" "$download_base/$asset" || die "二进制下载失败，项目未部署"
  curl -fL --retry 3 --connect-timeout 15 -o "$TMP_DIR/SHA256SUMS" "$download_base/SHA256SUMS" || die "校验文件下载失败，项目未部署"
  expected="$(awk -v name="$asset" '$2 == name {print $1}' "$TMP_DIR/SHA256SUMS")"
  [[ "$expected" =~ ^[0-9a-fA-F]{64}$ ]] || die "SHA256SUMS 中缺少 ${asset}，项目未部署"
  actual="$(sha256sum "$TMP_DIR/$asset" | awk '{print $1}')"
  [[ "$actual" == "$expected" ]] || die "二进制 SHA256 校验失败，项目未部署"

  prepare_install_dir
  stop_docker_deployment
  install -m 0755 "$TMP_DIR/$asset" "$INSTALL_DIR/proxylite.new"
  mv -f "$INSTALL_DIR/proxylite.new" "$INSTALL_DIR/proxylite"
  cat >"$SERVICE_FILE" <<EOF
[Unit]
Description=ProxyLiteChecker
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}
EnvironmentFile=${INSTALL_DIR}/.env
ExecStart=${INSTALL_DIR}/proxylite
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable --now "$SERVICE_NAME"
  systemctl restart "$SERVICE_NAME"
  if ! wait_for_health; then
    systemctl status "$SERVICE_NAME" --no-pager >&2 || true
    die "服务未能通过 8899 health 检查，请查看：journalctl -u ${SERVICE_NAME} -n 100"
  fi
}

docker_ready() {
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 && docker info >/dev/null 2>&1
}

ensure_docker() {
  if docker_ready; then
    return 0
  fi
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 && command -v systemctl >/dev/null 2>&1; then
    systemctl start docker >/dev/null 2>&1 || true
    if docker_ready; then
      return 0
    fi
  fi
  say "未检测到可用的 Docker Engine + Compose 环境。"
  if ! confirm "是否使用 Docker 官方脚本安装/修复 Docker"; then
    die "用户选择不安装 Docker，项目部署已停止"
  fi
  TMP_DIR="$(mktemp -d)"
  say "正在下载并执行 Docker 官方安装脚本..."
  curl -fsSL --retry 3 --connect-timeout 15 -o "$TMP_DIR/get-docker.sh" https://get.docker.com || die "Docker 安装脚本下载失败，项目部署已停止"
  sh "$TMP_DIR/get-docker.sh" || die "Docker 安装失败，项目部署已停止"
  if command -v systemctl >/dev/null 2>&1; then
    systemctl enable --now docker >/dev/null 2>&1 || true
  fi
  docker_ready || die "Docker 或 Docker Compose 安装后仍不可用，项目部署已停止"
}

install_docker() {
  ensure_docker
  say "正在拉取 ${IMAGE}:${RELEASE_TAG}..."
  docker pull "${IMAGE}:${RELEASE_TAG}" || die "GHCR 镜像拉取失败，项目部署已停止"
  prepare_install_dir
  cat >"$INSTALL_DIR/compose.yaml" <<EOF
services:
  proxylitechecker:
    image: ${IMAGE}:${RELEASE_TAG}
    container_name: proxylitechecker
    restart: unless-stopped
    env_file:
      - .env
    environment:
      TZ: Asia/Shanghai
    ports:
      - "8899:8899"
      - "18080-18089:18080-18089"
    volumes:
      - ./data:/app/data
EOF
  stop_binary_deployment
  docker compose -f "$INSTALL_DIR/compose.yaml" up -d --remove-orphans || die "Docker 容器启动失败"
  if ! wait_for_health; then
    docker compose -f "$INSTALL_DIR/compose.yaml" logs --tail=100 >&2 || true
    die "容器未能通过 8899 health 检查"
  fi
}

choose_mode
resolve_release
say "部署方式：${MODE}"
say "部署版本：${RELEASE_TAG}"
say "安装目录：${INSTALL_DIR}"

case "$MODE" in
  binary) install_binary ;;
  docker) install_docker ;;
esac

say ""
say "${PROJECT_NAME} ${RELEASE_TAG} 部署成功"
say "访问地址：http://服务器IP:8899"
say "配置文件：${INSTALL_DIR}/.env"
say "数据目录：${INSTALL_DIR}/data"
if [[ -n "$GENERATED_PASSWORD" ]]; then
  say "管理员用户：admin"
  say "管理员密码：${GENERATED_PASSWORD}"
  say "请立即保存密码；后续可在 ${INSTALL_DIR}/.env 修改。"
fi
if [[ "$MODE" == "binary" ]]; then
  say "查看日志：journalctl -u ${SERVICE_NAME} -f"
else
  say "查看日志：cd ${INSTALL_DIR} && docker compose logs -f"
fi
