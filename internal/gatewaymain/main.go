package gatewaymain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bluesky-social/indigo/atproto/identity"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/devicekey"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/foldmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/plcresolver"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/plcsweep"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/sweepkcp"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tenantclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/xrpcserver"
)

const directoryCacheCapacity = 10_000

func Main(ctx context.Context, inv foldmain.Invocation) int {
	if err := run(ctx, inv); err != nil {
		fmt.Fprintln(inv.Stderr, "socialweb-gateway:", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, inv foldmain.Invocation) error {
	cfg, err := load(inv.Env)
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(inv.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	deps, err := buildDeps(cfg)
	if err != nil {
		return err
	}
	g, err := Build(cfg, deps, log)
	if err != nil {
		return err
	}

	log.Info("serving",
		"addr", cfg.Addr,
		"kcp", cfg.KCPHost,
		"audiences", cfg.Audiences,
		"xrpc", xrpcserver.XRPCPrefix,
		"mint", xrpcserver.MintPath,
	)
	return serve(ctx, cfg, g, log)
}

func buildDeps(cfg Config) (Deps, error) {
	dir, err := directory(cfg)
	if err != nil {
		return Deps{}, err
	}

	kcpRestConfig, err := clientcmd.BuildConfigFromFlags("", cfg.KCPKubeconfig)
	if err != nil {
		return Deps{}, fmt.Errorf("reading %s: %w", envKCPKubeconfig, err)
	}
	if fi, err := os.Stat(cfg.MintKubeconfigs); err != nil {
		return Deps{}, fmt.Errorf("%s: %w", envMintKubeconfigs, err)
	} else if !fi.IsDir() {
		return Deps{}, fmt.Errorf("%s: %s is not a directory", envMintKubeconfigs, cfg.MintKubeconfigs)
	}
	registryConfig := rest.CopyConfig(kcpRestConfig)
	registryConfig.Host = strings.TrimSuffix(cfg.KCPHost, "/")
	if len(cfg.Audiences) == 0 {
		return Deps{}, fmt.Errorf("%s is not set: without an audience there is nothing for a "+
			"device credential to be signed for", envAudiences)
	}
	mintStore, err := tenantclient.New(tenantclient.Options{
		Registry:          registryConfig,
		RegistryWorkspace: cfg.DevkeyRegistry,
		RegistryNamespace: DevkeyRegistryNamespace,
		Audience:          cfg.Audiences[0],
		MintConfig:        mintCredentials(cfg.MintKubeconfigs),
	})
	if err != nil {
		return Deps{}, fmt.Errorf("the mint store: %w", err)
	}

	if cfg.DeviceKeyAudience != cfg.Audiences[0] {
		return Deps{}, fmt.Errorf("%s is %q and %s is %q: the device credential is signed for "+
			"the first and the mint verifies it against the second, so every kubeconfig this "+
			"gateway handed out would mint nothing and be refused as Unauthorized",
			envDeviceKeyAudience, cfg.DeviceKeyAudience, envAudiences, cfg.Audiences[0])
	}
	deviceKeys, err := devicekey.New(devicekey.Options{
		Binder:      mintStore,
		Audience:    cfg.DeviceKeyAudience,
		TTL:         cfg.DeviceKeyTTL,
		ExecCommand: cfg.DeviceKeyExecCommand,
		ExecEnvName: cfg.DeviceKeyExecEnvName,
		MintURL:     cfg.PublicMintURL,
	})
	if err != nil {
		return Deps{}, fmt.Errorf("the device-key granter: %w", err)
	}

	client, err := kcpclient.New(kcpclient.Options{
		Host:       cfg.KCPHost,
		PublicHost: cfg.KCPPublicHost,
		RestConfig: kcpRestConfig,
		DeviceKeys: deviceKeys,
	})
	if err != nil {
		return Deps{}, err
	}

	worklist, err := sweepkcp.NewWorklist(sweepkcp.Options{Host: cfg.KCPHost, RestConfig: kcpRestConfig})
	if err != nil {
		return Deps{}, fmt.Errorf("the sweep's worklist: %w", err)
	}
	resolver, err := plcresolver.New(plcresolver.Options{
		URL:    cfg.PLCURL,
		Client: &http.Client{Timeout: plcTimeout},
	})
	if err != nil {
		return Deps{}, fmt.Errorf("the sweep's resolver: %w", err)
	}

	revoker := &sweepkcp.Revoker{Deny: client, FanOut: client, Erase: client}

	return Deps{
		Store:     client,
		Deny:      client,
		Directory: dir,

		Revoker: client,

		MintStore: mintStore,

		Sweep: &plcsweep.Sweeper{
			Worklist: worklist,
			Resolver: resolver,
			Revoker:  revoker,
		},
	}, nil
}

const plcTimeout = 10 * time.Second

func directory(cfg Config) (identity.Directory, error) {
	hardened := identity.DefaultDirectory()
	cached, ok := hardened.(*identity.CacheDirectory)
	if !ok {
		return nil, fmt.Errorf(
			"indigo's identity.DefaultDirectory() is a %T, not a *identity.CacheDirectory, so its "+
				"revalidation window cannot be set", hardened)
	}
	return identity.NewCacheDirectory(
		cached.Inner,
		directoryCacheCapacity,
		cfg.DirectoryCacheTTL,
		cfg.DirectoryCacheTTL,
		cfg.DirectoryCacheTTL,
	), nil
}

func serve(ctx context.Context, cfg Config, g *Gateway, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           g.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	var background sync.WaitGroup
	background.Add(1)
	go func() {
		defer background.Done()
		if err := g.Run(ctx); err != nil {
			log.Error("the background work stopped", "err", err)
		}
	}()

	served := make(chan error, 1)
	go func() { served <- server.ListenAndServe() }()

	var serveErr error
	select {
	case serveErr = <-served:
		stop()
	case <-ctx.Done():
		log.Info("signal received, draining")
	}

	drainCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(drainCtx); err != nil {
		return fmt.Errorf("draining after %s: %w", cfg.ShutdownTimeout, err)
	}
	background.Wait()

	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}
