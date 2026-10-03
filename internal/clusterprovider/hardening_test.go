//go:build linux

package clusterprovider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/processrunner"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

func silent(action k8stesting.Action) (bool, runtime.Object, error) {
	switch action.GetResource().Resource {
	case "serviceaccounts":
		return true, &corev1.ServiceAccount{}, nil
	default:
		return true, &corev1.Namespace{}, nil
	}
}

func writes(cs *fake.Clientset) []string {
	var out []string
	for _, a := range cs.Actions() {
		switch a.GetVerb() {
		case "create", "update", "patch", "delete":
			out = append(out, a.GetVerb()+" "+a.GetResource().Resource)
		}
	}
	return out
}

func TestTheHardeningWritesThePSALabelsAndTheAutomountField(t *testing.T) {
	cs := fake.NewSimpleClientset()
	ctx := context.Background()

	if err := Harden(ctx, cs); err != nil {
		t.Fatalf("Harden on an empty cluster: %v", err)
	}

	for _, ns := range TenantNamespaces() {
		got, err := cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("namespace %s: %v", ns, err)
		}
		if v := got.Labels["pod-security.kubernetes.io/enforce"]; v != "baseline" {
			t.Errorf("namespace %s pod-security.kubernetes.io/enforce = %q, want %q -- baseline "+
				"ENFORCE is what blocks the host-level escape classes (privileged, hostNetwork, "+
				"hostPID, hostPath)", ns, v, "baseline")
		}
		if v := got.Labels["pod-security.kubernetes.io/warn"]; v != "restricted" {
			t.Errorf("namespace %s pod-security.kubernetes.io/warn = %q, want %q",
				ns, v, "restricted")
		}

		sa, err := cs.CoreV1().ServiceAccounts(ns).Get(ctx, DefaultServiceAccountName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("default ServiceAccount in %s: %v", ns, err)
		}
		if sa.AutomountServiceAccountToken == nil {
			t.Fatalf("the default ServiceAccount in %s has no automountServiceAccountToken "+
				"field, which is the cluster default (true): every pod gets an API token it did "+
				"not ask for", ns)
		}
		if *sa.AutomountServiceAccountToken {
			t.Fatalf("the default ServiceAccount in %s still automounts a token", ns)
		}
	}
}

func TestTheHardeningNamesTheNamespaceTheBootstrapGrantsInto(t *testing.T) {
	if got := TenantNamespaces(); len(got) != 1 || got[0] != kcpclient.TenantNamespace {
		t.Fatalf("the hardening labels %v, want exactly [%s]: a namespace nobody grants into is "+
			"hardened for nothing, and every namespace the tenant actually uses is left open",
			got, kcpclient.TenantNamespace)
	}
}

func TestTheHardeningRefusesAPSLabelWriteThatDidNotTake(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: kcpclient.TenantNamespace},
	})
	cs.PrependReactor("patch", "namespaces", silent)

	err := Harden(context.Background(), cs)
	if !errors.Is(err, ErrHardeningNotInForce) {
		t.Fatalf("a namespace whose labels never landed was accepted: %v\n\nThe write "+
			"SUCCEEDED and the label is absent, which is the shape that made the old provision "+
			"script half-harden a cluster with nothing in the path saying so", err)
	}
	if !errors.Is(err, ErrHardeningNotInForce) || !strings.Contains(err.Error(), PSAEnforceLabel) {
		t.Errorf("the refusal does not name the label that is missing: %v", err)
	}
}

func TestTheHardeningRefusesAnAutomountFieldThatIsAbsent(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: kcpclient.TenantNamespace, Labels: psaLabels()},
		},
		&corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: DefaultServiceAccountName, Namespace: kcpclient.TenantNamespace},
		},
	)
	cs.PrependReactor("update", "serviceaccounts", silent)

	err := Harden(context.Background(), cs)
	if !errors.Is(err, ErrHardeningNotInForce) {
		t.Fatalf("an absent automountServiceAccountToken was read as false: %v\n\nThe field is "+
			"a POINTER: absent means the cluster default, which is TRUE", err)
	}
	if !strings.Contains(err.Error(), "cluster default") {
		t.Errorf("the refusal does not say that absent means the cluster default: %v", err)
	}
}

func TestTheHardeningIsANoOpOnASecondPass(t *testing.T) {
	cs := fake.NewSimpleClientset()
	ctx := context.Background()

	if err := Harden(ctx, cs); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	before, err := cs.CoreV1().Namespaces().Get(ctx, kcpclient.TenantNamespace, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("namespace after the first pass: %v", err)
	}

	cs.ClearActions()
	if err := Harden(ctx, cs); err != nil {
		t.Fatalf("second pass on an already-hardened cluster: %v", err)
	}
	if got := writes(cs); len(got) != 0 {
		t.Fatalf("the second pass wrote %v, want nothing: this step runs on every bring-up, and "+
			"a write that changes nothing still churns resourceVersion and wakes every watcher",
			got)
	}
	after, err := cs.CoreV1().Namespaces().Get(ctx, kcpclient.TenantNamespace, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("namespace after the second pass: %v", err)
	}
	if after.ResourceVersion != before.ResourceVersion {
		t.Errorf("the namespace moved from resourceVersion %s to %s across a no-op pass",
			before.ResourceVersion, after.ResourceVersion)
	}
}

func stackLeases(apiserver int) materialiser.Leases {
	return materialiser.Leases{
		portallocator.PurposePostgres:                  {Port: 21001, Width: 1, Cluster: "liveweb", Purpose: portallocator.PurposePostgres},
		portallocator.PurposeKine:                      {Port: 21002, Width: 1, Cluster: "liveweb", Purpose: portallocator.PurposeKine},
		portallocator.PurposeK3sSecure:                 {Port: apiserver, Width: 1, Cluster: "liveweb", Purpose: portallocator.PurposeK3sSecure},
		portallocator.PurposeK3sControllerManager:      {Port: 21009, Width: 1, Cluster: "liveweb", Purpose: portallocator.PurposeK3sControllerManager},
		portallocator.PurposeK3sScheduler:              {Port: 21010, Width: 1, Cluster: "liveweb", Purpose: portallocator.PurposeK3sScheduler},
		portallocator.PurposeK3sCloudControllerManager: {Port: 21011, Width: 1, Cluster: "liveweb", Purpose: portallocator.PurposeK3sCloudControllerManager},
	}
}

func observed() materialiser.Observed {
	return materialiser.Observed{
		Cluster: v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "liveweb"}},
		Now:     time.Now(),
	}
}

func stackConfig(t *testing.T, admin AdminClient) StackConfig {
	t.Helper()
	return StackConfig{
		Stack:            ProvenStack,
		Admin:            admin,
		StateDir:         t.TempDir(),
		StoreEndpoint:    func(string) (string, error) { return "postgres://postgres@127.0.0.1:21001/k3s?sslmode=disable", nil },
		AdvertiseAddress: portallocator.LoopbackAdvertiseAddress,
		BindAddress:      portallocator.LoopbackAdvertiseAddress,
		AuditPolicyPath:  "/tmp/audit-policy.yaml",
	}
}

func gateOn(t *testing.T, steps []procgroup.Step) func(context.Context, *processrunner.Process) error {
	t.Helper()
	for _, s := range steps {
		if s.Name == procgroup.ComponentK3s {
			if s.Ready == nil {
				t.Fatal("the k3s step has no readiness gate")
			}
			return s.Ready
		}
	}
	t.Fatalf("no k3s step in %v", names(steps))
	return nil
}

func TestStepsForRefusesWithNoClientForTheClustersOwnApiserver(t *testing.T) {
	_, err := StepsFor(stackConfig(t, nil))(observed(), stackLeases(21003))
	if !errors.Is(err, ErrNoHardeningCredential) {
		t.Fatalf("the steps were built with no client for the cluster's own apiserver: %v\n\n"+
			"A hardening step that quietly does not run and one that runs and passes are "+
			"indistinguishable from a green suite", err)
	}
}

func TestStepsForRefusesAStackWithNoApiserverToHarden(t *testing.T) {
	cfg := stackConfig(t, func(context.Context, string, string) (kubernetes.Interface, error) {
		return fake.NewSimpleClientset(), nil
	})
	cfg.Stack = Stack{procgroup.ComponentKine}
	_, err := StepsFor(cfg)(observed(), stackLeases(21003))
	if !errors.Is(err, ErrNoHardeningSeam) {
		t.Fatalf("a stack with no k3s step produced steps with the hardening nowhere to hang: "+
			"%v\n\nThis is the check that makes the composition itself load-bearing: delete the "+
			"compose call and this test stops refusing", err)
	}
}

func TestTheHardeningRunsOnlyAfterTheApiserversOwnGate(t *testing.T) {
	gateOpened := false
	sabotaged := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: kcpclient.TenantNamespace},
	})
	sabotaged.PrependReactor("patch", "namespaces", silent)

	steps := []procgroup.Step{{
		Name: procgroup.ComponentK3s,
		Ready: func(context.Context, *processrunner.Process) error {
			gateOpened = true
			return nil
		},
	}}
	wrapped, err := hardenAfterK3s(steps, func(_ context.Context, cluster, apiserver string) (kubernetes.Interface, error) {
		if !gateOpened {
			t.Error("the hardening was handed a client before the apiserver's own gate opened; " +
				"nothing proves the apiserver is answering yet")
		}
		if cluster != "liveweb" {
			t.Errorf("the hardening was given cluster %q, want liveweb", cluster)
		}
		if apiserver != "https://127.0.0.1:21003" {
			t.Errorf("the hardening was given %q, want the advertise address the mount uses, "+
				"so the write cannot land on another cluster's apiserver", apiserver)
		}
		return sabotaged, nil
	}, "liveweb", "https://127.0.0.1:21003")
	if err != nil {
		t.Fatalf("hardenAfterK3s: %v", err)
	}
	err = gateOn(t, wrapped)(context.Background(), nil)
	if !errors.Is(err, ErrHardeningNotInForce) {
		t.Fatalf("an answering apiserver that would not take the hardening passed the k3s "+
			"step's gate: %v\n\nThe gate is the only place the cluster is held before anything "+
			"is claimed Ready, so a hardening failure that does not surface here surfaces nowhere",
			err)
	}

	steps = []procgroup.Step{{Name: procgroup.ComponentK3s, Ready: func(context.Context, *processrunner.Process) error { return nil }}}
	wrapped, err = hardenAfterK3s(steps, func(context.Context, string, string) (kubernetes.Interface, error) {
		return fake.NewSimpleClientset(), nil
	}, "liveweb", "https://127.0.0.1:21003")
	if err != nil {
		t.Fatalf("hardenAfterK3s: %v", err)
	}
	if err := gateOn(t, wrapped)(context.Background(), nil); err != nil {
		t.Fatalf("a cluster that took the hardening was refused: %v", err)
	}
}

func TestTheHardeningRefusesAClientItCannotBuild(t *testing.T) {
	refused := errors.New("no credential")
	steps := []procgroup.Step{{Name: procgroup.ComponentK3s, Ready: func(context.Context, *processrunner.Process) error { return nil }}}
	wrapped, err := hardenAfterK3s(steps, func(context.Context, string, string) (kubernetes.Interface, error) {
		return nil, refused
	}, "liveweb", "https://127.0.0.1:21003")
	if err != nil {
		t.Fatalf("hardenAfterK3s: %v", err)
	}
	if err := gateOn(t, wrapped)(context.Background(), nil); !errors.Is(err, refused) {
		t.Fatalf("a cluster whose admin client could not be built passed the k3s gate: %v", err)
	}
}
