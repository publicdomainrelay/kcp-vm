//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/instance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/liveports"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodeprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/subnetallocator"
)

const (
	subnetImageStoreEnv = "SOCIALWEB_LIVE_SUBNET_IMAGE_OUT_DIR"

	subnetImageConfigEnv = "SOCIALWEB_LIVE_SUBNET_IMAGE_CONFIG"

	subnetVMMEnv = "SOCIALWEB_LIVE_SUBNET_FIRECRACKER"

	subnetAgentEnv = "SOCIALWEB_LIVE_SUBNET_AGENT"

	subnetScratchEnv = "SOCIALWEB_LIVE_SUBNET_SCRATCH"

	subnetDeadline = 30 * time.Minute

	subnetSystemPodsHold = 45 * time.Second

	subnetClusters = "alpha,beta"
)

type subnetBins struct {
	imageStore  string
	imageConfig string
	vmm         string
	agent       string
}

func subnetRequirements(t *testing.T) subnetBins {
	t.Helper()
	livegate.Short(t, "this test boots two real guests over two leased ranges and -short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	bins := subnetBins{
		imageStore:  os.Getenv(subnetImageStoreEnv),
		imageConfig: os.Getenv(subnetImageConfigEnv),
		vmm:         os.Getenv(subnetVMMEnv),
		agent:       os.Getenv(subnetAgentEnv),
	}
	if bins.imageStore == "" {
		livegate.Require(t, "set %s, %s, %s, %s and %s to the store socialweb-nodeimage published into, the "+
			"builder's own configuration -- which is what names the image in that store, so its "+
			"inputs have to be the files the build read -- a firecracker binary and the agent built "+
			"for the guest, and this test derives the addressing change on this host: two "+
			"clusters, each addressed from the range it leases, each with a guest that joins as a "+
			"Ready node reporting an InternalIP that is neither this host's nor the other "+
			"cluster's, a node that reaches the internet through its cluster's own gateway, and a "+
			"restarted gateway that re-finds the range rather than leasing a second one. It boots "+
			"two real guests, so it is skipped by default and a skip is NOT a pass",
			subnetImageStoreEnv, subnetImageConfigEnv, subnetVMMEnv, subnetAgentEnv, subnetScratchEnv)
	}
	for _, req := range []struct{ name, value string }{
		{subnetImageConfigEnv, bins.imageConfig},
		{subnetVMMEnv, bins.vmm},
		{subnetAgentEnv, bins.agent},
	} {
		if req.value == "" {
			t.Fatalf("%s is not set", req.name)
		}
	}
	for _, bin := range []string{"k3s", "kubectl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("%s is not on PATH: the node joins a REAL k3s server and its dial line is read "+
				"from the guest's own console, so a skip here would leave the wiring unmeasured", bin)
		}
	}
	if err := fourstepNodePasswordWritable(); err != nil {
		livegate.RequireLocal(t, "k3s server --disable-agent still bootstraps the host-global node password and this "+
			"process cannot open %s: %v", fourstepNodePasswordPath(), err)
	}
	return bins
}

func subnetScratch(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv(subnetScratchEnv); dir != "" {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatalf("clearing %s: %v", dir, err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
		return dir
	}
	return t.TempDir()
}

type subnetCluster struct {
	name string

	lease subnetallocator.Lease

	gateway string

	nodeName string

	nodeDir string

	kubeconfig string

	port int

	process *exec.Cmd

	serverLog string
}

type subnetRegistry struct {
	cluster *v1alpha1.Cluster

	nodes []v1alpha1.NodePhase

	live func() bool
}

func (r *subnetRegistry) Refs(context.Context) ([]clusterprovider.Ref, error) {
	return []clusterprovider.Ref{{
		Account: "root:subnet",
		DID:     "did:plc:subnet0000000000000000000",
		Home:    "root:subnet:home",
		Name:    r.cluster.Name,
	}}, nil
}

func (r *subnetRegistry) Read(context.Context, clusterprovider.Ref) (*v1alpha1.Cluster, error) {
	c := *r.cluster
	return &c, nil
}

func (r *subnetRegistry) WriteStatus(_ context.Context, _ clusterprovider.Ref, st v1alpha1.ClusterStatus) error {
	r.cluster.Status = st
	return nil
}

func (r *subnetRegistry) RemoveFinalizer(context.Context, clusterprovider.Ref) error { return nil }

func (r *subnetRegistry) MountStatus(context.Context, clusterprovider.Ref) int {
	if r.cluster.DeletionTimestamp != nil {
		return 503
	} else if r.live != nil && r.live() {
		return 401
	} else {
		return 502
	}
}

func (r *subnetRegistry) IdentityState(context.Context, clusterprovider.Ref) (v1alpha1.IdentityState, error) {
	return v1alpha1.IdentityRevoked, nil
}

func (r *subnetRegistry) Nodes(context.Context, clusterprovider.Ref) ([]v1alpha1.Node, error) {
	return nil, nil
}

func (r *subnetRegistry) WriteNode(_ context.Context, _ clusterprovider.Ref, _ string, status v1alpha1.NodeStatus) error {
	r.nodes = append(r.nodes, status.Phase)
	return nil
}

func subnetStartServer(t *testing.T, ctx context.Context, dir string, ports *portallocator.Allocator, gateway, cluster string) subnetCluster {
	t.Helper()
	lease, err := ports.Allocate(portallocator.Request{Cluster: cluster, Purpose: portallocator.PurposeK3sSecure})
	if err != nil {
		t.Fatalf("the apiserver lease for %s: %v", cluster, err)
	}
	advertisePort, listenPort, err := clusterprovider.K3sAPIPorts(
		materialiser.Leases{portallocator.PurposeK3sSecure: lease}, cluster)
	if err != nil {
		t.Fatalf("the ports %s's k3s is told to advertise and to bind: %v", cluster, err)
	}
	t.Logf("%s's apiserver advertises %d and binds %d: both are read from the ONE lease this "+
		"cluster's apiserver holds, and this harness does not choose them -- the ports a cluster "+
		"is started with are the composition root's decision, so a harness that picked its own "+
		"would stay internally consistent while the argv under it was wrong. The port a cluster "+
		"advertises is the port its in-cluster kubernetes Service endpoints carry, so a second, "+
		"separately leased advertise port is a port nothing binds", cluster, advertisePort, listenPort)
	for _, purpose := range []portallocator.Purpose{
		portallocator.PurposePostgres,
		portallocator.PurposeKine,
		portallocator.PurposeK3sControllerManager,
		portallocator.PurposeK3sScheduler,
		portallocator.PurposeK3sCloudControllerManager,
	} {
		if _, err := ports.Allocate(portallocator.Request{Cluster: cluster, Purpose: purpose}); err != nil {
			t.Fatalf("the %s lease for %s: %v", purpose, cluster, err)
		}
	}
	clusterDir, err := clusterprovider.ClusterDataDir(dir, cluster)
	if err != nil {
		t.Fatalf("ClusterDataDir: %v", err)
	}
	if err := os.MkdirAll(clusterDir, 0o700); err != nil {
		t.Fatalf("creating %s: %v", clusterDir, err)
	}
	token, err := instance.New(nil)
	if err != nil {
		t.Fatalf("instance.New: %v", err)
	}
	if err := instance.WriteFile(clusterprovider.TokenPath(clusterDir), token); err != nil {
		t.Fatalf("writing the token: %v", err)
	}
	dataDir := filepath.Join(clusterDir, "server")
	policy := filepath.Join(clusterDir, "audit-policy.yaml")
	if err := os.WriteFile(policy, []byte("apiVersion: audit.k8s.io/v1\nkind: Policy\nomitStages:\n  - RequestReceived\nrules:\n  - level: Metadata\n"), 0o600); err != nil {
		t.Fatalf("writing the audit policy: %v", err)
	}
	kine, ok := ports.PortFor(cluster, portallocator.PurposeKine)
	if !ok {
		t.Fatalf("no kine lease for %s", cluster)
	}
	spec := clusterstack.Spec{
		DataDir:                       dataDir,
		PostgresPort:                  mustPort(t, ports, cluster, portallocator.PurposePostgres),
		OpenBaoConfig:                 filepath.Join(clusterDir, "openbao.hcl"),
		StoreEndpoint:                 "sqlite://" + filepath.Join(dataDir, "state.db"),
		KineListen:                    fmt.Sprintf("127.0.0.1:%d", kine.Port),
		DatastoreEndpoint:             "sqlite://" + filepath.Join(dataDir, "state.db"),
		AdvertiseAddress:              gateway,
		BindAddress:                   portallocator.LoopbackAdvertiseAddress,
		AdvertisePort:                 advertisePort,
		K3sSecurePort:                 listenPort,
		K3sControllerManagerPort:      mustPort(t, ports, cluster, portallocator.PurposeK3sControllerManager),
		K3sSchedulerPort:              mustPort(t, ports, cluster, portallocator.PurposeK3sScheduler),
		K3sCloudControllerManagerPort: mustPort(t, ports, cluster, portallocator.PurposeK3sCloudControllerManager),
		TokenFile:                     clusterprovider.TokenPath(clusterDir),
		TLSSANs:                       []string{gateway, portallocator.LoopbackAdvertiseAddress},
		Audit: clusterstack.Audit{
			PolicyPath: policy,
			LogPath:    filepath.Join(dataDir, "audit.log"),
		},
	}
	argv, err := spec.ServerArgv()
	if err != nil {
		t.Fatalf("the apiserver argv for %s: %v", cluster, err)
	}
	if got := flagValue(argv, "--advertise-address"); got != gateway {
		t.Fatalf("%s's apiserver is advertised on %q, want the gateway of the range it leases, %q",
			cluster, got, gateway)
	}
	logPath := filepath.Join(clusterDir, "server.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("creating %s: %v", logPath, err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s for %s: %v", argv[0], cluster, err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		_ = logFile.Close()
	})
	kubeconfig := clusterstack.AdminKubeconfig(dataDir)
	deadline := time.Now().Add(3 * time.Minute)
	for {
		if _, err := os.Stat(kubeconfig); err == nil {
			if _, err := exec.Command("kubectl", "--kubeconfig", kubeconfig,
				"--request-timeout=5s", "get", "nodes").CombinedOutput(); err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			raw, _ := os.ReadFile(logPath)
			t.Fatalf("%s's apiserver never answered; its log ends:\n%s", cluster, lastLines(string(raw), 30))
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s's apiserver: %v", cluster, ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
	return subnetCluster{
		name:       cluster,
		gateway:    gateway,
		kubeconfig: kubeconfig,
		port:       lease.Port,
		process:    cmd,
		serverLog:  logPath,
	}
}

func mustPort(t *testing.T, ports *portallocator.Allocator, cluster string, purpose portallocator.Purpose) int {
	t.Helper()
	lease, ok := ports.PortFor(cluster, purpose)
	if !ok {
		t.Fatalf("no %s lease for %s", purpose, cluster)
	}
	return lease.Port
}

func flagValue(argv []string, flag string) string {
	for i, arg := range argv {
		if arg == flag && i+1 < len(argv) {
			return argv[i+1]
		}
		if v, ok := strings.CutPrefix(arg, flag+"="); ok {
			return v
		}
	}
	return ""
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func subnetNodes(t *testing.T, client kubernetes.Interface, cluster string) []corev1.Node {
	t.Helper()
	list, err := client.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing %s's nodes: %v", cluster, err)
	}
	return list.Items
}

func subnetWaitReady(t *testing.T, ctx context.Context, client kubernetes.Interface, cluster, nodeName string) corev1.Node {
	t.Helper()
	deadline := time.Now().Add(6 * time.Minute)
	for {
		for _, node := range subnetNodes(t, client, cluster) {
			if node.Name != nodeName {
				continue
			}
			for _, cond := range node.Status.Conditions {
				if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
					return node
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s's node %s never reported Ready; the tenant cluster holds %d nodes",
				cluster, nodeName, len(subnetNodes(t, client, cluster)))
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s's node: %v", cluster, ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func internalIP(node corev1.Node) string {
	for _, a := range node.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			return a.Address
		}
	}
	return ""
}

func hostAddresses() map[string]bool {
	out := map[string]bool{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			out[ipnet.IP.String()] = true
		}
	}
	return out
}

func instanceStartNanos(t *testing.T, nodeDir string) int64 {
	t.Helper()
	raw, err := os.ReadFile(nodefirecracker.InstancePath(nodeDir))
	if err != nil {
		t.Fatalf("reading the instance record at %s: %v", nodefirecracker.InstancePath(nodeDir), err)
	}
	var rec struct {
		PID        int   `json:"pid"`
		StartNanos int64 `json:"startNanos"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("%s: %v", nodefirecracker.InstancePath(nodeDir), err)
	}
	return rec.StartNanos
}

func guestConsole(t *testing.T, nodeDir string) string {
	t.Helper()
	raw, err := os.ReadFile(nodefirecracker.ConsolePath(nodeDir))
	if err != nil {
		t.Fatalf("reading the guest console at %s: %v", nodefirecracker.ConsolePath(nodeDir), err)
	}
	return string(raw)
}

func TestLiveTwoClustersAreAddressedFromTheRangesTheyLease(t *testing.T) {
	bins := subnetRequirements(t)
	ctx, cancel := context.WithTimeout(context.Background(), subnetDeadline)
	defer cancel()

	dir := subnetScratch(t)
	t.Logf("scratch %s", dir)

	slot, err := liveports.TakeForProcess(liveports.SubnetProof)
	if err != nil {
		t.Fatalf("%v: this proof runs the composition root over two clusters, and every port those "+
			"clusters bind -- store, kine, apiserver and the three control-plane components -- comes "+
			"from this block", err)
	}
	ports, err := portallocator.Open(portallocator.Config{
		LeasePath: filepath.Join(dir, "ports.json"),
		Range:     slot,
	})
	if err != nil {
		t.Fatalf("portallocator.Open: %v", err)
	}
	networks, err := subnetallocator.Open(subnetallocator.Config{
		LeasePath: subnetLeasePath(dir),
	})
	if err != nil {
		t.Fatalf("subnetallocator.Open: %v", err)
	}
	substrate, err := nodeSubstrateFor(Config{
		Substrate:   SubstrateFirecracker,
		ImageOutDir: bins.imageStore,
		ImageConfig: bins.imageConfig,
		Firecracker: bins.vmm,
		NodeAgent:   bins.agent,
	})
	if err != nil {
		t.Fatalf("the substrate the configuration names: %v", err)
	}

	images := manifestImages(t, filepath.Dir(substrate.Image.Kernel))
	held := map[string]subnetCluster{}

	for _, cluster := range strings.Split(subnetClusters, ",") {
		registry := &subnetRegistry{cluster: &v1alpha1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: cluster},
			Spec:       v1alpha1.ClusterSpec{Account: "did:plc:subnet0000000000000000000", Running: true},
		}}
		lease, err := networks.Allocate(subnetallocator.Request{Cluster: cluster})
		if err != nil {
			t.Fatalf("%s's range, which holds the gateway its guests dial and one address per "+
				"node: %v", cluster, err)
		}
		started := subnetStartServer(t, ctx, dir, ports, lease.Gateway().String(), cluster)
		registry.live = func() bool { return started.process != nil && started.process.Process != nil }

		cfg, err := clientcmd.BuildConfigFromFlags("", started.kubeconfig)
		if err != nil {
			t.Fatalf("reading %s's kubeconfig: %v", cluster, err)
		}
		client, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			t.Fatalf("the tenant client for %s: %v", cluster, err)
		}
		tenant := func(context.Context, string) (kubernetes.Interface, error) { return client, nil }
		hosts, err := newNodeHosts(nodeHostsOptions{
			Allocator: ports,
			Networks:  networks,
			Registry:  registry,
			StateDir:  dir,
			Tenant:    tenant,
			Log:       testLogger(),
			Substrate: substrate,
		})
		if err != nil {
			t.Fatalf("newNodeHosts: %v", err)
		}
		t.Cleanup(func() {
			reapCtx, reap := context.WithTimeout(context.Background(), 2*time.Minute)
			defer reap()
			if err := hosts.reap(reapCtx); err != nil {
				t.Logf("reaping %s's node hosts: %v", cluster, err)
			}
		})
		nodes, err := hosts.forCluster(cluster)
		if err != nil {
			t.Fatalf("forCluster(%s): %v", cluster, err)
		}
		t.Logf("%s leases %s; its gateway is %s and its node is %s", cluster, lease.Block.Masked(),
			lease.Gateway(), mustHost(t, lease, 2))
		if err := nodes.Provision(ctx); err != nil {
			t.Fatalf("provisioning %s's node: %v -- the guest's own console is at %s",
				cluster, err, filepath.Join(dir, cluster))
		}
		name, err := nodeprovider.NodeName(cluster, nodeID)
		if err != nil {
			t.Fatalf("NodeName: %v", err)
		}
		nodeDir := clusterprovider.NodeDataDir(dir, cluster, name)
		startNanos := instanceStartNanos(t, nodeDir)
		node := subnetWaitReady(t, ctx, client, cluster, name)
		ready := time.Since(time.Unix(0, startNanos))
		t.Logf("INSTANCE_START_TO_READY_S=%.3f cluster=%s node=%s internalIP=%s",
			ready.Seconds(), cluster, name, internalIP(node))

		started.name = cluster
		started.lease = lease
		started.nodeName = name
		started.nodeDir = nodeDir
		held[cluster] = started
	}

	if len(held) != 2 {
		t.Fatalf("expected two clusters, held %d", len(held))
	}

	host := hostAddresses()

	alpha, beta := held["alpha"], held["beta"]
	if alpha.lease.Block.Masked() == beta.lease.Block.Masked() {
		t.Fatalf("both clusters were leased %s: two clusters in one range address their nodes "+
			"identically, and every address involved is legal, so nothing in either reports it",
			alpha.lease.Block.Masked())
	}
	alphaClient := subnetClient(t, alpha)
	betaClient := subnetClient(t, beta)
	alphaIP := internalIP(subnetWaitReady(t, ctx, alphaClient, alpha.name, alpha.nodeName))
	betaIP := internalIP(subnetWaitReady(t, ctx, betaClient, beta.name, beta.nodeName))
	for cluster, ip := range map[string]string{"alpha": alphaIP, "beta": betaIP} {
		if ip == "" {
			t.Fatalf("%s's node reports no InternalIP", cluster)
		}
		if host[ip] {
			t.Fatalf("%s's node reports InternalIP %s, which is an address THIS host carries. That "+
				"is the measured failure this work removes: the node's InternalIP was the address of "+
				"the machine running the gateway", cluster, ip)
		}
	}
	if alphaIP == betaIP {
		t.Fatalf("both clusters' nodes report InternalIP %s: a packet addressed to a guest of one "+
			"arrives at a guest of the other, and each cluster's flannel is handed the other's pod "+
			"CIDR as its own", alphaIP)
	}
	if len(alpha.gateway) == 0 || alpha.gateway == beta.gateway {
		t.Fatalf("the two clusters' guests dial %q and %q", alpha.gateway, beta.gateway)
	}
	t.Logf("alpha %s node %s dials %s", alpha.lease.Block.Masked(), alphaIP, alpha.gateway)
	t.Logf("beta  %s node %s dials %s", beta.lease.Block.Masked(), betaIP, beta.gateway)

	for _, c := range []subnetCluster{alpha, beta} {
		console := guestConsole(t, c.nodeDir)
		for _, signature := range []string{
			"Failed to execute iptables-restore",
			"Failed to run iptables-restore",
			"Aborting sync",
			"not supported, missing kernel module",
		} {
			if strings.Contains(console, signature) {
				t.Errorf("%s's console carries %q, so a ruleset did not land in this guest: the "+
					"firewall the cluster believes it has is not the one it has", c.name, signature)
			}
		}
		if !strings.Contains(console, "KCP_CONN apiserver "+c.gateway+":"+strconv.Itoa(c.port)+" -> OK") {
			t.Errorf("%s's guest console does not show its agent dialling %s:%d successfully. What "+
				"it does show about its dial is:\n%s", c.name, c.gateway, c.port,
				consoleDialLines(console))
		}
		subnetRequireSystemPods(t, ctx, subnetClient(t, c), c)
	}

	restartCase(t, dir, networks, held)
	subnetEgress(t, ctx, alpha, alphaClient, images)
}

func manifestImages(t *testing.T, imageDir string) []string {
	t.Helper()
	manifest, err := nodefirecracker.LoadManifest(imageDir)
	if err != nil {
		t.Fatalf("the image %s: %v", imageDir, err)
	}
	out := make([]string, 0, len(manifest.Images))
	for _, img := range manifest.Images {
		out = append(out, img.Reference)
	}
	sort.Strings(out)
	return out
}

func subnetClient(t *testing.T, c subnetCluster) kubernetes.Interface {
	t.Helper()
	cfg, err := clientcmd.BuildConfigFromFlags("", c.kubeconfig)
	if err != nil {
		t.Fatalf("reading %s's kubeconfig: %v", c.name, err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("the client for %s: %v", c.name, err)
	}
	return client
}

func mustHost(t *testing.T, lease subnetallocator.Lease, n int) string {
	t.Helper()
	addr, err := lease.Host(n)
	if err != nil {
		t.Fatalf("Host(%d) of %s: %v", n, lease, err)
	}
	return addr.String()
}

func firstSystemPod(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 5 && fields[0] == "kube-system" {
			return fields[1]
		}
	}
	t.Fatalf("no kube-system pod in what kubectl printed, so there is nothing to read a log "+
		"from:\n%s", out)
	return ""
}

func systemPodsConverged(out string) (bool, string) {
	var pending []string
	seen := 0
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] == "NAMESPACE" {
			continue
		}
		if fields[0] != "kube-system" {
			continue
		}
		seen++
		ready, status := fields[2], fields[3]
		if status == "Completed" || status == "Succeeded" {
			continue
		}
		parts := strings.SplitN(ready, "/", 2)
		if len(parts) == 2 && parts[0] == parts[1] && status == "Running" {
			continue
		}
		pending = append(pending, fields[1]+" "+ready+" "+status)
	}
	if seen == 0 {
		return false, "no pods in kube-system yet"
	}
	if len(pending) > 0 {
		return false, "still waiting on " + strings.Join(pending, ", ")
	}
	return true, ""
}

type subnetPodCounter struct {
	ready    int
	total    int
	restarts int
	status   string
}

func subnetPodCounters(out string) map[string]subnetPodCounter {
	counters := make(map[string]subnetPodCounter)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] == "NAMESPACE" || fields[0] != "kube-system" {
			continue
		}
		parts := strings.SplitN(fields[2], "/", 2)
		if len(parts) != 2 {
			continue
		}
		ready, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		total, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		restarts, err := strconv.Atoi(fields[4])
		if err != nil {
			continue
		}
		counters[fields[1]] = subnetPodCounter{ready: ready, total: total,
			restarts: restarts, status: fields[3]}
	}
	return counters
}

func systemPodsRegressed(before, after string) (bool, string) {
	saw := subnetPodCounters(before)
	now := subnetPodCounters(after)
	if len(saw) == 0 {
		return false, ""
	}
	for pod, was := range saw {
		is, ok := now[pod]
		if !ok {
			return true, pod + " left kube-system"
		}
		if is.restarts > was.restarts {
			return true, fmt.Sprintf("%s restarted %d more times and is now %d/%d %s",
				pod, is.restarts-was.restarts, is.ready, is.total, is.status)
		}
		if is.total-is.ready > was.total-was.ready {
			return true, fmt.Sprintf("%s lost a container and is now %d/%d %s",
				pod, is.ready, is.total, is.status)
		}
		if is.status == "CrashLoopBackOff" || is.status == "Error" {
			return true, pod + " is " + is.status
		}
	}
	return false, ""
}

func consoleDialLines(console string) string {
	var keep []string
	for _, line := range strings.Split(console, "\n") {
		if strings.Contains(line, "KCP_CONN") || strings.Contains(line, "KCP_AGENT_IPADDR") ||
			strings.Contains(line, "KCP_AGENT_ROUTE") || strings.Contains(line, "KCP_AGENT_RESOLV") {
			keep = append(keep, line)
		}
	}
	if len(keep) == 0 {
		return "the console carries no dial line at all"
	}
	return lastLines(strings.Join(keep, "\n"), 12)
}

func subnetRequireSystemPods(t *testing.T, ctx context.Context, client kubernetes.Interface, c subnetCluster) {
	t.Helper()
	shell := func(args ...string) string {
		argv := append([]string{"--kubeconfig", c.kubeconfig, "--request-timeout=10s"}, args...)
		out, _ := exec.Command("kubectl", argv...).CombinedOutput()
		return string(out)
	}
	deadline := time.Now().Add(5 * time.Minute)
	var last string
	for time.Now().Before(deadline) {
		last = shell("get", "pods", "-A", "-o", "wide")
		if converged, why := systemPodsConverged(last); converged {
			t.Logf("%s's system pods, immediately after convergence:\n%s", c.name, last)
			subnetRequireEndpointsDialable(t, client, c)
			subnetRequirePodLogs(t, c, firstSystemPod(t, last))
			heldFor := time.Now().Add(subnetSystemPodsHold)
			for time.Now().Before(heldFor) {
				poll := shell("get", "pods", "-A", "-o", "wide")
				if regressed, why := systemPodsRegressed(last, poll); regressed {
					t.Fatalf("%s's system pods converged and then %s -- pods-Ready is the WEAKER "+
						"instrument: three system pods have been measured sitting at 1/1 over a "+
						"guest whose firewall was incomplete, so the console is read too\n%s",
						c.name, why, poll)
				}
				last = poll
				select {
				case <-ctx.Done():
					t.Fatalf("%s's system pods: %v", c.name, ctx.Err())
				case <-time.After(2 * time.Second):
				}
			}
			t.Logf("%s's system pods held for %s; the guest console's own account follows:\n%s",
				c.name, subnetSystemPodsHold, consoleDialLines(guestConsole(t, c.nodeDir)))
			return
		} else {
			last = last + "\n(" + why + ")"
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s's system pods: %v", c.name, ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	t.Fatalf("%s's system pods never came up:\n%s", c.name, last)
}

func restartCase(t *testing.T, dir string, networks *subnetallocator.Allocator, held map[string]subnetCluster) {
	t.Helper()
	before, err := os.ReadFile(subnetLeasePath(dir))
	if err != nil {
		t.Fatalf("reading the lease file: %v", err)
	}
	restarted, err := subnetallocator.Open(subnetallocator.Config{LeasePath: subnetLeasePath(dir)})
	if err != nil {
		t.Fatalf("a restarted gateway could not open the lease file: %v", err)
	}
	for _, cluster := range strings.Split(subnetClusters, ",") {
		lease, err := restarted.Allocate(subnetallocator.Request{Cluster: cluster})
		if err != nil {
			t.Fatalf("allocating %s after a restart: %v", cluster, err)
		}
		was := held[cluster].lease
		if lease.Block.Masked() != was.Block.Masked() {
			t.Fatalf("a restarted gateway leased %s to %s, which already holds %s. Leasing a second "+
				"range renumbers a cluster whose nodes are addressed in the first one, and the first "+
				"range is then held by nobody: the next cluster to walk the pool is handed a range "+
				"live nodes are still in", lease.Block.Masked(), cluster, was.Block.Masked())
		}
		if got := lease.Gateway().String(); got != held[cluster].gateway {
			t.Fatalf("a restarted gateway gives %s the gateway %s, want %s", cluster, got,
				held[cluster].gateway)
		}
	}
	after, err := os.ReadFile(subnetLeasePath(dir))
	if err != nil {
		t.Fatalf("reading the lease file: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("a restart rewrote the lease file:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	for _, cluster := range strings.Split(subnetClusters, ",") {
		c := held[cluster]
		live, livePort := advertisedAddress(t, subnetClient(t, c), c)
		subnetRequireEndpointsDialable(t, subnetClient(t, c), c)
		if live != c.gateway {
			t.Fatalf("%s's apiserver advertises %s and this gateway read %s from the lease file: "+
				"a gateway that restarted must derive the address its cluster is ALREADY "+
				"advertised on, or the address in the running k3s and the address in the lease "+
				"have diverged and the next node dials one the apiserver no longer answers on",
				cluster, live, c.gateway)
		}
		if livePort != c.port {
			t.Fatalf("%s advertises %s:%d after a gateway restart and binds %d before it", cluster,
				live, livePort, c.port)
		}
		derived, err := restarted.Allocate(subnetallocator.Request{Cluster: cluster})
		if err != nil {
			t.Fatalf("deriving %s's advertise address after the restart: %v", cluster, err)
		}
		if derived.Gateway().String() != live {
			t.Fatalf("a restarted gateway would announce %s for %s while the running apiserver "+
				"announces %s", derived.Gateway(), cluster, live)
		}
		t.Logf("%s's running apiserver advertises %s, the range it holds is %s and a restarted "+
			"gateway derives the same address; its guests dial %s:%d", cluster, live,
			c.lease.Block.Masked(), c.gateway, c.port)
	}
	t.Logf("a restarted gateway re-found both ranges from the lease file and leased nothing new")
}

func advertisedAddress(t *testing.T, client kubernetes.Interface, c subnetCluster) (string, int) {
	t.Helper()
	slice, err := client.DiscoveryV1().EndpointSlices("default").Get(context.Background(), "kubernetes", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading %s's kubernetes EndpointSlice, which is where a cluster records the "+
			"address its apiserver is advertised on: %v", c.name, err)
	}
	port := -1
	for _, p := range slice.Ports {
		if p.Port != nil {
			port = int(*p.Port)
		}
	}
	for _, endpoint := range slice.Endpoints {
		for _, address := range endpoint.Addresses {
			if address == "" {
				continue
			}
			if _, err := netip.ParseAddr(address); err == nil {
				return address, port
			}
		}
	}
	t.Fatalf("%s's kubernetes EndpointSlice names no address, so there is nothing to compare the "+
		"leased gateway against", c.name)
	return "", -1
}

func subnetRequireEndpointsDialable(t *testing.T, client kubernetes.Interface, c subnetCluster) {
	t.Helper()
	address, port := advertisedAddress(t, client, c)
	if port != c.port {
		t.Fatalf("%s's kubernetes Service endpoints carry %s:%d and the apiserver this harness "+
			"started binds %d. Every client in the cluster -- coredns, metrics-server, "+
			"local-path-provisioner, and the tunnel a guest's kubelet dials back to the apiserver "+
			"-- reaches the apiserver through THIS endpoint, so an endpoint naming a port "+
			"--advertise-port named and --https-listen-port did not is an endpoint nothing answers "+
			"and the pods stay unready with a refused dial rather than any error that names the "+
			"flag", c.name, address, port, c.port)
	}
	t.Logf("%s's kubernetes Service endpoints are %s:%d, which is the port k3s was told to bind",
		c.name, address, port)
}

func subnetRequirePodLogs(t *testing.T, c subnetCluster, pod string) {
	t.Helper()
	out, err := exec.Command("kubectl", "--kubeconfig", c.kubeconfig, "--request-timeout=20s",
		"logs", "-n", "kube-system", pod, "--tail=5").CombinedOutput()
	body := string(out)
	if err != nil && strings.Contains(body, "502") {
		t.Fatalf("`kubectl logs %s` was refused with %q. The apiserver reaches a guest's kubelet "+
			"through the tunnel the node opened to the apiserver, and the node opened it at the "+
			"address the kubernetes Service endpoints carry, so a 502 here is the apiserver "+
			"answering for an endpoint that names a port nothing binds:\n%s", pod, err, body)
	}
	if strings.Contains(body, "connection refused") {
		t.Fatalf("`kubectl logs %s` reports a refused dial:\n%s", pod, body)
	}
	if strings.TrimSpace(body) == "" {
		t.Fatalf("`kubectl logs %s` returned nothing at all (err %v): an apiserver whose log proxy "+
			"is tunneled answers with what the container wrote, so an empty answer is the proxy "+
			"having nothing to forward rather than a pod that wrote nothing", pod, err)
	}
	t.Logf("`kubectl logs %s --tail=5` answered through the apiserver's own tunnel:\n%s", pod, body)
}

func subnetEgress(t *testing.T, ctx context.Context, c subnetCluster, client kubernetes.Interface, images []string) {
	t.Helper()
	busybox := ""
	for _, ref := range images {
		if strings.Contains(ref, "busybox") {
			busybox = ref
		}
	}
	if busybox == "" {
		t.Fatalf("the image carries no busybox, so there is nothing seeded to run an egress probe in")
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "egress", Namespace: "default"},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "egress",
				Image:   busybox,
				Command: []string{"sh", "-c", subnetEgressScript},
			}},
		},
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		_, err := client.CoreV1().Pods("default").Create(ctx, pod, metav1.CreateOptions{})
		if err == nil {
			break
		}
		if !apierrors.IsAlreadyExists(err) && !strings.Contains(err.Error(), "serviceaccount") {
			t.Fatalf("applying the egress pod: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the default namespace never admitted the egress pod: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
	wait := time.Now().Add(4 * time.Minute)
	for time.Now().Before(wait) {
		got, err := client.CoreV1().Pods("default").Get(ctx, "egress", metav1.GetOptions{})
		if err == nil && got.Status.Phase == corev1.PodSucceeded {
			logs, err := client.CoreV1().Pods("default").GetLogs("egress", &corev1.PodLogOptions{}).Do(ctx).Raw()
			if err != nil {
				t.Fatalf("reading the egress pod's log: %v", err)
			}
			t.Logf("--- %s's egress pod log ---\n%s", c.name, string(logs))
			if !strings.Contains(string(logs), "EGRESS_DONE") {
				t.Errorf("the egress pod did not finish its probes:\n%s", logs)
			}
			if !strings.Contains(string(logs), "HEALTHZ_RC=0") || !strings.Contains(string(logs), "ok") {
				t.Errorf("a pod on the node could not reach the cluster's own API at its Service "+
					"IP with its own service-account token, which is the assertion that the "+
					"cluster's DNS and API path work rather than merely that containers "+
					"started:\n%s", logs)
			}
			if !strings.Contains(string(logs), "HTTP/") {
				t.Errorf("the egress pod got no HTTP status line from outside the guest, so nothing "+
					"in it demonstrably left this host:\n%s", logs)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("the egress pod: %v", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	got, _ := client.CoreV1().Pods("default").Get(ctx, "egress", metav1.GetOptions{})
	events, _ := client.CoreV1().Events("default").List(ctx, metav1.ListOptions{})
	t.Fatalf("the egress pod never finished: %+v\nevents:\n%+v", got, events)
}

const subnetEgressScript = `TOKEN=$(cat /var/run/secrets/kubernetes.io/serviceaccount/token); ` +
	`echo "--- the cluster's own API, at its Service IP, with this pod's own token"; ` +
	`wget -qO- --no-check-certificate --header "Authorization: Bearer $TOKEN" https://10.43.0.1:443/healthz; ` +
	`echo "HEALTHZ_RC=$?"; ` +
	`echo "--- the cluster's own DNS"; nslookup kubernetes.default.svc.cluster.local 2>&1 | head -6; ` +
	`echo "--- a public name, which needs the gateway to forward DNS"; nslookup example.com 2>&1 | head -6; ` +
	`echo "--- a public address, which needs the namespace's traffic NATed out"; ` +
	`wget -T 15 -S -O /dev/null http://1.1.1.1/ 2>&1 | head -4; ` +
	`echo "EGRESS_DONE"`
