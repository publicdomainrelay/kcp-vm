//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scaledriver"
)

type scaleInFixture struct {
	claims   *marketClaims
	clock    *lossClock
	secrets  *recordedSecrets
	hosts    *nodeHosts
	client   *fake.Clientset
	driver   *scaledriver.Driver
	key      string
	start    time.Time
	mu       sync.Mutex
	evicted  []string
	cordoned []string
}

func (f *scaleInFixture) node(i int) string {
	return f.key + "-node" + string(rune('0'+i))
}

func newScaleInFixture(t *testing.T) *scaleInFixture {
	t.Helper()
	ctx := context.Background()
	f := &scaleInFixture{claims: &marketClaims{}, secrets: &recordedSecrets{},
		clock: &lossClock{now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}}
	f.start = f.clock.Now()
	tenant, client := tenantClientFor(t)
	f.client = client
	observer := clusterprovider.NodeObserver{Client: tenant, Operators: scale.OurOperators}
	opts := marketOptions(t, f.claims, tenant)
	opts.Now = f.clock.Now
	opts.NodeSecrets = f.secrets.factory
	opts.Links = listeningLinks{ticket: "apiserver-ticket"}
	opts.Observe = observer.Observe
	hosts, err := newNodeHosts(opts)
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	f.hosts = hosts
	f.key = clusterKey(t, "liveweb")
	clusterDir, err := clusterprovider.ClusterDataDir(opts.StateDir, f.key)
	if err != nil {
		t.Fatalf("ClusterDataDir: %v", err)
	}
	for path, content := range map[string]string{
		clusterprovider.TokenPath(clusterDir):                           "server-token",
		filepath.Join(clusterstack.TLSDir(clusterDir), "server-ca.crt"): "server-ca",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("creating %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
	nodes, err := hosts.forCluster(f.key)
	if err != nil {
		t.Fatalf("forCluster: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if err := nodes.Provision(ctx); err != nil {
			t.Fatalf("Provision %d: %v", i, err)
		}
	}
	for i := 1; i <= 3; i++ {
		name := f.node(i)
		f.claims.setPhase(testHomePrefix+"liveweb", name, v1alpha1.NodeClaimRunning)
		_, err := client.CoreV1().Nodes().Create(ctx, &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(f.start.Add(time.Duration(i) * time.Second))},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
				Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("registering %s: %v", name, err)
		}
	}
	client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		create := action.(k8stesting.CreateAction)
		eviction := create.GetObject().(interface{ GetName() string })
		f.mu.Lock()
		f.evicted = append(f.evicted, action.GetNamespace()+"/"+eviction.GetName())
		f.mu.Unlock()
		err := client.Tracker().Delete(schema.GroupVersionResource{Version: "v1", Resource: "pods"},
			action.GetNamespace(), eviction.GetName())
		return true, nil, err
	})
	client.PrependReactor("update", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		n := action.(k8stesting.UpdateAction).GetObject().(*corev1.Node)
		if n.Spec.Unschedulable {
			f.mu.Lock()
			f.cordoned = append(f.cordoned, n.Name)
			f.mu.Unlock()
		}
		return false, nil, nil
	})
	f.driver, err = scaledriver.New(hosts.forCluster, scaledriver.Options{
		Now:     f.clock.Now,
		Observe: hosts.observeHeld(observer.Observe),
		Log:     testLogger(),
	})
	if err != nil {
		t.Fatalf("scaledriver.New: %v", err)
	}
	return f
}

func (f *scaleInFixture) pod(t *testing.T, p *corev1.Pod) {
	t.Helper()
	if _, err := f.client.CoreV1().Pods(p.Namespace).Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating pod %s: %v", p.Name, err)
	}
}

func (f *scaleInFixture) present(t *testing.T, name string) bool {
	t.Helper()
	_, err := f.client.CoreV1().Nodes().Get(context.Background(), name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false
	}
	if err != nil {
		t.Fatalf("reading node %s: %v", name, err)
	}
	return true
}

func (f *scaleInFixture) claimed(name string) bool {
	claim, _ := f.claims.ReadNodeClaim(context.Background(), testHomePrefix+"liveweb", name)
	return claim != nil
}

func (f *scaleInFixture) pass(t *testing.T, after time.Duration) scale.Decision {
	t.Helper()
	f.clock.set(f.start.Add(after))
	dec, err := f.driver.RunPass(context.Background(), f.key)
	if err != nil {
		t.Fatalf("pass at %s: %v", after, err)
	}
	return dec
}

func controlled(kind, name string) []metav1.OwnerReference {
	yes := true
	return []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: kind, Name: name, Controller: &yes}}
}

func TestIdleMarketNodesAreCordonedDrainedAndReleasedOnePerHold(t *testing.T) {
	f := newScaleInFixture(t)
	f.pod(t, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "work"},
		Spec: corev1.PodSpec{NodeName: f.node(1)}, Status: corev1.PodStatus{Phase: corev1.PodRunning}})
	f.pod(t, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "obs", Name: "agent-2", OwnerReferences: controlled("DaemonSet", "agent")},
		Spec: corev1.PodSpec{NodeName: f.node(2)}, Status: corev1.PodStatus{Phase: corev1.PodRunning}})
	coredns := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "coredns-x"},
		Spec: corev1.PodSpec{NodeName: f.node(3)}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	scale.StampAppliedByTheControlPlane(&coredns.ObjectMeta)
	f.pod(t, coredns)

	if dec := f.pass(t, 0); dec.Action != scale.None {
		t.Fatalf("first sight decided %s: %s", dec.Action, dec.Reason)
	}
	if dec := f.pass(t, scale.DefaultHold-time.Second); dec.Action != scale.None {
		t.Fatalf("inside the hold decided %s %q", dec.Action, dec.Release)
	}

	dec := f.pass(t, scale.DefaultHold)
	if dec.Action != scale.Release || dec.Release != f.node(2) {
		t.Fatalf("past the hold decided %s %q, want Release %s (emptiest: only a DaemonSet pod): %s",
			dec.Action, dec.Release, f.node(2), dec.Reason)
	}
	if !slices.Equal(f.cordoned, []string{f.node(2)}) || len(f.evicted) != 0 {
		t.Fatalf("cordoned %v and evicted %v, want %s cordoned and its DaemonSet pod left alone",
			f.cordoned, f.evicted, f.node(2))
	}
	if f.claimed(f.node(2)) || f.present(t, f.node(2)) || !slices.Equal(f.secrets.revokes, []string{f.node(2)}) {
		t.Fatalf("%s: claim %v, Node %v, revokes %v; want the claim deleted (the door releases with "+
			"vm.delete), the Node deleted and the vault state revoked", f.node(2), f.claimed(f.node(2)),
			f.present(t, f.node(2)), f.secrets.revokes)
	}

	if dec := f.pass(t, scale.DefaultHold+10*time.Second); dec.Action != scale.None {
		t.Fatalf("right after a release decided %s %q; the hold restarts for the nodes left", dec.Action, dec.Release)
	}
	dec = f.pass(t, 2*scale.DefaultHold)
	if dec.Action != scale.Release || dec.Release != f.node(3) {
		t.Fatalf("one hold later decided %s %q, want Release %s: %s", dec.Action, dec.Release, f.node(3), dec.Reason)
	}
	if !slices.Equal(f.evicted, []string{"kube-system/coredns-x"}) {
		t.Fatalf("evicted %v, want the k3s add-on pod on %s", f.evicted, f.node(3))
	}
	if f.claimed(f.node(3)) || f.present(t, f.node(3)) {
		t.Fatalf("%s was not released", f.node(3))
	}

	for _, after := range []time.Duration{3 * scale.DefaultHold, 10 * scale.DefaultHold} {
		if dec := f.pass(t, after); dec.Action != scale.None {
			t.Fatalf("at %s decided %s %q; %s runs a tenant pod and the target is 1", after, dec.Action,
				dec.Release, f.node(1))
		}
	}
	if !f.claimed(f.node(1)) || !f.present(t, f.node(1)) || f.claims.deletes != 2 {
		t.Fatalf("%s: claim %v Node %v deletes %d; the node running work must be kept", f.node(1),
			f.claimed(f.node(1)), f.present(t, f.node(1)), f.claims.deletes)
	}
}

func TestANodeThatTakesWorkBeforeTheCordonIsUncordonedAndKept(t *testing.T) {
	f := newScaleInFixture(t)
	f.pod(t, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "work"},
		Spec: corev1.PodSpec{NodeName: f.node(1)}, Status: corev1.PodStatus{Phase: corev1.PodRunning}})
	f.pass(t, 0)
	late := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "late"},
		Spec: corev1.PodSpec{NodeName: f.node(3)}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	f.client.PrependReactor("update", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		n := action.(k8stesting.UpdateAction).GetObject().(*corev1.Node)
		if n.Spec.Unschedulable && n.Name == f.node(3) {
			_ = f.client.Tracker().Add(late)
		}
		return false, nil, nil
	})
	f.clock.set(f.start.Add(scale.DefaultHold))
	dec, err := f.driver.RunPass(context.Background(), f.key)
	if dec.Action != scale.Release || dec.Release != f.node(3) || err == nil {
		t.Fatalf("decided %s %q with err %v, want a Release of %s that is refused", dec.Action, dec.Release, err, f.node(3))
	}
	n, getErr := f.client.CoreV1().Nodes().Get(context.Background(), f.node(3), metav1.GetOptions{})
	if getErr != nil || n.Spec.Unschedulable {
		t.Fatalf("%s after the refused release: %v unschedulable=%v, want it present and uncordoned",
			f.node(3), getErr, n != nil && n.Spec.Unschedulable)
	}
	if !f.claimed(f.node(3)) || f.claims.deletes != 0 || len(f.evicted) != 0 {
		t.Fatalf("the refused release deleted %d claims and evicted %v", f.claims.deletes, f.evicted)
	}
}

func TestARefusedEvictionKeepsTheNode(t *testing.T) {
	f := newScaleInFixture(t)
	f.pod(t, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "work"},
		Spec: corev1.PodSpec{NodeName: f.node(1)}, Status: corev1.PodStatus{Phase: corev1.PodRunning}})
	for i := 2; i <= 3; i++ {
		guarded := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "guarded-" + string(rune('0'+i))},
			Spec: corev1.PodSpec{NodeName: f.node(i)}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
		scale.StampAppliedByTheControlPlane(&guarded.ObjectMeta)
		f.pod(t, guarded)
	}
	f.client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		return true, nil, apierrors.NewTooManyRequests("Cannot evict pod as it would violate the pod's disruption budget.", 10)
	})
	f.pass(t, 0)
	f.clock.set(f.start.Add(scale.DefaultHold))
	dec, err := f.driver.RunPass(context.Background(), f.key)
	if dec.Action != scale.Release || err == nil {
		t.Fatalf("decided %s with err %v, want a Release refused by the disruption budget", dec.Action, err)
	}
	n, getErr := f.client.CoreV1().Nodes().Get(context.Background(), dec.Release, metav1.GetOptions{})
	if getErr != nil || n.Spec.Unschedulable || !f.claimed(dec.Release) || f.claims.deletes != 0 {
		t.Fatalf("after a refused eviction %s: %v unschedulable=%v claimed=%v deletes=%d; want it kept and uncordoned",
			dec.Release, getErr, n != nil && n.Spec.Unschedulable, f.claimed(dec.Release), f.claims.deletes)
	}
}
