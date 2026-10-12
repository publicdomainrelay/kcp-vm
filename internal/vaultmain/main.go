package vaultmain

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/foldmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

func Main(ctx context.Context, inv foldmain.Invocation) int {
	if err := run(ctx, inv.Args, inv); err != nil {
		fmt.Fprintln(inv.Stderr, "socialweb-vault:", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string, inv foldmain.Invocation) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: socialweb-vault apply-root|prepare-root")
	}

	cfg, err := load(inv.Env)
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(inv.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
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
