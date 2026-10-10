package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/cloudprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodemarket"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodeprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodeqemu"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/subnetallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultcert"
)

type tenantClient func(ctx context.Context, cluster string) (kubernetes.Interface, error)

var errNoTenantClient = errors.New(
	"clusterprovider: deleting the Node object this node registered needs its cluster's tenant " +
		"apiserver, and no tenant client was supplied -- a k3s agent does not delete its own Node " +
		"object when it stops (kubelet removes it only when a cloud provider is configured, and we " +
		"configure none), so a teardown that skips the delete leaves the tenant cluster counting a " +
		"node that is gone: scale.Decide reads a node count above zero with no intent on every pass " +
		"and calls Teardown again, and the cluster can never be observed at zero nodes, which is the " +
		"objective. A skipped cleanup and a completed one look identical from a green suite")

var errNoJoiningClient = errors.New(
	"clusterprovider: answering whether the tenant cluster reports this node needs that cluster's " +
		"apiserver, and no tenant client was supplied -- that answer is what moves a node from " +
		"Pending to Running, and a guess either promotes a node k3s never registered or holds back a " +
		"node that is already serving")

var errNoNodeAllocator = errors.New(
	"clusterprovider: the apiserver port a node is told to dial comes from the lease file, and no " +
		"allocator was supplied -- without the k3s-secure lease a node dials a port no lease owns, " +
		"which reads as an x509 error from another cluster's apiserver")

var errNoNodeRegistry = errors.New(
	"clusterprovider: every node phase is written through the cluster registry, and none was " +
		"supplied -- the Node CR is the idempotency record the scale decision re-reads on every " +
		"pass, so a node materialised without it is one nothing can count and nothing will tear down")

var errNoNodeStateDir = errors.New(
	"clusterprovider: a node needs a state dir it can write its record under, and none was " +
		"supplied -- the record is what lets a gateway that restarts reap the guest the last one " +
		"booted, and without it that guest keeps running while a new gateway provisions a second one " +
		"under the same node name")

var errNoNodeSubnets = errors.New(
	"clusterprovider: a node is addressed inside the range its cluster holds in the subnet lease " +
		"file, and no allocator for it was supplied -- the range's first usable address is the " +
		"gateway the guest's default route names and the address its apiserver is advertised on, so " +
		"a node built without the lease is addressed in a range nobody holds: two clusters then " +
		"report the same InternalIP for different nodes and each cluster's flannel takes the other's " +
		"pod CIDR as its own")

var errNoAdvertiseAddress = errors.New(
	"clusterprovider: a node is told to dial this host at --advertise-address, and none was given " +
		"-- for a node that is not a guest there is no rule to derive it from, so it is a parameter " +
		"rather than a guess, and a wrong one points a node at nothing usable. A guest is the opposite " +
		"case and is derived rather than given: its dial address is the first usable address of the " +
		"range its cluster holds in the subnet lease file, and --advertise-address is refused outright " +
		"for that substrate, because a guest reaches none of this host's own addresses")

var errNoNodeCluster = errors.New(
	"clusterprovider: a node is per cluster, and the empty name has no Cluster CR, no lease and no " +
		"data dir to hang one on")

type nodeSubstrate struct {
	Name string

	Firecracker string

	QEMU string

	NodeAgent string

	Image nodevm.Image

	Guest nodevm.Guest

	Network nodevm.Network

	DiskMiB int
}

func (s nodeSubstrate) IsGuest() bool {
	return s.Name == SubstrateFirecracker || s.Name == SubstrateQEMU
}

func (s nodeSubstrate) deferredJoin() bool { return s.Name == SubstrateMarket }

func nodeSubstrateFor(cfg Config) (nodeSubstrate, error) {
	switch cfg.Substrate {
	case SubstrateFirecracker:
		if !hostos.Firecracker() {
			return nodeSubstrate{}, fmt.Errorf(
				"clusterprovider: no firecracker node can be placed on this host, which is %s. "+
					"Firecracker is a KVM guest and it exists only on Linux, so a Windows host "+
					"cannot boot it at all and no check downstream would catch a placement that "+
					"was never made. The name is refused rather than approximated: use %s, which "+
					"runs on both hosts", hostos.Name(), SubstrateQEMU)
		}
		image, manifest, err := nodefirecracker.LoadImage(cfg.ImageOutDir, cfg.ImageConfig)
		if err != nil {
			return nodeSubstrate{}, err
		}
		if err := manifest.CheckVMM(SubstrateFirecracker); err != nil {
			return nodeSubstrate{}, err
		}
		return nodeSubstrate{
			Name:        SubstrateFirecracker,
			Firecracker: cfg.Firecracker,
			NodeAgent:   cfg.NodeAgent,
			Image:       image,
			Guest:       manifest.Guest(),
			Network:     nodevm.Network{Interface: manifest.GuestInterface},
			DiskMiB:     cfg.NodeDiskMiB,
		}, nil
	case SubstrateQEMU:
		image, manifest, err := nodefirecracker.LoadImage(cfg.ImageOutDir, cfg.ImageConfig)
		if err != nil {
			return nodeSubstrate{}, err
		}
		if err := manifest.CheckVMM(SubstrateQEMU); err != nil {
			return nodeSubstrate{}, err
		}
		return nodeSubstrate{
			Name:      SubstrateQEMU,
			QEMU:      cfg.QEMU,
			NodeAgent: cfg.NodeAgent,
			Image:     image,
			Guest:     manifest.Guest(),
			Network:   nodevm.Network{Interface: manifest.GuestInterface},
			DiskMiB:   cfg.NodeDiskMiB,
		}, nil
	case SubstrateMarket:
		return nodeSubstrate{
			Name: SubstrateMarket,
		}, nil
	case "":
		return nodeSubstrate{}, errNoSubstrateChoice
	}
	return nodeSubstrate{}, fmt.Errorf(
		"clusterprovider: no substrate named %q places nodes here, and %s, %s and %s are the only "+
			"ones implemented today. The name is refused rather than approximated: it decides what "+
			"a node's record names, what its teardown stops and what its leases mean, so a gateway "+
			"that ran anyway would report a placement it is not honouring",
		cfg.Substrate, SubstrateFirecracker, SubstrateQEMU, SubstrateMarket)
}

func (s nodeSubstrate) Validate() error {
	switch s.Name {
	case SubstrateFirecracker:
		if s.Firecracker == "" || s.NodeAgent == "" {
			return errNoSubstratePlacement
		}
		if err := s.Image.Validate(); err != nil {
			return fmt.Errorf("%w: %v", errNoSubstratePlacement, err)
		}
		return nil
	case SubstrateQEMU:
		if s.QEMU == "" || s.NodeAgent == "" {
			return errNoSubstratePlacementQEMU
		}
		if err := s.Image.Validate(); err != nil {
			return fmt.Errorf("%w: %v", errNoSubstratePlacementQEMU, err)
		}
		return nil
	case SubstrateMarket:
		return nil
	case "":
		return errNoSubstrateChoice
	}
	return fmt.Errorf(
		"clusterprovider: no substrate named %q places nodes here, and %s, %s and %s are the only "+
			"ones implemented today", s.Name, SubstrateFirecracker, SubstrateQEMU, SubstrateMarket)
}

type nodeSubstrateOptions struct {
	Cluster string

	Server string

	NodeName string

	TokenFile string

	ServerCAFile string

	DataDir string

	AdvertiseAddress string

	ApiserverPort int

	DenyBidderDids []string

	Claims nodemarket.Claims

	ClusterSource nodemarket.ClusterSource

	ReleaseTimeout time.Duration

	Record func(ctx context.Context, nodeName string, phase v1alpha1.NodePhase) error

	Log *slog.Logger

	NodeDiskStage func(ctx context.Context, fillDir, nodePassword string) error
}

func (s nodeSubstrate) NewNodeHost(opts nodeSubstrateOptions) (nodeHost, error) {
	switch s.Name {
	case SubstrateFirecracker:
		return nodefirecracker.New(nodefirecracker.Options{
			Cluster:       opts.Cluster,
			Server:        opts.Server,
			NodeName:      opts.NodeName,
			TokenFile:     opts.TokenFile,
			DataDir:       opts.DataDir,
			Record:        opts.Record,
			Log:           opts.Log,
			Agent:         s.NodeAgent,
			Firecracker:   s.Firecracker,
			Image:         s.Image,
			Guest:         s.Guest,
			Network:       s.Network,
			DiskMiB:       s.DiskMiB,
			NodeDiskStage: opts.NodeDiskStage,
		})
	case SubstrateQEMU:
		return nodeqemu.New(nodeqemu.Options{
			Cluster:       opts.Cluster,
			Server:        opts.Server,
			NodeName:      opts.NodeName,
			TokenFile:     opts.TokenFile,
			DataDir:       opts.DataDir,
			Record:        opts.Record,
			Log:           opts.Log,
			Agent:         s.NodeAgent,
			QEMU:          s.QEMU,
			Image:         s.Image,
			Guest:         s.Guest,
			Network:       s.Network,
			DiskMiB:       s.DiskMiB,
			NodeDiskStage: opts.NodeDiskStage,
		})
	case SubstrateMarket:
		return nodemarket.New(nodemarket.Options{
			Cluster:          opts.Cluster,
			NodeName:         opts.NodeName,
			AdvertiseAddress: opts.AdvertiseAddress,
			ApiserverPort:    opts.ApiserverPort,
			TokenFile:        opts.TokenFile,
			ServerCAFile:     opts.ServerCAFile,
			DataDir:          opts.DataDir,
			DenyBidderDids:   opts.DenyBidderDids,
			Resolve:          opts.ClusterSource,
			Claims:           opts.Claims,
			Record:           opts.Record,
			Log:              opts.Log,
			ReleaseTimeout:   opts.ReleaseTimeout,
		})
	case "":
		return nil, errNoSubstrateChoice
	}
	return nil, fmt.Errorf(
		"clusterprovider: no substrate named %q places nodes here, and %s, %s and %s are the only "+
			"ones implemented today", s.Name, SubstrateFirecracker, SubstrateQEMU, SubstrateMarket)
}

func (s nodeSubstrate) withPerNodeNetwork(lease subnetallocator.Lease, ordinal int) (nodeSubstrate, error) {
	network, err := perNodeNetwork(s.Network, lease, ordinal)
	if err != nil {
		return s, err
	}
	s.Network = network
	return s, nil
}

type nodeHostsOptions struct {
	Allocator *portallocator.Allocator

	Networks *subnetallocator.Allocator

	Registry clusterprovider.Clusters

	Claims nodemarket.Claims

	StateDir string

	AdvertiseAddress string

	DenyBidderDids []string

	ClaimReleaseTimeout time.Duration

	JoinTimeout time.Duration

	Tenant tenantClient

	Log *slog.Logger

	Substrate nodeSubstrate
}

var errNoNodeClaims = errors.New(
	"clusterprovider: a market node IS a NodeClaim written into its cluster's home workspace, and " +
		"none of the store that writes one was supplied -- a gateway that started anyway would " +
		"count a node it never asked anybody to rent, and the scale decision would ask again on " +
		"every pass for ever")

func (o nodeHostsOptions) joinTimeout() time.Duration {
	if o.JoinTimeout <= 0 {
		return NodeJoinTimeout
	}
	return o.JoinTimeout
}

var errNoSubstrateChoice = errors.New(
	"clusterprovider: a node is placed by a substrate and none was chosen here. The name decides " +
		"what a node's record names, what its teardown stops and what its leases mean, so the " +
		"composition root refuses rather than picking one: every substrate that has been tried on " +
		"this host's own kernel has been refuted by measurement, and a gateway that fell back to one " +
		"would place a node whose lifetime this process owns while everything downstream still " +
		"reported a guest")

var errNoSubstratePlacement = errors.New(
	"clusterprovider: the firecracker substrate needs the VMM binary, the agent binary that both " +
		"wires the guest's network from outside it and runs inside it, and the image the guest boots " +
		"from; none of the three can be derived, and a gateway that started anyway would count a node " +
		"it never booted")

var errNoSubstratePlacementQEMU = errors.New(
	"clusterprovider: the qemu substrate needs the qemu-system binary, the agent binary that both " +
		"wires the guest's network from outside it and runs inside it, and the image the guest boots " +
		"from; none of the three can be derived, and a gateway that started anyway would count a node " +
		"it never booted")

type nodeHost interface {
	scale.Nodes

	ConfirmJoined(ctx context.Context) error

	Reap(ctx context.Context) error
}

type guestWatcher interface {
	Fatal() string
}

func guestFatal(host nodeHost) string {
	watcher, ok := host.(guestWatcher)
	if !ok {
		return ""
	}
	return watcher.Fatal()
}

type tenantNodes struct {
	cluster  string
	nodeName string
	host     nodeHost
	tenant   tenantClient

	log *slog.Logger

	opts nodeHostsOptions

	mu      sync.Mutex
	started int
	extra   map[int]heldNode
	live    map[string]bool
}

type heldNode struct {
	name string
	host nodeHost
}

var _ scale.Nodes = (*tenantNodes)(nil)

const NodeJoinTimeout = 30 * time.Second

const nodeJoinPoll = 250 * time.Millisecond

func (n *tenantNodes) Provision(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	ordinal := 1
	if n.started > 0 || n.host == nil {
		next, err := n.nextOrdinal(ctx)
		if err != nil {
			return err
		}
		ordinal = next
	}

	host, name, held := n.hostFor(ordinal)
	if !held {
		id := nodeIDFor(ordinal)
		nodeName, err := nodeprovider.NodeName(n.cluster, id)
		if err != nil {
			return err
		}
		built, err := newNodeHost(n.opts, n.log, n.cluster, id)
		if err != nil {
			return err
		}
		host, name = built.host, nodeName
	}

	if err := host.Provision(ctx); err != nil {
		return fmt.Errorf("clusterprovider: provisioning node %s of %s: %w -- there is no lease to "+
			"give back here, because a node holds none of its own: every port its guest binds, its "+
			"kubelet's included, is bound inside the guest's own network namespace, and the "+
			"substrate stops whatever it started before it returns", name, n.cluster, err)
	}
	if !held {
		n.retain(ordinal, heldNode{name: name, host: host})
	}
	n.markLive(name)
	n.started++
	if err := n.writeSnapshot(); err != nil {
		return err
	}
	if n.opts.Substrate.deferredJoin() {
		n.log.Info("cluster provider: the claim is written and this pass returns without waiting "+
			"for the machine", "cluster", n.cluster, "node", name,
			"reason", "a market machine takes minutes to appear in the tenant cluster's node list, "+
				"and this pass is one of many in one process: blocking here would hold back every "+
				"other cluster for the whole join timeout",
			"next", "a later pass confirms the node once the claim reads Running AND the tenant "+
				"cluster reports the Node, and a claim that reads Failed fails the node here")
		return nil
	}
	return n.confirmJoined(ctx, host, name)
}

func (n *tenantNodes) confirmHeld(ctx context.Context) error {
	if !n.opts.Substrate.deferredJoin() {
		return nil
	}
	n.mu.Lock()
	held := n.heldNodesLocked()
	n.mu.Unlock()

	var problems []error
	for _, node := range held {
		if fatal := guestFatal(node.host); fatal != "" {
			problems = append(problems, fmt.Errorf(
				"clusterprovider: node %s of %s stopped making progress before the tenant cluster "+
					"reported it, so it is failed rather than left counted: %s -- the claim is "+
					"released and the next pass may ask the market again",
				node.name, n.cluster, fatal))
			if err := n.teardownHeld(ctx, node); err != nil {
				problems = append(problems, err)
			}
			continue
		}
		reported, err := nodeReported(ctx, n.tenant, n.cluster, node.name)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if !reported {
			continue
		}
		if err := node.host.ConfirmJoined(ctx); err != nil {
			problems = append(problems, fmt.Errorf("clusterprovider: confirming node %s of %s "+
				"after the tenant cluster reported it: %w", node.name, n.cluster, err))
		}
	}
	return errors.Join(problems...)
}

func (n *tenantNodes) markLive(name string) {
	if n.live == nil {
		n.live = map[string]bool{}
	}
	n.live[name] = true
}

func (n *tenantNodes) snapshot() []cloudprovider.Instance {
	names := make([]string, 0, len(n.live))
	for name := range n.live {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]cloudprovider.Instance, 0, len(names))
	for _, name := range names {
		out = append(out, cloudprovider.Instance{NodeName: name, Cluster: n.cluster, Exists: true})
	}
	return out
}

func (n *tenantNodes) writeSnapshot() error {
	if n.opts.StateDir == "" {
		return fmt.Errorf("clusterprovider: %s's instance snapshot has no state dir to be written "+
			"under, so the cloud controller manager cannot be told which of its Nodes still have a "+
			"guest. A snapshot that was never written reads as \"the guest is gone\"", n.cluster)
	}
	dir, err := clusterprovider.ClusterDataDir(n.opts.StateDir, n.cluster)
	if err != nil {
		return err
	}
	if err := cloudprovider.WriteSnapshot(clusterstack.NodeSnapshotPath(dir), n.snapshot()); err != nil {
		return fmt.Errorf("clusterprovider: writing the instance snapshot for %s: %w. The cloud "+
			"controller manager reads it per call to decide whether a Node still has a guest, and a "+
			"write that landed short of the whole set would read as \"the guest is gone\"", n.cluster, err)
	}
	return nil
}

func (n *tenantNodes) hostFor(ordinal int) (nodeHost, string, bool) {
	if ordinal == 1 && n.host != nil {
		return n.host, n.nodeName, true
	}
	if held, ok := n.extra[ordinal]; ok {
		return held.host, held.name, true
	}
	return nil, "", false
}

func (n *tenantNodes) retain(ordinal int, held heldNode) {
	if ordinal == 1 {
		return
	}
	if n.extra == nil {
		n.extra = map[int]heldNode{}
	}
	n.extra[ordinal] = held
}

func (n *tenantNodes) nextOrdinal(ctx context.Context) (int, error) {
	present := map[int]bool{}
	prefix := strings.ToLower(n.cluster) + "-node"
	for _, held := range n.heldNodesLocked() {
		if ordinal, ok := nodeOrdinalInName(prefix, held.name); ok {
			present[ordinal] = true
		}
	}
	if n.tenant != nil {
		client, err := n.tenant(ctx, n.cluster)
		if err != nil {
			return 0, fmt.Errorf("clusterprovider: the tenant client for %s, which says which of "+
				"its nodes already exist before the next one is named: %w", n.cluster, err)
		}
		list, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return 0, fmt.Errorf("clusterprovider: listing the nodes of %s before naming the next "+
				"one: %w -- a read that failed is not a cluster with no nodes, and naming a node "+
				"over one that already exists is the collision this list exists to avoid; nothing "+
				"was provisioned", n.cluster, err)
		}
		for i := range list.Items {
			if ordinal, ok := nodeOrdinalInName(prefix, list.Items[i].Name); ok {
				present[ordinal] = true
			}
		}
	}
	for ordinal := 1; ; ordinal++ {
		if !present[ordinal] {
			return ordinal, nil
		}
	}
}

func (n *tenantNodes) confirmJoined(ctx context.Context, host nodeHost, name string) error {
	deadline := time.Now().Add(n.opts.joinTimeout())
	for {
		reported, err := nodeReported(ctx, n.tenant, n.cluster, name)
		if err != nil {
			return err
		}
		if reported {
			return host.ConfirmJoined(ctx)
		}
		if fatal := guestFatal(host); fatal != "" {
			return fmt.Errorf("clusterprovider: node %s of %s stopped making progress before the "+
				"tenant cluster reported it: %s -- the guest's own words are returned here rather "+
				"than waiting out the join timeout, because a host that died at its first step and "+
				"a host that is merely slow look identical until the clock runs out, and only one "+
				"of them is fixed by waiting", name, n.cluster, fatal)
		}
		if time.Now().After(deadline) {
			n.log.Warn("clusterprovider: the tenant cluster has not reported this node within the "+
				"join timeout, so its phase stays Pending. Pending is the honest phase -- it says "+
				"the command is up and k3s has not registered it -- and the next pass re-observes "+
				"and confirms it, because the count the scale decision reads is the tenant "+
				"cluster's own node list and not this phase",
				"cluster", n.cluster, "node", name, "timeout", n.opts.joinTimeout())
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(nodeJoinPoll):
		}
	}
}

func (n *tenantNodes) Teardown(ctx context.Context) error {
	if _, err := n.client(ctx); err != nil {
		return fmt.Errorf("clusterprovider: the tenant client for %s, which every teardown of its "+
			"nodes needs before it stops anything -- stopping an agent first would leave a node with "+
			"no process behind a Node object nothing here can delete: %w", n.cluster, err)
	}
	n.mu.Lock()
	held := n.heldNodesLocked()
	n.mu.Unlock()

	var problems []error
	for _, node := range held {
		if err := n.teardownHeld(ctx, node); err != nil {
			problems = append(problems, err)
		}
	}
	if err := n.writeSnapshot(); err != nil {
		problems = append(problems, err)
	}
	if len(problems) > 0 {
		return errors.Join(problems...)
	}
	n.mu.Lock()
	n.extra = nil
	n.mu.Unlock()
	return nil
}

func (n *tenantNodes) teardownHeld(ctx context.Context, node heldNode) error {
	if err := node.host.Teardown(ctx); err != nil {
		return err
	}
	n.mu.Lock()
	delete(n.live, node.name)
	n.mu.Unlock()
	client, err := n.client(ctx)
	if err != nil {
		return fmt.Errorf("clusterprovider: the tenant client, needed to delete the Node object of "+
			"%s after its guest was stopped: %w -- left behind it reads as a node that still exists, "+
			"so the next pass tears down a node that is already gone", node.name, err)
	}
	if err := client.CoreV1().Nodes().Delete(ctx, node.name, metav1.DeleteOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			n.log.Info("clusterprovider: the tenant cluster holds no Node object for this node",
				"cluster", n.cluster, "node", node.name)
			return nil
		}
		return fmt.Errorf("clusterprovider: deleting the tenant cluster's "+
			"Node object %s: %w -- the guest is stopped, and this object is the count the scale "+
			"decision reads: left behind it reads as a node that still exists, so the next pass "+
			"tears down a node that is already gone and the cluster never reaches the zero state",
			node.name, err)
	}
	n.log.Info("clusterprovider: removed the node from the tenant cluster",
		"cluster", n.cluster, "node", node.name)
	return nil
}

func (n *tenantNodes) inFlight(ctx context.Context) int {
	n.mu.Lock()
	held := n.heldNodesLocked()
	n.mu.Unlock()
	if len(held) == 0 {
		return 0
	}
	reported, err := n.reportedNodeNames(ctx)
	if err != nil {
		n.log.Warn("clusterprovider: the tenant cluster's node list could not be read while counting "+
			"the nodes this one has in flight, so every node held here is counted as in flight. "+
			"Counting a node that has already joined costs one pass of a slower scale-up; counting "+
			"a node that has not joined is what rents a machine nobody asked for, and the next pass "+
			"reads the list again",
			"cluster", n.cluster, "held", len(held), "error", err.Error())
		return len(held)
	}
	count := 0
	for _, node := range held {
		if !reported[node.name] {
			count++
		}
	}
	return count
}

func (n *tenantNodes) reportedNodeNames(ctx context.Context) (map[string]bool, error) {
	client, err := n.client(ctx)
	if err != nil {
		return nil, err
	}
	list, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("clusterprovider: listing the nodes of %s, which is what says which "+
			"of the nodes held here its k3s agent has already registered: %w", n.cluster, err)
	}
	out := make(map[string]bool, len(list.Items))
	for i := range list.Items {
		out[list.Items[i].Name] = true
	}
	return out, nil
}

func (n *tenantNodes) heldNodesLocked() []heldNode {
	out := make([]heldNode, 0, 1+len(n.extra))
	if n.host != nil {
		out = append(out, heldNode{name: n.nodeName, host: n.host})
	}
	ordinals := make([]int, 0, len(n.extra))
	for ordinal := range n.extra {
		ordinals = append(ordinals, ordinal)
	}
	slices.Sort(ordinals)
	for _, ordinal := range ordinals {
		out = append(out, n.extra[ordinal])
	}
	return out
}

func (n *tenantNodes) client(ctx context.Context) (kubernetes.Interface, error) {
	if n.tenant == nil {
		return nil, errNoTenantClient
	}
	client, err := n.tenant(ctx, n.cluster)
	if err != nil {
		return nil, fmt.Errorf("clusterprovider: the tenant client for %s: %w", n.cluster, err)
	}
	if client == nil {
		return nil, errNoTenantClient
	}
	return client, nil
}

type nodeHosts struct {
	opts nodeHostsOptions
	log  *slog.Logger

	mu    sync.Mutex
	hosts map[string]*tenantNodes
}

func newNodeHosts(opts nodeHostsOptions) (*nodeHosts, error) {
	switch {
	case opts.Allocator == nil:
		return nil, errNoNodeAllocator
	case opts.Networks == nil:
		return nil, errNoNodeSubnets
	case opts.Registry == nil:
		return nil, errNoNodeRegistry
	case opts.StateDir == "":
		return nil, errNoNodeStateDir
	case opts.Substrate.Name == SubstrateMarket && opts.Claims == nil:
		return nil, errNoNodeClaims
	case opts.AdvertiseAddress == "" && !opts.Substrate.IsGuest():
		return nil, errNoAdvertiseAddress
	}
	if err := opts.Substrate.Validate(); err != nil {
		return nil, err
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &nodeHosts{opts: opts, log: log, hosts: map[string]*tenantNodes{}}, nil
}

func (h *nodeHosts) forCluster(cluster string) (scale.Nodes, error) {
	if cluster == "" {
		return nil, errNoNodeCluster
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if n, ok := h.hosts[cluster]; ok {
		return n, nil
	}
	created, err := newNodeHost(h.opts, h.log, cluster, nodeID)
	if err != nil {
		return nil, err
	}
	h.hosts[cluster] = created
	return created, nil
}

func (h *nodeHosts) inFlight(ctx context.Context, cluster string) int {
	h.mu.Lock()
	n, ok := h.hosts[cluster]
	h.mu.Unlock()
	if !ok {
		return 0
	}
	return n.inFlight(ctx)
}

func (h *nodeHosts) clusters() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.hosts))
	for cluster := range h.hosts {
		out = append(out, cluster)
	}
	slices.Sort(out)
	return out
}

func (h *nodeHosts) resetSnapshot(cluster string) error {
	dir, err := clusterprovider.ClusterDataDir(h.opts.StateDir, cluster)
	if err != nil {
		return err
	}
	return cloudprovider.WriteSnapshot(clusterstack.NodeSnapshotPath(dir), nil)
}

func (h *nodeHosts) confirm(ctx context.Context, cluster string) error {
	h.mu.Lock()
	n, ok := h.hosts[cluster]
	h.mu.Unlock()
	if !ok {
		return nil
	}
	return n.confirmHeld(ctx)
}

func (h *nodeHosts) reapCluster(ctx context.Context, cluster string) error {
	h.mu.Lock()
	n, ok := h.hosts[cluster]
	if ok {
		delete(h.hosts, cluster)
	}
	h.mu.Unlock()
	if !ok {
		return nil
	}
	n.mu.Lock()
	nodes := n.heldNodesLocked()
	n.mu.Unlock()
	var problems []error
	for _, node := range nodes {
		h.log.Info("cluster provider: reaping the node host of a cluster that is OFF or deleting",
			"cluster", cluster, "node", node.name)
		if err := node.host.Reap(ctx); err != nil {
			problems = append(problems, fmt.Errorf(
				"clusterprovider: reaping node %s of %s, whose CR is OFF or deleting: %w",
				node.name, cluster, err))
		}
	}
	n.mu.Lock()
	n.live = nil
	n.mu.Unlock()
	if err := n.writeSnapshot(); err != nil {
		problems = append(problems, err)
	}
	return errors.Join(problems...)
}

func (h *nodeHosts) reap(ctx context.Context) error {
	h.mu.Lock()
	held := make(map[string]*tenantNodes, len(h.hosts))
	for cluster, n := range h.hosts {
		held[cluster] = n
	}
	h.mu.Unlock()
	clusters := make([]string, 0, len(held))
	for cluster := range held {
		clusters = append(clusters, cluster)
	}
	slices.Sort(clusters)

	var problems []error
	for _, cluster := range clusters {
		n := held[cluster]
		n.mu.Lock()
		nodes := n.heldNodesLocked()
		n.mu.Unlock()
		for _, node := range nodes {
			h.log.Info("cluster provider: reaping the node host this gateway booted a guest for",
				"cluster", cluster, "node", node.name)
			if err := node.host.Reap(ctx); err != nil {
				problems = append(problems, fmt.Errorf(
					"clusterprovider: reaping node %s of %s on shutdown: %w -- the agent outlives this "+
						"process whichever way it exits, and the record it left is what makes the next "+
						"gateway's Provision answer ErrAlreadyRunning, so that cluster can never "+
						"provision a node again", node.name, cluster, err))
			}
		}
		n.mu.Lock()
		n.live = nil
		n.mu.Unlock()
		if err := n.writeSnapshot(); err != nil {
			problems = append(problems, err)
		}
	}
	return errors.Join(problems...)
}

func newNodeHost(opts nodeHostsOptions, log *slog.Logger, cluster, nodeID string) (*tenantNodes, error) {
	apiserver, err := opts.Allocator.Allocate(portallocator.Request{
		Cluster: cluster, Purpose: portallocator.PurposeK3sSecure,
	})
	if err != nil {
		return nil, fmt.Errorf("clusterprovider: the apiserver lease for %s, which is both where "+
			"its k3s binds and what a node of the cluster is told to dial: %w", cluster, err)
	}
	name, err := nodeprovider.NodeName(cluster, nodeID)
	if err != nil {
		return nil, err
	}
	ordinal, err := nodeOrdinal(nodeID)
	if err != nil {
		return nil, err
	}
	clusterDir, err := clusterprovider.ClusterDataDir(opts.StateDir, cluster)
	if err != nil {
		return nil, err
	}
	substrate := opts.Substrate
	if substrate.IsGuest() {
		if hostos.NetworkMode() == hostos.NetworkUser {
			substrate.Network = nodevm.UserNetwork(substrate.Network.Interface)
		} else {
			network, err := opts.Networks.Allocate(subnetallocator.Request{Cluster: cluster})
			if err != nil {
				return nil, fmt.Errorf("clusterprovider: the range %s's guests are addressed in, "+
					"which holds the gateway their apiserver is advertised on and one address per node: "+
					"%w -- without it there is no range to address a guest in, and a guest addressed "+
					"outside its cluster's range is one whose packets go to a node of another cluster "+
					"or to this host", cluster, err)
			}
			substrate, err = substrate.withPerNodeNetwork(network, ordinal)
			if err != nil {
				return nil, err
			}
		}
	}
	host, err := newNodeHostOf(substrate, opts, log, cluster, name, apiserver.Port, clusterDir)
	if err != nil {
		return nil, err
	}
	return &tenantNodes{
		cluster:  cluster,
		nodeName: name,
		host:     host,
		tenant:   opts.Tenant,
		log:      log,
		opts:     opts,
	}, nil
}

func perNodeNetwork(n nodevm.Network, lease subnetallocator.Lease, ordinal int) (nodevm.Network, error) {
	address, err := lease.Host(1 + ordinal)
	if err != nil {
		return n, fmt.Errorf("clusterprovider: addressing node %d of cluster %s, whose range holds "+
			"one address per node and whose first usable address is the gateway its guests dial the "+
			"apiserver on: %w", ordinal, lease.Cluster, err)
	}
	n.Gateway = lease.Gateway().String()
	n.Prefix = lease.PrefixBits()
	n.GuestIP = address.String()
	n.GuestMAC = guestMAC(address)
	n.PastaIP = lease.Block.Masked().Addr().String()
	return n, nil
}

func guestMAC(address netip.Addr) string {
	v := address.As4()
	return fmt.Sprintf("06:00:%02x:%02x:%02x:%02x", v[0], v[1], v[2], v[3])
}

func nodeOrdinal(nodeID string) (int, error) {
	digits, ok := strings.CutPrefix(nodeID, "node")
	if !ok || digits == "" {
		return 0, fmt.Errorf("clusterprovider: the node id %q does not name which node of its "+
			"cluster it is, and that number is what picks the node's address out of the cluster's "+
			"range: the range holds one address per node, numbered from the gateway, so an id with "+
			"no ordinal in it leaves the node with no address to take. The id is composed with the "+
			"cluster into the node's name, and this host already builds ids as \"node<N>\"; reading "+
			"the number off the id rather than carrying a second constant beside it is what keeps "+
			"an address and a name from disagreeing", nodeID)
	}
	ordinal, err := strconv.Atoi(digits)
	if err != nil {
		return 0, fmt.Errorf("clusterprovider: the node id %q ends in %q, which is not the number "+
			"of the node within its cluster: %w", nodeID, digits, err)
	}
	if ordinal < 1 {
		return 0, fmt.Errorf("clusterprovider: the node id %q names node %d of its cluster, and "+
			"nodes are numbered from 1: the addresses in a cluster's range are numbered from 1 as "+
			"well, and address 1 is the gateway the guests dial, so there is no node 0 to give one "+
			"to", nodeID, ordinal)
	}
	return ordinal, nil
}

func nodeIDFor(ordinal int) string {
	return fmt.Sprintf("node%d", ordinal)
}

func nodeOrdinalInName(prefix, name string) (int, bool) {
	rest, ok := strings.CutPrefix(strings.ToLower(name), prefix)
	if !ok || rest == "" {
		return 0, false
	}
	ordinal, err := strconv.Atoi(rest)
	if err != nil || ordinal < 1 {
		return 0, false
	}
	return ordinal, true
}

func newNodeHostOf(substrate nodeSubstrate, opts nodeHostsOptions, log *slog.Logger, cluster, name string, apiserverPort int, clusterDir string) (nodeHost, error) {
	server := fmt.Sprintf("https://%s:%d", opts.AdvertiseAddress, apiserverPort)
	var stage func(ctx context.Context, fillDir, nodePassword string) error
	if substrate.IsGuest() {
		server = fmt.Sprintf("https://%s:%d", substrate.Network.Gateway, apiserverPort)
		if _, err := os.Stat(clusterprovider.VaultUnsealPath(clusterDir)); err == nil {
			stage = nodeVaultStage(substrate, cluster, clusterDir, name)
		}
	}
	return substrate.NewNodeHost(nodeSubstrateOptions{
		Cluster:          cluster,
		Server:           server,
		NodeName:         name,
		TokenFile:        clusterprovider.TokenPath(clusterDir),
		ServerCAFile:     filepath.Join(clusterstack.TLSDir(clusterDir), "server-ca.crt"),
		DataDir:          clusterprovider.NodeDataDir(opts.StateDir, cluster, name),
		AdvertiseAddress: opts.AdvertiseAddress,
		ApiserverPort:    apiserverPort,
		DenyBidderDids:   opts.DenyBidderDids,
		Claims:           opts.Claims,
		ClusterSource:    claimClusterFor(opts.Registry, cluster),
		ReleaseTimeout:   opts.ClaimReleaseTimeout,
		Record:           nodeRecorder(opts.Registry, cluster, name, substrate.NodeExpiry()),
		Log:              log,
		NodeDiskStage:    stage,
	})
}

func claimClusterFor(registry clusterprovider.Clusters, cluster string) nodemarket.ClusterSource {
	return func(ctx context.Context) (nodemarket.Cluster, error) {
		ref, err := nodeRefFor(ctx, registry, cluster)
		if err != nil {
			return nodemarket.Cluster{}, err
		}
		if ref.Home == "" {
			return nodemarket.Cluster{}, fmt.Errorf(
				"clusterprovider: the Cluster CR for %s was found with no home workspace, so there "+
					"is no logical cluster to write this node's claim into: the claim lives in the "+
					"owner's own home, and a claim written anywhere else is one the door's listing "+
					"across clusters/* never sees", cluster)
		}
		cl, err := registry.Read(ctx, ref)
		if err != nil {
			return nodemarket.Cluster{}, fmt.Errorf(
				"clusterprovider: reading Cluster %s out of %s, whose spec.account is the DID the "+
					"market rents this node AS: %w -- the door fulfils the claim through that "+
					"owner's own OAuth session, so a claim with no owner DID is one no door can "+
					"act for", cluster, ref.Home, err)
		}
		if cl.Spec.Account == "" {
			return nodemarket.Cluster{}, fmt.Errorf(
				"clusterprovider: Cluster %s in %s carries no spec.account. That DID is the "+
					"requester the node is rented as, and it is not derivable from the cluster name: "+
					"a claim written without it names no session the door holds", cluster, ref.Home)
		}
		return nodemarket.Cluster{Workspace: ref.Home, OwnerDid: cl.Spec.Account}, nil
	}
}

func nodeVaultStage(substrate nodeSubstrate, cluster, clusterDir, nodeName string) func(context.Context, string, string) error {
	return func(ctx context.Context, fillDir, nodePassword string) error {
		seal, err := clusterprovider.ReadVaultSeal(clusterDir)
		if err != nil {
			return err
		}
		vaultTLS, err := vaultcert.EnsureVaultTLSCertificate(clusterDir, substrate.Network.Gateway, "127.0.0.1")
		if err != nil {
			return err
		}
		adminAddr := clusterprovider.VaultAddr(seal.Port)
		vaultURL := "https://" + net.JoinHostPort(substrate.Network.Gateway, strconv.Itoa(seal.Port))
		cert, err := vaultcert.MintNodeCertificate(ctx, adminAddr, seal.RootToken, nodeName, vaultURL, vaultTLS.CAPEM)
		if err != nil {
			return fmt.Errorf("clusterprovider: minting the vault certificate for node %s of %s: %w",
				nodeName, cluster, err)
		}
		if err := vaultcert.WriteNodeSecret(ctx, adminAddr, seal.RootToken, nodeName,
			map[string]any{"password": nodePassword}, vaultTLS.CAPEM); err != nil {
			return fmt.Errorf("clusterprovider: writing the per-node secret for node %s of %s into the "+
				"cluster vault: %w", nodeName, cluster, err)
		}
		if err := cert.WriteTo(fillDir); err != nil {
			return fmt.Errorf("clusterprovider: placing the vault certificate for node %s of %s on "+
				"its identity disk: %w", nodeName, cluster, err)
		}
		return nil
	}
}

func (s nodeSubstrate) NodeExpiry() *int64 {
	if !s.IsGuest() && s.Name != SubstrateMarket {
		return nil
	}
	expiry := int64(clusterprovider.NodeExpiryUnlimited)
	return &expiry
}

func nodeRecorder(registry clusterprovider.Clusters, cluster, name string, expiry *int64) func(context.Context, string, v1alpha1.NodePhase) error {
	return func(ctx context.Context, _ string, phase v1alpha1.NodePhase) error {
		ref, err := nodeRefFor(ctx, registry, cluster)
		if err != nil {
			return err
		}
		return registry.WriteNode(ctx, ref, name, v1alpha1.NodeStatus{Phase: phase, NodeExpiry: expiry})
	}
}

func nodeRefFor(ctx context.Context, registry clusterprovider.Clusters, key string) (clusterprovider.Ref, error) {
	refs, err := registry.Refs(ctx)
	if err != nil {
		return clusterprovider.Ref{}, err
	}
	for _, ref := range refs {
		found, err := ref.Key()
		if err != nil {
			return clusterprovider.Ref{}, err
		}
		if found == key {
			return ref, nil
		}
	}
	return clusterprovider.Ref{}, fmt.Errorf(
		"clusterprovider: no Cluster CR with the key %s in the tree this registry searched, so this "+
			"node's phase has nowhere to be written and the scale decision would read a cluster with "+
			"no node on every pass", key)
}

func nodeReported(ctx context.Context, tenant tenantClient, cluster, nodeName string) (bool, error) {
	if tenant == nil {
		return false, errNoJoiningClient
	}
	client, err := tenant(ctx, cluster)
	if err != nil {
		return false, fmt.Errorf("clusterprovider: the tenant client for %s, which is what says "+
			"whether node %s is in that cluster's node list: %w", cluster, nodeName, err)
	}
	if client == nil {
		return false, errNoJoiningClient
	}
	if _, err := client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("clusterprovider: reading node %s from the tenant cluster %s: %w "+
			"-- a read that failed is not a node that is missing, and acting on the two alike holds "+
			"back a node that is already serving", nodeName, cluster, err)
	}
	return true, nil
}
