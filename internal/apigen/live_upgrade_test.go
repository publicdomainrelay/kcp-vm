//go:build linux

package apigen

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/liveports"
)

var (
	gvrWorkspace = schema.GroupVersionResource{Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces"}
	gvrSchema    = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas"}
	gvrExport    = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
	gvrBinding   = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}
	gvrNodeClaim = schema.GroupVersionResource{Group: "socialweb.computer", Version: "v1alpha1", Resource: "nodeclaims"}
)

const (
	priorNodeClaimSchema  = "v1alpha1-1.nodeclaims.socialweb.computer"
	upgradeProviderWS     = "provider"
	upgradeTenantWS       = "tenant"
	upgradeDeadline       = 120 * time.Second
	upgradePollInterval   = 500 * time.Millisecond
	upgradeClaimName      = "web-node1"
	upgradeExportName     = "clusters"
	upgradeStartupTimeout = 120 * time.Second
)

func requireOnPath(t *testing.T, bin string) string {
	t.Helper()
	path, err := exec.LookPath(bin)
	if err != nil {
		livegate.RequireLocal(t, "%s is not on PATH: the schema upgrade proof starts a real kcp over a "+
			"real kine, because what kcp does to an already-bound workspace when an APIExport is "+
			"re-pointed is a fact of kcp, not of this code. A skip is NOT a pass", bin)
	}
	return path
}

func startProcess(t *testing.T, logPath, bin string, args ...string) {
	t.Helper()
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
		_ = f.Close()
		if t.Failed() {
			if data, err := os.ReadFile(logPath); err == nil {
				lines := strings.Split(string(data), "\n")
				if len(lines) > 40 {
					lines = lines[len(lines)-40:]
				}
				t.Logf("tail of %s:\n%s", logPath, strings.Join(lines, "\n"))
			}
		}
	})
}

func eventually(t *testing.T, what string, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(upgradeDeadline)
	var last error
	for time.Now().Before(deadline) {
		if last = fn(); last == nil {
			return
		}
		time.Sleep(upgradePollInterval)
	}
	t.Fatalf("%s within %s: %v", what, upgradeDeadline, last)
}

func startKCP(t *testing.T) *rest.Config {
	t.Helper()
	kcpBin := requireOnPath(t, "kcp")
	kineBin := requireOnPath(t, "kine")
	ports, err := liveports.TakeForProcess(liveports.SchemaUpgrade)
	if err != nil {
		t.Fatal(err)
	}
	kinePort, kcpPort := ports.First, ports.First+1
	dir := t.TempDir()
	root := filepath.Join(dir, "kcp")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	startProcess(t, filepath.Join(dir, "kine.log"), kineBin,
		"--endpoint", "sqlite://"+filepath.Join(dir, "kine.db"),
		"--listen-address", fmt.Sprintf("127.0.0.1:%d", kinePort),
		"--metrics-bind-address=0")
	eventually(t, "kine listens", func() error {
		c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", kinePort))
		if err == nil {
			_ = c.Close()
		}
		return err
	})
	startProcess(t, filepath.Join(dir, "kcp.log"), kcpBin, "start",
		"--root-directory="+root,
		"--etcd-servers="+fmt.Sprintf("http://127.0.0.1:%d", kinePort),
		"--bind-address=127.0.0.1",
		"--secure-port="+strconv.Itoa(kcpPort))
	kubeconfig := filepath.Join(root, "admin.kubeconfig")
	host := fmt.Sprintf("https://127.0.0.1:%d", kcpPort)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	deadline := time.Now().Add(upgradeStartupTimeout)
	for {
		if _, err := os.Stat(kubeconfig); err == nil {
			if res, err := client.Get(host + "/readyz"); err == nil {
				_ = res.Body.Close()
				if res.StatusCode == http.StatusOK || res.StatusCode == http.StatusUnauthorized ||
					res.StatusCode == http.StatusForbidden {
					break
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("kcp did not come up at %s within %s", host, upgradeStartupTimeout)
		}
		time.Sleep(upgradePollInterval)
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Host = host
	cfg.TLSClientConfig = rest.TLSClientConfig{Insecure: true}
	return cfg
}

func clientFor(t *testing.T, cfg *rest.Config, path string) dynamic.Interface {
	t.Helper()
	c := rest.CopyConfig(cfg)
	c.Host = cfg.Host + "/clusters/" + path
	dc, err := dynamic.NewForConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	return dc
}

func manifest(t *testing.T, data []byte) *unstructured.Unstructured {
	t.Helper()
	obj := map[string]any{}
	if err := yaml.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: obj}
}

func readRepo(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func createWorkspace(t *testing.T, ctx context.Context, root dynamic.Interface, name string) {
	t.Helper()
	ws := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"type": map[string]any{"name": "universal", "path": "root"}},
	}}
	if _, err := root.Resource(gvrWorkspace).Create(ctx, ws, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create workspace %s: %v", name, err)
	}
	eventually(t, "workspace "+name+" reaches Ready", func() error {
		got, err := root.Resource(gvrWorkspace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		if phase != "Ready" {
			return fmt.Errorf("phase %q", phase)
		}
		return nil
	})
}

func boundNodeClaimSchema(ctx context.Context, tenant dynamic.Interface) (string, string, error) {
	b, err := tenant.Resource(gvrBinding).Get(ctx, upgradeExportName, metav1.GetOptions{})
	if err != nil {
		return "", "", err
	}
	bound, _, _ := unstructured.NestedSlice(b.Object, "status", "boundResources")
	for _, r := range bound {
		m, _ := r.(map[string]any)
		if m["resource"] != gvrNodeClaim.Resource {
			continue
		}
		name, _, _ := unstructured.NestedString(m, "schema", "name")
		hash, _, _ := unstructured.NestedString(m, "schema", "identityHash")
		return name, hash, nil
	}
	return "", "", fmt.Errorf("the binding has no bound nodeclaims yet")
}

func fixtureStatus(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	status, ok := obj["status"].(map[string]any)
	if !ok {
		t.Fatalf("%s carries no status", name)
	}
	return status
}

func patchStatus(t *testing.T, ctx context.Context, tenant dynamic.Interface, status map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"status": status})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.Resource(gvrNodeClaim).Patch(ctx, upgradeClaimName, types.MergePatchType, body,
		metav1.PatchOptions{}, "status"); err != nil {
		t.Fatalf("patch status: %v", err)
	}
	got, err := tenant.Resource(gvrNodeClaim).Get(ctx, upgradeClaimName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out, _, _ := unstructured.NestedMap(got.Object, "status")
	return normalized(t, out)
}

func normalized(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAnAlreadyBoundWorkspaceKeepsTheNewContractFieldsAfterTheSchemaUpgrade(t *testing.T) {
	livegate.Short(t, "this test starts a real kcp and -short was asked for: a skip is NOT a pass; drop "+
		"-short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")

	prior := readRepo(t, "testdata/apiresourceschema/"+priorNodeClaimSchema+".yaml")
	lockData := readRepo(t, RevisionLockFile)
	lock, err := ParseRevisionLock(lockData)
	if err != nil {
		t.Fatal(err)
	}
	name, hash, err := SpecHash(prior)
	if err != nil || name != priorNodeClaimSchema || lock[name] != hash {
		t.Fatalf("testdata holds %s %s, and the lock records %s for it: the prior schema in testdata "+
			"must be the one already deployed (%v)", name, hash, lock[priorNodeClaimSchema], err)
	}

	m := mustModel(t)
	var current Resource
	for _, r := range m.Resources {
		if r.Kind == "NodeClaim" {
			current = r
		}
	}
	currentName := current.SchemaName(m.Group, m.Version)
	if currentName == priorNodeClaimSchema {
		t.Fatalf("the NodeClaim schema is still %s; this proof is of a re-point", currentName)
	}
	exportNow := readRepo(t, "deploy/cluster-apiexport.yaml")
	exportPrior := []byte(strings.ReplaceAll(string(exportNow), currentName, priorNodeClaimSchema))

	cfg := startKCP(t)
	ctx := context.Background()
	root := clientFor(t, cfg, "root")
	createWorkspace(t, ctx, root, upgradeProviderWS)
	createWorkspace(t, ctx, root, upgradeTenantWS)
	provider := clientFor(t, cfg, "root:"+upgradeProviderWS)
	tenant := clientFor(t, cfg, "root:"+upgradeTenantWS)

	for _, rel := range []string{"deploy/cluster-apiresourceschema.yaml", "deploy/node-apiresourceschema.yaml"} {
		if _, err := provider.Resource(gvrSchema).Create(ctx, manifest(t, readRepo(t, rel)), metav1.CreateOptions{}); err != nil {
			t.Fatalf("create %s: %v", rel, err)
		}
	}
	if _, err := provider.Resource(gvrSchema).Create(ctx, manifest(t, prior), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create the prior schema: %v", err)
	}
	if _, err := provider.Resource(gvrExport).Create(ctx, manifest(t, exportPrior), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create the prior export: %v", err)
	}
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIBinding",
		"metadata":   map[string]any{"name": upgradeExportName},
		"spec": map[string]any{"reference": map[string]any{"export": map[string]any{
			"path": "root:" + upgradeProviderWS, "name": upgradeExportName,
		}}},
	}}
	if _, err := tenant.Resource(gvrBinding).Create(ctx, binding, metav1.CreateOptions{}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	var priorHash string
	eventually(t, "the tenant binds the prior NodeClaim schema", func() error {
		name, hash, err := boundNodeClaimSchema(ctx, tenant)
		if err != nil {
			return err
		}
		if name != priorNodeClaimSchema {
			return fmt.Errorf("bound %s", name)
		}
		priorHash = hash
		return nil
	})

	claim := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "socialweb.computer/v1alpha1",
		"kind":       "NodeClaim",
		"metadata":   map[string]any{"name": upgradeClaimName},
		"spec": map[string]any{
			"cluster": "web", "nodeName": upgradeClaimName, "ownerDid": "did:plc:ownerowner",
			"apiserverPort": int64(24000), "advertiseAddress": "100.127.255.254",
			"tokenFile": "/t", "nodePasswordFile": "/p", "serverCAFile": "/c",
		},
	}}
	eventually(t, "the tenant serves nodeclaims", func() error {
		_, err := tenant.Resource(gvrNodeClaim).Create(ctx, claim, metav1.CreateOptions{})
		return err
	})

	legacy := fixtureStatus(t, "valid-running-legacy-contract.json")
	full := fixtureStatus(t, "valid-running-full-contract.json")

	if got := patchStatus(t, ctx, tenant, legacy); !reflect.DeepEqual(got, legacy) {
		t.Fatalf("the prior schema did not keep the status prod writes today:\n got %v\nwant %v", got, legacy)
	}
	pruned := patchStatus(t, ctx, tenant, full)
	if c, _ := pruned["contract"].(map[string]any); c["payload"] != nil || c["phase"] != nil {
		t.Fatalf("the prior schema kept a field it does not declare, so this proof proves nothing: %v", c)
	}

	inPlace := manifest(t, []byte(strings.ReplaceAll(string(readRepo(t, SchemaFile(current))), currentName, priorNodeClaimSchema)))
	existing, err := provider.Resource(gvrSchema).Get(ctx, priorNodeClaimSchema, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	inPlace.SetResourceVersion(existing.GetResourceVersion())
	if _, err := provider.Resource(gvrSchema).Update(ctx, inPlace, metav1.UpdateOptions{}); err == nil {
		t.Fatalf("kcp accepted a changed spec under %s; APIResourceSchema.spec is no longer immutable "+
			"and the revision lock is guarding nothing", priorNodeClaimSchema)
	}

	if _, err := provider.Resource(gvrSchema).Create(ctx, manifest(t, readRepo(t, SchemaFile(current))), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create %s: %v", currentName, err)
	}
	export, err := provider.Resource(gvrExport).Get(ctx, upgradeExportName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	next := manifest(t, exportNow)
	next.SetResourceVersion(export.GetResourceVersion())
	if _, err := provider.Resource(gvrExport).Update(ctx, next, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("re-point the export: %v", err)
	}

	eventually(t, "the already-bound tenant moves to "+currentName, func() error {
		name, hash, err := boundNodeClaimSchema(ctx, tenant)
		if err != nil {
			return err
		}
		if hash != priorHash {
			t.Fatalf("the identity hash changed across the re-point (%s -> %s): every stored claim "+
				"now reads from an empty etcd prefix", priorHash, hash)
		}
		if name != currentName {
			return fmt.Errorf("still bound to %s", name)
		}
		return nil
	})

	before, err := tenant.Resource(gvrNodeClaim).Get(ctx, upgradeClaimName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the claim written before the upgrade is gone: %v", err)
	}
	if !reflect.DeepEqual(before.Object["spec"], claim.Object["spec"]) {
		t.Fatalf("the claim's spec changed across the upgrade:\n got %v\nwant %v", before.Object["spec"], claim.Object["spec"])
	}
	eventually(t, "the tenant keeps every new contract field", func() error {
		got := patchStatus(t, ctx, tenant, full)
		if !reflect.DeepEqual(got, full) {
			return fmt.Errorf("status read back as %v", got)
		}
		return nil
	})
	if issues := m.Validate("NodeClaimStatus", full); len(issues) != 0 {
		t.Fatalf("the status kcp kept is not valid under the generated schema: %v", issues)
	}
}
