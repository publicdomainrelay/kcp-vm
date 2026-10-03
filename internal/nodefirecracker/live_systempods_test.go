//go:build linux

package nodefirecracker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

const (
	liveSystemPodsTimeout = 5 * time.Minute

	liveSystemPodsStabilityWindow = 60 * time.Second

	liveSystemPodsNamespace = "kube-system"

	liveSystemPodsProbePod = "liveweb-reach"

	liveSystemPodsServiceAddress = "https://10.43.0.1:443/healthz"

	liveSystemPodsServiceProbe = `wget -q -O - --no-check-certificate --header "Authorization: Bearer $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)" ` + liveSystemPodsServiceAddress

	liveSystemPodsProbeDeadline = 3 * time.Minute
)

var liveSystemPodsUnreachableSignatures = []string{
	"connection refused",
	"can't connect",
	"bad address",
	"connection timed out",
	"no route to host",
	"network is unreachable",
}

var liveRegistryFailureSignatures = []string{
	"ErrImagePull",
	"ImagePullBackOff",
	"no such host",
	"dial tcp",
	"manifest unknown",
	"failed to resolve reference",
}

type liveServer struct {
	Kubeconfig string

	Port int

	TokenFile string

	LogFile string
}

func startLiveServer(t *testing.T, k3s, kubectl, dir, advertise string) liveServer {
	t.Helper()
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
		serverLog.Close()
		_ = syscall.Kill(-server.Process.Pid, syscall.SIGKILL)
		_ = server.Wait()
	})
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(kubeconfig); err == nil {
			if _, err := exec.Command(kubectl, "--kubeconfig", kubeconfig,
				"--request-timeout=5s", "get", "nodes").CombinedOutput(); err == nil {
				return liveServer{
					Kubeconfig: kubeconfig,
					Port:       serverPort,
					TokenFile:  tokenFile,
					LogFile:    filepath.Join(dir, "server.log"),
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "server.log"))
	t.Fatalf("k3s server never became ready:\n%s", string(raw))
	return liveServer{}
}

func TestLiveGuestBringsUpTheClustersSystemPodsWithNoResolver(t *testing.T) {
	imageDir, firecracker, agent, k3s, kubectl := liveConfig(t)
	manifest, err := LoadManifest(imageDir)
	if err != nil {
		t.Fatalf("the image this test boots: %v", err)
	}
	network, lease := liveLeasedNetwork(t, liveClusterName, manifest.GuestInterface)
	advertise := lease.Gateway().String()
	if len(manifest.Images) == 0 {
		t.Fatalf("%s records no seeded images, so this image predates the seeding and the guest "+
			"booted from it would have to reach a registry it cannot resolve", imageDir)
	}
	for _, image := range manifest.Images {
		if image.Digest == "" {
			t.Fatalf("%s records %s with no digest, so a rebuild that resolved it elsewhere would "+
				"not be visible", ManifestName, image.Reference)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	dir := scratchDir(t)
	server := startLiveServer(t, k3s, kubectl, dir, advertise)

	nodeDir := filepath.Join(dir, "nodes", "liveweb-node1")
	host, err := New(Options{
		Cluster:     "liveweb",
		Server:      fmt.Sprintf("https://%s:%d", advertise, server.Port),
		NodeName:    "liveweb-node1",
		TokenFile:   server.TokenFile,
		DataDir:     nodeDir,
		Record:      func(context.Context, string, v1alpha1.NodePhase) error { return nil },
		Agent:       agent,
		Firecracker: firecracker,
		Image: Image{
			Kernel:    filepath.Join(imageDir, VMLinuxName),
			Initramfs: filepath.Join(imageDir, InitramfsName),
			Rootfs:    filepath.Join(imageDir, RootfsImageName),
		},
		Guest:   manifest.Guest(),
		Network: network,
	})
	if err != nil {
		t.Fatalf("building the substrate: %v", err)
	}
	defer func() {
		if err := host.Teardown(ctx); err != nil {
			t.Logf("teardown: %v", err)
		}
	}()
	if err := host.Provision(ctx); err != nil {
		t.Fatalf("provisioning a node: %v", err)
	}
	startNanos := readStartNanos(t, nodeDir)
	startedAt := time.Unix(0, startNanos)

	kubectlAt := func(args ...string) string {
		argv := append([]string{"--kubeconfig", server.Kubeconfig, "--request-timeout=10s"}, args...)
		out, _ := exec.Command(kubectl, argv...).CombinedOutput()
		return string(out)
	}
	deadline := time.Now().Add(4 * time.Minute)
	ready := false
	for time.Now().Before(deadline) {
		if strings.Contains(kubectlAt("get", "nodes", "--no-headers"), " Ready") {
			ready = true
			break
		}
		if fatal := Fatal(nodeDir); fatal != "" {
			t.Fatalf("the guest stopped making progress after %s: %s\n%s",
				time.Since(startedAt), fatal, consoleTail(nodeDir))
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("the guest never registered as a Ready node.\n--- kubectl get nodes ---\n%s\n%s",
			kubectlAt("get", "nodes"), consoleTail(nodeDir))
	}
	t.Logf("node ready after %s", time.Since(startedAt).Round(100*time.Millisecond))

	podsDeadline := time.Now().Add(liveSystemPodsTimeout)
	dumped := make(map[string]struct{})
	var last string
	for time.Now().Before(podsDeadline) {
		poll := kubectlAt("get", "pods", "-A", "-o", "wide")
		if strings.Contains(poll, "was refused") || strings.Contains(poll, "couldn't get current server API group list") {
			raw, _ := os.ReadFile(server.LogFile)
			t.Fatalf("the k3s server this guest joined stopped answering %s after the instance "+
				"started, so this node's cluster cannot be observed.\n--- server.log tail ---\n%s",
				time.Since(startedAt).Round(time.Second), lastLines(string(raw), 40))
		}
		last = poll
		if converged, why := systemPodsConverged(last); converged {
			t.Logf("system pods converged %s after instance start", time.Since(startedAt).Round(100*time.Millisecond))
			t.Logf("--- kubectl get pods -A -o wide ---\n%s", last)
			assertTheClusterIsReachableFromInsideIt(t, kubectl, server.Kubeconfig, kubectlAt, last)
			held, heldFor := systemPodsHeld(t, kubectlAt, last, startedAt)
			t.Logf("system pods held %s", heldFor.Round(100*time.Millisecond))
			t.Logf("--- kubectl get pods -A -o wide ---\n%s", held)
			t.Logf("--- guest console, image lines ---\n%s", consoleImageLines(consoleAll(nodeDir)))
			t.Logf("--- guest console, preflight ---\n%s", consolePreflightLines(consoleAll(nodeDir)))
			t.Logf("--- guest console tail ---\n%s", consoleTail(nodeDir))
			assertNoRegistryAccess(t, consoleAll(nodeDir))
			assertNoIptablesRestoreFailure(t, nodeDir)
			return
		} else {
			last = last + "\n(" + why + ")"
		}
		reportSystemPods(t, kubectl, server.Kubeconfig, poll, dumped)
		time.Sleep(2 * time.Second)
	}
	console := consoleAll(nodeDir)
	reportSystemPods(t, kubectl, server.Kubeconfig, last, nil)
	t.Fatalf("the cluster's system pods never came up in this guest %s after the instance started.\n"+
		"--- kubectl get pods -A -o wide ---\n%s\n--- guest console, preflight ---\n%s\n"+
		"--- guest console, image lines ---\n%s\n--- guest console tail ---\n%s",
		time.Since(startedAt).Round(time.Second), last, consolePreflightLines(console),
		consoleImageLines(console), lastLines(console, 80))
}

func reportSystemPods(t *testing.T, kubectl, kubeconfig, out string, dumped map[string]struct{}) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != liveSystemPodsNamespace {
			continue
		}
		pod, status := fields[1], fields[3]
		if status == "Running" || status == "Completed" {
			continue
		}
		key := pod + " " + status
		if dumped != nil {
			if _, done := dumped[key]; done {
				continue
			}
			dumped[key] = struct{}{}
		}
		logs, _ := exec.Command(kubectl, "--kubeconfig", kubeconfig, "--request-timeout=20s",
			"logs", "-n", liveSystemPodsNamespace, pod, "--all-containers", "--tail=25").CombinedOutput()
		t.Logf("--- logs %s (%s) ---\n%s", pod, status, logs)
	}
}

func consoleAll(dataDir string) string {
	raw, err := os.ReadFile(filepath.Join(dataDir, ConsoleName))
	if err != nil {
		return err.Error()
	}
	return string(raw)
}

func consoleImageLines(console string) string {
	var keep []string
	for _, line := range strings.Split(console, "\n") {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "image") {
			continue
		}
		if strings.Contains(lower, "import") || strings.Contains(lower, "prestag") ||
			strings.Contains(lower, "agent/images") || strings.Contains(lower, "pull") {
			keep = append(keep, line)
		}
	}
	if len(keep) == 0 {
		return "the guest's console says nothing about images at all"
	}
	return strings.Join(keep, "\n")
}

func consolePreflightLines(console string) string {
	seen := make(map[string]struct{})
	var keep []string
	for _, line := range strings.Split(console, "\n") {
		if !strings.Contains(line, "KCP_MOD") && !strings.Contains(line, "KCP_PROOF") &&
			!strings.Contains(line, "KCP_CHECKCONFIG_RC") {
			continue
		}
		parts := strings.SplitN(line, "]: ", 2)
		trimmed := strings.TrimSpace(parts[len(parts)-1])
		if _, dup := seen[trimmed]; dup {
			continue
		}
		seen[trimmed] = struct{}{}
		keep = append(keep, trimmed)
	}
	if len(keep) == 0 {
		return "the guest's console carries no preflight output"
	}
	return strings.Join(keep, "\n")
}

func systemPodsConverged(out string) (bool, string) {
	var pending []string
	seen := 0
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] == "NAMESPACE" {
			continue
		}
		if fields[0] != liveSystemPodsNamespace {
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
		return false, "no pods in " + liveSystemPodsNamespace + " yet"
	}
	if len(pending) > 0 {
		return false, "still waiting on " + strings.Join(pending, ", ")
	}
	return true, ""
}

type livePodCounter struct {
	ready    int
	total    int
	restarts int
	status   string
}

func livePodCounters(out string) map[string]livePodCounter {
	counters := make(map[string]livePodCounter)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] == "NAMESPACE" || fields[0] != liveSystemPodsNamespace {
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
		counters[fields[1]] = livePodCounter{ready: ready, total: total, restarts: restarts, status: fields[3]}
	}
	return counters
}

func systemPodsRegressed(before, after string) (bool, string) {
	saw := livePodCounters(before)
	now := livePodCounters(after)
	if len(saw) == 0 {
		return false, ""
	}
	for pod, was := range saw {
		is, ok := now[pod]
		if !ok {
			return true, pod + " left " + liveSystemPodsNamespace
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

func systemPodsHeld(t *testing.T, kubectlAt func(...string) string, converged string, startedAt time.Time) (string, time.Duration) {
	t.Helper()
	held := converged
	deadline := time.Now().Add(liveSystemPodsStabilityWindow)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		poll := kubectlAt("get", "pods", "-A", "-o", "wide")
		if regressed, why := systemPodsRegressed(held, poll); regressed {
			t.Fatalf("the cluster's system pods were %s after the instance started and then %s, so "+
				"this guest brought them up and did not keep them up.\n--- kubectl get pods -A -o wide ---\n%s",
				time.Since(startedAt).Round(time.Second), why, poll)
		}
		held = poll
	}
	return held, time.Since(startedAt)
}

func assertNoIptablesRestoreFailure(t *testing.T, nodeDir string) {
	t.Helper()
	console := consoleAll(nodeDir)
	for _, signature := range []string{
		"Failed to execute iptables-restore",
		"Failed to run iptables-restore",
		"Aborting sync",
		"not supported, missing kernel module",
	} {
		if strings.Contains(console, signature) {
			t.Errorf("the guest's console carries %q, so an iptables ruleset did not land in this "+
				"guest even though its system pods are Ready: the firewall the cluster believes it "+
				"has is not the one it has", signature)
		}
	}
}

func assertNoRegistryAccess(t *testing.T, console string) {
	t.Helper()
	lines := consoleImageLines(console)
	if !strings.Contains(strings.ToLower(lines), "import") &&
		!strings.Contains(strings.ToLower(lines), "prestag") {
		t.Errorf("the guest's console says nothing about importing the images it was seeded with, "+
			"so there is no evidence in it that the system pods came up from the local tarballs "+
			"rather than from a registry. The lines it does have about images are:\n%s", lines)
	}
	for _, signature := range liveRegistryFailureSignatures {
		if strings.Contains(console, signature) {
			t.Errorf("the guest's console mentions %q, so something in it reached for a registry "+
				"even though the images it needs were seeded into its filesystem", signature)
		}
	}
}

func kubectlOut(kubectl, kubeconfig string, args ...string) (string, error) {
	argv := append([]string{"--kubeconfig", kubeconfig, "--request-timeout=30s"}, args...)
	out, err := exec.Command(kubectl, argv...).CombinedOutput()
	return string(out), err
}

func liveSystemPodsNamed(pods, prefix string) string {
	for _, line := range strings.Split(pods, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != liveSystemPodsNamespace {
			continue
		}
		if strings.HasPrefix(fields[1], prefix) {
			return fields[1]
		}
	}
	return ""
}

func assertTheClusterIsReachableFromInsideIt(t *testing.T, kubectl, kubeconfig string, kubectlAt func(...string) string, pods string) {
	t.Helper()

	endpoints := strings.TrimSpace(kubectlAt("get", "node", liveNodeName, "-o",
		"jsonpath={.status.daemonEndpoints.kubeletEndpoint}"))
	if !strings.Contains(endpoints, strconv.Itoa(liveDefaultKubeletPort)) {
		t.Errorf("this node reports its own kubelet endpoint as %q, which does not name %d. Nothing "+
			"tells this guest's kubelet which port to bind, so it binds its own default, and that "+
			"field is how the apiserver and metrics-server find it: a Ready column says a kubelet ran "+
			"the pods' probes, and says nothing about anything being able to reach that kubelet "+
			"afterwards", endpoints, liveDefaultKubeletPort)
	} else {
		t.Logf("the node reports its own kubelet endpoint as %s, the port an untold kubelet binds", endpoints)
	}

	health := strings.TrimSpace(kubectlAt("get", "--raw", "/api/v1/nodes/"+liveNodeName+"/proxy/healthz"))
	if !strings.Contains(health, "ok") {
		t.Errorf("the apiserver could not reach this node's kubelet: "+
			"/api/v1/nodes/%s/proxy/healthz answered %q. kubectl logs, exec and port-forward all "+
			"travel this path, so a node whose system pods are every one of them Ready can still be "+
			"a node nothing can be asked about", liveNodeName, health)
	} else {
		t.Logf("the apiserver reached this node's kubelet: proxied /healthz answered %q", health)
	}

	if pod := liveSystemPodsNamed(pods, "coredns-"); pod != "" {
		logs, err := kubectlOut(kubectl, kubeconfig, "logs", "-n", liveSystemPodsNamespace, pod, "--tail=2")
		if err != nil || strings.Contains(logs, "Error from server") {
			t.Errorf("`kubectl logs` on %s answered %q (%v). The apiserver fetches a container's logs "+
				"from the kubelet that runs the pod, so this failing while the pod reads Ready is the "+
				"node's kubelet being unreachable by the path the cluster uses for it",
				pod, strings.TrimSpace(logs), err)
		}
	}

	runOut, runErr := kubectlOut(kubectl, kubeconfig, "run", liveSystemPodsProbePod, "-n", "default",
		"--image=rancher/mirrored-library-busybox:1.37.0", "--restart=Never", "--command",
		"--", "sleep", "900")
	if runErr != nil {
		t.Fatalf("creating the pod that asks this cluster whether its own Service address works: %v: "+
			"%s. The system pods cannot answer that question on their own -- each of them is "+
			"configured to dial the kubernetes Service, and when that address does not answer they "+
			"crash rather than report", runErr, strings.TrimSpace(runOut))
	}
	t.Cleanup(func() {
		_, _ = kubectlOut(kubectl, kubeconfig, "delete", "pod", "-n", "default", liveSystemPodsProbePod,
			"--ignore-not-found", "--wait=false")
	})

	phase := ""
	probeDeadline := time.Now().Add(liveSystemPodsProbeDeadline)
	for time.Now().Before(probeDeadline) {
		phase = strings.TrimSpace(kubectlAt("get", "pod", "-n", "default", liveSystemPodsProbePod, "-o",
			"jsonpath={.status.phase} {.status.containerStatuses[0].ready}"))
		if strings.HasPrefix(phase, "Running true") {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !strings.HasPrefix(phase, "Running true") {
		t.Fatalf("the pod that probes this cluster from inside it never ran on this node (%q). Its own "+
			"state: %s", phase, kubectlAt("get", "pod", "-n", "default", liveSystemPodsProbePod, "-o", "wide"))
	}

	reach, reachErr := kubectlOut(kubectl, kubeconfig, "exec", "-n", "default", liveSystemPodsProbePod,
		"--", "sh", "-c", liveSystemPodsServiceProbe)
	for _, signature := range liveSystemPodsUnreachableSignatures {
		if strings.Contains(strings.ToLower(reach), strings.ToLower(signature)) {
			t.Errorf("a pod on this node could not reach this cluster's own apiserver through the "+
				"kubernetes Service: `wget %s` answered %q (%v). Every kube-system component dials "+
				"that address, and their logs say what follows when it does not answer -- "+
				"metrics-server panics with exit 2 reading a ConfigMap, local-path-provisioner exits "+
				"1 reading one, and coredns never reaches Ready. The rule that turns a ClusterIP into "+
				"a backend is kube-proxy's, and its iptables-restore is where that rule is installed "+
				"or lost", liveSystemPodsServiceAddress, strings.TrimSpace(reach), reachErr)
			break
		}
	}
	t.Logf("--- a pod on this node, dialing this cluster's own Service address ---\n%s",
		strings.TrimSpace(reach))

	var top string
	var topErr error
	topDeadline := time.Now().Add(liveSystemPodsProbeDeadline)
	for {
		top, topErr = kubectlOut(kubectl, kubeconfig, "top", "nodes")
		if topErr == nil && strings.Contains(top, liveNodeName) {
			break
		}
		if time.Now().After(topDeadline) {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if topErr != nil || !strings.Contains(top, liveNodeName) {
		t.Errorf("metrics-server could not report this node's usage: `kubectl top nodes` answered %q "+
			"(%v). That is the second path to the same kubelet the proxied healthz and `kubectl logs` "+
			"use, and it is the one that scrapes the kubelet instead of proxying to it, so it says "+
			"whether the port this node reports for its own kubelet is the port its cluster dials",
			strings.TrimSpace(top), topErr)
	} else {
		t.Logf("--- kubectl top nodes (metrics-server scraping this node's kubelet) ---\n%s", strings.TrimSpace(top))
	}
}
