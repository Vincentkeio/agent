package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Vincentkeio/agent/internal/util"
)

type Config struct {
	// Static
	MasterWSURL string `json:"master_ws_url"`
	Token       string `json:"token"`

	// Persistent identity. Generated once on first run if empty.
	AgentID string `json:"agent_id,omitempty"`

	// Optional; shown in UI (master may also allow editing server-side)
	Alias string `json:"alias,omitempty"`

	// Defaults (master may override via config_push)
	MetricsIntervalMS int `json:"metrics_interval_ms,omitempty"`

	// Network
	NetIface string `json:"net_iface,omitempty"` // "auto" or specific iface

	// TLS
	InsecureSkipVerify bool `json:"insecure_skip_verify,omitempty"`

	// Optional: TCP-PING defaults (master usually pushes targets)
	TCPPing struct {
		Enabled     bool `json:"enabled,omitempty"`
		IntervalSec int  `json:"interval_sec,omitempty"`
	} `json:"tcpping,omitempty"`
}

// Candidate default locations (ordered)
var defaultPaths = []string{
	"/etc/kokoro-agent/config.json",
	"/opt/kokoro-agent/config.json",
	"./config.json",
}

func Load(explicitPath string) (cfg Config, usedPath string, err error) {
	if explicitPath != "" {
		usedPath = explicitPath
	} else if env := os.Getenv("KOKORO_CONFIG"); env != "" {
		usedPath = env
	} else {
		for _, p := range defaultPaths {
			if _, e := os.Stat(p); e == nil {
				usedPath = p
				break
			}
		}
		if usedPath == "" {
			usedPath = defaultPaths[0]
		}
	}

	b, e := os.ReadFile(usedPath)
	if e != nil {
		return cfg, usedPath, fmt.Errorf("read %s: %w", usedPath, e)
	}
	if e := json.Unmarshal(b, &cfg); e != nil {
		return cfg, usedPath, fmt.Errorf("parse %s: %w", usedPath, e)
	}

	if cfg.MasterWSURL == "" {
		return cfg, usedPath, errors.New("master_ws_url is required")
	}
	if cfg.Token == "" {
		return cfg, usedPath, errors.New("token is required")
	}
	if cfg.MetricsIntervalMS <= 0 {
		cfg.MetricsIntervalMS = 1000 // your default
	}
	if cfg.NetIface == "" {
		cfg.NetIface = "auto"
	}

	// Generate persistent AgentID on first run.
	if cfg.AgentID == "" {
		cfg.AgentID = util.NewUUIDv4()
		if e := SaveAtomic(usedPath, cfg); e != nil {
			return cfg, usedPath, fmt.Errorf("save generated agent_id: %w", e)
		}
	}

	return cfg, usedPath, nil
}

func SaveAtomic(path string, cfg Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
