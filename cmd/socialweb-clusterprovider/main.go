package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scaledriver"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/subnetallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultcert"
)

func vaultTLSFor() func(string, ...string) (clusterprovider.VaultTLS, error) {
	return func(dataDir string, hosts ...string) (clusterprovider.VaultTLS, error) {
		tls, err := vaultcert.EnsureVaultTLSCertificate(dataDir, hosts...)
		if err != nil {
			return clusterprovider.VaultTLS{}, err
		}
		return clusterprovider.VaultTLS{CertFile: tls.CertFile, KeyFile: tls.KeyFile, CAPEM: tls.CAPEM}, nil
	}
}

const nodeID = "node1"

func subnetLeasePath(stateDir string) string {
	return filepath.Join(stateDir, "networks.json")
}

func guestGateway(networks *subnetallocator.Allocator) func(cluster string) (string, error) {
	if hostos.NetworkMode() == hostos.NetworkUser {
		return func(string) (string, error) { return hostos.UserGateway, nil }
	}
	return func(cluster string) (string, error) {
		lease, err := networks.Allocate(subnetallocator.Request{Cluster: cluster})
		if err != nil {
			return "", err
		}
		return lease.Gateway().String(), nil
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "socialweb-clusterprovider:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d, err := buildDeps(ctx, cfg, log)
	if err != nil {
		return err
	}
	p, err := Build(cfg, d, log)
	if err != nil {
		return err
	}

	runErr := p.Run(ctx)
	if err := p.Shutdown(); err != nil {
		log.Error("cluster provider: the shutdown reap did not complete", "error", err.Error())
	}
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) {
			return nil
		}
		return runErr
	}
	return nil
}

func buildDeps(ctx context.Context, cfg Config, log *slog.Logger) (Deps, error) {
	restCfg, err := clientcmd.BuildConfigFromFlags("", cfg.KCPKubeconfig)
	if err != nil {
		return Deps{}, fmt.Errorf("reading %s: %w", cfg.KCPKubeconfig, err)
	}
	registry, err := clusterprovider.NewRegistry(clusterprovider.RegistryOptions{
		Host:       cfg.KCPHost,
		RestConfig: restCfg,
	})
	if err != nil {
		return Deps{}, err
	}

	alloc, err := portallocator.Open(portallocator.Config{
		LeasePath:    cfg.LeasePath,
		Range:        cfg.PortRange,
		ReclaimGrace: DefaultReclaimGrace,
	})
	if err != nil {
		return Deps{}, fmt.Errorf("opening the lease file %s: %w", cfg.LeasePath, err)
	}

	networks, err := subnetallocator.Open(subnetallocator.Config{
		LeasePath: subnetLeasePath(cfg.StateDir),
	})
	if err != nil {
		return Deps{}, fmt.Errorf("opening the subnet lease file %s: %w. A cluster's range is "+
			"leased for as long as the cluster is, and its first usable address is the gateway its "+
			"guests dial the apiserver through, so a gateway with no subnet lease has no range to "+
			"address a node in and no gateway to advertise; it refuses to start rather than "+
			"falling back to an address of its own, which is how a node's InternalIP became the "+
			"address of the machine running the gateway", subnetLeasePath(cfg.StateDir), err)
	}

	postgres := newPostgresRegistry()

	tokens := newTokenStore(cfg.StateDir, log)

	deciderHost := materialiser.NewLocalHost(procgroup.Options{})
	deciderHost.PostgresUp = postgres.up

	stack := clusterprovider.DesignedStack
	needsPostgres := stack.NeedsLocalPostgres(cfg.StoreEndpoint == "")
	purposes, err := clusterprovider.PurposesFor(stack, needsPostgres)
	if err != nil {
		return Deps{}, err
	}

	substrate, err := nodeSubstrateFor(cfg)
	if err != nil {
		return Deps{}, err
	}

	minter := instanceMinter()
	stackCfg := clusterprovider.StackConfig{
		Stack:            stack,
		Admin:            adminForCluster(cfg.StateDir),
		StateDir:         cfg.StateDir,
		StoreEndpoint:    storeEndpoint(cfg, postgres),
		VaultStoreDSN:    postgres.vaultDSN,
		AdvertiseAddress: cfg.AdvertiseAddress,
		GuestDialled:     substrate.IsGuest(),
		BindAddress:      portallocator.LoopbackAdvertiseAddress,
		VaultTLS:         vaultTLSFor(),
		AuditPolicyPath:  cfg.AuditPolicyPath,
		Harvest: func(ctx context.Context, cluster string) error {
			if err := clusterprovider.HarvestBundle(cfg.StateDir, cluster); err != nil {
				return err
			}
			port := 0
			if lease, ok := alloc.PortFor(cluster, portallocator.PurposeOpenBao); ok {
				port = lease.Port
			}
			dsn := ""
			if pg, ok := postgres.get(cluster); ok {
				dsn = pg.DSN()
			}
			return clusterprovider.CustodyClusterVault(ctx, clusterprovider.CustodyOptions{
				StateDir:    cfg.StateDir,
				Cluster:     cluster,
				VaultPort:   port,
				PostgresDSN: dsn,
			})
		},
		GuardRides: true,
	}
	if substrate.IsGuest() {
		stackCfg.GuestGateway = guestGateway(networks)
	}
	decider, err := materialiser.New(materialiser.Options{
		Host:        deciderHost,
		Ports:       alloc,
		Networks:    networks,
		Steps:       clusterprovider.StepsFor(stackCfg),
		Identity:    clusterprovider.GuardFor(guardDeps(cfg, tokens, postgres, log)),
		NewInstance: minter,
		Now:         time.Now,
		Purposes:    purposes,
	})
	if err != nil {
		return Deps{}, err
	}

	var host materialiser.Host = deciderHost
	if !cfg.ObserveOnly() {
		host, err = Startup(ctx, StartupOptions{
			Allocator: alloc,
			Registry:  registry,
			StateDir:  cfg.StateDir,
			Stack:     stackCfg,
			Host:      deciderHost,
			Decider:   decider,
			EnsureStore: func(ctx context.Context, cluster string) error {
				_, err := openStore(ctx, cfg, alloc, postgres, cluster, log, needsPostgres)
				return err
			},
			Log: log,
		})
		if err != nil {
			return Deps{}, err
		}
	}

	tenantConfig := tenantFromClusterCA(alloc, cfg.StateDir)
	tenant := tenantClients(registry, tenantConfig)
	nodeObserver := clusterprovider.NodeObserver{Client: tenant}
	nodeHosts, err := newNodeHosts(nodeHostsOptions{
		Allocator:        alloc,
		Networks:         networks,
		Registry:         registry,
		StateDir:         cfg.StateDir,
		AdvertiseAddress: cfg.AdvertiseAddress,
		Tenant:           tenant,
		Log:              log,
		Substrate:        substrate,
	})
	if err != nil {
		return Deps{}, err
	}
	scaleDriver, err := scaledriver.New(nodeHosts.forCluster, scaledriver.Options{
		Observe: nodeObserver.Observe,
		Scale: scale.Options{
			Hold:        cfg.ScaleHold,
			MaxNodes:    cfg.ScaleMaxNodes,
			PodsPerNode: cfg.ScalePodsPerNode,
		},
		Log: log,
	})
	if err != nil {
		return Deps{}, err
	}

	deps := Deps{
		Registry:    registry,
		Decider:     decider,
		Allocator:   alloc,
		Tenant:      tenantConfig,
		Postgres:    postgres,
		NewInstance: minter,
		NodeScale:   scaleDriver,

		PostgresNeeded: needsPostgres,

		InstallTenant: tenantAddOns(cfg, networks, log),
	}
	if !cfg.ObserveOnly() {
		deps.Host = host
		deps.NodeHosts = nodeHosts
	}
	return deps, nil
}

func storeEndpoint(cfg Config, postgres *postgresRegistry) func(cluster string) (string, error) {
	if cfg.StoreEndpoint != "" {
		return func(string) (string, error) { return cfg.StoreEndpoint, nil }
	}
	return postgres.dsn
}

func openStore(ctx context.Context, cfg Config, alloc *portallocator.Allocator, postgres *postgresRegistry, cluster string, log *slog.Logger, needsPostgres bool) (string, error) {
	if !needsPostgres {
		log.Info("cluster provider: store supplied by the operator",
			"cluster", cluster, "store", cfg.StoreEndpoint,
			"durable", "wherever that endpoint lives, which is not this host's data dir")
		return cfg.StoreEndpoint, nil
	}
	pg, ok := postgres.get(cluster)
	if !ok {
		name, err := clusterprovider.InstanceName(cluster)
		if err != nil {
			return "", err
		}
		lease, err := alloc.Allocate(portallocator.Request{
			Cluster: cluster,
			Purpose: portallocator.PurposePostgres,
		})
		if err != nil {
			return "", fmt.Errorf("clusterprovider: allocating the postgres port for %s: %w", cluster, err)
		}
		pg, err = clusterprovider.StartPostgres(ctx, clusterprovider.PostgresSpec{
			Cluster:  cluster,
			StateDir: filepath.Join(cfg.StateDir, name),
			Port:     lease.Port,
			BinDir:   cfg.PostgresBinDir,
			Log:      log,
		})
		if err != nil {
			return "", fmt.Errorf(
				"clusterprovider: refusing to join %s without its per-cluster postgres. The store "+
					"IS the cluster -- kine writes every object of this tenant cluster into it, and "+
					"k3s dials kine rather than the store -- and it is ALSO the vault's store, so "+
					"without it no vault starts and no identity bundle is custodied. The store the "+
					"vault needs is this cluster's own embedded-postgres: %w", cluster, err)
		}
		log.Info("cluster provider: store", "cluster", cluster, "port", lease.Port,
			"dataDir", pg.DataDir(), "durable", "on this host, so the cluster can be destroyed and re-provisioned")
		postgres.set(cluster, pg)
	}
	if cfg.StoreEndpoint != "" {
		log.Info("cluster provider: the kine store is supplied by the operator; the per-cluster "+
			"postgres runs for the vault alone", "cluster", cluster, "store", cfg.StoreEndpoint,
			"vaultStore", "this cluster's own embedded-postgres")
		return cfg.StoreEndpoint, nil
	}
	return pg.DSN(), nil
}

func tenantClients(registry clusterprovider.Clusters, mint func(context.Context, clusterprovider.Ref, *v1alpha1.Cluster) (*rest.Config, error)) func(context.Context, string) (kubernetes.Interface, error) {
	return func(ctx context.Context, cluster string) (kubernetes.Interface, error) {
		ref, cl, err := tenantCluster(ctx, registry, cluster)
		if err != nil {
			return nil, err
		}
		cfg, err := mint(ctx, ref, cl)
		if err != nil {
			return nil, err
		}
		return kubernetes.NewForConfig(cfg)
	}
}

func tenantCluster(ctx context.Context, reg clusterprovider.Clusters, cluster string) (clusterprovider.Ref, *v1alpha1.Cluster, error) {
	refs, err := reg.Refs(ctx)
	if err != nil {
		return clusterprovider.Ref{}, nil, err
	}
	for _, r := range refs {
		if r.Name != cluster {
			continue
		}
		cl, err := reg.Read(ctx, r)
		if err != nil {
			return clusterprovider.Ref{}, nil, err
		}
		return r, cl, nil
	}
	return clusterprovider.Ref{}, nil, fmt.Errorf("clusterprovider: no Cluster CR named %s", cluster)
}

func guardDeps(cfg Config, tokens *tokenStore, postgres *postgresRegistry, log *slog.Logger) clusterprovider.GuardDeps {
	return clusterprovider.GuardDeps{
		StateDir: cfg.StateDir,
		Token:    tokens.token,
		HasRow:   bootstrapRowObserved(cfg, postgres, log),
		DeleteRow: func(ctx context.Context, cluster string) error {
			pg, err := postgresHandle(postgres, cluster)
			if err != nil {
				return err
			}
			return pg.DeleteBootstrapRows(ctx)
		},
		SeedBundle: func(ctx context.Context, cluster string) error {
			return clusterprovider.SeedBundleForWake(ctx, cfg.StateDir, cluster)
		},
		Bundle: clusterprovider.BundleGuard(cfg.StateDir),
		Log:    log,
	}
}

func postgresHandle(postgres *postgresRegistry, cluster string) (*clusterprovider.Postgres, error) {
	pg, ok := postgres.get(cluster)
	if !ok {
		return nil, fmt.Errorf(
			"clusterprovider: this cluster's store is a per-cluster postgres that THIS process "+
				"started, and there is no handle for %s, so k3s's bootstrap row can be neither "+
				"read nor deleted. A wrong answer here is fatal rather than degraded: k3s exits at "+
				"boot with \"bootstrap data already found and encrypted with different token\"", cluster)
	}
	return pg, nil
}

func bootstrapRowObserved(cfg Config, postgres *postgresRegistry, log *slog.Logger) func(cluster string) bool {
	return func(cluster string) bool {
		if cfg.StoreEndpoint != "" {
			log.Warn("cluster provider: the store is external, so whether it holds k3s's bootstrap "+
				"row cannot be observed from here; falling back to the operator's declaration",
				"cluster", cluster, "datastoreHasRow", cfg.DatastoreHasRow)
			return cfg.DatastoreHasRow
		}
		pg, err := postgresHandle(postgres, cluster)
		if err != nil {
			log.Error("cluster provider: no store handle, so the bootstrap row cannot be observed",
				"cluster", cluster, "error", err.Error())
			return cfg.DatastoreHasRow
		}
		has, err := pg.HasBootstrapRow(context.Background())
		if err != nil {
			log.Error("cluster provider: reading the bootstrap row failed, so the operator's "+
				"declaration is used instead", "cluster", cluster, "error", err.Error(),
				"datastoreHasRow", cfg.DatastoreHasRow)
			return cfg.DatastoreHasRow
		}
		log.Info("cluster provider: the store was read for k3s's bootstrap row",
			"cluster", cluster, "hasBootstrapRow", has, "declared", cfg.DatastoreHasRow)
		return has
	}
}

func adminForCluster(stateDir string) clusterprovider.AdminClient {
	return func(_ context.Context, cluster, apiserver string) (kubernetes.Interface, error) {
		dataDir, err := clusterprovider.ClusterDataDir(stateDir, cluster)
		if err != nil {
			return nil, err
		}
		path := clusterstack.AdminKubeconfig(dataDir)
		cfg, err := clientcmd.BuildConfigFromFlags("", path)
		if err != nil {
			return nil, fmt.Errorf(
				"clusterprovider: the cluster's own admin credential at %s (k3s writes it there "+
					"because ServerArgv passes --write-kubeconfig): %w. Without it the PSA labels "+
					"and automountServiceAccountToken=false cannot be written or read back", path, err)
		}
		if apiserver != "" {
			cfg.Host = apiserver
		}
		return kubernetes.NewForConfig(cfg)
	}
}

func instanceMinter() func() string {
	start := time.Now().UTC().Format("20060102T150405Z")
	var n atomic.Int64
	return func() string {
		return fmt.Sprintf("gen-%s-%d", start, n.Add(1))
	}
}

func tenantFromClusterCA(alloc *portallocator.Allocator, stateDir string) func(context.Context, clusterprovider.Ref, *v1alpha1.Cluster) (*rest.Config, error) {
	return func(ctx context.Context, _ clusterprovider.Ref, cl *v1alpha1.Cluster) (*rest.Config, error) {
		lease, err := alloc.Allocate(portallocator.Request{
			Cluster: cl.Name, Purpose: portallocator.PurposeK3sSecure,
		})
		if err != nil {
			return nil, fmt.Errorf("clusterprovider: the apiserver lease for %s, which is the "+
				"address this cluster's apiserver answers on: %w", cl.Name, err)
		}
		return clusterprovider.MintTenantCredentialForCluster(ctx, stateDir, cl.Name, clusterprovider.APIServerURL(lease.Port))
	}
}
