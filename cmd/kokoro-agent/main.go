package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Vincentkeio/agent/internal/agent"
	"github.com/Vincentkeio/agent/internal/config"
)

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", "", "path to config.json (default: /etc/kokoro-agent/config.json, /opt/kokoro-agent/config.json, ./config.json)")
	flag.Parse()

	cfg, cfgFile, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	fmt.Printf("[kokoro-agent] config=%s agent_id=%s master=%s\n", cfgFile, cfg.AgentID, cfg.MasterWSURL)

	a := agent.New(cfg, cfgFile)

	// Signals
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	go func() {
		for s := range sigCh {
			switch s {
			case syscall.SIGHUP:
				fmt.Println("[kokoro-agent] received SIGHUP: reload config and reconnect if needed")
				if err := a.ReloadConfig(); err != nil {
					fmt.Printf("[kokoro-agent] reload config failed: %v\n", err)
				}
			default:
				fmt.Printf("[kokoro-agent] received %v: exiting...\n", s)
				a.Stop()
				return
			}
		}
	}()

	if err := a.Run(); err != nil {
		log.Fatalf("agent exited: %v", err)
	}
}
