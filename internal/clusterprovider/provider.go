package clusterprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/revocation"
)

var readyPhase = func() v1alpha1.ClusterPhase {
	settled, err := revocation.WakeSettled(revocation.Effect{State: v1alpha1.IdentityRevoked})
	if err != nil {
		panic("clusterprovider: revocation.WakeSettled refused a terminal effect: " + err.Error())
	}
	phase, err := revocation.Ready(settled)
	if err != nil {
		panic("clusterprovider: revocation.Ready refused a Settled: " + err.Error())
	}
	return phase
}()

const DefaultInterval = 2 * time.Second

var ErrNoDecider = errors.New("clusterprovider: Decider is required: the phase machine is internal/materialiser's, not this package's")

var ErrNoMintConfig = errors.New("clusterprovider: no mint credential for this cluster; refusing rather than falling back to a wider one")

type Clusters interface {
	Refs(ctx context.Context) ([]Ref, error)

	Read(ctx context.Context, ref Ref) (*v1alpha1.Cluster, error)

	WriteStatus(ctx context.Context, ref Ref, st v1alpha1.ClusterStatus) error

	MountStatus(ctx context.Context, ref Ref) int

	IdentityState(ctx context.Context, ref Ref) (v1alpha1.IdentityState, error)

	Nodes(ctx context.Context, ref Ref) ([]v1alpha1.Node, error)

	WriteNode(ctx context.Context, ref Ref, nodeName string, status v1alpha1.NodeStatus) error

	RemoveFinalizer(ctx context.Context, ref Ref) error
}

var _ Clusters = (*Registry)(nil)

type NodeExpirySource func(cl *v1alpha1.Cluster, nodes []v1alpha1.Node) (expiry int64, reported bool)

const NodeExpiryUnlimited int64 = 0

func NodeExpiryRollup(_ *v1alpha1.Cluster, nodes []v1alpha1.Node) (int64, bool) {
	live := 0
	rollup := NodeExpiryUnlimited
	for i := range nodes {
		if nodes[i].Status.Phase == v1alpha1.NodeDraining {
			continue
		}
		live++
		expiry := nodes[i].Status.NodeExpiry
		if expiry == nil {
			return 0, false
		}
		if *expiry == NodeExpiryUnlimited {
			continue
		}
		if rollup == NodeExpiryUnlimited || *expiry < rollup {
			rollup = *expiry
		}
	}
	if live == 0 {
		return NodeExpiryUnlimited, true
	}
	return rollup, true
}

type Options struct {
	Registry Clusters

	Decider *materialiser.Reconciler

	Ports *portallocator.Allocator

	Host materialiser.Host

	NodeExpirySource NodeExpirySource

	WriteStatus bool

	OnReady func(ctx context.Context, ref Ref, cl *v1alpha1.Cluster) error

	InstallTenant func(ctx context.Context, cluster string, cfg *rest.Config) error

	Tenant func(ctx context.Context, ref Ref, cl *v1alpha1.Cluster) (*rest.Config, error)

	GrantPath string

	MintDir string

	TokenTimeout time.Duration

	Probe func(ctx context.Context, url string) (int, bool)

	Interval time.Duration

	Now func() time.Time

	Log *slog.Logger
}

type Provider struct {
	opts Options

	mu    sync.Mutex
	mints map[string]*rest.Config

	readySatisfied map[string]bool
}

type Pass struct {
	Ref      Ref
	Cluster  *v1alpha1.Cluster
	Observed materialiser.Observed
	Result   materialiser.Result

	ObserveOnly bool

	Foreign bool

	Ratchet bool

	Wrote bool

	Unfinalized bool

	Err error
}

func (p Pass) Alert() string {
	switch {
	case p.Ratchet:
		return fmt.Sprintf(
			"cluster %s: phase is %s and this CR carries no deletionTimestamp; HELD -- Draining is a "+
				"one-way ratchet and nothing returns it to service",
			p.Ref.Name, p.Result.Phase)
	case p.Foreign:
		return fmt.Sprintf(
			"cluster %s: an apiserver answers at %s and this process holds no group for it; "+
				"left alone (spawning over it would be two servers on one advertise port)",
			p.Ref.Name, p.Cluster.Status.URL)
	case p.ObserveOnly:
		return fmt.Sprintf("cluster %s: observed only, no process lifecycle attached", p.Ref.Name)
	default:
		return fmt.Sprintf("cluster %s: phase %s", p.Ref.Name, p.Result.Phase)
	}
}

func New(opts Options) (*Provider, error) {
	if opts.Registry == nil {
		return nil, errors.New("clusterprovider: Registry is required")
	}
	if opts.Decider == nil {
		return nil, ErrNoDecider
	}
	if opts.Probe == nil {
		opts.Probe = Probe
	}
	if opts.Interval == 0 {
		opts.Interval = DefaultInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.TokenTimeout == 0 {
		opts.TokenTimeout = DefaultTokenTimeout
	}
	tenant := 0
	for _, set := range []bool{opts.Tenant != nil, opts.GrantPath != "", opts.MintDir != ""} {
		if set {
			tenant++
		}
	}
	if tenant != 0 && tenant != 3 {
		return nil, ErrNoBootstrap
	}
	p := &Provider{opts: opts, mints: map[string]*rest.Config{}, readySatisfied: map[string]bool{}}
	if tenant == 3 && opts.OnReady == nil {
		p.opts.OnReady = p.Bootstrap
	}
	return p, nil
}

func (p *Provider) MintConfig(cluster string) (*rest.Config, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cfg, ok := p.mints[cluster]
	if !ok || cfg == nil {
		return nil, fmt.Errorf("%w: %q", ErrNoMintConfig, cluster)
	}
	return rest.CopyConfig(cfg), nil
}

func (p *Provider) SetMintConfig(cluster string, cfg *rest.Config) error {
	if cluster == "" || cfg == nil {
		return errors.New("clusterprovider: SetMintConfig needs a cluster name and a config")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mints[cluster] = rest.CopyConfig(cfg)
	return nil
}

func (p *Provider) MintClusters() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.mints))
	for name := range p.mints {
		out = append(out, name)
	}
	return out
}

type Established struct {
	URL string

	Instance string
}

func APIServerURL(port int) string {
	return fmt.Sprintf("https://127.0.0.1:%d", port)
}

func (p *Provider) Reconcile(ctx context.Context, ref Ref) (Pass, error) {
	return p.reconcile(ctx, ref, nil)
}

func (p *Provider) Joined(ctx context.Context, ref Ref, at Established) (Pass, error) {
	if at.URL == "" {
		return Pass{Ref: ref}, errors.New(
			"clusterprovider: Joined needs the address the apiserver answered at; a Ready with " +
				"an empty status.URL is the hard stop that loops the reconcile forever (K2)")
	}
	return p.reconcile(ctx, ref, func(o *materialiser.Observed) {
		o.Cluster.Status.URL = at.URL
		o.Cluster.Status.Instance = at.Instance
		_, o.Reachable = p.opts.Probe(ctx, at.URL)
	})
}

func (p *Provider) reconcile(ctx context.Context, ref Ref, with func(*materialiser.Observed)) (Pass, error) {
	cl, err := p.opts.Registry.Read(ctx, ref)
	if err != nil {
		return Pass{Ref: ref}, err
	}
	if err := p.reportNodeExpiry(ctx, ref, cl); err != nil {
		return Pass{Ref: ref}, err
	}
	o, err := p.observe(ctx, ref, cl)
	if err != nil {
		return Pass{Ref: ref, Cluster: cl}, err
	}
	if with != nil {
		with(&o)
	}
	pass := Pass{Ref: ref, Cluster: cl, Observed: o}

	if cl.Status.Phase == v1alpha1.ClusterDraining && cl.DeletionTimestamp == nil {
		pass.Ratchet = true
		pass.Result = carryStatus(cl)
		return pass, nil
	}

	if p.opts.Host == nil {
		pass.ObserveOnly = true
		pass.Result = carryStatus(cl)
		return pass, nil
	}
	if !p.opts.Host.Live(o.Cluster.Name) && o.Reachable && cl.Status.URL != "" {
		pass.Foreign = true
		pass.Result = carryStatus(cl)
		return pass, nil
	}

	res, err := p.opts.Decider.Reconcile(ctx, o)
	if err != nil {
		return pass, err
	}
	pass.Result = res
	if !p.opts.WriteStatus {
		return pass, nil
	}
	if err := p.opts.Registry.WriteStatus(ctx, ref, statusOf(cl, res)); err != nil {
		pass.Err = err
		return pass, err
	}
	pass.Wrote = true

	if res.RemoveFinalizer {
		if err := p.opts.Registry.RemoveFinalizer(ctx, ref); err != nil {
			pass.Err = err
			return pass, err
		}
		pass.Unfinalized = true
	}

	if res.Phase == readyPhase && p.opts.OnReady != nil && !p.readyDone(o.Cluster.Name) {
		after := cl.DeepCopy()
		after.Status = statusOf(cl, res)
		if err := p.opts.OnReady(ctx, ref, after); err != nil {
			pass.Err = err
			return pass, err
		}
		p.markReadyDone(o.Cluster.Name)
	}
	return pass, nil
}

func (p *Provider) readyDone(cluster string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.readySatisfied[cluster]
}

func (p *Provider) markReadyDone(cluster string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.readySatisfied == nil {
		p.readySatisfied = map[string]bool{}
	}
	p.readySatisfied[cluster] = true
}

func (p *Provider) reportNodeExpiry(ctx context.Context, ref Ref, cl *v1alpha1.Cluster) error {
	if p.opts.NodeExpirySource == nil {
		return nil
	}
	nodes, err := p.opts.Registry.Nodes(ctx, ref)
	if err != nil {
		return err
	}
	expiry, reported := p.opts.NodeExpirySource(cl, nodes)
	if !reported {
		cl.Status.NodeExpiry = nil
		return nil
	}
	cl.Status.NodeExpiry = &expiry
	return nil
}

func (p *Provider) Observe(ctx context.Context, ref Ref) (materialiser.Observed, error) {
	cl, err := p.opts.Registry.Read(ctx, ref)
	if err != nil {
		return materialiser.Observed{}, err
	}
	return p.observe(ctx, ref, cl)
}

func (p *Provider) observe(ctx context.Context, ref Ref, cl *v1alpha1.Cluster) (materialiser.Observed, error) {
	key, err := ref.Key()
	if err != nil {
		return materialiser.Observed{}, err
	}
	o := materialiser.Observed{
		Cluster:    *cl,
		PathStatus: p.opts.Registry.MountStatus(ctx, ref),
		Now:        p.opts.Now(),
	}
	o.Cluster.Name = key
	if cl.Status.URL != "" {
		_, o.Reachable = p.opts.Probe(ctx, cl.Status.URL)
	}
	state, err := p.opts.Registry.IdentityState(ctx, ref)
	if err != nil {
		return o, err
	}
	o.IdentityState = state

	if state == v1alpha1.IdentityRevocationPending {
		return o, errors.New(
			"clusterprovider: the account's identity is RevocationPending and whether its " +
				"ServiceAccount still exists is only observable through the tenant cluster's " +
				"credential, which this build does not have; refusing to claim Ready over a " +
				"deny that may not have landed")
	}
	return o, nil
}

func (p *Provider) Run(ctx context.Context) error {
	for {
		refs, err := p.opts.Registry.Refs(ctx)
		if err != nil {
			return err
		}
		p.reclaim(refs)
		for _, ref := range refs {
			pass, err := p.Reconcile(ctx, ref)
			if err != nil {
				p.opts.Log.Error("reconcile failed", "cluster", ref.Name, "home", ref.Home, "err", err)
				continue
			}
			p.opts.Log.Info("reconciled",
				"cluster", ref.Name,
				"home", ref.Home,
				"did", ref.DID,
				"running", pass.Cluster.Spec.Running,
				"phase", pass.Result.Phase,
				"url", pass.Result.URL,
				"foreign", pass.Foreign,
				"observeOnly", pass.ObserveOnly,
				"wrote", pass.Wrote,
				"alert", pass.Alert(),
			)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(p.opts.Interval):
		}
	}
}

func (p *Provider) Reclaim(refs []Ref) { p.reclaim(refs) }

func (p *Provider) reclaim(refs []Ref) {
	if p.opts.Ports == nil || len(refs) == 0 {
		return
	}
	known := make(map[string]bool, len(refs))
	for _, ref := range refs {
		key, err := ref.Key()
		if err != nil {
			p.opts.Log.Error("portallocator: a Cluster CR has no derivable key, so its leases "+
				"cannot be told from leases whose CR is gone", "cluster", ref.Name, "home", ref.Home,
				"err", err.Error())
			return
		}
		known[key] = true
	}
	freed, err := p.opts.Ports.Reclaim(known)
	if err != nil {
		p.opts.Log.Error("portallocator: the reclaim pass failed; leases with no Cluster CR are still held",
			"err", err.Error())
		return
	}
	if len(freed) == 0 {
		return
	}
	held := make([]string, 0, len(freed))
	for _, l := range freed {
		held = append(held, fmt.Sprintf("%s/%s:%d", l.Cluster, l.Purpose, l.Port))
	}
	p.opts.Log.Info("portallocator: reclaimed leases whose Cluster CR is absent",
		"refs", len(refs), "freed", strings.Join(held, " "))
}

func carryStatus(cl *v1alpha1.Cluster) materialiser.Result {
	return materialiser.Result{
		Phase:      cl.Status.Phase,
		URL:        cl.Status.URL,
		Instance:   cl.Status.Instance,
		Conditions: cl.Status.Conditions,
	}
}

func statusOf(cl *v1alpha1.Cluster, res materialiser.Result) v1alpha1.ClusterStatus {
	return v1alpha1.ClusterStatus{
		Phase:      res.Phase,
		URL:        res.URL,
		Instance:   res.Instance,
		NodeExpiry: cl.Status.NodeExpiry,
		Conditions: res.Conditions,
	}
}

type clusterStatusPatch struct {
	Phase      v1alpha1.ClusterPhase `json:"phase,omitempty"`
	URL        string                `json:"URL,omitempty"`
	Instance   string                `json:"instance,omitempty"`
	NodeExpiry *int64                `json:"nodeExpiry"`
	Conditions []metav1.Condition    `json:"conditions,omitempty"`
}

func statusPatch(st v1alpha1.ClusterStatus) ([]byte, error) {
	body, err := json.Marshal(map[string]any{"status": clusterStatusPatch{
		Phase:      st.Phase,
		URL:        st.URL,
		Instance:   st.Instance,
		NodeExpiry: st.NodeExpiry,
		Conditions: st.Conditions,
	}})
	if err != nil {
		return nil, fmt.Errorf("clusterprovider: status patch: %w", err)
	}
	return body, nil
}
