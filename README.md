# kokoro-agent (Go)

Kokoro Probe Agent (Go).  
Connects to Master via **WSS** and sends:
- `hello` (first frame): token auth + static sys + one-time net probe (IPv4/IPv6 + public IP)
- `metrics` (default 1s; master can override via `config_push`)
- `tcpping_batch` (targets provided by master)

## Protocol (MVP)

**Agent → Master**
- `hello` (first)
- `metrics`
- `tcpping_batch`
- `config_ack`

**Master → Agent**
- `hello_ok` / `hello_ack`
- `config_push`
- `auth_err`
- `kick` (optional)

## Install (server)

1) Build:
```bash
go build -o kokoro-agent ./cmd/kokoro-agent
```

2) Config:
```bash
sudo mkdir -p /etc/kokoro-agent
sudo cp config/config.example.json /etc/kokoro-agent/config.json
sudo nano /etc/kokoro-agent/config.json
```

3) systemd:
```bash
sudo cp systemd/kokoro-agent.service /etc/systemd/system/kokoro-agent.service
sudo install -m 0755 ./kokoro-agent /usr/local/bin/kokoro-agent
sudo systemctl daemon-reload
sudo systemctl enable --now kokoro-agent.service
journalctl -u kokoro-agent.service -f --no-pager
```

## Token rotation

- Master rotates token → **existing connections keep running**
- When agent reconnects, it must use the **new token**
- Update `/etc/kokoro-agent/config.json` `token`, then:
```bash
sudo systemctl reload kokoro-agent.service
# or restart:
sudo systemctl restart kokoro-agent.service
```

## Become a “new agent”

This agent generates and persists `agent_id` (UUID) on first run.

To be treated as a brand new node by the master, clear `agent_id` and restart:
```bash
sudo jq '.agent_id=""' /etc/kokoro-agent/config.json > /tmp/cfg && sudo mv /tmp/cfg /etc/kokoro-agent/config.json
sudo systemctl restart kokoro-agent.service
```

Or re-run the install script with `--reset-id`.

## Notes

- Linux-only metrics implementation via `/proc` (no heavy deps).
- Public IP probe uses ipify endpoints:
  - IPv4: https://api.ipify.org?format=json
  - IPv6: https://api6.ipify.org?format=json
