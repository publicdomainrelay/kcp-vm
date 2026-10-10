package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"k8s.io/client-go/rest"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scaledriver"
)

type Deps struct {
	Registry clusterprovider.Clusters

	Decider *materialiser.Reconciler

	Host materialiser.Host

	NewInstance func() string

	Postgres *postgresRegistry

	PostgresNeeded bool

	Allocator *portallocator.Allocator

	NodeScale *scaledriver.Driver

	NodeHosts *nodeHosts

	Tenant func(ctx context.Context, ref clusterprovider.Ref, cl *v1alpha1.Cluster) (*rest.Config, error)

	InstallTenant func(ctx context.Context, cluster string, cfg *rest.Config) error
}

var ErrNoTenant = errors.New(
	"no tenant credential seam was supplied, so the bootstrap cannot reach a cluster it " +
		"provisions. A nil here means the composition root skipped that construction, not " +
		"that something is missing from the repo")

var ErrNoHost = errors.New(
	"a join was asked for and no process lifecycle was supplied, so the pass that writes the " +
		"status the cluster earned would be observe-only: the cluster would be serving and the " +
		"CR would still read \"\"")

type Provider struct {
	cfg      Config
	provider *clusterprovider.Provider
	alloc    *portallocator.Allocator
	registry clusterprovider.Clusters

	postgres *postgresRegistry

	postgresNeeded bool

	nodeScale *scaledriver.Driver

	nodes *nodeHosts

	host materialiser.Host

	newInstance func() string
	log         *slog.Logger
}

const ShutdownTimeout = 60 * time.Second

type clusterLister interface{ Clusters() []string }

func (p *Provider) Shutdown() error {
	if p.cfg.ObserveOnly() || p.host == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	p.log.Info("cluster provider: shutting down; the process group is reaped in reverse order "+
		"rather than killed with this process", "join", p.cfg.Join, "timeout", ShutdownTimeout)
	held := []string{p.cfg.Join}
	if lister, ok := p.host.(clusterLister); ok {
		if live := lister.Clusters(); len(live) > 0 {
			held = live
		}
	}
	for _, cluster := range held {
		if cluster == "" {
			continue
		}
		if err := p.host.Reap(ctx, cluster); err != nil {
			return fmt.Errorf("clusterprovider: reaping %s on shutdown: %w", cluster, err)
		}
	}
	if err := p.reapNodeHosts(ctx); err != nil {
		return err
	}
	if p.postgres == nil {
		return nil
	}
	p.log.Info("cluster provider: stopping the store; it is a library child and not a Step, so "+
		"nothing in the group's reap reaches it", "cluster", p.cfg.Join)
	return p.postgres.stopAll(ctx)
}

func (p *Provider) reapNodeHosts(ctx context.Context) error {
	if p.nodes == nil {
		p.log.Info("cluster provider: no node hosts to reap on shutdown",
			"cluster", p.cfg.Join,
			"reason", "this gateway built none: either no node-scale driver was attached or no "+
				"pass ever provisioned a node, so there is no agent of ours for a restart to find")
		return nil
	}
	clusters := p.nodes.clusters()
	p.log.Info("cluster provider: reaping the node hosts, after the group",
		"cluster", p.cfg.Join, "nodeClusters", clusters,
		"reason", "a node's guest is not a Step of the group, so nothing in the group's reap "+
			"reaches it: left running it keeps naming its node at the cluster's apiserver, and "+
			"the record it leaves makes the next gateway's Provision answer ErrAlreadyRunning, "+
			"so that cluster can never provision again")
	if err := p.nodes.reap(ctx); err != nil {
		return fmt.Errorf("clusterprovider: reaping the node hosts on shutdown: %w", err)
	}
	return nil
}

func Build(cfg Config, d Deps, log *slog.Logger) (*Provider, error) {
	if d.Registry == nil {
		return nil, errors.New("clusterprovider: Deps.Registry is required")
	}
	if d.Decider == nil {
		return nil, clusterprovider.ErrNoDecider
	}
	if d.Allocator == nil {
		return nil, errors.New("clusterprovider: Deps.Allocator is required; the lease file is the authority on which cluster holds which port")
	}
	if !cfg.ObserveOnly() {
		if d.Tenant == nil {
			return nil, ErrNoTenant
		}
		if d.Host == nil {
			return nil, ErrNoHost
		}
		if d.NewInstance == nil {
			return nil, errors.New("clusterprovider: Deps.NewInstance is required; status.instance is the only durable discriminator there is")
		}
	}
	if log == nil {
		log = slog.Default()
	}

	opts := clusterprovider.Options{
		Registry: d.Registry,
		Decider:  d.Decider,

		Host: d.Host,

		WriteStatus: !cfg.ObserveOnly(),

		Ports: d.Allocator,

		NodeExpirySource: clusterprovider.NodeExpiryRollup,

		TokenTimeout: cfg.TokenTimeout,
		Interval:     cfg.Interval,
		Log:          log,
	}
	if !cfg.ObserveOnly() {
		opts.Tenant, opts.GrantPath, opts.MintDir = d.Tenant, cfg.GrantPath, cfg.MintKubeconfigs
		opts.InstallTenant = d.InstallTenant
	}
	provider, err := clusterprovider.New(opts)
	if err != nil {
		return nil, err
	}

	p := &Provider{
		cfg:            cfg,
		provider:       provider,
		alloc:          d.Allocator,
		registry:       d.Registry,
		postgres:       d.Postgres,
		postgresNeeded: d.PostgresNeeded,
		nodeScale:      d.NodeScale,
		nodes:          d.NodeHosts,
		host:           d.Host,
		newInstance:    d.NewInstance,
		log:            log,
	}
	p.report()
	return p, nil
}

func (p *Provider) Run(ctx context.Context) error {
	if p.cfg.ObserveOnly() {
		p.log.Info("cluster provider: observing only", "join", "unset: no process lifecycle is attached, so no pass may act or write")
		return p.provider.Run(ctx)
	}
	if p.cfg.AllClusters() {
		return p.joinAll(ctx)
	}
	return p.join(ctx)
}

func (p *Provider) join(ctx context.Context) error {
	ref, _, err := p.lookup(ctx, p.cfg.Join)
	if err != nil {
		return err
	}
	key, err := ref.Key()
	if err != nil {
		return err
	}
	dsn, err := p.store(ctx, key)
	if err != nil {
		return err
	}
	p.log.Info("cluster provider: joining", "cluster", ref.Name, "key", key, "home", ref.Home,
		"datastore", dsn)

	if err := p.resetSnapshots([]string{key}); err != nil {
		return err
	}
	if err := p.sweepNodeRecords(ctx); err != nil {
		return err
	}

	if p.nodeScale == nil {
		p.log.Warn("cluster provider: no node-scale driver; the cluster will not scale its nodes")
	}
	tick := time.NewTicker(p.cfg.Interval)
	defer tick.Stop()
	for {
		p.pass(ctx, ref, key)
		p.scale(ctx, key, ref.Name, true)
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func (p *Provider) resetSnapshots(keys []string) error {
	if p.nodes == nil {
		return nil
	}
	for _, key := range keys {
		if err := p.nodes.resetSnapshot(key); err != nil {
			return fmt.Errorf("clusterprovider: writing the empty instance snapshot for %s before "+
				"any node exists: %w. The cloud controller manager reads that file to decide which "+
				"Nodes still have a guest; the provider always writes the file rather than leaving "+
				"its absence to mean something", key, err)
		}
	}
	return nil
}

func (p *Provider) joinAll(ctx context.Context) error {
	p.log.Info("cluster provider: joining every Cluster in the tree", "join", p.cfg.Join,
		"passes", "serial, one cluster at a time",
		"reason", "a failure in one cluster is logged and the pass moves on, so one cluster that "+
			"cannot come up never holds back the others")
	refs, err := p.registry.Refs(ctx)
	if err != nil {
		return fmt.Errorf("clusterprovider: listing the Cluster CRs before the first all-clusters "+
			"pass: %w", err)
	}
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		key, err := ref.Key()
		if err != nil {
			return err
		}
		keys = append(keys, key)
	}
	if err := p.resetSnapshots(keys); err != nil {
		return err
	}
	if err := p.sweepNodeRecords(ctx); err != nil {
		return err
	}
	if p.nodeScale == nil {
		p.log.Warn("cluster provider: no node-scale driver; no cluster will scale its nodes")
	}
	tick := time.NewTicker(p.cfg.Interval)
	defer tick.Stop()
	for {
		p.passAll(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func (p *Provider) passAll(ctx context.Context) {
	refs, err := p.registry.Refs(ctx)
	if err != nil {
		p.log.Error("cluster provider: listing the Cluster CRs in the tree failed; no cluster was "+
			"touched this pass", "error", err.Error())
		return
	}
	p.provider.Reclaim(refs)
	for _, ref := range refs {
		if err := p.one(ctx, ref); err != nil {
			p.log.Error("cluster provider: this cluster's pass failed and the others continue",
				"cluster", ref.Name, "home", ref.Home, "error", err.Error())
		}
	}
}

func (p *Provider) one(ctx context.Context, ref clusterprovider.Ref) error {
	key, err := ref.Key()
	if err != nil {
		return err
	}
	cl, err := p.registry.Read(ctx, ref)
	if err != nil {
		return fmt.Errorf("clusterprovider: reading the Cluster CR of %s out of %s: %w",
			ref.Name, ref.Home, err)
	}
	if !cl.Spec.Running || cl.DeletionTimestamp != nil {
		p.log.Info("cluster provider: this cluster is not running, so its group is reaped and "+
			"nothing is brought up for it", "cluster", ref.Name, "key", key, "home", ref.Home,
			"running", cl.Spec.Running, "deleting", cl.DeletionTimestamp != nil)
		return p.reapOne(ctx, key)
	}
	if _, err := p.store(ctx, key); err != nil {
		return err
	}
	p.pass(ctx, ref, key)
	p.scale(ctx, key, ref.Name, true)
	return nil
}

func (p *Provider) reapOne(ctx context.Context, key string) error {
	if err := p.host.Reap(ctx, key); err != nil {
		return err
	}
	if p.nodes == nil {
		return nil
	}
	return p.nodes.reapCluster(ctx, key)
}

func (p *Provider) scale(ctx context.Context, key, name string, running bool) {
	if p.nodeScale == nil || !running {
		return
	}
	if p.nodes != nil {
		if err := p.nodes.reconcile(ctx, key); err != nil {
			p.log.Warn("cluster provider: reconciling the Nodes whose machines the door has taken "+
				"away did not complete", "cluster", name, "key", key, "error", err.Error())
		}
		if err := p.nodes.confirm(ctx, key); err != nil {
			p.log.Warn("cluster provider: confirming the nodes this cluster already holds did not "+
				"complete", "cluster", name, "key", key, "error", err.Error())
		}
	}
	dec, err := p.nodeScale.RunPass(ctx, key)
	if err != nil {
		p.log.Warn("cluster provider: the node-scale pass did not complete",
			"cluster", name, "key", key, "error", err.Error())
		return
	}
	p.log.Info("cluster provider: node scale", "cluster", name, "key", key,
		"action", dec.Action.String(), "nodes", dec.Nodes, "reason", dec.Reason)
}

func (p *Provider) pass(ctx context.Context, ref clusterprovider.Ref, key string) {
	pass, err := p.provider.Reconcile(ctx, ref)
	if pass.Result.Phase != "" {
		args := []any{
			"cluster", ref.Name, "key", key, "home", ref.Home,
			"phase", pass.Result.Phase, "url", pass.Result.URL, "instance", pass.Result.Instance,
			"wrote", pass.Wrote, "alert", pass.Alert(),
		}
		if mints := p.provider.MintClusters(); len(mints) > 0 {
			args = append(args, "mintCredential", p.cfg.MintKubeconfigs+"/"+key, "mints", mints)
		}
		p.log.Info("cluster provider: status", args...)
		if pass.Result.Alert != "" {
			p.log.Warn("cluster provider: the materialiser reported an alert",
				"cluster", ref.Name, "key", key, "alert", pass.Result.Alert)
		}
	}
	if err != nil {
		p.log.Error("cluster provider: the pass failed", "cluster", ref.Name, "key", key,
			"error", err.Error())
	}
}

func (p *Provider) store(ctx context.Context, cluster string) (string, error) {
	return openStore(ctx, p.cfg, p.alloc, p.postgres, cluster, p.log, p.postgresNeeded)
}

func (p *Provider) lookup(ctx context.Context, name string) (clusterprovider.Ref, *v1alpha1.Cluster, error) {
	refs, err := p.registry.Refs(ctx)
	if err != nil {
		return clusterprovider.Ref{}, nil, err
	}
	var named []clusterprovider.Ref
	for _, ref := range refs {
		if ref.Name == name {
			named = append(named, ref)
		}
	}
	switch len(named) {
	case 1:
	case 0:
		found := make([]string, 0, len(refs))
		for _, ref := range refs {
			found = append(found, ref.Home+"/"+ref.Name)
		}
		return clusterprovider.Ref{}, nil, fmt.Errorf(
			"clusterprovider: no Cluster %q in the tree this credential can see; it holds %d: %v",
			name, len(found), found)
	default:
		homes := make([]string, 0, len(named))
		for _, ref := range named {
			homes = append(homes, ref.Home)
		}
		return clusterprovider.Ref{}, nil, fmt.Errorf(
			"clusterprovider: %d workspaces in this tree hold a Cluster named %q (%v), and %s names "+
				"one cluster. A name is unique only inside its home workspace, so this join is "+
				"refused rather than resolved to whichever home was listed first: the two clusters "+
				"have different state dirs, different ports and different node names, and picking "+
				"one silently would provision the wrong cluster. Set %s to %s to reconcile every "+
				"cluster in the tree instead",
			len(named), name, homes, envJoin, envJoin, JoinAllClusters)
	}
	ref := named[0]
	cl, err := p.registry.Read(ctx, ref)
	if err != nil {
		return clusterprovider.Ref{}, nil, err
	}
	key, err := ref.Key()
	if err != nil {
		return clusterprovider.Ref{}, nil, err
	}
	p.log.Info("cluster provider: joining", "cluster", ref.Name, "key", key, "home", ref.Home,
		"did", ref.DID, "account", ref.Account,
		"running", cl.Spec.Running, "phase", cl.Status.Phase, "url", cl.Status.URL)
	return ref, cl, nil
}

func (p *Provider) report() {
	if p.cfg.ObserveOnly() {
		p.log.Info("cluster provider: report",
			"mode", "observe",
			"decides", "yes -- the phase machine is materialiser's",
			"acts", "no -- Options.Host is nil, so no pass may spawn, reap or write",
			"bootstraps", "not in this mode: the bootstrap is reached through a WRITE pass, and observe-only returns before it",
			"consequence", "the tenant grant is not applied and no mint credential is obtained while this mode is set")
		return
	}
	mode := "join"
	scope := p.cfg.Join
	status := "on, for THIS cluster only: the join holds its VM, so the pass may write"
	if p.cfg.AllClusters() {
		mode = "join-all"
		scope = "every Cluster in the tree"
		status = "on, for EVERY cluster this pass brings up: each one holds its own VM"
	}
	p.log.Info("cluster provider: report",
		"mode", mode,
		"join", scope,
		"key", "(derived: <accountHash>-<name>, from the cluster's home workspace)",
		"instance", "(derived: pdr-<key>)",
		"datastore", p.storePlan(),
		"datastoreHasRow", p.cfg.DatastoreHasRow,
		"mintDir", p.cfg.MintKubeconfigs,
		"statusWrite", status,
		"bootstrap", "runs on each cluster's own transition into Ready, once")
}

func (p *Provider) storePlan() string {
	if p.cfg.Datastore != "" {
		return p.cfg.Datastore + " (supplied by the operator)"
	}
	return "per-cluster postgres on this host, launched by this join, data under " + p.cfg.StateDir
}
