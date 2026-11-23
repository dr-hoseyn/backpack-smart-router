package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dr-hoseyn/backpack-smart-router/internal/agent"
	"github.com/dr-hoseyn/backpack-smart-router/internal/panel"
	"github.com/dr-hoseyn/backpack-smart-router/internal/router"
)

var version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		slog.Error("backpack-router", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("expected engine, panel, agent, check, backup, restore-config, hash-password, or version")
	}
	command := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "engine", "check":
		config := command.String("config", "/var/lib/backpack-router/config/engine.json", "engine snapshot")
		socket := command.String("socket", "/run/backpack-router/control.sock", "engine control socket")
		if err := command.Parse(args[1:]); err != nil {
			return err
		}
		cfg, err := router.Load(*config)
		if err != nil {
			return err
		}
		if args[0] == "check" {
			if err := cfg.ValidateLocal(); err != nil {
				return err
			}
			fmt.Println("configuration valid")
			return nil
		}
		engine, err := router.New(cfg)
		if err != nil {
			return err
		}
		control, listener, err := engine.Control(*socket, *config)
		if err != nil {
			return err
		}
		defer control.Close()
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		controlErr := make(chan error, 1)
		go func() {
			controlErr <- control.Serve(listener)
			cancel()
		}()
		if err := engine.Serve(ctx); err != nil {
			return err
		}
		select {
		case err := <-controlErr:
			if !errors.Is(err, http.ErrServerClosed) {
				return err
			}
		default:
		}
		return nil
	case "panel":
		var opt panel.Options
		command.StringVar(&opt.Address, "listen", "127.0.0.1:9080", "private dashboard address")
		command.StringVar(&opt.Socket, "socket", "/run/backpack-router/control.sock", "engine control socket")
		command.StringVar(&opt.KernelMetrics, "kernel-metrics", os.Getenv("BACKPACK_ROUTER_KERNEL_METRICS"), "optional kernel forwarding traffic snapshot")
		command.StringVar(&opt.Snapshot, "config", "/var/lib/backpack-router/config/engine.json", "engine snapshot")
		command.StringVar(&opt.Database, "database", "/var/lib/backpack-router/panel/router.sqlite", "SQLite database")
		command.StringVar(&opt.Assets, "assets", "/opt/backpack-router/current/ui", "built dashboard")
		command.StringVar(&opt.Admin, "admin", "admin", "bootstrap administrator")
		command.StringVar(&opt.HashFile, "admin-hash", "/etc/backpack-router/admin.hash", "bootstrap Argon2id hash file")
		command.StringVar(&opt.TLSCert, "tls-cert", "", "TLS certificate")
		command.StringVar(&opt.TLSKey, "tls-key", "", "TLS private key")
		command.IntVar(&opt.SessionMinutes, "session-minutes", 480, "session lifetime")
		command.IntVar(&opt.RetentionDays, "retention-days", 14, "metrics and event retention")
		if err := command.Parse(args[1:]); err != nil {
			return err
		}
		return panel.Serve(ctx, opt)
	case "agent":
		listen := command.String("listen", "", "private health address")
		socks := command.String("socks", "127.0.0.1:1081", "local SOCKS endpoint through this tunnel")
		target := command.String("target", "", "controlled HTTPS endpoint returning 204")
		iface := command.String("interface", "", "network interface for VM counters")
		if err := command.Parse(args[1:]); err != nil {
			return err
		}
		return agent.Serve(ctx, *listen, *socks, *target, *iface)
	case "hash-password":
		if len(args) > 1 {
			return errors.New("provide the password on stdin")
		}
		line, err := bufio.NewReader(io.LimitReader(os.Stdin, 259)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		hash, err := panel.HashPassword(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
		if err != nil {
			return err
		}
		fmt.Println(hash)
		return nil
	case "backup":
		database := command.String("database", "/var/lib/backpack-router/panel/router.sqlite", "SQLite database")
		output := command.String("out", "", "new private backup file")
		if err := command.Parse(args[1:]); err != nil {
			return err
		}
		return panel.BackupDatabase(*database, *output)
	case "restore-config":
		database := command.String("database", "/var/lib/backpack-router/panel/router.sqlite", "SQLite database")
		snapshot := command.String("config", "/var/lib/backpack-router/config/engine.json", "engine snapshot")
		source := command.String("from", "", "validated configuration file")
		if err := command.Parse(args[1:]); err != nil {
			return err
		}
		return panel.RestoreConfiguration(*database, *snapshot, *source)
	case "version":
		fmt.Println(version)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
