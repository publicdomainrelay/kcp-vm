package clusterstack

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/processrunner"
)

func TestAMarketClusterNamesItsSchedulerConfigInsideItsDataDir(t *testing.T) {
	s := market()
	argv, err := s.ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	want := "--kube-scheduler-arg=config=" + KubeSchedulerConfigPath(s.DataDir)
	if !contains(argv, want) {
		t.Fatalf("argv = %v\n\nwant %s: a market cluster starts from zero nodes, and kube-scheduler "+
			"holds a pod it could not place for a backoff that doubles to %d s. The node that "+
			"finally makes it placeable does not cancel that wait, so the pod that waits for the "+
			"first node waits again after it arrives. The config file is the ONLY form k3s "+
			"v1.36.4 takes: kube-scheduler dropped --pod-initial-backoff-seconds and "+
			"--pod-max-backoff-seconds (MEASURED: k3s answers `scheduler exited: unknown flag` and "+
			"shuts the whole server down), and --config is what is left",
			argv, want, 10)
	}
	if !strings.HasPrefix(KubeSchedulerConfigPath(s.DataDir), s.DataDir+string(os.PathSeparator)) {
		t.Fatalf("the config path %s is not inside the data dir %s: two clusters would share one "+
			"file, and the path in it is the cluster that wrote it last",
			KubeSchedulerConfigPath(s.DataDir), s.DataDir)
	}
}

func TestTheSchedulerConfigShortensBothBackoffs(t *testing.T) {
	s := market()
	body, err := s.KubeSchedulerConfig()
	if err != nil {
		t.Fatalf("KubeSchedulerConfig: %v", err)
	}
	text := string(body)
	for _, want := range []string{
		"apiVersion: kubescheduler.config.k8s.io/v1",
		"kind: KubeSchedulerConfiguration",
		"clientConnection:",
		"kubeconfig: " + SchedulerKubeconfig(s.DataDir),
		"podInitialBackoffSeconds: 1",
		"podMaxBackoffSeconds: 1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("the scheduler config is:\n%s\n\nwant %q. The client connection is not "+
				"decoration: with a --config file kube-scheduler IGNORES the deprecated "+
				"--kubeconfig flag k3s still passes, so a config with no clientConnection fails "+
				"with `no configuration has been provided` and takes the server down", text, want)
		}
	}
}

func TestAPlainClusterCarriesNoSchedulerConfig(t *testing.T) {
	s := valid()
	argv, err := s.ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	for _, a := range argv {
		if strings.HasPrefix(a, "--kube-scheduler-arg=config=") {
			t.Fatalf("a stack that dials no market carries %s: nothing starts from zero nodes "+
				"there, so nothing needs the backoff, and the file it names is not written for "+
				"this cluster", a)
		}
	}
	if _, err := s.KubeSchedulerConfig(); !errors.Is(err, ErrNoSchedulerBackoffForAPlainCluster) {
		t.Fatalf("a plain cluster rendered a scheduler config: %v", err)
	}
}

func TestTheSchedulerConfigNamesItsOwnCluster(t *testing.T) {
	a := market()
	other := market()
	other.DataDir = "/var/lib/socialweb/clusters/b"

	first, err := a.KubeSchedulerConfig()
	if err != nil {
		t.Fatalf("KubeSchedulerConfig: %v", err)
	}
	second, err := other.KubeSchedulerConfig()
	if err != nil {
		t.Fatalf("KubeSchedulerConfig: %v", err)
	}
	if KubeSchedulerConfigPath(a.DataDir) == KubeSchedulerConfigPath(other.DataDir) {
		t.Fatal("two clusters share one scheduler config path")
	}
	if string(first) == string(second) {
		t.Fatal("two clusters render the same scheduler config: its client connection would " +
			"point both at one cluster's apiserver, so one of them schedules against the other's " +
			"objects")
	}
	if !strings.Contains(string(first), SchedulerKubeconfig(a.DataDir)) {
		t.Fatalf("the first cluster's config does not name its own scheduler kubeconfig:\n%s", first)
	}
}

func probeSpec(t *testing.T) Spec {
	t.Helper()
	dir := t.TempDir()
	policy := filepath.Join(dir, "audit-policy.yaml")
	if err := os.WriteFile(policy, []byte("apiVersion: audit.k8s.io/v1\nkind: Policy\nrules:\n  - level: Metadata\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := market()
	s.DataDir = dir
	s.TokenFile = filepath.Join(dir, "token")
	if err := os.WriteFile(s.TokenFile, []byte("probeSchedulerToken0123456789abcdef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Audit.PolicyPath = policy
	s.Audit.LogPath = filepath.Join(dir, "audit.log")
	body, err := s.KubeSchedulerConfig()
	if err != nil {
		t.Fatalf("KubeSchedulerConfig: %v", err)
	}
	if err := os.WriteFile(KubeSchedulerConfigPath(dir), body, 0o600); err != nil {
		t.Fatal(err)
	}
	return s
}

func probeArgv(t *testing.T, s Spec) []string {
	t.Helper()
	argv, err := s.ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	argv[0] = k3sOnPath(t)
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--datastore-endpoint" {
			argv[i+1] = "sqlite://" + filepath.Join(s.DataDir, "probe.db")
		}
	}
	return argv
}

func startK3s(t *testing.T, argv []string) string {
	t.Helper()
	log, err := os.CreateTemp(t.TempDir(), "k3s.log")
	if err != nil {
		t.Fatal(err)
	}
	name := log.Name()
	p, err := processrunner.Start(t.Context(), processrunner.Spec{Argv: argv, Output: log, DieWithParent: true})
	if err != nil {
		t.Fatalf("start k3s: %v", err)
	}
	t.Cleanup(func() {
		_ = p.Stop(os.Kill)
		_ = p.Wait()
		_ = log.Close()
	})
	return name
}

func kubectl(t *testing.T, s Spec, stdin string, args ...string) string {
	t.Helper()
	bin := k3sOnPath(t)
	argv := append([]string{"kubectl", "--kubeconfig", AdminKubeconfig(s.DataDir)}, args...)
	cmd := exec.Command(bin, argv...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("k3s %v: %v\n%s", argv, err, out)
	}
	return string(out)
}

func kubectlLenient(t *testing.T, s Spec, args ...string) string {
	t.Helper()
	bin := k3sOnPath(t)
	argv := append([]string{"kubectl", "--kubeconfig", AdminKubeconfig(s.DataDir)}, args...)
	out, _ := exec.Command(bin, argv...).CombinedOutput()
	return string(out)
}

func waitForKube(t *testing.T, s Spec, wait time.Duration) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if out := kubectlLenient(t, s, "get", "--raw=/readyz"); strings.Contains(out, "ok") {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("the cluster's apiserver never answered /readyz within %s", wait)
}

const (
	probeNodeName = "probe-node"
	probePodName  = "probe-pod"
)

func probeNodeManifest() string {
	return `apiVersion: v1
kind: Node
metadata:
  name: probe-node
  labels:
    kubernetes.io/hostname: probe-node
    kubernetes.io/os: linux
spec: {}
`
}

func probePodManifest() string {
	return `apiVersion: v1
kind: Pod
metadata:
  name: probe-pod
  namespace: default
spec:
  containers:
  - name: c
    image: busybox
`
}

func probeNodeReady(t *testing.T, s Spec) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	kubectl(t, s, "", "patch", "node", "probe-node", "--subresource=status", "--type=merge", "-p",
		`{"status":{"capacity":{"cpu":"8","memory":"16Gi","pods":"110"},`+
			`"allocatable":{"cpu":"8","memory":"16Gi","pods":"110"},"phase":"Running",`+
			`"nodeInfo":{"kubeletVersion":"v1.36.4","operatingSystem":"linux","architecture":"amd64"},`+
			`"conditions":[{"type":"Ready","status":"True","reason":"KubeletReady","message":"probe",`+
			`"lastHeartbeatTime":"`+now+`","lastTransitionTime":"`+now+`"}]}}`)
}

func probePlacesThePod(t *testing.T, s Spec, wait time.Duration) (bool, string) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		placed := strings.TrimSpace(kubectlLenient(t, s, "get", "pod", probePodName, "-o", "jsonpath={.spec.nodeName}"))
		if placed == probeNodeName {
			return true, placed
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false, ""
}

func probeStartsAndWaitsForAPod(t *testing.T, s Spec) bool {
	t.Helper()
	startK3s(t, probeArgv(t, s))
	waitForKube(t, s, 120*time.Second)
	kubectlLenient(t, s, "create", "serviceaccount", "default", "-n", "default")
	kubectl(t, s, probePodManifest(), "apply", "-f", "-")
	kubectl(t, s, probeNodeManifest(), "apply", "-f", "-")
	probeNodeReady(t, s)
	placed, where := probePlacesThePod(t, s, 60*time.Second)
	if placed {
		t.Logf("the scheduler placed the pod on %s", where)
	}
	return placed
}

func TestK3sPlacesAZeroNodePodWithTheMarketSchedulerConfig(t *testing.T) {
	s := probeSpec(t)
	if !probeStartsAndWaitsForAPod(t, s) {
		t.Fatalf("k3s started with the scheduler config this package writes and never placed the " +
			"pod it was holding for a node. A scheduler that is not running is exactly what a " +
			"market cluster's first node arrives to: the pod that waited for it waits again")
	}
}

func TestK3sPlacesNoPodWhenTheSchedulerConfigIsUnreadable(t *testing.T) {
	s := probeSpec(t)
	if err := os.WriteFile(KubeSchedulerConfigPath(s.DataDir), []byte("this is: [ not: valid: yaml:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if probeStartsAndWaitsForAPod(t, s) {
		t.Fatalf("k3s placed the pod with a config file kube-scheduler cannot read, so the file " +
			"this package writes is not what the scheduler is running from and the acceptance " +
			"test above proves nothing")
	}
}

func TestTheSchedulerConfigTurnsLeaderElectionOff(t *testing.T) {
	body, err := market().KubeSchedulerConfig()
	if err != nil {
		t.Fatalf("KubeSchedulerConfig: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "leaderElection:\n  leaderElect: false\n") {
		t.Fatalf("the scheduler config is:\n%s\n\nwant leaderElection.leaderElect false. Measured on prod "+
			"2026-10-10: k3s --disable-agent starts kube-scheduler only once a node registers "+
			"(02:53:17.22Z), and the scheduler then waited out the lease its previous incarnation held "+
			"(\"Attempting to acquire leader lease\" 02:53:17.35Z, \"Successfully acquired lease\" "+
			"02:53:33.08Z), so the first pod was scheduled 15 s after its Node was Ready. One k3s "+
			"server per cluster means there is nothing to elect", text)
	}
}
