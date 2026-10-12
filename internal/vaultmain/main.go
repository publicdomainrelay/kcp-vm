package vaultmain

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/foldmain"
)

func Main(ctx context.Context, inv foldmain.Invocation) int {
	if err := run(ctx, inv.Args, inv); err != nil {
		fmt.Fprintln(inv.Stderr, "socialweb-vault:", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string, inv foldmain.Invocation) error {
	if len(args) != 1 || args[0] != "apply-root" {
		return fmt.Errorf("usage: vault apply-root")
	}
	cfg, err := load(inv.Env)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(inv.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	access, err := ReadRootAccess(ctx, cfg.KCPKubeconfig, cfg.Workspace, cfg.Server)
	if err != nil {
		return err
	}
	return ApplyRoot(ctx, access, cfg, log)
}
