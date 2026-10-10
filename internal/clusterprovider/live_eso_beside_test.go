//go:build linux

package clusterprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/esovault"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/liveports"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

const (
	envExternalSecretsBinary = "SOCIALWEB_EXTERNAL_SECRETS"
	envExternalSecretsBundle = "SOCIALWEB_ESO_BUNDLE"
	besideMarketAdvertise    = "198.18.0.7"
)

func externalSecretsInputs(t *testing.T) (string, string) {
	t.Helper()
	bin := os.Getenv(envExternalSecretsBinary)
	if bin == "" {
		if path, err := exec.LookPath("external-secrets"); err == nil {
			bin = path
		}
	}
	bundle := os.Getenv(envExternalSecretsBundle)
	if bin == "" || bundle == "" {
		livegate.Require(t, "the External Secrets binary (%s or external-secrets on PATH) and the pinned "+
			"upstream bundle (%s) are the artifacts this proof runs; the control-plane image carries "+
			"both, at /usr/local/bin/external-secrets and /opt/socialweb/eso/external-secrets.yaml",
			envExternalSecretsBinary, envExternalSecretsBundle)
	}
	return bin, bundle
}

func adminRest(t *testing.T, stateDir, cluster, apiserver string) (kubernetes.Interface, dynamic.Interface) {
	t.Helper()
	dataDir, err := ClusterDataDir(stateDir, cluster)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", clusterstack.AdminKubeconfig(dataDir))
	if err != nil {
		t.Fatalf("the admin kubeconfig: %v", err)
	}
	cfg.Host = apiserver
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return core, dyn
}

func TestAZeroNodeMarketClusterSyncsAnExternalSecretFromItsOwnVaultWithNoESOPodInTheTenant(t *testing.T) {
	skipWithoutK3sAndKine(t)
	skipWithoutBao(t)
	binary, bundle := externalSecretsInputs(t)
	ctx, cancel := context.WithTimeout(context.Background(), proofDeadline)
	defer cancel()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	stateDir := proofStateDir(t)
	const clusterName = "esobeside"
	const did = "did:plc:esobeside00000000000000"
	clusterKey := proofKey(t, clusterName)

	ports, err := liveports.TakeForProcess(liveports.ZeroNodeProof)
	if err != nil {
		t.Fatalf("%v: this proof binds a store, an apiserver and the External Secrets pair", err)
	}
	t.Logf("holding %s", liveports.Text(ports))
	alloc, err := portallocator.Open(portallocator.Config{
		LeasePath: filepath.Join(t.TempDir(), "leases.json"),
		Range:     ports,
	})
	if err != nil {
		t.Fatalf("portallocator.Open: %v", err)
	}
	pgLease, err := alloc.Allocate(portallocator.Request{Cluster: clusterKey, Purpose: portallocator.PurposePostgres})
	if err != nil {
		t.Fatalf("the store's lease: %v", err)
	}
	pg, err := StartPostgres(ctx, PostgresSpec{
		Cluster: clusterName, StateDir: filepath.Join(stateDir, "store"), Port: pgLease.Port, Log: log,
	})
	if err != nil {
		t.Fatalf("StartPostgres: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		_ = pg.Stop(stopCtx)
	})
	store := &proofStore{pg: pg}

	policy := filepath.Join(t.TempDir(), "audit-policy.yaml")
	if err := os.WriteFile(policy, []byte("apiVersion: audit.k8s.io/v1\nkind: Policy\nomitStages:\n  - RequestReceived\nrules:\n  - level: Metadata\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stack := append(Stack{}, DesignedStack...)
	stack = append(stack, ExternalSecretsComponents...)
	stackCfg := StackConfig{
		Stack:            stack,
		Admin:            adminFromDataDir(stateDir),
		StateDir:         stateDir,
		StoreEndpoint:    store.endpoint,
		VaultStoreDSN:    func(string) (string, error) { return store.pg.VaultDSN(), nil },
		AdvertiseAddress: besideMarketAdvertise,
		BindAddress:      portallocator.LoopbackAdvertiseAddress,
		MarketDialled:    true,
		VaultTLS:         testVaultTLS(),
		AuditPolicyPath:  policy,
		ExternalSecrets:  &ExternalSecrets{Binary: binary, Bundle: bundle, LegacyNamespace: "default"},
		Harvest: func(ctx context.Context, cluster string) error {
			if err := HarvestBundle(stateDir, cluster); err != nil {
				return err
			}
			port := 0
			if lease, ok := alloc.PortFor(cluster, portallocator.PurposeOpenBao); ok {
				port = lease.Port
			}
			return CustodyClusterVault(ctx, CustodyOptions{
				StateDir: stateDir, Cluster: cluster, VaultPort: port, PostgresDSN: store.pg.DSN(),
			})
		},
		GuardRides: true,
	}
	purposes, err := PurposesFor(stackCfg.Stack, true)
	if err != nil {
		t.Fatalf("PurposesFor: %v", err)
	}
	host := materialiser.NewLocalHost(procgroup.Options{})
	host.PostgresUp = func(string) bool { return pg.Up() }
	decider, err := materialiser.New(materialiser.Options{
		Host:        host,
		Ports:       alloc,
		Networks:    testNetworks(t),
		Steps:       StepsFor(stackCfg),
		Identity:    GuardFor(proofGuardDeps(t, stateDir, clusterName, store, log)),
		NewInstance: func() string { return "gen-eso-1" },
		Purposes:    purposes,
	})
	if err != nil {
		t.Fatalf("materialiser.New: %v", err)
	}
	t.Cleanup(func() {
		reapCtx, reap := context.WithTimeout(context.Background(), 30*time.Second)
		defer reap()
		_ = host.Reap(reapCtx, clusterKey)
	})
	registry := &proofClusters{cluster: &v1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName},
		Spec:       v1alpha1.ClusterSpec{Account: did, Running: true},
	}, live: func() bool { return host.Live(clusterKey) }}
	prov, err := New(Options{
		Registry: registry, Decider: decider, Ports: alloc, Host: host, WriteStatus: true,
		NodeExpirySource: NodeExpiryRollup, Probe: Probe, Interval: time.Second, Log: log,
	})
	if err != nil {
		t.Fatalf("clusterprovider.New: %v", err)
	}

	started := time.Now()
	ready := waitReady(t, ctx, prov, registry, fmt.Sprintf("the ESO-beside proof, holding %s", liveports.Text(ports)))
	t.Logf("STEP 1 the market cluster reached %s at %s in %s, the External Secrets pair inside its group",
		ready.Phase, ready.URL, time.Since(started).Round(10*time.Millisecond))
	apiserver := APIServerURL(mustAPIPort(t, alloc, clusterKey))
	core, dyn := adminRest(t, stateDir, clusterKey, apiserver)
	if nodes, err := core.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err != nil || len(nodes.Items) != 0 {
		t.Fatalf("STEP 2 the cluster is not at zero nodes: %d nodes, %v", len(nodes.Items), err)
	}

	if err := applyGrant(ctx, dyn, renderedGrant(t, did)); err != nil {
		t.Fatalf("applying the tenant grant, whose policy admits only the provisioning admin and the "+
			"beside External Secrets identity to write Secrets in %s: %v", kcpclient.TenantNamespace, err)
	}

	if _, err := core.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: kcpclient.TenantNamespace}}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("creating the tenant namespace: %v", err)
	}
	probe := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "external-secrets.io/v1", "kind": "ExternalSecret",
		"metadata": map[string]any{"name": "webhook-probe", "namespace": kcpclient.TenantNamespace},
		"spec": map[string]any{
			"secretStoreRef": map[string]any{"name": "openbao", "kind": "ClusterSecretStore"},
			"target":         map[string]any{"name": "webhook-probe"},
		},
	}}
	esGVR := schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1", Resource: "externalsecrets"}
	_, err = dyn.Resource(esGVR).Namespace(kcpclient.TenantNamespace).Create(ctx, probe, metav1.CreateOptions{})
	if err == nil {
		_ = dyn.Resource(esGVR).Namespace(kcpclient.TenantNamespace).Delete(ctx, "webhook-probe", metav1.DeleteOptions{})
		t.Fatal("STEP 2 an ExternalSecret carrying neither data nor dataFrom was admitted: only the " +
			"webhook refuses that, and its registration is failurePolicy Ignore, so an admission " +
			"means the apiserver never reached the webhook on its loopback URL")
	}
	if !strings.Contains(err.Error(), "admission webhook") {
		t.Fatalf("STEP 2 the probe store was refused by something other than the webhook: %v", err)
	}
	t.Logf("STEP 2 zero nodes, and the apiserver called the webhook on loopback: %v", err)

	dataDir, err := ClusterDataDir(stateDir, clusterKey)
	if err != nil {
		t.Fatal(err)
	}
	seal, err := ReadVaultSeal(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	vaultURL, err := esovault.ApplyVaultStore(ctx, dyn, esovault.VaultStore{
		Cluster: clusterKey, DataDir: dataDir, VaultPort: seal.Port, RootToken: seal.RootToken,
		ManifestDir: "../../deploy/eso", Namespace: kcpclient.TenantNamespace,
	})
	if err != nil {
		t.Fatalf("STEP 3 ApplyVaultStore: %v", err)
	}
	if !strings.HasPrefix(vaultURL, "https://127.0.0.1:") {
		t.Fatalf("the store dials %s, want the cluster vault on loopback", vaultURL)
	}
	applied := time.Now()
	t.Logf("STEP 3 the store and ExternalSecret are applied, the store dialling %s", vaultURL)

	want := "openbao:" + clusterKey
	var got string
	deadline := time.Now().Add(90 * time.Second)
	for {
		s, err := core.CoreV1().Secrets(kcpclient.TenantNamespace).Get(ctx, "openbao-demo", metav1.GetOptions{})
		if err == nil {
			got = string(s.Data["demo"])
			if got == want {
				break
			}
		}
		if time.Now().After(deadline) {
			for _, c := range ExternalSecretsComponents {
				raw, _ := os.ReadFile(filepath.Join(LogDir(dataDir), string(c)+".log"))
				t.Logf("%s log tail:\n%s", c, tail(string(raw)))
			}
			logESOStatus(t, ctx, dyn)
			t.Fatalf("STEP 4 the Secret %s/openbao-demo never carried %q (last %q, %v)",
				kcpclient.TenantNamespace, want, got, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("STEP 4 the Secret %s/openbao-demo carries %q from the cluster vault, %s after the apply",
		kcpclient.TenantNamespace, got, time.Since(applied).Round(10*time.Millisecond))

	pods, err := core.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pods.Items {
		if strings.Contains(p.Name, "external-secrets") {
			t.Fatalf("STEP 5 the tenant holds an External Secrets pod %s/%s", p.Namespace, p.Name)
		}
	}
	deps, err := core.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deps.Items {
		if strings.HasPrefix(d.Name, "external-secrets") {
			t.Fatalf("STEP 5 the tenant holds the Deployment %s/%s", d.Namespace, d.Name)
		}
	}
	nodes, err := core.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) != 0 {
		t.Fatalf("STEP 5 the cluster left zero nodes: %d, %v", len(nodes.Items), err)
	}
	t.Logf("STEP 5 zero nodes, %d pods in the tenant, none of them External Secrets, no External Secrets Deployment", len(pods.Items))
}

func logESOStatus(t *testing.T, ctx context.Context, dyn dynamic.Interface) {
	t.Helper()
	for _, r := range []struct {
		gvr       schema.GroupVersionResource
		namespace string
		name      string
	}{
		{schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1", Resource: "clustersecretstores"}, "", "openbao"},
		{schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1", Resource: "externalsecrets"}, kcpclient.TenantNamespace, "openbao-demo"},
	} {
		var ri dynamic.ResourceInterface = dyn.Resource(r.gvr)
		if r.namespace != "" {
			ri = dyn.Resource(r.gvr).Namespace(r.namespace)
		}
		u, err := ri.Get(ctx, r.name, metav1.GetOptions{})
		if err != nil {
			t.Logf("%s %s: %v", r.gvr.Resource, r.name, err)
			continue
		}
		status, _ := json.Marshal(u.Object["status"])
		t.Logf("%s %s status %s", r.gvr.Resource, r.name, status)
	}
}
