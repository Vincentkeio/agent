#!/usr/bin/env bash
set -euo pipefail

REPO="Vincentkeio/agent"
BIN_NAME="kokoro-agent"

CONFIG_DIR="/etc/kokoro-agent"
CONFIG_FILE="${CONFIG_DIR}/config.json"
BIN_PATH="/usr/local/bin/${BIN_NAME}"
UNIT_PATH="/etc/systemd/system/kokoro-agent.service"

MASTER=""
TOKEN=""
ALIAS=""
RESET_ID="0"
INSECURE="0"

usage() {
  cat <<'EOF'
Usage:
  install.sh --master <wss://domain/agent/ws> --token <TOKEN> [--alias <NAME>] [--reset-id] [--insecure-skip-verify]
EOF
}

need_root() { [ "$(id -u)" -eq 0 ] || { echo "❌ run as root (sudo)"; exit 1; }; }

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) echo "❌ unsupported arch"; exit 3 ;;
  esac
}

download_latest_release() {
  local arch="$1"
  local asset="${BIN_NAME}_linux_${arch}.tar.gz"
  local url="https://github.com/${REPO}/releases/latest/download/${asset}"

  echo "== download ${asset} =="
  local tmpd; tmpd="$(mktemp -d)"; trap 'rm -rf "$tmpd"' EXIT
  curl -fsSL "$url" -o "$tmpd/pkg.tgz"
  tar -xzf "$tmpd/pkg.tgz" -C "$tmpd"
  install -m 0755 "$tmpd/${BIN_NAME}" "$BIN_PATH"
}

preserve_agent_id() {
  [ "$RESET_ID" = "1" ] && return 0
  [ -f "$CONFIG_FILE" ] || return 0
  grep -oE '"agent_id"[[:space:]]*:[[:space:]]*"[^"]+"' "$CONFIG_FILE" | head -n1 \
    | sed -E 's/.*"([^"]+)".*/\1/' || true
}

write_config() {
  mkdir -p "$CONFIG_DIR"
  chmod 0755 "$CONFIG_DIR"

  local keep_id=""; keep_id="$(preserve_agent_id || true)"
  local agent_id_line
  if [ "$RESET_ID" = "1" ]; then
    agent_id_line='  "agent_id": ""'
  elif [ -n "$keep_id" ]; then
    agent_id_line="  \"agent_id\": \"${keep_id}\""
  else
    agent_id_line='  "agent_id": ""'
  fi

  local alias_line=""
  [ -n "$ALIAS" ] && alias_line="  \"alias\": \"${ALIAS}\","

  cat >"$CONFIG_FILE" <<EOF
{
  "master_ws_url": "${MASTER}",
  "token": "${TOKEN}",
${alias_line}
${agent_id_line},
  "metrics_interval_ms": 1000,
  "net_iface": "auto",
  "insecure_skip_verify": $( [ "$INSECURE" = "1" ] && echo "true" || echo "false" )
}
EOF
  chmod 0600 "$CONFIG_FILE"
}

write_unit() {
  cat >"$UNIT_PATH" <<EOF
[Unit]
Description=kokoro agent (Go)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${BIN_PATH} --config ${CONFIG_FILE}
ExecReload=/bin/kill -HUP \$MAINPID
Restart=always
RestartSec=2
LimitNOFILE=65535
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
}

enable_start() {
  systemctl daemon-reload
  systemctl enable --now kokoro-agent.service
  systemctl --no-pager -l status kokoro-agent.service | sed -n '1,25p' || true
  echo "Logs: journalctl -u kokoro-agent.service -f --no-pager"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --master) MASTER="${2:-}"; shift 2;;
    --token) TOKEN="${2:-}"; shift 2;;
    --alias) ALIAS="${2:-}"; shift 2;;
    --reset-id) RESET_ID="1"; shift;;
    --insecure-skip-verify) INSECURE="1"; shift;;
    -h|--help) usage; exit 0;;
    *) echo "Unknown arg: $1"; usage; exit 1;;
  esac
done

need_root
[ -n "$MASTER" ] && [ -n "$TOKEN" ] || { usage; exit 1; }
command -v systemctl >/dev/null 2>&1 || { echo "❌ systemd required"; exit 6; }

arch="$(detect_arch)"
download_latest_release "$arch"
write_config
write_unit
enable_start
