package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "socialweb-vault:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: socialweb-vault apply-root|prepare-root")
	}

	cfg, err := Load()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "prepare-root":
		log.Info("materialising the root vault's mutual TLS material",
			"state_dir", cfg.StateDir,
			"addr", cfg.Addr)
		return prepareRoot(cfg, log)
	case "apply-root":
		log.Info("applying the root vault layout",
			"addr", cfg.Addr,
			"vault", vaultlayout.Root,
			"unseal", cfg.UnsealPath,
			"kubeconfig", cfg.KCPKubeconfig)
		return applyRoot(ctx, cfg, log)
	default:
		return fmt.Errorf("usage: socialweb-vault apply-root|prepare-root (got %q)", args[0])
	}
}
