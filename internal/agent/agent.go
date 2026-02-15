package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Vincentkeio/agent/internal/config"
	"github.com/Vincentkeio/agent/internal/metrics"
	"github.com/Vincentkeio/agent/internal/netprobe"
	"github.com/Vincentkeio/agent/internal/tcpping"
	"github.com/Vincentkeio/agent/internal/ws"
)

type runtimeConfig struct {
	MetricsIntervalMS  int
	TCPPingEnabled     bool
	TCPPingIntervalSec int
	TCPPingTargets     []tcpping.Target
	ConfigVersion      int64
}

type Agent struct {
	mu      sync.RWMutex
	cfg     config.Config
	cfgFile string

	rtMu sync.RWMutex
	rt   runtimeConfig

	stopCh      chan struct{}
	stopped     atomic.Bool
	reconnectCh chan struct{} // SIGHUP -> ask current connection to reconnect

	seq atomic.Uint64

	// One-time per process start (reported in hello only; not in metrics)
	netProbe     netprobe.Result
	netProbeDone bool
}

func New(cfg config.Config, cfgFile string) *Agent {
	return &Agent{
		cfg:         cfg,
		cfgFile:     cfgFile,
		stopCh:      make(chan struct{}),
		reconnectCh: make(chan struct{}, 1),
	}
}

func (a *Agent) Stop() {
	if a.stopped.CompareAndSwap(false, true) {
		close(a.stopCh)
	}
}

// ReloadConfig reloads local config.json (token/master url etc) and requests reconnect.
func (a *Agent) ReloadConfig() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	newCfg, _, err := config.Load(a.cfgFile)
	if err != nil {
		return err
	}
	a.cfg = newCfg

	select {
	case a.reconnectCh <- struct{}{}:
	default:
	}
	return nil
}

func (a *Agent) Run() error {
	// One-time net probe at process start (reported in hello only).
	a.netProbe = netprobe.Probe(3*time.Second, a.cfg.InsecureSkipVerify)
	a.netProbeDone = true

	backoff := time.Second
	for {
		select {
		case <-a.stopCh:
			return nil
		default:
		}

		err := a.runOnce()
		if err == nil {
			backoff = time.Second
			continue
		}

		fmt.Printf("[kokoro-agent] disconnected: %v; reconnect in %v\n", err, backoff)
		select {
		case <-time.After(backoff):
		case <-a.stopCh:
			return nil
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30*time.Second
			}
		}
	}
}

func (a *Agent) runOnce() error {
	cfg := a.getCfg()

	ctxDial, cancelDial := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelDial()

	conn, _, err := ws.Dial(ctxDial, cfg.MasterWSURL, cfg.InsecureSkipVerify)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Reconnect trigger (SIGHUP)
	go func() {
		select {
		case <-a.reconnectCh:
			_ = conn.WriteClose(1000, "reload")
			_ = conn.Close()
		case <-a.stopCh:
			return
		}
	}()

	// Client keepalive ping
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				_ = conn.WritePing([]byte("ping"))
			case <-a.stopCh:
				return
			}
		}
	}()

	// Send hello (first message)
	hello := map[string]any{
		"type":      "hello",
		"agent_id":  cfg.AgentID,
		"token":     cfg.Token,
		"agent_ver": "0.1.0",
		"client_ts": time.Now().Unix(),
		"cap":       []string{"metrics", "tcpping"},
		"sys": map[string]any{
			"hostname": mustHostname(),
			"os":       runtime.GOOS,
			"arch":     runtime.GOARCH,
		},
	}
	if cfg.Alias != "" {
		hello["alias"] = cfg.Alias
	}
	if a.netProbeDone {
		hello["net_probe"] = a.netProbe
	}

	if err := writeJSON(conn, hello); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recvErr := make(chan error, 1)
	ready := make(chan struct{})
	go a.recvLoop(ctx, conn, ready, recvErr)

	select {
	case <-ready:
	case err := <-recvErr:
		return err
	case <-time.After(10 * time.Second):
		return errors.New("timeout waiting hello_ok")
	case <-a.stopCh:
		return nil
	}

	// metrics loop
	metCollector := metrics.NewCollector(cfg.NetIface)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-a.stopCh:
				return
			default:
			}

			interval := a.getMetricsInterval()
			timer := time.NewTimer(interval)
			select {
			case <-timer.C:
				snap, err := metCollector.Collect()
				if err == nil {
					seq := a.seq.Add(1)
					msg := map[string]any{
						"type":     "metrics",
						"agent_id": cfg.AgentID,
						"seq":      seq,
						"ts":       snap.TS,
						"metrics":  snap,
					}
					_ = writeJSON(conn, msg)
				}
			case <-ctx.Done():
				timer.Stop()
				return
			case <-a.stopCh:
				timer.Stop()
				return
			}
		}
	}()

	// tcpping loop
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-a.stopCh:
				return
			default:
			}

			enabled, interval, targets := a.getTCPPing()
			if !enabled || interval <= 0 || len(targets) == 0 {
				time.Sleep(2 * time.Second)
				continue
			}

			t := time.NewTicker(time.Duration(interval) * time.Second)
			for {
				select {
				case <-t.C:
					ctx2, cancel2 := context.WithTimeout(ctx, 4*time.Second)
					samples := make([]tcpping.Sample, 0, len(targets))
					for _, tg := range targets {
						samples = append(samples, tcpping.Ping(ctx2, tg))
					}
					cancel2()

					seq := a.seq.Add(1)
					msg := map[string]any{
						"type":     "tcpping_batch",
						"agent_id": cfg.AgentID,
						"seq":      seq,
						"ts":       time.Now().Unix(),
						"samples":  samples,
					}
					_ = writeJSON(conn, msg)

				case <-ctx.Done():
					t.Stop()
					return
				case <-a.stopCh:
					t.Stop()
					return
				default:
					en2, i2, tg2 := a.getTCPPing()
					if !en2 || i2 != interval || len(tg2) != len(targets) {
						t.Stop()
						goto OUTER
					}
					time.Sleep(200 * time.Millisecond)
				}
			}
		OUTER:
			continue
		}
	}()

	select {
	case err := <-recvErr:
		cancel()
		return err
	case <-a.stopCh:
		cancel()
		return nil
	}
}

func (a *Agent) recvLoop(ctx context.Context, conn *ws.Conn, ready chan<- struct{}, recvErr chan<- error) {
	seenReady := false

	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		default:
		}

		_ = conn.SetDeadline(time.Now().Add(90 * time.Second))
		op, data, err := conn.ReadMessage()
		if err != nil {
			recvErr <- err
			return
		}
		if op != 0x1 { // only text
			continue
		}

		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		typ, _ := m["type"].(string)

		switch typ {
		case "hello_ok", "hello_ack":
			a.applyConfigFromMessage(m)
			if !seenReady {
				seenReady = true
				close(ready)
			}
		case "auth_err":
			recvErr <- netprobe.ErrAuth
			return
		case "config_push":
			a.applyConfigFromMessage(m)
			ack := map[string]any{
				"type":           "config_ack",
				"agent_id":       a.getCfg().AgentID,
				"config_version": a.getConfigVersion(),
				"ok":             true,
				"ts":             time.Now().Unix(),
			}
			_ = writeJSON(conn, ack)
		case "kick":
			recvErr <- errors.New("kicked by server")
			return
		default:
			// ignore
		}
	}
}

func (a *Agent) applyConfigFromMessage(m map[string]any) {
	cfgAny, ok := m["config"]
	if !ok {
		return
	}
	b, err := json.Marshal(cfgAny)
	if err != nil {
		return
	}
	var c struct {
		MetricsIntervalMS int `json:"metrics_interval_ms"`
		TCPPing struct {
			Enabled     bool             `json:"enabled"`
			IntervalSec int              `json:"interval_sec"`
			Targets     []tcpping.Target `json:"targets"`
		} `json:"tcpping"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return
	}

	var ver int64
	if v, ok := m["config_version"].(float64); ok {
		ver = int64(v)
	}

	a.rtMu.Lock()
	defer a.rtMu.Unlock()

	if c.MetricsIntervalMS > 0 {
		a.rt.MetricsIntervalMS = c.MetricsIntervalMS
	}
	a.rt.TCPPingEnabled = c.TCPPing.Enabled
	if c.TCPPing.IntervalSec > 0 {
		a.rt.TCPPingIntervalSec = c.TCPPing.IntervalSec
	}
	if c.TCPPing.Targets != nil {
		a.rt.TCPPingTargets = c.TCPPing.Targets
	}
	if ver > 0 {
		a.rt.ConfigVersion = ver
	}

	fmt.Printf("[kokoro-agent] applied config: metrics=%dms tcpping=%v interval=%ds targets=%d ver=%d\n",
		a.rt.MetricsIntervalMS, a.rt.TCPPingEnabled, a.rt.TCPPingIntervalSec, len(a.rt.TCPPingTargets), a.rt.ConfigVersion)
}

func (a *Agent) getMetricsInterval() time.Duration {
	a.rtMu.RLock()
	defer a.rtMu.RUnlock()
	ms := a.rt.MetricsIntervalMS
	if ms <= 0 {
		ms = a.cfg.MetricsIntervalMS
	}
	return time.Duration(ms) * time.Millisecond
}

func (a *Agent) getTCPPing() (bool, int, []tcpping.Target) {
	a.rtMu.RLock()
	defer a.rtMu.RUnlock()

	enabled := a.rt.TCPPingEnabled || a.cfg.TCPPing.Enabled
	interval := a.rt.TCPPingIntervalSec
	if interval <= 0 {
		interval = a.cfg.TCPPing.IntervalSec
	}
	return enabled, interval, a.rt.TCPPingTargets
}

func (a *Agent) getConfigVersion() int64 {
	a.rtMu.RLock()
	defer a.rtMu.RUnlock()
	return a.rt.ConfigVersion
}

func (a *Agent) getCfg() config.Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg
}

func mustHostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
}

func writeJSON(conn *ws.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.WriteText(b)
}
