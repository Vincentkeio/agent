#!/usr/bin/env bash
set -euo pipefail

# Usage:
#   bash install.sh --master wss://YOUR_DOMAIN/agent/ws --token TOKEN [--alias NAME] [--reset-id]
#
# Notes:
# - This script installs /usr/local/bin/kokoro-agent, writes /etc/kokoro-agent/config.json,
#   installs a systemd unit, and starts the service.
# - To "become a new agent", run again with --reset-id (it clears agent_id so the binary will generate a new UUID).

MASTER=""
TOKEN=""
ALIAS=""
RESET_ID="0"
CONFIG_DIR="/etc/kokoro-agent"
CONFIG_FILE="$CONFIG_DIR/config.json"
BIN_PATH="/usr/local/bin/kokoro-agent"
UNIT_PATH="/etc/systemd/system/kokoro-agent.service"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --master) MASTER="${2:-}"; shift 2;;
    --token) TOKEN="${2:-}"; shift 2;;
    --alias) ALIAS="${2:-}"; shift 2;;
    --reset-id) RESET_ID="1"; shift 1;;
    *) echo "Unknown arg: $1" >&2; exit 2;;
  esac
done

[[ -n "$MASTER" ]] || { echo "Missing --master" >&2; exit 2; }
[[ -n "$TOKEN" ]]  || { echo "Missing --token" >&2; exit 2; }

echo "== install kokoro-agent =="

mkdir -p "$CONFIG_DIR"

ARCH="$(uname -m)"
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64";;
  aarch64|arm64) ARCH="arm64";;
  *) echo "Unsupported arch: $ARCH" >&2; exit 2;;
esac

# ---- Download binary from GitHub Release (EDIT THESE TWO LINES after you publish) ----
# REPO="YOUR_GITHUB_USER/kokoro-agent"
# URL="https://github.com/${REPO}/releases/latest/download/kokoro-agent_${OS}_${ARCH}.tar.gz"
# For now, fallback to building from source if URL is empty.
URL=""

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

if [[ -n "$URL" ]]; then
  echo "Downloading: $URL"
  curl -fsSL "$URL" -o "$tmpdir/agent.tgz"
  tar -xzf "$tmpdir/agent.tgz" -C "$tmpdir"
  install -m 0755 "$tmpdir/kokoro-agent" "$BIN_PATH"
else
  echo "No release URL configured; building from source requires git + go."
  command -v git >/dev/null || { echo "git not found" >&2; exit 2; }
  command -v go >/dev/null || { echo "go not found" >&2; exit 2; }
  git clone --depth=1 https://github.com/YOUR_GITHUB_USER/kokoro-agent "$tmpdir/src"
  (cd "$tmpdir/src" && go build -o "$tmpdir/kokoro-agent" ./cmd/kokoro-agent)
  install -m 0755 "$tmpdir/kokoro-agent" "$BIN_PATH"
fi

# Write config (keep agent_id blank to allow auto-generate, unless existing file has id and no reset requested)
AGENT_ID=""
if [[ -f "$CONFIG_FILE" && "$RESET_ID" != "1" ]]; then
  AGENT_ID="$(python3 -c 'import json;import sys; p=sys.argv[1]; d=json.load(open(p)); print(d.get("agent_id",""))' "$CONFIG_FILE" 2>/dev/null || true)"
fi

cat > "$CONFIG_FILE" <<JSON
{
  "master_ws_url": "$(printf %s "$MASTER" | sed 's/"/\\"/g')",
  "token": "$(printf %s "$TOKEN" | sed 's/"/\\"/g')",
  "agent_id": "$(printf %s "$AGENT_ID" | sed 's/"/\\"/g')",
  "alias": "$(printf %s "$ALIAS" | sed 's/"/\\"/g')",
  "metrics_interval_ms": 1000,
  "net_iface": "auto",
  "insecure_skip_verify": false,
  "tcpping": { "enabled": false, "interval_sec": 15 }
}
JSON

# systemd unit
cat > "$UNIT_PATH" <<'UNIT'
[Unit]
Description=kokoro agent (Go)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/kokoro-agent --config /etc/kokoro-agent/config.json
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=2
LimitNOFILE=65535
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now kokoro-agent.service

echo "✅ installed. logs: journalctl -u kokoro-agent.service -f --no-pager"
if [[ "$RESET_ID" == "1" ]]; then
  echo "ℹ️ --reset-id was used, agent_id will be regenerated on first start."
fi
