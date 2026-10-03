//go:build linux

package nodefirecracker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/liveports"
)

const (
	liveImageEnv       = "SOCIALWEB_NODEFIRECRACKER_IMAGE"
	liveBinEnv         = "SOCIALWEB_NODEFIRECRACKER_BIN"
	liveAgentEnv       = "SOCIALWEB_NODEFIRECRACKER_AGENT"
	liveK3sEnv         = "SOCIALWEB_NODEFIRECRACKER_K3S"
	liveKubectlEnv     = "SOCIALWEB_NODEFIRECRACKER_KUBECTL"
	liveScratchEnv     = "SOCIALWEB_NODEFIRECRACKER_SCRATCH"
	livePortBaseEnv    = "SOCIALWEB_NODEFIRECRACKER_PORT_BASE"
	livePortBaseValue  = liveports.GuestJoinProbeFirst
	livePortBaseWindow = liveports.GuestJoinProbeWidth

	liveClusterName = "liveweb"

	liveNodeName = "liveweb-node1"

	liveDefaultKubeletPort = 10250

	liveReadyHold = 15 * time.Second

	liveServerControllerMarker = "Kind=Addon controller"
)

var kubeletFirewallFailures = []string{
	"Failed to ensure",
	"missing kernel module?",
	"No chain/target/match by that name",
}

func liveConfig(t *testing.T) (image string, firecracker string, agent string, k3s string, kubectl string) {
	t.Helper()
	livegate.Short(t, "this test boots a real firecracker guest against a real k3s server and -short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	image = os.Getenv(liveImageEnv)
	if image == "" {
		livegate.Require(t, "set %s, %s, %s, %s and %s to a built node image, a firecracker binary, the "+
			"agent built for the guest, k3s and kubectl, and these tests boot ONE guest each against "+
			"a real k3s server on this host: one measures how long the guest takes to register as a "+
			"Ready node, and one has the guest audit the kernel and the firewall it is running. They "+
			"are skipped by default because they need a host with pasta, kvm and the image, and they "+
			"leave nothing behind when they fail",
			liveImageEnv, liveBinEnv, liveAgentEnv, liveK3sEnv, liveKubectlEnv)
	}
	firecracker = os.Getenv(liveBinEnv)
	agent = os.Getenv(liveAgentEnv)
	k3s = os.Getenv(liveK3sEnv)
	kubectl = os.Getenv(liveKubectlEnv)
	for _, req := range []struct{ name, value string }{
		{liveImageEnv, image},
		{liveBinEnv, firecracker},
		{liveAgentEnv, agent},
		{liveK3sEnv, k3s},
		{liveKubectlEnv, kubectl},
	} {
		if req.value == "" {
			t.Fatalf("%s is not set", req.name)
		}
	}
	return image, firecracker, agent, k3s, kubectl
}

type liveCluster struct {
	imageDir    string
	firecracker string
	agent       string
	kubectl     string
	network     Network
	serverPort  int
	dir         string
	tokenFile   string
	kubeconfig  string
}

func startLiveCluster(t *testing.T) *liveCluster {
	t.Helper()
	imageDir, firecracker, agent, k3s, kubectl := liveConfig(t)
	manifest, err := LoadManifest(imageDir)
	if err != nil {
		t.Fatalf("the image this test boots: %v", err)
	}
	network, lease := liveLeasedNetwork(t, liveClusterName, manifest.GuestInterface)
	advertise := lease.Gateway().String()
	dir := scratchDir(t)
	base := freeBlock(t, 4)
	serverPort, cmPort, schedPort, ccmPort := base, base+2, base+3, base+4
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte(randomToken(t)), 0o600); err != nil {
		t.Fatalf("writing the token: %v", err)
	}
	dbDir := filepath.Join(dir, "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("creating the datastore dir: %v", err)
	}
	dataDir := filepath.Join(dir, "server")
	kubeconfig := filepath.Join(dir, "kubeconfig")
	serverLog, err := os.Create(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatalf("creating the server log: %v", err)
	}
	server := exec.Command(k3s, "server",
		"--disable-agent",
		"--disable=traefik",
		"--data-dir", dataDir,
		"--datastore-endpoint", "sqlite://"+filepath.Join(dbDir, "state.db"),
		"--egress-selector-mode", "cluster",
		"--advertise-address", advertise,
		"--advertise-port", strconv.Itoa(serverPort),
		"--bind-address", "127.0.0.1",
		"--https-listen-port", strconv.Itoa(serverPort),
		"--token-file", tokenFile,
		"--write-kubeconfig", kubeconfig,
		"--kube-controller-manager-arg=secure-port="+strconv.Itoa(cmPort),
		"--kube-scheduler-arg=secure-port="+strconv.Itoa(schedPort),
		"--kube-cloud-controller-manager-arg=secure-port="+strconv.Itoa(ccmPort),
		"--tls-san", advertise,
		"--tls-san", "127.0.0.1",
	)
	server.Stdout, server.Stderr = serverLog, serverLog
	server.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := server.Start(); err != nil {
		t.Fatalf("starting k3s server: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-server.Process.Pid, syscall.SIGKILL)
		_ = server.Wait()
		_ = serverLog.Close()
	})

	cluster := &liveCluster{
		imageDir:    imageDir,
		firecracker: firecracker,
		agent:       agent,
		kubectl:     kubectl,
		network:     network,
		serverPort:  serverPort,
		dir:         dir,
		tokenFile:   tokenFile,
		kubeconfig:  kubeconfig,
	}
	deadline := time.Now().Add(3 * time.Minute)
	for !cluster.answers() {
		if time.Now().After(deadline) {
			raw, _ := os.ReadFile(filepath.Join(dir, "server.log"))
			t.Fatalf("k3s server never became ready:\n%s", string(raw))
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("server ready; before:%q", strings.TrimSpace(cluster.nodes()))
	cluster.awaitControllers(t)
	return cluster
}

func (c *liveCluster) awaitControllers(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if raw, err := os.ReadFile(filepath.Join(c.dir, "server.log")); err == nil &&
			strings.Contains(string(raw), liveServerControllerMarker) {
			t.Logf("the server's own controllers are running; a node may be booted")
			return
		}
		if time.Now().After(deadline) {
			raw, _ := os.ReadFile(filepath.Join(c.dir, "server.log"))
			t.Fatalf("the k3s server never reported %q, so a node booted now would be asking a "+
				"server whose agent-facing controllers are not up yet: k3s registers the "+
				"node-password controller immediately before it sets Runtime.Core and answers "+
				"/v1-k3s/*.crt for a remote node, and the only sign it has done so that a test "+
				"holding nothing but the cluster token can see is a controller starting on its own "+
				"console.\n--- server log tail ---\n%s", liveServerControllerMarker,
				lastLines(string(raw), 40))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (c *liveCluster) answers() bool {
	if _, err := os.Stat(c.kubeconfig); err != nil {
		return false
	}
	_, err := exec.Command(c.kubectl, "--kubeconfig", c.kubeconfig,
		"--request-timeout=5s", "get", "nodes").CombinedOutput()
	return err == nil
}

func (c *liveCluster) nodes() string {
	out, _ := exec.Command(c.kubectl, "--kubeconfig", c.kubeconfig,
		"--request-timeout=5s", "get", "nodes", "--no-headers").CombinedOutput()
	return string(out)
}

func (c *liveCluster) boot(t *testing.T, auditKernel bool) (string, int64) {
	t.Helper()
	manifest, err := LoadManifest(c.imageDir)
	if err != nil {
		t.Fatalf("the image this test boots: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	nodeDir := filepath.Join(c.dir, "nodes", liveNodeName)
	host, err := New(Options{
		Cluster:     "liveweb",
		Server:      fmt.Sprintf("https://%s:%d", c.network.Gateway, c.serverPort),
		NodeName:    liveNodeName,
		TokenFile:   c.tokenFile,
		DataDir:     nodeDir,
		Record:      func(context.Context, string, v1alpha1.NodePhase) error { return nil },
		Agent:       c.agent,
		Firecracker: c.firecracker,
		Image: Image{
			Kernel:    filepath.Join(c.imageDir, VMLinuxName),
			Initramfs: filepath.Join(c.imageDir, InitramfsName),
			Rootfs:    filepath.Join(c.imageDir, RootfsImageName),
		},
		Guest:       manifest.Guest(),
		Network:     c.network,
		AuditKernel: auditKernel,
	})
	if err != nil {
		t.Fatalf("building the substrate: %v", err)
	}
	t.Cleanup(func() {
		if err := host.Teardown(ctx); err != nil {
			t.Logf("teardown: %v", err)
		}
	})
	if err := host.Provision(ctx); err != nil {
		t.Fatalf("provisioning a node: %v", err)
	}
	startNanos := readStartNanos(t, nodeDir)
	t.Logf("instance start recorded at %s", time.Unix(0, startNanos).Format(time.RFC3339Nano))
	return nodeDir, startNanos
}

func (c *liveCluster) awaitReady(t *testing.T, nodeDir string, startNanos int64) time.Time {
	t.Helper()
	readyAt := time.Time{}
	for time.Since(time.Unix(0, startNanos)) < 4*time.Minute {
		if strings.Contains(c.nodes(), " Ready") {
			readyAt = time.Now()
			break
		}
		if fatal := Fatal(nodeDir); fatal != "" {
			t.Fatalf("the guest stopped making progress after %s: %s\n--- guest console tail ---\n%s",
				time.Since(time.Unix(0, startNanos)), fatal, consoleTail(nodeDir))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if readyAt.IsZero() {
		t.Fatalf("the guest never registered as a Ready node.\n--- kubectl get nodes ---\n%s\n"+
			"--- guest console tail ---\n%s", c.nodes(), consoleTail(nodeDir))
	}
	return readyAt
}

func (c *liveCluster) requireStillReady(t *testing.T, window time.Duration) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		report := c.nodes()
		if !strings.Contains(report, liveNodeName) || !strings.Contains(report, " Ready") {
			t.Fatalf("this node was Ready when it registered and is not any more, %s into a hold "+
				"meant to show that it stays that way.\n--- kubectl get nodes ---\n%s", window, report)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func TestLiveFirecrackerGuestJoinsAsAReadyNode(t *testing.T) {
	cluster := startLiveCluster(t)
	nodeDir, startNanos := cluster.boot(t, false)
	readyAt := cluster.awaitReady(t, nodeDir, startNanos)
	console := consoleTail(nodeDir)
	elapsed := readyAt.Sub(time.Unix(0, startNanos))
	t.Logf("INSTANCE_START_TO_READY_S=%.3f", elapsed.Seconds())
	t.Logf("--- kubectl get nodes ---\n%s", cluster.nodes())
	t.Logf("--- guest console tail ---\n%s", lastLines(console, 40))
	requireNothingAuditedTheGuest(t, console)
}

func TestLiveFirecrackerGuestAuditsItsKernelAndItsFirewall(t *testing.T) {
	cluster := startLiveCluster(t)
	nodeDir, startNanos := cluster.boot(t, true)
	readyAt := cluster.awaitReady(t, nodeDir, startNanos)
	cluster.requireStillReady(t, liveReadyHold)
	console := guestConsole(nodeDir)
	t.Logf("INSTANCE_START_TO_READY_S=%.3f (this boot asked for the audit on its disk, so it is not "+
		"the number the join test reports)", readyAt.Sub(time.Unix(0, startNanos)).Seconds())
	t.Logf("--- audit ---\n%s", auditSection(console))
	requireKernelAuditPassed(t, console)
	requireKubeletFirewallLanded(t, console)
}

func requireKernelAuditPassed(t *testing.T, console string) {
	t.Helper()
	if !strings.Contains(console, "KCP_AUDIT_START") {
		t.Fatalf("this guest was booted with the audit asked for on its own disk and never reported "+
			"one, so nothing checked the kernel and the module set this image ships.\n--- guest "+
			"console tail ---\n%s", lastLines(console, 40))
	}
	if absent := consoleLines(console, "KCP_AUDIT_MODULE_ABSENT"); len(absent) > 0 {
		t.Fatalf("the guest this image booted does not have every module this image names: %s. "+
			"iptables as this image ships it does not load an xtables extension on its own, so a "+
			"module that is named and not loaded is a rule kubelet cannot append and a firewall with "+
			"a hole in it. check-config does not report this: it reads the config file staged beside "+
			"the kernel and never asks what is loaded inside the guest.\n--- audit ---\n%s",
			strings.Join(absent, "; "), auditSection(console))
	}
	if !strings.Contains(console, "KCP_AUDIT_STATUS pass") {
		t.Fatalf("k3s check-config inside the guest did not pass.\n--- audit ---\n%s",
			auditSection(console))
	}
}

func requireNothingAuditedTheGuest(t *testing.T, console string) {
	t.Helper()
	if started := consoleLines(console, "KCP_AUDIT_START"); len(started) > 0 {
		t.Fatalf("this node was booted with no audit asked for on its disk and audited its kernel "+
			"anyway: %s. The audit reads this kernel's config and runs k3s check-config, which is "+
			"seconds of every node's boot, and what it verifies changes only when the image does; it "+
			"is asked for by the auditKernel flag on a node's own disk, and only a test sets it.\n--- "+
			"guest console tail ---\n%s", strings.Join(started, "; "), lastLines(console, 40))
	}
}

func requireKubeletFirewallLanded(t *testing.T, console string) {
	t.Helper()
	for _, marker := range kubeletFirewallFailures {
		if lines := consoleLines(console, marker); len(lines) > 0 {
			t.Fatalf("kubelet in this guest could not lay down its firewall: %s. check-config "+
				"returns 0 over this: it audits the config file staged beside the kernel rather than "+
				"the kernel the guest is running, and a module the config enables and the guest never "+
				"loaded reads to it as present. The guest's own console is what names it",
				strings.Join(lines, "; "))
		}
	}
}

func consoleLines(console, marker string) []string {
	var found []string
	for _, line := range strings.Split(console, "\n") {
		if strings.Contains(line, marker) {
			found = append(found, strings.TrimSpace(stripJournalPrefix(line)))
		}
	}
	return found
}

func auditSection(console string) string {
	start := strings.Index(console, "KCP_AUDIT_START")
	if start < 0 {
		return "this guest reported no audit at all"
	}
	rest := console[start:]
	if end := strings.Index(rest, "KCP_AUDIT_END"); end >= 0 {
		return rest[:end+len("KCP_AUDIT_END")]
	}
	return rest
}

func freeBlock(t *testing.T, width int) int {
	t.Helper()
	base := livePortBaseValue
	if raw := os.Getenv(livePortBaseEnv); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("%s=%q: %v", livePortBaseEnv, raw, err)
		}
		base = n
	}
	for ceiling := base + livePortBaseWindow; base < ceiling; base += 11 {
		free := true
		for p := base; p <= base+width; p++ {
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
			if err != nil {
				free = false
				break
			}
			ln.Close()
		}
		if free {
			return base
		}
	}
	t.Fatalf("no free port block of %d ports was found in the %d above %d. A fixed ceiling "+
		"under a settable base is what makes this message appear instead of a test: a base above "+
		"the ceiling scans nothing and reports the host as full, so the window is measured from "+
		"whatever base was asked for, and agents sharing a host pin different bases to stay out of "+
		"each other's way. This window is liveports.GuestJoinProbe, which no other live claim "+
		"overlaps; %s=%d moves it", width, livePortBaseWindow, base-livePortBaseWindow, livePortBaseEnv, livePortBaseValue)
	return 0
}

func randomToken(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("reading a token: %v", err)
	}
	return hex.EncodeToString(raw)
}

func scratchDir(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv(liveScratchEnv); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
		run, err := os.MkdirTemp(dir, "kcp-live-")
		if err != nil {
			t.Fatalf("creating a run directory under %s: %v", dir, err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(run) })
		return run
	}
	return t.TempDir()
}

func readStartNanos(t *testing.T, dataDir string) int64 {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(filepath.Join(dataDir, InstanceName))
		if err == nil {
			var rec struct {
				PID        int   `json:"pid"`
				StartNanos int64 `json:"startNanos"`
			}
			if err := json.Unmarshal(raw, &rec); err == nil && rec.StartNanos > 0 {
				return rec.StartNanos
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the VMM never published its instance start time")
	return 0
}

func guestConsole(dataDir string) string {
	raw, err := os.ReadFile(filepath.Join(dataDir, ConsoleName))
	if err != nil {
		return err.Error()
	}
	return string(raw)
}

func consoleTail(dataDir string) string {
	return lastLines(guestConsole(dataDir), 60)
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
