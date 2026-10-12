package appctl

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpapply"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/manifests"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/stack/denoruntime"
)

const (
	NetworkSet          = "market-network"
	NetworkProfile      = "market-network"
	BidderAccountDomain = "bob.test"
	CABundleFile        = "ca-bundle.pem"
	NetworkServiceState = "state"
)

type service struct {
	Workspace string
	Name      string
	Host      string
	Long      bool
}

var networkServices = []service{
	{"global", "plc", "plc.default.global.svc.kcp.local", true},
	{"relay", "relay", "relay.default.relay.svc.kcp.local", true},
	{"relay", "dispatcher", "dispatcher.default.relay.svc.kcp.local", true},
	{"relay", "dispatcher-tls", "", true},
	{"alice", "pds", "pds.default.alice.svc.kcp.local", true},
	{"bob", "pds", "pds.default.bob.svc.kcp.local", true},
	{"alice", "verifier", "", false},
}

type networks struct {
	ctl        *Controller
	mu         sync.Mutex
	applied    map[string]int64
	frontMoved sync.Map
}

func newNetworks(ctl *Controller) *networks {
	return &networks{ctl: ctl, applied: map[string]int64{}}
}

func (n *networks) values(spec v1alpha1.MarketNetworkSpec) (manifests.MarketNetwork, error) {
	dir := NetworkStateDir(n.ctl.c.Layout.StateDir)
	alice, err := EnsureHexKey(filepath.Join(dir, keysDir, "alice-pds.hex"))
	if err != nil {
		return manifests.MarketNetwork{}, err
	}
	bob, err := EnsureHexKey(filepath.Join(dir, keysDir, "bob-pds.hex"))
	if err != nil {
		return manifests.MarketNetwork{}, err
	}
	guest := filepath.Join(dir, GuestTLSDir)
	if err := EnsureGuestTLS(guest); err != nil {
		return manifests.MarketNetwork{}, err
	}
	label := spec.GuestLabel
	if label == "" {
		label = n.ctl.c.Layout.ID
	}
	return manifests.MarketNetwork{NetworkID: label, StateDir: filepath.Join(dir, NetworkServiceState), GuestTLSDir: guest,
		PDSPrivateKeyHex: alice, BobPDSPrivateKeyHex: bob}, nil
}

func (n *networks) applyServices(ctx context.Context, owner string, values *manifests.MarketNetwork) error {
	ix, err := manifests.Load()
	if err != nil {
		return err
	}
	set, ok := ix.Set(NetworkSet)
	if !ok {
		return fmt.Errorf("appctl: deploy/sets.yaml names no %s set", NetworkSet)
	}
	ap, err := kcpapply.New(n.ctl.restCfg, kcpapply.Options{WaitTimeout: ApplyTimeout, FieldManager: FieldManager,
		Labels: map[string]string{OwnerLabel: owner}, Log: n.ctl.c.log()})
	if err != nil {
		return err
	}
	_, err = ap.Apply(ctx, set, values)
	return err
}

func (n *networks) services(ctx context.Context) (map[string]podView, []v1alpha1.ApplicationComponent, []v1alpha1.ApplicationListener, bool, error) {
	views := map[string]podView{}
	var comps []v1alpha1.ApplicationComponent
	var ls []v1alpha1.ApplicationListener
	ready := true
	for _, s := range networkServices {
		ws := n.ctl.c.workspace() + ":" + s.Workspace
		v, found, err := n.ctl.pod(ctx, ws, s.Name)
		if err != nil {
			return nil, nil, nil, false, err
		}
		views[s.Workspace+"/"+s.Name] = v
		comps = append(comps, v.component())
		for _, l := range v.Listeners {
			l.Component = s.Workspace + "/" + s.Name
			ls = append(ls, l)
		}
		if s.Long && (!found || !v.Ready) {
			ready = false
		}
	}
	return views, comps, ls, ready, nil
}

func portFrom(address string) (string, error) {
	_, p, err := net.SplitHostPort(address)
	if err != nil || p == "" || p == "0" {
		return "", fmt.Errorf("no bound port in %q", address)
	}
	return p, nil
}

func NetworkValue(views map[string]podView, caBundle string) (v1alpha1.MarketNetworkValue, error) {
	ports := map[string]string{}
	for _, k := range []string{"global/plc", "relay/relay", "relay/dispatcher", "relay/dispatcher-tls"} {
		p, err := portFrom(views[k].Address)
		if err != nil {
			return v1alpha1.MarketNetworkValue{}, fmt.Errorf("%s: %w", k, err)
		}
		ports[k] = p
	}
	var hosts []v1alpha1.NetworkHost
	for _, s := range networkServices {
		if v := views[s.Workspace+"/"+s.Name]; s.Host != "" && v.Address != "" {
			hosts = append(hosts, v1alpha1.NetworkHost{Name: s.Host, Address: v.Address})
		}
	}
	return v1alpha1.MarketNetworkValue{
		Hosts:            hosts,
		PlcDirectoryUrl:  "https://127.0.0.1:" + ports["global/plc"],
		IngressProxyHost: GuestTLSHost + ":" + ports["relay/dispatcher"],
		RelayUrls:        []string{"https://127.0.0.1:" + ports["relay/relay"]},
		FirehoseUrls:     []string{},
		QrOrigin:         "",
		GuestTlsUrl:      "https://" + GuestTLSHost + ":" + ports["relay/dispatcher-tls"],
		CaBundleRef:      caBundle,
	}, nil
}

func (n *networks) caBundle(ctx context.Context, ob denoruntime.OpenBao) (string, error) {
	dir := NetworkStateDir(n.ctl.c.Layout.StateDir)
	pki, err := pkiCA(ctx, ob)
	if err != nil {
		return "", err
	}
	guest, err := os.ReadFile(filepath.Join(dir, GuestTLSDir, guestCAFile))
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, CABundleFile)
	want := strings.TrimSpace(string(pki)) + "\n" + strings.TrimSpace(string(guest)) + "\n"
	if have, err := os.ReadFile(path); err == nil && string(have) == want {
		return path, nil
	}
	return path, os.WriteFile(path, []byte(want), 0o644)
}

func (n *networks) deleteServices(ctx context.Context, owner string) (int, error) {
	left := 0
	seen := map[string]bool{}
	for _, s := range networkServices {
		ws := n.ctl.c.workspace() + ":" + s.Workspace
		if seen[ws] {
			continue
		}
		seen[ws] = true
		l, err := n.ctl.deletePods(ctx, ws, owner, nil)
		if err != nil && !apierrors.IsNotFound(err) {
			return left, err
		}
		left += l
	}
	return left, nil
}

func (n *networks) knownChildren() []string {
	entries, err := os.ReadDir(devinstance.DevRoot(filepath.Join(n.ctl.c.Layout.StateDir, AppsDir)))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

func (n *networks) childByID(id string) child {
	name := strings.TrimPrefix(id, n.ctl.c.Layout.ID+"-")
	c, err := n.child(v1alpha1.NetworkApp{Name: name})
	if err != nil {
		l, _ := devinstance.NewLayout(id, filepath.Join(n.ctl.c.Layout.StateDir, AppsDir), filepath.Join(n.ctl.c.Layout.RuntimeDir, AppsDir))
		return child{id: id, layout: l}
	}
	return c
}

func (n *networks) teardownAll(ctx context.Context, keep []string) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []string
	for _, id := range n.knownChildren() {
		if slices.Contains(keep, id) {
			continue
		}
		wg.Add(1)
		go func(c child) {
			defer wg.Done()
			if err := n.teardownChild(ctx, c); err != nil {
				mu.Lock()
				errs = append(errs, c.id+": "+err.Error())
				mu.Unlock()
			}
		}(n.childByID(id))
	}
	wg.Wait()
	if len(errs) > 0 {
		return fmt.Errorf("tearing children down: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (n *networks) reconcile(ctx context.Context, u *unstructured.Unstructured) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	var o v1alpha1.MarketNetwork
	if err := fromUnstructured(u, &o); err != nil {
		return err
	}
	owner := ownerOf(u)
	log := n.ctl.c.log().With("marketnetwork", o.Name)
	st := v1alpha1.MarketNetworkStatus{ObservedGeneration: o.Generation, Network: o.Status.Network, Apps: o.Status.Apps}
	write := func(phase v1alpha1.ApplicationPhase, msg string) error {
		st.Phase, st.Message = phase, msg
		return n.ctl.writeStatus(ctx, MarketNetworkGVR, o.Name, st)
	}
	if u.GetDeletionTimestamp() != nil {
		write(v1alpha1.ApplicationPhaseDeleting, "tearing down the child applications, then the network's services")
		if err := n.teardownAll(ctx, nil); err != nil {
			return write(v1alpha1.ApplicationPhaseDeleting, err.Error())
		}
		left, err := n.deleteServices(ctx, owner)
		if err != nil {
			return err
		}
		if left > 0 {
			return write(v1alpha1.ApplicationPhaseDeleting, fmt.Sprintf("%d service(s) stopping", left))
		}
		if err := n.deleteOpenBao(ctx); err != nil {
			return err
		}
		delete(n.applied, string(o.UID))
		log.Info("the market network is gone: children, services and its OpenBao")
		return n.ctl.setFinalizer(ctx, MarketNetworkGVR, u, false)
	}
	if !hasFinalizer(u) {
		if err := n.ctl.setFinalizer(ctx, MarketNetworkGVR, u, true); err != nil {
			return err
		}
	}
	if o.Spec.Profile != NetworkProfile {
		return write(v1alpha1.ApplicationPhaseFailed, fmt.Sprintf("profile %q: the built MarketNetwork profile is %s", o.Spec.Profile, NetworkProfile))
	}
	ob, err := n.ensureOpenBao(ctx)
	if err != nil {
		return write(v1alpha1.ApplicationPhaseProvisioning, "the network's OpenBao: "+err.Error())
	}
	if n.applied[string(o.UID)] != o.Generation {
		values, err := n.values(o.Spec)
		if err != nil {
			return write(v1alpha1.ApplicationPhaseFailed, err.Error())
		}
		write(v1alpha1.ApplicationPhaseProvisioning, "applying the network's services")
		if err := n.applyServices(ctx, owner, &values); err != nil {
			return write(v1alpha1.ApplicationPhaseProvisioning, "the network's services: "+err.Error())
		}
		n.applied[string(o.UID)] = o.Generation
		log.Info("applied the network's services", "set", NetworkSet)
	}
	views, comps, ls, servicesReady, err := n.services(ctx)
	if err != nil {
		return err
	}
	st.Components, st.Listeners = comps, ls
	if !servicesReady {
		return write(v1alpha1.ApplicationPhaseProvisioning, "waiting for the network's services to report ready")
	}
	bundle, err := n.caBundle(ctx, ob)
	if err != nil {
		return write(v1alpha1.ApplicationPhaseProvisioning, "the network CA bundle: "+err.Error())
	}
	value, err := NetworkValue(views, bundle)
	if err != nil {
		return write(v1alpha1.ApplicationPhaseProvisioning, err.Error())
	}
	st.Network = &value
	bob := views["bob/pds"]
	pdsPort, err := portFrom(bob.Address)
	if err != nil {
		return write(v1alpha1.ApplicationPhaseProvisioning, "bob/pds: "+err.Error())
	}
	pdsURL := "https://127.0.0.1:" + pdsPort
	var keep []string
	for _, app := range o.Spec.Apps {
		if c, err := n.child(app); err == nil {
			keep = append(keep, c.id)
		}
	}
	if err := n.teardownAll(ctx, keep); err != nil {
		log.Warn("removing children the spec no longer names", "err", err.Error())
	}
	var bidderDids []string
	for _, a := range o.Status.Apps {
		if a.Kind == v1alpha1.ApplicationKindMarketBidder && a.Did != "" {
			bidderDids = append(bidderDids, a.Did)
		}
	}
	apps := make([]v1alpha1.NetworkAppStatus, len(o.Spec.Apps))
	var wg sync.WaitGroup
	for i, app := range o.Spec.Apps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			apps[i] = n.ensureApp(ctx, app, value, pdsURL, bidderDids)
		}()
	}
	wg.Wait()
	st.Apps = apps
	allReady := true
	for i, a := range apps {
		if o.Spec.Apps[i].Kind == v1alpha1.ApplicationKindMarketRequester {
			continue
		}
		allReady = allReady && a.Phase == v1alpha1.ApplicationPhaseReady
	}
	if !allReady {
		return write(v1alpha1.ApplicationPhaseProvisioning, "the network's services are ready; waiting for its child applications")
	}
	return write(v1alpha1.ApplicationPhaseReady, fmt.Sprintf("%d service(s) and %d child application(s), each with its own kcp, are ready", len(comps), len(apps)))
}

func (n *networks) ensureApp(ctx context.Context, app v1alpha1.NetworkApp, value v1alpha1.MarketNetworkValue, pdsURL string, bidderDids []string) v1alpha1.NetworkAppStatus {
	out := v1alpha1.NetworkAppStatus{Name: app.Name, Kind: app.Kind, Phase: v1alpha1.ApplicationPhaseProvisioning}
	fail := func(err error) v1alpha1.NetworkAppStatus {
		out.Message = err.Error()
		n.ctl.c.log().Warn("a child application is not ready yet", "app", app.Name, "err", err.Error())
		return out
	}
	c, err := n.child(app)
	if err != nil {
		out.Phase = v1alpha1.ApplicationPhaseFailed
		return fail(err)
	}
	out.Instance = c.id
	if app.Stopped {
		if err := n.stopChild(ctx, c); err != nil {
			return fail(err)
		}
		out.Phase, out.Message = v1alpha1.ApplicationPhasePending, StoppedMessage
		return out
	}
	if _, err := GVRFor(app.Kind); err != nil {
		out.Phase = v1alpha1.ApplicationPhaseFailed
		return fail(err)
	}
	rec, err := n.placeChild(ctx, c)
	if err != nil {
		return fail(err)
	}
	out.KcpUrl = rec.KcpURL
	for _, u := range rec.Units {
		if strings.HasPrefix(u.Role, "kcp-") {
			out.KcpRoot = filepath.Join(c.layout.KcpDir(), "slot-"+strings.TrimPrefix(u.Role, "kcp-"))
		}
	}
	meta := metav1.ObjectMeta{Name: ChildRootName}
	var root any
	switch app.Kind {
	case v1alpha1.ApplicationKindMarketBidder:
		handle := app.Name + "." + BidderAccountDomain
		var fc *v1alpha1.FirecrackerProvider
		if app.Firecracker != nil {
			f := *app.Firecracker
			fc = &f
		}
		var kcp *v1alpha1.KcpProvider
		if app.Kcp != nil {
			k := *app.Kcp
			k.Handle = KcpHandle(app)
			kcp = &k
			fc = nil
		}
		root = &v1alpha1.MarketBidder{ObjectMeta: meta, Spec: v1alpha1.MarketBidderSpec{Profile: app.Profile, Network: &value,
			Identity: v1alpha1.ApplicationIdentity{Mode: v1alpha1.MarketIdentityModePdsAccount, Handle: handle, PdsUrl: pdsURL},
			Policy:   app.Policy, Firecracker: fc, Kcp: kcp, Options: app.Options}}
	case v1alpha1.ApplicationKindMarketRequester:
		root = &v1alpha1.MarketRequester{ObjectMeta: meta, Spec: v1alpha1.MarketRequesterSpec{Profile: app.Profile, Network: &value,
			Identity:   v1alpha1.ApplicationIdentity{Mode: v1alpha1.MarketIdentityModeLocalRepo, Association: v1alpha1.AssociationSkip},
			BidderDids: bidderDids, Options: app.Options}}
	case v1alpha1.ApplicationKindGateway:
		root = &v1alpha1.Gateway{ObjectMeta: meta, Spec: v1alpha1.GatewaySpec{Profile: app.Profile, Network: &value, Options: app.Options}}
	}
	if err := applyRoot(ctx, rec, app.Kind, root); err != nil {
		return fail(fmt.Errorf("applying the root object into %s's own kcp: %w", c.id, err))
	}
	rs, found, err := readRoot(ctx, rec, app.Kind)
	if err != nil {
		return fail(err)
	}
	if !found {
		return fail(fmt.Errorf("the root object of %s is not readable yet", c.id))
	}
	out.Phase, out.Message, out.Did, out.Listeners = rs.Phase, rs.Message, rs.Did, rs.Listeners
	if out.Phase == "" {
		out.Phase = v1alpha1.ApplicationPhasePending
	}
	return out
}
