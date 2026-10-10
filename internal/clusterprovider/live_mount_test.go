//go:build linux

package clusterprovider

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/gateway"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/liveports"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

const (
	mountProofCluster = "mountweb"
	mountProofDID     = "did:plc:mountproof0000000000000000"
	mountProofCaller  = gateway.Caller(mountProofDID)

	mountProofProviderWorkspace = "socialweb-provider"
	mountProofProviderPath      = kcpclient.RootWorkspace + ":" + mountProofProviderWorkspace

	mountProofRefusalBody = "Not available."
)

var (
	mountProofWorkspaces = schema.GroupVersionResource{
		Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces",
	}
	mountProofClusters = schema.GroupVersionResource{
		Group: "socialweb.computer", Version: "v1alpha1", Resource: "clusters",
	}
	mountProofSchemas = schema.GroupVersionResource{
		Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas",
	}
	mountProofExports = schema.GroupVersionResource{
		Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports",
	}
	mountProofTypes = schema.GroupVersionResource{
		Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspacetypes",
	}
)

type mountProofManifest struct {
	File     string
	Path     string
	Resource schema.GroupVersionResource
}

var mountProofManifests = []mountProofManifest{
	{"cluster-apiresourceschema.yaml", mountProofProviderPath, mountProofSchemas},
	{"node-apiresourceschema.yaml", mountProofProviderPath, mountProofSchemas},
	{"atprotoidentity-apiresourceschema.yaml", mountProofProviderPath, mountProofSchemas},
	{"cluster-apiexport.yaml", mountProofProviderPath, mountProofExports},
	{"atprotoidentity-apiexport.yaml", mountProofProviderPath, mountProofExports},
	{"workspacetype-account.yaml", kcpclient.RootWorkspace, mountProofTypes},
	{"workspacetype-cluster.yaml", kcpclient.RootWorkspace, mountProofTypes},
}

func skipWithoutKCP(t *testing.T) {
	t.Helper()
	livegate.Short(t, "this test starts a real kcp serving a workspace mount and -short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	bin := os.Getenv("KCP_BIN")
	if bin == "" {
		bin = "kcp"
	}
	if _, err := exec.LookPath(bin); err != nil {
		livegate.RequireLocal(t, "%s is not on PATH: this proof starts a real kcp serving a workspace mount under "+
			"--feature-gates=WorkspaceMounts=true, and nothing else in this tree starts one. A skip "+
			"here is NOT a pass and NOT a reading -- it is the open question in section 14 staying "+
			"open, which is what every fake registry in this package has been quietly saying",
			bin)
	}
}

func TestTheMountShutsBeforeTheTenantProcessGroupDies(t *testing.T) {
	skipWithoutK3sAndKine(t)
	skipWithoutKCP(t)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	stateDir := proofStateDir(t)

	slot, err := liveports.TakeForProcess(liveports.MountProof)
	if err != nil {
		t.Fatalf("%v: this proof binds a real kcp, the kine it dials and a tenant control plane on "+
			"this host", err)
	}
	t.Logf("this run holds %s on the host for kcp, its kine and the tenant control plane; a copy of "+
		"this test running beside it takes another slot of the block", liveports.Text(slot))

	kinePort, kcpPort := slot.First, slot.First+1
	groupRange := portallocator.Range{First: slot.First + 2, Last: slot.Last}

	alloc, err := portallocator.Open(portallocator.Config{
		LeasePath: filepath.Join(t.TempDir(), "leases.json"),
		Range:     groupRange,
	})
	if err != nil {
		t.Fatalf("portallocator.Open: %v", err)
	}

	pgLease, err := alloc.Allocate(portallocator.Request{
		Cluster: mountProofCluster, Purpose: portallocator.PurposePostgres,
	})
	if err != nil {
		t.Fatalf("the store's lease: %v", err)
	}
	pg, err := StartPostgres(ctx, PostgresSpec{
		Cluster:  mountProofCluster,
		StateDir: filepath.Join(stateDir, "store"),
		Port:     pgLease.Port,
		Log:      log,
	})
	if err != nil {
		t.Fatalf("StartPostgres: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := pg.Stop(stopCtx); err != nil {
			t.Logf("stopping the store: %v", err)
		}
	})
	store := &proofStore{pg: pg}

	policy := filepath.Join(t.TempDir(), "audit-policy.yaml")
	if err := os.WriteFile(policy, []byte("apiVersion: audit.k8s.io/v1\nkind: Policy\nomitStages:\n  - RequestReceived\nrules:\n  - level: Metadata\n"), 0o600); err != nil {
		t.Fatalf("writing the audit policy: %v", err)
	}

	stackCfg := StackConfig{
		Stack:            Stack{procgroup.ComponentKine, procgroup.ComponentK3s},
		Admin:            adminFromDataDir(stateDir),
		StateDir:         stateDir,
		StoreEndpoint:    store.endpoint,
		AdvertiseAddress: portallocator.LoopbackAdvertiseAddress,
		BindAddress:      portallocator.LoopbackAdvertiseAddress,
		AuditPolicyPath:  policy,
		Harvest: func(_ context.Context, cluster string) error {
			return HarvestBundle(stateDir, cluster)
		},
		GuardRides: true,
	}
	purposes, err := PurposesFor(stackCfg.Stack, true)
	if err != nil {
		t.Fatalf("PurposesFor: %v", err)
	}

	host := materialiser.NewLocalHost(procgroup.Options{})
	host.PostgresUp = func(string) bool { return pg.Up() }
	t.Cleanup(func() {
		reapCtx, reap := context.WithTimeout(context.Background(), 60*time.Second)
		defer reap()
		if err := host.Reap(reapCtx, mountProofCluster); err != nil {
			t.Logf("reaping the group: %v", err)
		}
	})

	decider, err := materialiser.New(materialiser.Options{
		Host:        host,
		Ports:       alloc,
		Networks:    testNetworks(t),
		Steps:       StepsFor(stackCfg),
		Identity:    GuardFor(proofGuardDeps(t, stateDir, mountProofCluster, store, log)),
		NewInstance: func() string { return "gen-mount-1" },
		Purposes:    purposes,
	})
	if err != nil {
		t.Fatalf("materialiser.New: %v", err)
	}

	kcpHost, kcpCfg, kcpKubeconfig := startKCPForMountProof(t, stateDir, kinePort, kcpPort)
	installMountProvider(t, ctx, kcpCfg, kcpHost)

	registry, err := NewRegistry(RegistryOptions{Host: kcpHost, RestConfig: kcpCfg})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	mountCfg, err := kcpclient.ConfigFromContext(kcpKubeconfig, kcpclient.ShardAdminContext)
	if err != nil {
		t.Fatalf("the kcp shard-admin context %q of %s: %v. spec.URL is refused from any other "+
			"identity with `workspaces.tenancy.kcp.io \"clusters\" is forbidden: spec.URL can "+
			"only be set by system privileged users`, and the mount then serves kcp itself",
			kcpclient.ShardAdminContext, kcpKubeconfig, err)
	}
	mountCfg.TLSClientConfig.Insecure = true
	mountCfg.TLSClientConfig.CAData = nil
	mountCfg.TLSClientConfig.CAFile = ""
	mountChild, err := kcpclient.NewMountChild(kcpclient.Options{Host: kcpHost, RestConfig: mountCfg})
	if err != nil {
		t.Fatalf("NewMountChild: %v", err)
	}

	prov, err := New(Options{
		Registry:         registry,
		Decider:          decider,
		Ports:            alloc,
		Host:             host,
		WriteStatus:      true,
		MountChild:       mountChild,
		NodeExpirySource: NodeExpiryRollup,
		Probe:            Probe,
		Interval:         time.Second,
		Log:              log,
	})
	if err != nil {
		t.Fatalf("clusterprovider.New: %v", err)
	}

	ensureMountProofCluster(t, ctx, kcpHost, kcpCfg)
	ref := mountProofRef(t, ctx, registry)

	t.Logf("STEP 0 the Cluster CR is %s in %s and its mount is %s, both derived from one DID by "+
		"kcpclient, so the path the mount answers on is the path the provider asks for",
		ref.Name, ref.Home, ref.MountPath())

	readyPass := mountProofAwaitReady(t, ctx, prov, registry, ref, "cold")
	ready := readyPass.Result
	t.Logf("STEP 1 the cluster reached %s at %s", ready.Phase, ready.URL)

	apiURL := APIServerURL(mustAPIPort(t, alloc, mountProofCluster))
	mountURL := kcpHost + "/clusters/" + ref.MountPath() + "/version"

	mountProofAwaitMountReady(t, ctx, kcpHost, kcpCfg, ref)

	directCode, directBody := mountProofGet(t, apiURL+"/version")
	servingCode, servingBody := mountProofGet(t, mountURL)
	production := registry.MountStatus(ctx, ref)
	if servingCode == 0 || directCode == 0 {
		t.Fatalf("the mount answered %d (body %q) and the tenant apiserver %s answered %d (body "+
			"%q) while the CR read %s: one of the two did not answer at all, so nothing below can "+
			"be attributed to the mount", servingCode, servingBody, apiURL, directCode, directBody, readyPhase)
	}
	t.Logf("MEASURED THREE ANSWERS with the Cluster CR reading %s and the group serving. The tenant "+
		"apiserver at %s answers %d to a credential-free GET on /version (body %q). Through the "+
		"mount at %s the same GET answers %d with body %q -- the SAME %d and the same body, so the "+
		"mount did not produce that answer, the tenant's apiserver did. The client the provider "+
		"ACTUALLY sends, clusterprovider.Registry.MountStatus over rest.HTTPClientFor, answers %d, "+
		"and the pass that reached %s read %d. shut() asks whether the tenant's apiserver was "+
		"REACHED through the mount, and a code the apiserver itself produced is a REACH, so all of "+
		"these are reached and the gate holds on them",
		readyPhase, apiURL, directCode, directBody, mountURL, servingCode, servingBody, servingCode,
		production, readyPhase, readyPass.Observed.PathStatus)

	if code, reachable := Probe(ctx, apiURL); !reachable || !apiserverIsAnswering(code) {
		t.Fatalf("the group's apiserver at %s answered %d (reachable=%v) BEFORE the control reap, "+
			"so this run cannot attribute anything to the reap", apiURL, code, reachable)
	}
	if err := host.Reap(ctx, mountProofCluster); err != nil {
		t.Fatalf("reaping the group for the control: %v", err)
	}

	deadCode, deadBody := mountProofGet(t, mountURL)
	after, err := registry.Read(ctx, ref)
	if err != nil {
		t.Fatalf("reading the CR after the control reap: %v", err)
	}
	if after.Status.Phase != readyPhase {
		t.Fatalf("the CR read %s after the group was reaped and before any pass ran: the control "+
			"needs it to still read %s, because a phase change here would shut the mount for the "+
			"other reason and the %d below would prove nothing", after.Status.Phase, readyPhase, deadCode)
	}
	if deadCode == servingCode {
		t.Fatalf("CONTROL FAILED: the mount answered %d both with the tenant process group serving "+
			"and after it was SIGTERMed, while the CR still read %s. A reading that cannot move when "+
			"the backend dies cannot report anything about a live backend, and every reading below "+
			"would be uninterpretable", deadCode, readyPhase)
	}
	if deadCode >= 200 && deadCode < 300 {
		t.Fatalf("CONTROL FAILED: after the group was SIGTERMed the mount answered %d, a 2xx with "+
			"no backend to serve it", deadCode)
	}
	t.Logf("CONTROL the dead direction reproduces against this real kcp: after the group was "+
		"reaped the mount answered %d with body %q where a serving cluster had answered %d with "+
		"body %q, while the CR still read %s and the mounted workspace still read Ready. The %d is "+
		"the reverse proxy's own answer to a refused backend, which is the same 502 section 14 "+
		"already recorded, and it is what makes the %d BELOW a reading rather than a default",
		deadCode, deadBody, servingCode, servingBody, readyPhase, deadCode, servingCode)

	restored := mountProofAwaitReady(t, ctx, prov, registry, ref, "restored").Result
	mountProofAwaitMountReady(t, ctx, kcpHost, kcpCfg, ref)
	backCode, backBody := mountProofAwaitServingAnswer(t, mountURL, servingCode, 60*time.Second)
	if backCode != servingCode {
		t.Fatalf("after the group was brought back up to %s the mount answered %d with body %q "+
			"where a serving cluster had answered %d with body %q: the instrument is not reversible, "+
			"so the Draining reading below would be indistinguishable from a mount that had simply "+
			"stopped working", restored.Phase, backCode, backBody, servingCode, servingBody)
	}
	t.Logf("MEASURED RESTORED the mount answers %d with body %q again at %s", backCode, backBody, restored.URL)

	mountProofWritePhase(t, ctx, kcpHost, kcpCfg, string(v1alpha1.ClusterDraining))
	wroteDraining := time.Now()
	written, err := registry.Read(ctx, ref)
	if err != nil {
		t.Fatalf("reading the CR after writing %s into it: %v", v1alpha1.ClusterDraining, err)
	}
	if written.Status.Phase != v1alpha1.ClusterDraining {
		t.Fatalf("the CR reads %s after %s was written into it", written.Status.Phase, v1alpha1.ClusterDraining)
	}

	drainingCode, drainingBody := mountProofAwaitMountAnswer(t, mountURL, servingCode, 90*time.Second)
	drainingAt := time.Since(wroteDraining)
	if drainingCode >= 200 && drainingCode < 300 {
		t.Fatalf("the mount answered %d with body %q while the Cluster CR read %s", drainingCode,
			drainingBody, v1alpha1.ClusterDraining)
	}
	if !strings.Contains(drainingBody, mountProofRefusalBody) {
		t.Fatalf("the mount moved to %d with body %q, and that body is not %q. The refusal this "+
			"test reads is kcp's own answer to a workspace it will not serve; anything else is a "+
			"different event at the same path, and the answer to section 14 would then be about "+
			"that event and not about the phase", drainingCode, drainingBody, mountProofRefusalBody)
	}
	if code, reachable := Probe(ctx, apiURL); !reachable || !apiserverIsAnswering(code) {
		t.Fatalf("the mount had stopped serving with %d by +%s, but the tenant apiserver at %s was "+
			"no longer answering either (%d, reachable=%v). This is the one ordering this whole "+
			"test exists to establish, and a group that died first establishes the opposite",
			drainingCode, drainingAt.Round(time.Millisecond), apiURL, code, reachable)
	}
	t.Logf("MEASURED THE ANSWER the real mount moved off the %d it gives a SERVING cluster to %d "+
		"with body %q at +%s after %s was written into the Cluster CR, and the tenant process "+
		"group was STILL SERVING at that instant. That body is kcp's refusal, not the tenant "+
		"apiserver's answer and not the reverse proxy's: kcp's mount gate reads the CR's "+
		"status.phase and serves only %s, so %s is what shut the mount. The group did not shut "+
		"it -- the group was alive and its apiserver was answering when kcp refused",
		servingCode, drainingCode, drainingBody, drainingAt.Round(time.Millisecond),
		v1alpha1.ClusterDraining, readyPhase, v1alpha1.ClusterDraining)

	mountProofWritePhase(t, ctx, kcpHost, kcpCfg, string(readyPhase))
	mountProofAwaitServingAnswer(t, mountURL, servingCode, 60*time.Second)
	t.Logf("the mount serves %d again once the CR reads %s, so the %d above is the phase and not a "+
		"mount that had stopped working", servingCode, readyPhase, drainingCode)

	deleted := time.Now()
	if err := deleteMountProofCluster(t, ctx, kcpHost, kcpCfg); err != nil {
		t.Fatalf("deleting the Cluster CR: %v", err)
	}
	terminating, err := registry.Read(ctx, ref)
	if err != nil {
		t.Fatalf("reading the CR after the delete: %v", err)
	}
	if terminating.DeletionTimestamp == nil {
		t.Fatalf("the Cluster CR carries no deletionTimestamp after a delete: nothing in this "+
			"repository adds %s, so the test adds it itself, and a CR that went away instead of "+
			"terminating would make the teardown path unreachable here too", v1alpha1.FinalizerCluster)
	}
	prePass, prePassBody := mountProofGet(t, mountURL)
	prePassCR, err := registry.Read(ctx, ref)
	if err != nil {
		t.Fatalf("re-reading the CR before the first teardown pass: %v", err)
	}
	if code, reachable := Probe(ctx, apiURL); !reachable || !apiserverIsAnswering(code) {
		t.Fatalf("the tenant apiserver at %s answered %d (reachable=%v) before the first teardown "+
			"pass ran, so this run cannot say what that pass destroyed", apiURL, code, reachable)
	}
	t.Logf("the CR is terminating at %s with finalizer %v, still reads %s, the group is up and the "+
		"mount answers %d with body %q -- NOTHING has told kcp anything yet, and the only thing "+
		"that will is the phase the pass below is about to write",
		terminating.DeletionTimestamp, terminating.Finalizers, prePassCR.Status.Phase, prePass, prePassBody)

	first := mountProofPass(t, ctx, prov, ref, "first teardown pass")
	if first.Result.Phase != v1alpha1.ClusterDraining {
		t.Fatalf("the first pass over a terminating Cluster CR reported phase %s, not %s",
			first.Result.Phase, v1alpha1.ClusterDraining)
	}
	if prePass != servingCode {
		t.Fatalf("the mount answered %d with body %q immediately before the first teardown pass "+
			"where a serving cluster answers %d, so this pass did not start from a serving mount",
			prePass, prePassBody, servingCode)
	}
	if first.Observed.PathStatus != servingCode {
		t.Fatalf("the first teardown pass read PathStatus %d where the mount answered %d with body "+
			"%q immediately before it: the pass and the mount disagree about the same instant",
			first.Observed.PathStatus, prePass, prePassBody)
	}
	if slices.Contains(first.Result.Ops, materialiser.OpReap) {
		t.Fatalf("the first teardown pass read PathStatus %d, which is the answer a SERVING cluster "+
			"gives, and named %s anyway: the tenant's apiserver was reached through the mount at "+
			"that instant, so this pass destroyed a serving group. ops=%v",
			first.Observed.PathStatus, materialiser.OpReap, first.Result.Ops)
	}
	if !slices.Contains(first.Result.Ops, materialiser.OpShutPath) {
		t.Fatalf("the first teardown pass did not name %s: ops=%v",
			materialiser.OpShutPath, first.Result.Ops)
	}
	if first.Result.RemoveFinalizer {
		t.Fatalf("the first teardown pass released the finalizer while the mount still answered %d",
			first.Observed.PathStatus)
	}
	heldAt := time.Since(deleted)
	if code, reachable := Probe(ctx, apiURL); !reachable || !apiserverIsAnswering(code) {
		t.Fatalf("the tenant apiserver at %s answered %d (reachable=%v) after a pass that claimed "+
			"to hold: a group that is gone makes the hold meaningless", apiURL, code, reachable)
	}
	t.Logf("MEASURED THE GATE HELD phase=%s ops=%v PathStatus=%d (+%s after the delete). That %d is "+
		"the answer a SERVING cluster gives -- the tenant apiserver's own refusal, with the "+
		"apiserver's own body, read before the pass wrote %s -- and shut() read it as REACHED, so "+
		"the pass named %s and %s, released no finalizer, reaped nothing, and the tenant process "+
		"group was still up and answering when it finished. Before the predicate was fixed this "+
		"same pass read the same %d and named %s instead",
		first.Result.Phase, first.Result.Ops, first.Observed.PathStatus, heldAt.Round(time.Millisecond),
		first.Observed.PathStatus, v1alpha1.ClusterDraining, materialiser.OpShutPath,
		materialiser.OpAwaitPathShut, first.Observed.PathStatus, materialiser.OpReap)

	flipCode, flipBody := mountProofAwaitMountAnswer(t, mountURL, servingCode, 90*time.Second)
	flipAt := time.Since(deleted)
	if !strings.Contains(flipBody, mountProofRefusalBody) {
		t.Fatalf("the mount moved to %d with body %q, and that body is not %q: the refusal this "+
			"test reads is kcp's own answer to a workspace it will not serve",
			flipCode, flipBody, mountProofRefusalBody)
	}
	if code, reachable := Probe(ctx, apiURL); !reachable || !apiserverIsAnswering(code) {
		t.Fatalf("the mount had stopped serving with %d by +%s, but the tenant apiserver at %s was "+
			"no longer answering either (%d, reachable=%v). The one ordering this test exists to "+
			"establish is that the mount shuts FIRST, and a group that died first establishes the "+
			"opposite", flipCode, flipAt.Round(time.Millisecond), apiURL, code, reachable)
	}
	t.Logf("MEASURED THE ANSWER the mount moved off the %d it gives a serving cluster to %d with "+
		"body %q at +%s, and the tenant process group was STILL SERVING at that instant: kcp shut "+
		"the mount because this reconciler wrote %s, not because anything died",
		servingCode, flipCode, flipBody, flipAt.Round(time.Millisecond), v1alpha1.ClusterDraining)

	reapAt := time.Duration(0)
	for i := 0; i < 40; i++ {
		pass := mountProofPass(t, ctx, prov, ref, "later teardown pass")
		if slices.Contains(pass.Result.Ops, materialiser.OpReap) {
			if pass.Observed.PathStatus == servingCode {
				t.Fatalf("a pass named %s with PathStatus %d, the answer a serving cluster gives",
					materialiser.OpReap, pass.Observed.PathStatus)
			}
			if !pass.Result.RemoveFinalizer {
				t.Fatalf("the reaping pass did not release the finalizer: ops=%v", pass.Result.Ops)
			}
			reapAt = time.Since(deleted)
			t.Logf("MEASURED THE REAP phase=%s ops=%v PathStatus=%d (%s after the hold, +%s after the "+
				"delete): the reap happens on a LATER pass and only after the mount stopped serving. "+
				"The order section 14 records is now a reading and not a claim",
				pass.Result.Phase, pass.Result.Ops, pass.Observed.PathStatus,
				(reapAt - flipAt).Round(time.Millisecond), reapAt.Round(time.Millisecond))
			break
		}
		if !slices.Contains(pass.Result.Ops, materialiser.OpShutPath) {
			t.Fatalf("a later pass neither held nor reaped: ops=%v", pass.Result.Ops)
		}
		time.Sleep(time.Second)
	}
	if reapAt == 0 {
		t.Fatalf("no pass reaped within 40 passes after the mount had stopped serving with %d: the "+
			"gate would hold for ever on a shut path", flipCode)
	}

	if code, reachable := Probe(ctx, apiURL); reachable && apiserverIsAnswering(code) {
		deadline := time.Now().Add(60 * time.Second)
		for {
			code, reachable = Probe(ctx, apiURL)
			if !reachable || !apiserverIsAnswering(code) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the reaping pass named %s and the tenant apiserver at %s still answers %d "+
					"a minute later", materialiser.OpReap, apiURL, code)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	t.Logf("MEASURED THE GROUP the tenant apiserver at %s stopped answering after the reap, which "+
		"is %s after the mount had already refused: the group died last", apiURL,
		time.Since(deleted).Round(time.Millisecond))

	gone := time.Now().Add(60 * time.Second)
	for {
		_, err := mountProofDynamic(t, kcpCfg, kcpHost, kcpclient.HomePath(mountProofDID, mountProofCluster)).
			Resource(mountProofClusters).Get(ctx, mountProofCluster, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			t.Logf("MEASURED THE CR IS GONE the reaping pass released %s and kcp deleted the object, "+
				"so the delete this run started at +0 completes at +%s. Nothing else removes it: "+
				"before this change the finalizer was never installed, the delete removed the object "+
				"at once and the pass errored on a NotFound -- and a CR left terminating for ever "+
				"would reap the same dead cluster on every later pass",
				v1alpha1.FinalizerCluster, time.Since(deleted).Round(time.Millisecond))
			break
		}
		if err != nil {
			t.Fatalf("reading the Cluster CR after the reap: %v", err)
		}
		if time.Now().After(gone) {
			t.Fatalf("the reaping pass released the finalizer and the Cluster CR is still there a "+
				"minute later: it would stay terminating for ever, and every later pass would name "+
				"%s again on a cluster that is already gone", materialiser.OpReap)
		}
		time.Sleep(200 * time.Millisecond)
	}

	settledCode, settledBody := mountProofGet(t, mountURL)
	t.Logf("MEASURED THE END mount=%d body=%q, the Cluster CR is gone, tenant apiserver %s",
		settledCode, settledBody, mountProofUpOrDown(ctx, apiURL))
}

func mountProofGet(t *testing.T, url string) (int, string) {
	t.Helper()
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return resp.StatusCode, "unreadable: " + err.Error()
	}
	return resp.StatusCode, strings.TrimSpace(string(body))
}

func mountProofAwaitReady(t *testing.T, ctx context.Context, prov *Provider, clusters Clusters, ref Ref, what string) Pass {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		pass, err := prov.Reconcile(ctx, ref)
		if err != nil {
			t.Logf("%s: the pass failed: %v", what, err)
		} else if len(pass.Result.Ops) > 0 {
			t.Logf("%s: phase=%s ops=%v mount=%d alert=%q",
				what, pass.Result.Phase, pass.Result.Ops, pass.Observed.PathStatus, pass.Alert())
		}
		if err == nil && pass.Result.Phase == readyPhase {
			return pass
		}
		if time.Now().After(deadline) {
			cr, readErr := clusters.Read(ctx, ref)
			last := "unreadable"
			if readErr == nil {
				last = fmt.Sprintf("%+v", cr.Status)
			}
			t.Fatalf("%s: the cluster never reached %s in 3m; last status %s. The ports this run "+
				"holds are named above: a readiness timeout and a port another run took look the "+
				"same from here, and only the range tells them apart", what, readyPhase, last)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func mountProofPass(t *testing.T, ctx context.Context, prov *Provider, ref Ref, what string) Pass {
	t.Helper()
	pass, err := prov.Reconcile(ctx, ref)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	return pass
}

func mountProofAwaitServingAnswer(t *testing.T, url string, serving int, within time.Duration) (int, string) {
	t.Helper()
	deadline := time.Now().Add(within)
	var last int
	var lastBody string
	for {
		code, body := mountProofGet(t, url)
		last, lastBody = code, body
		if code == serving {
			return code, body
		}
		if time.Now().After(deadline) {
			t.Fatalf("the mount answered %d with body %q for %s after the Cluster CR had gone back "+
				"to %s, where a serving cluster answers %d: the group is up but kcp is not serving "+
				"it again, and the readings below would then be about kcp and not about the phase",
				last, lastBody, within, readyPhase, serving)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func mountProofAwaitMountAnswer(t *testing.T, url string, serving int, within time.Duration) (int, string) {
	t.Helper()
	deadline := time.Now().Add(within)
	var last int
	var lastBody string
	for {
		code, body := mountProofGet(t, url)
		last, lastBody = code, body
		if code != 0 && code != serving {
			return code, body
		}
		if time.Now().After(deadline) {
			t.Fatalf("the mount still answered %d with body %q after %s and after the CR's "+
				"status.phase was written as %s: nothing this reconcile wrote moves the mount, so "+
				"the gate would time out and the cluster would sit stuck-and-billing. That is a "+
				"real answer to section 14 and it is the opposite of the one this run expected",
				last, lastBody, within, v1alpha1.ClusterDraining)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func mountProofAwaitMountReady(t *testing.T, ctx context.Context, host string, cfg *rest.Config, ref Ref) {
	t.Helper()
	d := mountProofDynamic(t, cfg, host, ref.Home)
	deadline := time.Now().Add(90 * time.Second)
	last := "never read"
	for {
		ws, err := d.Resource(mountProofWorkspaces).Get(ctx, kcpclient.ChildLabel, metav1.GetOptions{})
		if err != nil {
			last = err.Error()
		} else {
			phase, _, _ := unstructured.NestedString(ws.Object, "status", "phase")
			url, _, _ := unstructured.NestedString(ws.Object, "spec", "URL")
			last = fmt.Sprintf("phase %s spec.URL %q", phase, url)
			if phase == "Ready" && url != "" {
				t.Logf("the mounted workspace %s is %s and kcp holds its mount target in spec.URL as "+
					"%q, so the mount this test reads is a workspace kcp is actually serving",
					ref.MountPath(), phase, url)
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the mounted workspace %s never became Ready with a spec.URL within 90s "+
				"(last read: %s). Without it kcp serves nothing at that path, and any non-2xx read "+
				"there would be a missing workspace rather than a shut mount", ref.MountPath(), last)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func mountProofRef(t *testing.T, ctx context.Context, registry *Registry) Ref {
	t.Helper()
	refs, err := registry.Refs(ctx)
	if err != nil {
		t.Fatalf("Refs against the real kcp: %v", err)
	}
	for _, ref := range refs {
		if ref.Name == mountProofCluster {
			return ref
		}
	}
	t.Fatalf("the provider's walk over root -> accounts -> homes found no Cluster CR named %s "+
		"in the kcp this test started; it read %d refs", mountProofCluster, len(refs))
	return Ref{}
}

func ensureMountProofCluster(t *testing.T, ctx context.Context, host string, cfg *rest.Config) {
	t.Helper()
	client, err := kcpclient.New(kcpclient.Options{Host: host, RestConfig: cfg})
	if err != nil {
		t.Fatalf("kcpclient.New: %v", err)
	}
	if err := client.Ensure(ctx, mountProofCaller, mountProofCluster); err != nil {
		t.Fatalf("kcpclient.Ensure: %v. This is the production bootstrap: it creates the account "+
			"workspace, the home workspace and the Cluster CR, and the mount path the provider "+
			"reads is derived from the same DID and name by the same package, so a hand-built "+
			"tree could differ from the one production builds and the reading would not be "+
			"about production", err)
	}
	home := kcpclient.HomePath(mountProofDID, mountProofCluster)
	d := mountProofDynamic(t, cfg, host, home)
	if _, err := d.Resource(mountProofClusters).Patch(
		ctx, mountProofCluster, "application/merge-patch+json", []byte(`{"spec":{"running":true}}`),
		metav1.PatchOptions{},
	); err != nil {
		t.Fatalf("setting running=true on the Cluster CR: %v", err)
	}
	cr, err := d.Resource(mountProofClusters).Get(ctx, mountProofCluster, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading back the Cluster CR that kcpclient.Ensure created: %v", err)
	}
	if !slices.Contains(cr.GetFinalizers(), v1alpha1.FinalizerCluster) {
		t.Fatalf("the Cluster CR kcpclient.Ensure created carries finalizers %v and not %s, which "+
			"is the finalizer materialiser.Result.RemoveFinalizer releases. This test used to "+
			"install it itself because nothing in the repository did, and it does not any more: "+
			"if the production create path does not put it there, the teardown path is still "+
			"unreachable in production and a test that supplies its own would keep passing over "+
			"that -- which is the defect this whole exercise is about", cr.GetFinalizers(), v1alpha1.FinalizerCluster)
	}
	t.Logf("the Cluster CR arrived from kcpclient.Ensure already carrying finalizer %v, so this "+
		"test supplies nothing the production create path does not and the teardown below is a "+
		"production path", cr.GetFinalizers())
}

func mountProofWritePhase(t *testing.T, ctx context.Context, host string, cfg *rest.Config, phase string) {
	t.Helper()
	home := kcpclient.HomePath(mountProofDID, mountProofCluster)
	d := mountProofDynamic(t, cfg, host, home)
	body := fmt.Sprintf(`{"status":{"phase":%q}}`, phase)
	if _, err := d.Resource(mountProofClusters).Patch(
		ctx, mountProofCluster, types.MergePatchType, []byte(body), metav1.PatchOptions{}, "status",
	); err != nil {
		t.Fatalf("writing status.phase=%s on the Cluster CR: %v", phase, err)
	}
}

func mountProofUpOrDown(ctx context.Context, apiURL string) string {
	code, reachable := Probe(ctx, apiURL)
	if !reachable {
		return "is not answering"
	}
	return fmt.Sprintf("answers %d", code)
}

func deleteMountProofCluster(t *testing.T, ctx context.Context, host string, cfg *rest.Config) error {
	t.Helper()
	home := kcpclient.HomePath(mountProofDID, mountProofCluster)
	d := mountProofDynamic(t, cfg, host, home)
	err := d.Resource(mountProofClusters).Delete(ctx, mountProofCluster, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func mountProofDynamic(t *testing.T, cfg *rest.Config, host, logicalCluster string) dynamic.Interface {
	t.Helper()
	c := rest.CopyConfig(cfg)
	c.Host = host + "/clusters/" + logicalCluster
	d, err := dynamic.NewForConfig(c)
	if err != nil {
		t.Fatalf("a dynamic client for %s: %v", logicalCluster, err)
	}
	return d
}

func installMountProvider(t *testing.T, ctx context.Context, cfg *rest.Config, host string) {
	t.Helper()
	root := mountProofDynamic(t, cfg, host, kcpclient.RootWorkspace)
	provider := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata":   map[string]any{"name": mountProofProviderWorkspace},
		"spec": map[string]any{
			"type": map[string]any{"name": "universal", "path": kcpclient.RootWorkspace},
		},
	}}
	if _, err := root.Resource(mountProofWorkspaces).Create(ctx, provider, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("creating %s, the workspace the APIExport lives in: %v", mountProofProviderPath, err)
	}
	mountProofAwaitWorkspacePhase(t, ctx, root, mountProofProviderWorkspace, "the provider workspace")

	for _, m := range mountProofManifests {
		raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", m.File))
		if err != nil {
			t.Fatalf("reading deploy/%s: %v. This proof applies the DEPLOYED manifests rather than "+
				"a copy of them, so a drift between what is deployed and what the mount needs is "+
				"visible here", m.File, err)
		}
		obj := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(raw, obj); err != nil {
			t.Fatalf("decoding deploy/%s: %v", m.File, err)
		}
		d := mountProofDynamic(t, cfg, host, m.Path)
		if _, err := d.Resource(m.Resource).Create(ctx, obj, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("applying deploy/%s into %s: %v", m.File, m.Path, err)
		}
	}

	providerClient := mountProofDynamic(t, cfg, host, mountProofProviderPath)
	for _, name := range []string{"clusters", "atprotoidentities"} {
		mountProofAwaitExportIdentity(t, ctx, providerClient, name)
	}
	t.Logf("the provider workspace %s serves the APIResourceSchema, the APIExport and the "+
		"WorkspaceTypes that deploy/install-provider.sh applies, read from deploy/ rather than "+
		"restated here", mountProofProviderPath)
}

func mountProofAwaitWorkspacePhase(t *testing.T, ctx context.Context, d dynamic.Interface, name, what string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	last := "never read"
	for {
		ws, err := d.Resource(mountProofWorkspaces).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			last = err.Error()
		} else {
			phase, _, _ := unstructured.NestedString(ws.Object, "status", "phase")
			last = phase
			if phase == "Ready" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never became Ready within 90s (last %s); nothing may be written into a "+
				"workspace kcp cannot address", what, last)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func mountProofAwaitExportIdentity(t *testing.T, ctx context.Context, d dynamic.Interface, name string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	last := "never read"
	for {
		export, err := d.Resource(mountProofExports).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			last = err.Error()
		} else {
			conditions, _, _ := unstructured.NestedSlice(export.Object, "status", "conditions")
			last = fmt.Sprintf("%v", conditions)
			for _, c := range conditions {
				m, ok := c.(map[string]any)
				if !ok {
					continue
				}
				if m["type"] == "IdentityValid" && m["status"] == "True" {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the APIExport %s never reported IdentityValid within 90s (last %s). An "+
				"APIExport with no identity cannot be bound, and a workspace with no binding serves "+
				"no Cluster CR at all -- the mount would then answer for the wrong reason", name, last)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func startKCPForMountProof(t *testing.T, dir string, kinePort, kcpPort int) (string, *rest.Config, string) {
	t.Helper()
	bin := os.Getenv("KCP_BIN")
	if bin == "" {
		bin = "kcp"
	}
	root := filepath.Join(dir, "kcp")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("creating kcp's own root directory %s: %v", root, err)
	}
	policy := filepath.Join(root, "audit-policy.yaml")
	src, err := os.ReadFile(filepath.Join("..", "..", "deploy", "kcp-audit-policy.yaml"))
	if err != nil {
		t.Fatalf("reading deploy/kcp-audit-policy.yaml: %v; an unarmed kcp audits nothing while "+
			"looking armed", err)
	}
	if err := os.WriteFile(policy, src, 0o600); err != nil {
		t.Fatalf("writing the audit policy into %s: %v", root, err)
	}

	mountProofStartProcess(t, filepath.Join(dir, "kcp-kine.log"), "kine",
		"--endpoint", "sqlite://"+filepath.Join(dir, "kcp-kine.db"),
		"--listen-address", fmt.Sprintf("127.0.0.1:%d", kinePort),
		"--metrics-bind-address=0")
	mountProofAwaitListener(t, kinePort, "the kine kcp dials")

	mountProofStartProcess(t, filepath.Join(dir, "kcp.log"), bin,
		"start",
		"--root-directory="+root,
		"--etcd-servers="+fmt.Sprintf("http://127.0.0.1:%d", kinePort),
		"--bind-address=127.0.0.1",
		"--secure-port="+strconv.Itoa(kcpPort),
		"--feature-gates=WorkspaceMounts=true",
		"--audit-policy-file="+policy,
		"--audit-log-path="+filepath.Join(root, "audit.log"),
	)

	host := fmt.Sprintf("https://127.0.0.1:%d", kcpPort)
	kubeconfig := filepath.Join(root, "admin.kubeconfig")
	mountProofAwaitFile(t, kubeconfig, 120*time.Second,
		"kcp never wrote its own admin kubeconfig into the root directory it was given. That "+
			"directory is a temp directory this test owns as uid "+strconv.Itoa(os.Getuid())+", NOT "+
			"the root-owned /var/.kcp, and /var/.kcp is not running and is not borrowed")

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("reading the kubeconfig kcp wrote to %s: %v", kubeconfig, err)
	}
	cfg.TLSClientConfig.Insecure = true
	cfg.TLSClientConfig.CAData = nil
	cfg.TLSClientConfig.CAFile = ""
	mountProofAwaitKCP(t, host)
	t.Logf("a real kcp is serving at %s as uid %d out of %s with WorkspaceMounts=true and a temp "+
		"kine behind it, not the host's root-owned /var/.kcp and not an embedded etcd on the "+
		"host-fixed 2379/2380", host, os.Getuid(), root)
	return host, cfg, kubeconfig
}

func mountProofStartProcess(t *testing.T, logPath, bin string, args ...string) {
	t.Helper()
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("creating %s: %v", logPath, err)
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", bin, err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _, _ = cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		_ = f.Close()
	})
}

func mountProofAwaitListener(t *testing.T, port int, what string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never listened on 127.0.0.1:%d within 60s: %v", what, port, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func mountProofAwaitFile(t *testing.T, path string, within time.Duration, why string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s", why)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func mountProofAwaitKCP(t *testing.T, host string) {
	t.Helper()
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	deadline := time.Now().Add(90 * time.Second)
	last := "never attempted"
	for {
		resp, err := client.Get(host + "/healthz")
		if err == nil {
			last = resp.Status
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		} else {
			last = err.Error()
		}
		if time.Now().After(deadline) {
			t.Fatalf("kcp at %s never answered 200 on /healthz within 90s (last: %s)", host, last)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
