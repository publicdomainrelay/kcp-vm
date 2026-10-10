package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portscan"
)

type StartupOptions struct {
	Allocator *portallocator.Allocator

	Registry clusterprovider.Clusters

	StateDir string

	Stack clusterprovider.StackConfig

	Host *materialiser.LocalHost

	Decider *materialiser.Reconciler

	EnsureStore func(ctx context.Context, cluster string) error

	Scan func([]portallocator.Lease, []portscan.Stack) (portscan.Survivors, []portscan.Drop, error)

	Log *slog.Logger
}

func Startup(ctx context.Context, opts StartupOptions) (materialiser.Host, error) {
	if err := opts.check(); err != nil {
		return nil, err
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	scan := opts.Scan
	if scan == nil {
		scan = portscan.Scan
	}

	refs, err := opts.Registry.Refs(ctx)
	if err != nil {
		return nil, fmt.Errorf(
			"clusterprovider: listing the Cluster CRs before the restart reap: %w. A list that "+
				"failed is not an empty tree, and an empty tree reads as permission to reap and "+
				"spawn over every process group this host holds", err)
	}

	clusters := map[string]*v1alpha1.Cluster{}
	for _, ref := range refs {
		cl, err := opts.Registry.Read(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf(
				"clusterprovider: reading the Cluster %s before the restart reap: %w. Whether "+
					"this CR is running, deleting or halted decides whether this pass brings its "+
					"group up or reaps it, and guessing that from a partial read is how a group "+
					"comes up twice", ref.Name, err)
		}
		key, err := ref.Key()
		if err != nil {
			return nil, err
		}
		clusters[key] = cl
	}

	leases := opts.Allocator.Leases()
	byCluster := map[string]materialiser.Leases{}
	for _, l := range leases {
		held, ok := byCluster[l.Cluster]
		if !ok {
			held = materialiser.Leases{}
			byCluster[l.Cluster] = held
		}
		if _, ok := held[l.Purpose]; !ok {
			held[l.Purpose] = l
		}
	}

	names := make([]string, 0, len(clusters)+len(byCluster))
	for name := range clusters {
		names = append(names, name)
	}
	for name := range byCluster {
		if _, ok := clusters[name]; !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)

	owned := map[string]bool{}
	stacks := make([]portscan.Stack, 0, len(names))
	for _, name := range names {
		held, leased := byCluster[name]
		owned[name] = owns(clusters[name])
		if !leased && !owned[name] {
			continue
		}
		if err := opts.EnsureStore(ctx, name); err != nil {
			return nil, fmt.Errorf(
				"clusterprovider: adopting the store of %s before the restart reap derives "+
					"its argv: %w. The store is a library child and no Step of the group, so "+
					"nothing reaps it and nothing restarts it: without it the argv this pass "+
					"derives carries no DSN, and a pass that refuses here is a deployment that "+
					"cannot survive a restart", name, err)
		}
		if !leased {
			continue
		}
		argvs, err := spawnArgvs(opts.Stack, name, held)
		if err != nil {
			return nil, fmt.Errorf(
				"clusterprovider: refusing to restart over %s: its lease file entry is the "+
					"authority on which of this host's ports it holds, and the argv the running "+
					"path spawns it with cannot be derived (%w). Every process bound to those "+
					"ports is then unattributable: this pass could not tell this cluster's own "+
					"k3s from a stranger bound to the same port, could not reap either, and would "+
					"spawn a second process group over an apiserver that may still be writing to "+
					"the cluster's store", name, err)
		}
		stacks = append(stacks, portscan.Stack{Cluster: name, Argvs: argvs})
	}

	if len(leases) == 0 {
		log.Info("cluster provider: restart reap", "leases", 0,
			"decision", "nothing is leased on this host, so no process group of ours can be holding a leased port")
		return materialiser.NewOrphans(opts.Host, nil), nil
	}

	survivors, dropped, err := scan(leases, stacks)
	if err != nil {
		return nil, fmt.Errorf(
			"clusterprovider: scanning the leased ports before the restart reap: %w. Without "+
				"this scan the process groups a previous gateway left running are invisible to "+
				"this one, and the first pass would spawn a second group over them", err)
	}

	var refusals []error
	for _, d := range dropped {
		purpose, ok := purposeOf(byCluster[d.Cluster], d.Port)
		if !ok {
			refusals = append(refusals, fmt.Errorf(
				"clusterprovider: %s holds port %d, which no lease of that cluster covers, so "+
					"there is no purpose to weigh it against", d.Cluster, d.Port))
			continue
		}
		if !theGroupBinds(opts.Stack.Stack, purpose) {
			log.Info("cluster provider: a leased port is held by a process this group does not own",
				"cluster", d.Cluster, "port", d.Port, "purpose", purpose, "pid", d.PID,
				"decision", "left alone: this purpose outlives the process group, so its holder is adopted by the component that owns it rather than reaped here",
				"argv", d.Argv, "reason", d.Reason)
			continue
		}
		refusals = append(refusals, fmt.Errorf(
			"clusterprovider: %s is leased port %d for %s and it is held by pid %d with command "+
				"line %v, which is none of the components that stack spawns (%s). Nothing here "+
				"may kill it, because a process that is not ours must never become a kill "+
				"target, and nothing here may spawn over it: the group about to come up binds "+
				"that port, and two owners of one per-cluster port is the collision that routes "+
				"a node at another cluster's apiserver. Reap it by hand or remove the lease",
			d.Cluster, d.Port, purpose, d.PID, d.Argv, d.Reason))
	}
	if err := errors.Join(refusals...); err != nil {
		return nil, err
	}

	mine := portscan.Survivors{}
	for _, name := range names {
		handles := survivors[name]
		if !owned[name] {
			if len(handles) == 0 {
				continue
			}
			killed, err := materialiser.ReapSurvivors(ctx, opts.Host, name)
			if err != nil {
				return nil, fmt.Errorf(
					"clusterprovider: reaping the survivors of %s, which this pass does not bring "+
						"up (%s): %w. Leaving them is how an apiserver outlives the only record "+
						"that says it is ours", name, whyUnowned(clusters[name]), err)
			}
			log.Warn("cluster provider: reaped a process group no running CR owns",
				"cluster", name, "pids", killed, "state", whyUnowned(clusters[name]),
				"dataDir", dataDirOf(opts.StateDir, name),
				"decision", "reaped, never adopted: adopting it would make this process a second owner of the store it serves")
			continue
		}
		if len(handles) == 0 {
			log.Info("cluster provider: restart reap", "cluster", name, "survivors", 0,
				"decision", "nothing of this cluster is running, so the bring-up starts from nothing")
			continue
		}
		log.Warn("cluster provider: a previous gateway left this cluster's group running",
			"cluster", name, "pids", handles,
			"decision", "reaped by the bring-up before anything is spawned for it")
		mine[name] = handles
	}

	orphans := materialiser.NewOrphans(opts.Host, mine)

	now := time.Now()
	observed := make([]materialiser.Observed, 0, len(owned))
	for _, name := range names {
		if !owned[name] {
			continue
		}
		view := clusters[name].DeepCopy()
		view.Name = name
		observed = append(observed, materialiser.Observed{Cluster: *view, Now: now})
	}
	results, err := opts.Decider.Startup(ctx, observed)
	if err != nil {
		return nil, fmt.Errorf(
			"clusterprovider: the restart bring-up: %w. The reap has happened and the groups it "+
				"reaped are gone; what did not happen is the spawn behind them", err)
	}
	for i, res := range results {
		if i >= len(observed) {
			break
		}
		log.Info("cluster provider: restart bring-up", "cluster", clusters[observed[i].Cluster.Name].Name,
			"key", observed[i].Cluster.Name,
			"phase", res.Phase, "url", res.URL, "instance", res.Instance,
			"ops", opsOf(res), "alert", res.Alert)
	}
	return orphans, nil
}

func (o StartupOptions) check() error {
	switch {
	case o.Allocator == nil:
		return errors.New(
			"clusterprovider: StartupOptions.Allocator is required; the lease file is the " +
				"authority on which cluster holds which port, and a restart reap with no leases " +
				"has nothing to look for and would report an empty host")
	case o.Registry == nil:
		return errors.New(
			"clusterprovider: StartupOptions.Registry is required; without it nothing " +
				"distinguishes a cluster this pass may bring up from one whose CR is gone")
	case o.Host == nil:
		return errors.New(
			"clusterprovider: StartupOptions.Host is required; the survivors are recorded on " +
				"this host, and every later pass reaps them through the same object")
	case o.Decider == nil:
		return errors.New(
			"clusterprovider: StartupOptions.Decider is required; the reap and the phase " +
				"machine have to be the same decider the join loop then uses, or the group this " +
				"pass spawns is not the group that decider knows is live")
	case o.EnsureStore == nil:
		return errors.New(
			"clusterprovider: StartupOptions.EnsureStore is required; a restart derives the " +
				"argv the running path spawns a cluster with, and that argv carries the store's " +
				"DSN, so the store has to be adopted before the argv is derived. Without this " +
				"seam the pass refuses on the first restart with an empty DSN")
	case o.StateDir == "":
		return errors.New(
			"clusterprovider: StartupOptions.StateDir is required; the data directory under it " +
				"is what makes a cluster durable, and \"the cluster is gone\" is a caller " +
				"decision about that directory, not something a restart may assume")
	}
	return nil
}

func owns(cl *v1alpha1.Cluster) bool {
	return cl != nil && materialiser.StartupOwns(*cl)
}

func whyUnowned(cl *v1alpha1.Cluster) string {
	switch {
	case cl == nil:
		return "no Cluster CR names it"
	case cl.DeletionTimestamp != nil:
		return "its CR carries a deletionTimestamp, so the teardown path owns it"
	case !cl.Spec.Running:
		return "spec.running is false, so the OFF path owns it"
	default:
		return "its identity verification is halted, so nothing may start it"
	}
}

func spawnArgvs(cfg clusterprovider.StackConfig, cluster string, leases materialiser.Leases) ([][]string, error) {
	steps, err := clusterprovider.StepsFor(cfg)(
		materialiser.Observed{Cluster: v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: cluster}}},
		leases,
	)
	if err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf(
			"clusterprovider: the steps for %s are empty, so nothing it spawns has an argv to "+
				"attribute a bound port to", cluster)
	}
	argvs := make([][]string, 0, len(steps))
	for _, s := range steps {
		if len(s.Spec.Argv) == 0 {
			return nil, fmt.Errorf(
				"clusterprovider: the %s step of %s carries no argv, so a process it spawned "+
					"could not be told from a stranger holding the same port", s.Name, cluster)
		}
		argvs = append(argvs, s.Spec.Argv)
	}
	return argvs, nil
}

func purposeOf(leases materialiser.Leases, port int) (portallocator.Purpose, bool) {
	for p, l := range leases {
		if l.Covers(port) {
			return p, true
		}
	}
	return "", false
}

func theGroupBinds(stack clusterprovider.Stack, p portallocator.Purpose) bool {
	if p == portallocator.PurposePostgres {
		return false
	}
	purposes, err := clusterprovider.PurposesFor(stack, false)
	if err != nil {
		return false
	}
	return slices.Contains(purposes, p)
}

func dataDirOf(stateDir, cluster string) string {
	dir, err := clusterprovider.ClusterDataDir(stateDir, cluster)
	if err != nil {
		return stateDir
	}
	return dir
}

func opsOf(res materialiser.Result) string {
	names := make([]string, len(res.Ops))
	for i, op := range res.Ops {
		names[i] = string(op)
	}
	return fmt.Sprint(names)
}
