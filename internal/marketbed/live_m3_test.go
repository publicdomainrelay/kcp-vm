package marketbed

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

const (
	m3ImageEnv        = "SOCIALWEB_M3_CONTROL_PLANE_IMAGE"
	m3Cluster         = "m3"
	m3Workload        = "m3-workload"
	m3WorkloadImage   = "rancher/mirrored-library-busybox:1.37.0"
	m3PLCName         = "plc.default.global.svc.kcp.local"
	m3OwnerPDSName    = "pds.default.alice.svc.kcp.local"
	m3ContainerState  = "/var/lib/socialweb"
	m3KcpKubeconfig   = "/var/lib/socialweb/kcp/admin.kubeconfig"
	m3KcpServer       = "https://127.0.0.1:6443/clusters/"
	m3RestartWatch    = 150 * time.Second
	m3GuestGoneWithin = 180 * time.Second
)

type m3Bed struct {
	t        *testing.T
	facts    map[string]string
	gateway  string
	files    string
	image    string
	name     string
	volume   string
	issuers  []string
	ownerDid string
	key      string
	home     string
	tenantKC string
	readings []string
	ownerPDS string
	fwd      map[string]int
}

func (b *m3Bed) reading(format string, args ...any) {
	line := time.Now().UTC().Format(time.RFC3339) + " " + fmt.Sprintf(format, args...)
	b.readings = append(b.readings, line)
	b.t.Log(line)
}

func TestAMarketNodeJoinsATenantClusterThroughItsOpenBaoWithNoSsh(t *testing.T) {
	livegate.Short(t, "this test brings up the market test bed and the real control-plane container, rents a "+
		"firecracker guest for a tenant workload through the door, and watches it join with no ssh: a skip is NOT a pass")
	image := os.Getenv(m3ImageEnv)
	if image == "" {
		livegate.Require(t, "%s is unset; build the control-plane image from this tree with "+
			"deploy/control-plane-container/build.sh k8s (run from an org root whose siblings resolve) and name it here", m3ImageEnv)
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		livegate.Require(t, "%s names %s, which is not in the local docker store", m3ImageEnv, image)
	}
	root, workRoot, env := testBedEnv(t)

	deadline, hasDeadline := t.Deadline()
	if hasDeadline && time.Until(deadline) < 50*time.Minute {
		t.Fatalf("this test needs up to 50 minutes and has %s; run go test with -timeout 70m", time.Until(deadline))
	}

	b := &m3Bed{t: t, image: image}
	env = marketImageEnv(t, env, workRoot)
	b.facts = startHeldTestBed(t, root, env)
	if b.facts["CHECKS_FAILED"] != "0" {
		t.Fatalf("the test bed came up with failing checks: %v", b.facts)
	}
	b.reading("test bed held up: bidder %s, guests under %s", b.facts["BIDDER_DID"], workRoot)

	b.issuers = bidderIssuers(t, b.facts["BIDDER_STDOUT"])
	b.gateway = strings.TrimSpace(run(t, "docker", "network", "inspect", "bridge", "-f", "{{(index .IPAM.Config 0).Gateway}}"))
	b.ownerPDS = "https://127.0.0.1:" + b.facts["ALICE_PDS_PORT"]
	b.fwd = map[string]int{}
	for _, key := range []string{"ALICE_PDS_PORT", "PLC_PORT"} {
		port, err := strconv.Atoi(b.facts[key])
		if err != nil {
			t.Fatalf("the held test bed reported no %s: %v", key, b.facts)
		}
		b.fwd[key] = forwardLoopback(t, b.gateway, port)
	}
	b.ownerDid = b.createOwner()
	b.reading("owner %s created on the test bed PDS %s", b.ownerDid, b.ownerPDS)
	b.key = kcpclient.AccountHash(b.ownerDid) + "-" + m3Cluster
	b.home = "root:" + kcpclient.AccountHash(b.ownerDid) + ":" + m3Cluster
	dataDir, err := clusterprovider.ClusterDataDir(m3ContainerState+"/clusterprovider", b.key)
	if err != nil {
		t.Fatal(err)
	}
	b.tenantKC = clusterstack.AdminKubeconfig(dataDir)
	b.writeFiles()

	suffix := randomHex(t, 4)
	b.name, b.volume = "m3live-"+suffix, "m3live-state-"+suffix
	run(t, "docker", "volume", "create", b.volume)
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "logs", "--tail", "60", b.name).CombinedOutput()
			t.Logf("control plane log tail:\n%s", out)
		}
		_ = exec.Command("docker", "rm", "-f", b.name).Run()
		_ = exec.Command("docker", "volume", "rm", "-f", b.volume).Run()
	})

	b.startControlPlane()
	b.createCluster()
	linksAt := b.waitLinksListening(5 * time.Minute)
	b.reading("cluster %s Ready, ClusterLinks %s-apiserver and %s-openbao Listening", b.key, b.key, b.key)

	appliedAt := time.Now()
	b.tenant("apply", "-f", "-").stdin(workloadManifest()).must()
	b.reading("workload %s applied (intent, zero nodes)", m3Workload)

	node := b.waitForClaim(5 * time.Minute)
	created := b.claimField(node, "{.metadata.creationTimestamp}")
	b.reading("NodeClaim %s written (creationTimestamp %s), after both links listened at %s", node, created, linksAt.UTC().Format(time.RFC3339))
	b.assertLogOrder("cluster link recorded", "nodemarket: node claimed from the market")

	grant := b.waitClaimJSON(node, "{.status.grant}", 5*time.Minute)
	var g struct{ Issuer, Subject, Audience, Role string }
	if err := json.Unmarshal([]byte(grant), &g); err != nil {
		t.Fatalf("status.grant %q: %v", grant, err)
	}
	if !slices.Contains(bidderIssuers(t, b.facts["BIDDER_STDOUT"]), g.Issuer) || g.Audience != "socialweb.computer/node/"+b.key+"/"+node ||
		g.Role != node || g.Subject == "" {
		t.Fatalf("status.grant %+v; want an issuer the bidder mounted, audience socialweb.computer/node/%s/%s, role %s", g, b.key, node, node)
	}
	b.ensureIssuerHost(g.Issuer)
	b.reading("status.grant written: issuer %s subject %s audience %s", g.Issuer, g.Subject, g.Audience)
	b.waitLog("nodemarket: the node can log in to the cluster OpenBao with its workload token", node, 3*time.Minute)
	b.reading("provider created the OpenBao JWT role %s", node)

	guest := waitNewGuest(t, workRoot, 5*time.Minute)
	b.reading("guest %s booted from the bidder's cloud-init", guest)
	console := filepath.Join(workRoot, guest, "guest.console")
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		copyEvidence(t, b.name+"-guest-console.txt", console)
		copyEvidence(t, b.name+"-provider.log", b.facts["PROVIDER_LOG"])
		copyEvidence(t, b.name+"-bidder.txt", b.facts["BIDDER_STDOUT"])
		b.dumpContainerLog()
	})
	waitFileContains(t, console, "Finished setup-secrets.service", 12*time.Minute)
	b.reading("guest fetched the --secrets bundle (setup-secrets finished)")
	waitFileContains(t, console, "fetched the join secrets for "+node, 6*time.Minute)
	b.reading("guest logged in to the cluster OpenBao over its openbao link and read its KV (node-secrets)")

	b.waitTenant(fmt.Sprintf("node %s Ready", node), 10*time.Minute, func() bool {
		return b.tenant("get", "node", node, "-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`).out() == "True"
	})
	b.reading("tenant Node %s Ready", node)
	b.waitTenant("workload pod Running on "+node, 5*time.Minute, func() bool {
		out := b.tenant("get", "pods", "-n", "default", "-l", "app="+m3Workload, "-o",
			`jsonpath={range .items[*]}{.status.phase}@{.spec.nodeName} {end}`).out()
		return strings.Contains(out, "Running@"+node)
	})
	b.reading("workload pod Running on %s", node)
	if phase := b.claimField(node, "{.status.phase}"); phase != "Joining" {
		t.Fatalf("door phase %q; want Joining", phase)
	}
	b.reading("door phase Joining; joined %s after the workload was applied", time.Since(appliedAt).Round(time.Second))

	b.assertNetworkPolicyEnforced(node, console)

	b.assertNoSsh(console)

	uid := b.claimField(node, "{.metadata.uid}")
	restartAt := time.Now()
	b.startControlPlane()
	b.reading("control plane container replaced (door and provider restarted) with the node up")
	b.waitLinksListening(5 * time.Minute)
	end := time.Now().Add(m3RestartWatch)
	for time.Now().Before(end) {
		if got := b.claimField(node, "{.metadata.uid}"); got != uid {
			t.Fatalf("after the restart the claim uid is %q; want %q, untouched", got, uid)
		}
		if del := b.claimField(node, "{.metadata.deletionTimestamp}"); del != "" {
			t.Fatalf("after the restart the claim is being deleted at %s; a restart must adopt, not reap", del)
		}
		if !guestRunning(guest) {
			t.Fatalf("guest %s stopped after the restart", guest)
		}
		time.Sleep(10 * time.Second)
	}
	if ready := b.tenant("get", "node", node, "-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`).out(); ready != "True" {
		t.Fatalf("node %s reads Ready=%q %s after the restart", node, ready, m3RestartWatch)
	}
	b.waitLog("adopted a live market node a previous provider process rented", node, time.Minute)
	if strings.Contains(b.logsSince(restartAt), `"message":"submitting_vm_delete","prefix":"socialweb-computer-k8s","claim":"`) {
		t.Fatalf("a vm.delete was submitted after the restart")
	}
	b.reading("after the restart: claim %s adopted (same uid), node Ready, guest running, no vm.delete, for %s", node, m3RestartWatch)

	b.tenant("delete", "deployment", m3Workload, "-n", "default").must()
	b.reading("workload deleted")
	b.waitLog("node_claim_released", node, 8*time.Minute)
	if !strings.Contains(b.logLine("node_claim_released", node), `"vmReleased":true`) {
		t.Fatalf("the door released %s without a vm.delete: %s", node, b.logLine("node_claim_released", node))
	}
	b.reading("claim deleted and the door released the node with vm.delete (%s)", b.logLine("vm_delete_result", node))
	goneBy := time.Now().Add(m3GuestGoneWithin)
	for guestRunning(guest) {
		if time.Now().After(goneBy) {
			t.Fatalf("guest %s still running %s after its release", guest, m3GuestGoneWithin)
		}
		time.Sleep(2 * time.Second)
	}
	b.reading("guest %s gone", guest)
	b.waitTenant("zero nodes", 3*time.Minute, func() bool {
		return b.tenant("get", "nodes", "-o", "jsonpath={.items[*].metadata.name}").out() == ""
	})
	b.waitTenant("cluster phase Ready", 3*time.Minute, func() bool {
		return b.kcp(b.home, "get", "cluster", m3Cluster, "-o", "jsonpath={.status.phase}").out() == "Ready"
	})
	b.reading("back to zero nodes with the control plane Ready")
	b.assertNoSsh(console)
}

type cmdRun struct {
	t    *testing.T
	args []string
	in   string
}

func (c cmdRun) stdin(s string) cmdRun { c.in = s; return c }

func (c cmdRun) exec() (string, error) {
	cmd := exec.Command(c.args[0], c.args[1:]...)
	if c.in != "" {
		cmd.Stdin = strings.NewReader(c.in)
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (c cmdRun) out() string {
	out, err := c.exec()
	if err != nil {
		if c.t != nil {
			c.t.Logf("query %s failed: %v\n%s", strings.Join(c.args, " "), err, out)
		}
		return ""
	}
	return out
}

func (c cmdRun) must() string {
	c.t.Helper()
	out, err := c.exec()
	if err != nil {
		c.t.Fatalf("%s: %v\n%s", strings.Join(c.args, " "), err, out)
	}
	return out
}

func run(t *testing.T, args ...string) string {
	t.Helper()
	return cmdRun{t: t, args: args}.must()
}

func (b *m3Bed) kcp(workspace string, args ...string) cmdRun {
	base := []string{"docker", "exec", "-i", b.name, "kubectl", "--kubeconfig=" + m3KcpKubeconfig, "--server=" + m3KcpServer + workspace}
	return cmdRun{t: b.t, args: append(base, args...)}
}

func (b *m3Bed) tenant(args ...string) cmdRun {
	base := []string{"docker", "exec", "-i", b.name, "kubectl", "--kubeconfig=" + b.tenantKC}
	return cmdRun{t: b.t, args: append(base, args...)}
}

func startHeldTestBed(t *testing.T, root string, env []string) map[string]string {
	t.Helper()
	hold := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "accept.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join(root, "deploy/examples/atproto/market/accept.sh"))
	cmd.Env = append(env, "ACCEPT_HOLD_DIR="+hold)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(hold, "release"), nil, 0o644)
		select {
		case <-exited:
		case <-time.After(5 * time.Minute):
			_ = cmd.Process.Signal(syscall.SIGTERM)
			<-exited
		}
		_ = logFile.Close()
	})
	deadline := time.Now().Add(30 * time.Minute)
	for {
		if data, err := os.ReadFile(filepath.Join(hold, "facts.env")); err == nil {
			facts := map[string]string{}
			for _, line := range strings.Split(string(data), "\n") {
				if k, v, ok := strings.Cut(line, "="); ok {
					facts[k] = v
				}
			}
			return facts
		}
		select {
		case err := <-exited:
			tail, _ := exec.Command("tail", "-n", "60", logPath).Output()
			t.Fatalf("accept.sh exited before holding the test bed up (%v):\n%s", err, tail)
		case <-time.After(3 * time.Second):
		}
		if time.Now().After(deadline) {
			t.Fatalf("the test bed was not up within 30 minutes; see %s", logPath)
		}
	}
}

func bidderIssuers(t *testing.T, stdout string) []string {
	t.Helper()
	f, err := os.Open(stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	var issuers []string
	for sc.Scan() {
		var line struct {
			Message    string `json:"message"`
			ServiceURL string `json:"serviceUrl"`
		}
		if json.Unmarshal(sc.Bytes(), &line) == nil && line.Message == "firecracker oidc issuer mounted" &&
			!slices.Contains(issuers, line.ServiceURL) {
			issuers = append(issuers, line.ServiceURL)
		}
	}
	if len(issuers) == 0 {
		t.Fatalf("the bidder log %s names no firecracker oidc issuer", stdout)
	}
	return issuers
}

func (b *m3Bed) ensureIssuerHost(issuer string) {
	if slices.Contains(b.issuers, issuer) {
		return
	}
	b.issuers = append(b.issuers, issuer)
	host := strings.TrimPrefix(issuer, "https://")
	run(b.t, "docker", "exec", "-u", "0", b.name, "sh", "-c", "echo '127.0.0.1 "+host+"' >> /etc/hosts")
	b.reading("the bidder's issuer moved to %s during the run; named it in the control plane's /etc/hosts", host)
}

func imageHasCloudInit(t *testing.T, rootfs string) bool {
	t.Helper()
	out, _ := exec.Command("debugfs", "-R", "stat /usr/bin/cloud-init", rootfs).CombinedOutput()
	return bytes.Contains(out, []byte("Inode:"))
}

func marketImageEnv(t *testing.T, env []string, workRoot string) []string {
	t.Helper()
	if _, err := exec.LookPath("debugfs"); err != nil {
		livegate.Require(t, "debugfs is not on PATH; this test asks the image store which of its images "+
			"carry cloud-init, because a market guest is born from the door's cloud-init user_data")
	}
	hostConfig := os.Getenv("SOCIALWEB_FIRECRACKER_CONFIG")
	raw, err := os.ReadFile(hostConfig)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("%s: %v", hostConfig, err)
	}
	hostStore, _ := config["out_dir"].(string)
	if hostStore == "" {
		t.Fatalf("%s names no out_dir, so the store the bidder reuses images from is unknown", hostConfig)
	}
	entries, err := os.ReadDir(hostStore)
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(filepath.Dir(workRoot), filepath.Base(workRoot)+"-images")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(store) })
	var kept []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(hostStore, entry.Name())
		if _, err := os.Stat(filepath.Join(dir, "rootfs.ext4")); err != nil {
			continue
		}
		if !imageHasCloudInit(t, filepath.Join(dir, "rootfs.ext4")) {
			continue
		}
		if err := os.Symlink(dir, filepath.Join(store, entry.Name())); err != nil {
			t.Fatal(err)
		}
		kept = append(kept, entry.Name())
	}
	if len(kept) == 0 {
		t.Fatalf("none of the images in %s carries cloud-init, and a market guest is born from the "+
			"door's cloud-init user_data: the bidder reuses a stale image when it has none for its "+
			"configuration (plan 0005 M3), so every market node would boot with no units and never "+
			"join. Rebuild the store with its preinstall manifest "+
			"(socialweb-nodeimage -ensure -config <config> -preinstall <manifest>, as root: the "+
			"preinstall chroots)", hostStore)
	}
	config["out_dir"] = store
	config["stage_dir"] = store + "-stage"
	if err := os.MkdirAll(store+"-stage", 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(store + "-stage") })
	adjusted, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "nodeimage.json")
	if err := os.WriteFile(configPath, adjusted, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("image store for this run: %s, holding %v from %s (the images that carry cloud-init)", store, kept, hostStore)
	out := make([]string, 0, len(env))
	replaced := false
	for _, entry := range env {
		if strings.HasPrefix(entry, "FC_CONFIG=") {
			out = append(out, "FC_CONFIG="+configPath)
			replaced = true
			continue
		}
		out = append(out, entry)
	}
	if !replaced {
		out = append(out, "FC_CONFIG="+configPath)
	}
	return out
}

func forwardLoopback(t *testing.T, gateway string, port int) int {
	t.Helper()
	l, err := net.Listen("tcp", gateway+":0")
	if err != nil {
		t.Fatalf("forwarding %s to the loopback-only test bed service on %d: %v", gateway, port, err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() { _ = l.Close(); wg.Wait() })
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				up, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
				if err != nil {
					return
				}
				defer up.Close()
				go func() { _, _ = io.Copy(up, c) }()
				_, _ = io.Copy(c, up)
			}()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func (b *m3Bed) createOwner() string {
	t := b.t
	ca, err := os.ReadFile(b.facts["CA_PEM"])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	password := randomHex(t, 16)
	body, _ := json.Marshal(map[string]string{"handle": "m3owner" + randomHex(t, 3), "password": password})
	resp, err := client.Post(b.ownerPDS+"/xrpc/com.atproto.server.createAccount", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("creating the owner account: %v", err)
	}
	defer resp.Body.Close()
	var out struct{ Did string }
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &out) != nil || out.Did == "" {
		t.Fatalf("createAccount answered %d %s", resp.StatusCode, data)
	}
	sessions, _ := json.Marshal(map[string]any{out.Did: map[string]string{"service": b.ownerPDS, "identifier": out.Did, "password": password}})
	b.files = t.TempDir()
	if err := os.Chmod(b.files, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.files, "owner.json"), string(sessions))
	return out.Did
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (b *m3Bed) writeFiles() {
	t := b.t
	market, err := os.ReadFile(b.facts["CA_PEM"])
	if err != nil {
		t.Fatal(err)
	}
	guestCA, err := os.ReadFile(filepath.Join(b.facts["GUEST_TLS_DIR"], "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.files, "ca-bundle.pem"), strings.TrimSpace(string(market))+"\n"+strings.TrimSpace(string(guestCA))+"\n")
	base := map[string]any{
		"ca_certs": map[string]any{"trusted": []string{strings.TrimSpace(string(guestCA))}},
		"bootcmd": [][]string{
			{"systemctl", "set-environment", "CURL_HOME=/root"},
			{"sh", "-c", "mkdir -p /root/secrets && ln -sfn digitalocean.com /root/secrets/firecracker"},
			{"sh", "-c", `mkdir -p /etc/systemd/journald.conf.d && printf '[Journal]\nForwardToConsole=yes\nMaxLevelConsole=info\nTTYPath=/dev/ttyS0\n' > /etc/systemd/journald.conf.d/console.conf && systemctl restart systemd-journald`},
		},
	}
	data, _ := json.Marshal(base)
	writeFile(t, filepath.Join(b.files, "node-base.yaml"), "#cloud-config\n"+string(data)+"\n")
}

func (b *m3Bed) startControlPlane() {
	t := b.t
	_ = exec.Command("docker", "rm", "-f", b.name).Run()
	args := []string{"docker", "run", "-d", "--name", b.name}
	for _, issuer := range b.issuers {
		args = append(args, "--add-host", strings.TrimPrefix(issuer, "https://")+":127.0.0.1")
	}
	run(t, append(args,
		"--add-host", m3PLCName+":127.0.0.2",
		"--add-host", m3OwnerPDSName+":127.0.0.4",
		"-v", b.files+":/m3:ro",
		"-v", b.volume+":"+m3ContainerState,
		"-e", "SOCIALWEB_CONTAINER_BOOTSTRAP=0",
		"-e", "SOCIALWEB_CLUSTERPROVIDER_JOIN=*",
		"-e", "SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE=market",
		"-e", "SOCIALWEB_CLUSTERPROVIDER_SCALE_MAX_NODES=1",
		"-e", "SOCIALWEB_GATEWAY_PLC_URL=https://"+m3PLCName,
		"-e", "PLC_DIRECTORY_URL=https://"+m3PLCName,
		"-e", "INGRESS_PROXY_HOST=relay.localhost:"+b.facts["DISPATCHER_PORT"],
		"-e", "NODE_POLICY=",
		"-e", "NODE_EXTRA_BIDDER_DIDS="+b.facts["BIDDER_DID"],
		"-e", "VM_READY_TIMEOUT_SEC=900",
		"-e", "DENO_CERT=/m3/ca-bundle.pem",
		"-e", "SSL_CERT_FILE=/m3/ca-bundle.pem",
		"-e", "TEST_OWNER_SESSIONS_FILE=/m3/owner.json",
		"-e", "TEST_NODE_USER_DATA_BASE_FILE=/m3/node-base.yaml",
		"-e", "TEST_NODE_GUEST_HOST_ALIASES="+b.facts["HOST_IP"]+" relay.localhost",
		b.image)...)
	gw := b.gateway
	alice, plc := strconv.Itoa(b.fwd["ALICE_PDS_PORT"]), strconv.Itoa(b.fwd["PLC_PORT"])
	dispatcher, guestTLS := b.facts["DISPATCHER_PORT"], b.facts["GUEST_TLS_PORT"]
	for _, s := range [][2]string{
		{"TCP6-LISTEN:" + b.facts["ALICE_PDS_PORT"] + ",ipv6only=0,fork,reuseaddr", gw + ":" + alice},
		{"TCP6-LISTEN:" + dispatcher + ",ipv6only=0,fork,reuseaddr", gw + ":" + dispatcher},
		{"TCP-LISTEN:443,bind=127.0.0.1,fork,reuseaddr", gw + ":" + guestTLS},
		{"TCP6-LISTEN:443,bind=[::1],fork,reuseaddr", gw + ":" + guestTLS},
		{"TCP-LISTEN:443,bind=127.0.0.2,fork,reuseaddr", gw + ":" + plc},
		{"TCP-LISTEN:443,bind=127.0.0.4,fork,reuseaddr", gw + ":" + alice},
	} {
		run(t, "docker", "exec", "-d", "-u", "0", b.name, "socat", s[0], "TCP:"+s[1])
	}
	end := time.Now().Add(5 * time.Minute)
	for !strings.Contains(b.logs(), `"message":"ready","prefix":"socialweb-computer-k8s"`) {
		if time.Now().After(end) {
			t.Fatalf("the control plane container never logged the door ready")
		}
		time.Sleep(3 * time.Second)
	}
}

func (b *m3Bed) createCluster() {
	acct := kcpclient.AccountHash(b.ownerDid)
	b.kcp("root", "apply", "-f", "-").stdin(fmt.Sprintf(
		"apiVersion: tenancy.kcp.io/v1alpha1\nkind: Workspace\nmetadata:\n  name: %s\nspec:\n  type:\n    name: account\n    path: root\n", acct)).must()
	b.waitTenant("account workspace Ready", 2*time.Minute, func() bool {
		return b.kcp("root", "get", "workspace", acct, "-o", "jsonpath={.status.phase}").out() == "Ready"
	})
	b.kcp("root:"+acct, "apply", "-f", "-").stdin(fmt.Sprintf(
		"apiVersion: socialweb.computer/v1alpha1\nkind: AtprotoIdentity\nmetadata:\n  name: %s\n  annotations:\n    socialweb.computer/sweep: exempt\nspec:\n  did: %s\n  scope: Account\n  revoked: false\n---\n"+
			"apiVersion: tenancy.kcp.io/v1alpha1\nkind: Workspace\nmetadata:\n  name: %s\nspec:\n  type:\n    name: cluster\n    path: root\n",
		kcpclient.IdentityRecordName(b.ownerDid), b.ownerDid, m3Cluster)).must()
	b.waitTenant("cluster workspace Ready", 2*time.Minute, func() bool {
		return b.kcp("root:"+acct, "get", "workspace", m3Cluster, "-o", "jsonpath={.status.phase}").out() == "Ready"
	})
	b.kcp(b.home, "apply", "-f", "-").stdin(fmt.Sprintf(
		"apiVersion: socialweb.computer/v1alpha1\nkind: Cluster\nmetadata:\n  name: %s\nspec:\n  account: %s\n  running: true\n", m3Cluster, b.ownerDid)).must()
}

func (b *m3Bed) waitLinksListening(within time.Duration) time.Time {
	b.waitTenant("both ClusterLinks Listening and the cluster Ready", within, func() bool {
		for _, svc := range []string{"apiserver", "openbao"} {
			if b.kcp(b.home, "get", "clusterlinks", b.key+"-"+svc, "-o", "jsonpath={.status.phase}").out() != "Listening" {
				return false
			}
		}
		return b.kcp(b.home, "get", "cluster", m3Cluster, "-o", "jsonpath={.status.phase}").out() == "Ready"
	})
	return time.Now()
}

func (b *m3Bed) waitTenant(what string, within time.Duration, ok func() bool) {
	b.t.Helper()
	end := time.Now().Add(within)
	for !ok() {
		if time.Now().After(end) {
			b.t.Fatalf("%s did not happen within %s\n%s", what, within, b.diagnose())
		}
		time.Sleep(3 * time.Second)
	}
}

const evidenceDir = "/home/johnandersen777/.cache/handoff/m3-local-e2e/evidence"

func copyEvidence(t *testing.T, name, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Logf("no evidence from %s: %v", path, err)
		return
	}
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		t.Log(err)
		return
	}
	target := filepath.Join(evidenceDir, name)
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Log(err)
		return
	}
	t.Logf("evidence kept at %s", target)
}

func (b *m3Bed) dumpContainerLog() {
	out, err := exec.Command("docker", "logs", b.name).CombinedOutput()
	if err != nil {
		b.t.Logf("docker logs %s: %v", b.name, err)
		return
	}
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		b.t.Log(err)
		return
	}
	target := filepath.Join(evidenceDir, b.name+"-control-plane.log")
	if err := os.WriteFile(target, out, 0o644); err != nil {
		b.t.Log(err)
		return
	}
	b.t.Logf("evidence kept at %s", target)
}

func (b *m3Bed) diagnose() string {
	b.t.Helper()
	var out strings.Builder
	for _, c := range [][]string{
		{"get", "nodes", "-o", "wide"},
		{"get", "nodes", "-o", "jsonpath={range .items[*]}{.metadata.name} {.status.conditions}{end}"},
		{"get", "pods", "-A", "-o", "wide"},
		{"get", "events", "-A", "--sort-by=.lastTimestamp"},
	} {
		text, err := b.tenant(c...).exec()
		fmt.Fprintf(&out, "\n$ kubectl %s (err %v)\n%s\n", strings.Join(c, " "), err, text)
	}
	claims, _ := b.kcp(b.home, "get", "nodeclaims", "-o", "yaml").exec()
	fmt.Fprintf(&out, "\n$ nodeclaims\n%s\n", claims)
	return out.String()
}

func (b *m3Bed) waitForClaim(within time.Duration) string {
	var name string
	b.waitTenant("a NodeClaim of "+b.key, within, func() bool {
		name = b.kcp(b.home, "get", "nodeclaims", "-o", "jsonpath={.items[0].metadata.name}").out()
		return strings.HasPrefix(name, b.key+"-node")
	})
	return name
}

func (b *m3Bed) claimField(node, path string) string {
	return b.kcp(b.home, "get", "nodeclaim", node, "-o", "jsonpath="+path).out()
}

func (b *m3Bed) waitClaimJSON(node, path string, within time.Duration) string {
	var v string
	b.waitTenant("NodeClaim "+node+" "+path, within, func() bool {
		v = b.claimField(node, path)
		return v != "" && v != "{}"
	})
	return v
}

func (b *m3Bed) logs() string {
	return cmdRun{t: b.t, args: []string{"docker", "logs", b.name}}.out()
}

func (b *m3Bed) logsSince(at time.Time) string {
	return cmdRun{t: b.t, args: []string{"docker", "logs", "--since", at.UTC().Format(time.RFC3339), b.name}}.out()
}

func (b *m3Bed) logLine(msg, node string) string {
	line := ""
	for _, l := range strings.Split(b.logs(), "\n") {
		if strings.Contains(l, msg) && strings.Contains(l, node) {
			line = l
		}
	}
	return line
}

func (b *m3Bed) waitLog(msg, node string, within time.Duration) {
	b.t.Helper()
	b.waitTenant(fmt.Sprintf("log %q for %s", msg, node), within, func() bool { return b.logLine(msg, node) != "" })
}

func (b *m3Bed) assertLogOrder(first, then string) {
	logs := b.logs()
	i, j := strings.Index(logs, first), strings.Index(logs, then)
	if i < 0 || j < 0 || i > j {
		b.t.Fatalf("the log does not show %q before %q (at %d and %d)", first, then, i, j)
	}
}

func (b *m3Bed) assertNoSsh(console string) {
	t := b.t
	for _, l := range strings.Split(b.logs(), "\n") {
		if !strings.Contains(l, `"prefix":"socialweb-computer-k8s"`) {
			continue
		}
		for _, bad := range []string{"runSession", "ssh_session", "pollReady", "sshReady", "ssh_connect"} {
			if strings.Contains(l, bad) {
				t.Fatalf("the door log names an ssh session (%s): %s", bad, l)
			}
		}
	}
	data, err := os.ReadFile(console)
	if err != nil {
		t.Fatal(err)
	}
	probes := 0
	for _, l := range strings.Split(string(data), "\n") {
		if !strings.Contains(l, "sshd[") {
			continue
		}
		if strings.Contains(l, "Accepted ") || strings.Contains(l, "session opened") || strings.Contains(l, "Failed password") ||
			strings.Contains(l, "Invalid user") {
			t.Fatalf("the guest's sshd saw an ssh session: %s", l)
		}
		if strings.Contains(l, "[preauth]") {
			probes++
		}
	}
	b.reading("no ssh session: the door log names none and the guest's sshd accepted none (%d pre-auth TCP readiness probes from the guest's gateway)", probes)
}

func waitNewGuest(t *testing.T, workRoot string, within time.Duration) string {
	t.Helper()
	end := time.Now().Add(within)
	for {
		entries, _ := os.ReadDir(workRoot)
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), "fc-") && guestRunning(e.Name()) {
				return e.Name()
			}
		}
		if time.Now().After(end) {
			t.Fatalf("no guest came up under %s within %s", workRoot, within)
		}
		time.Sleep(3 * time.Second)
	}
}

func guestRunning(name string) bool {
	return cmdRun{args: []string{"docker", "inspect", "-f", "{{.State.Running}}", name}}.out() == "true"
}

func waitFileContains(t *testing.T, path, want string, within time.Duration) {
	t.Helper()
	end := time.Now().Add(within)
	for {
		if data, err := os.ReadFile(path); err == nil && bytes.Contains(data, []byte(want)) {
			return
		}
		if time.Now().After(end) {
			tail, _ := exec.Command("tail", "-n", "40", path).Output()
			t.Fatalf("%s never showed %q within %s:\n%s", path, want, within, tail)
		}
		time.Sleep(5 * time.Second)
	}
}

func workloadManifest() string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[1]s
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %[1]s
  template:
    metadata:
      labels:
        app: %[1]s
    spec:
      containers:
        - name: sleep
          image: %[2]s
          imagePullPolicy: IfNotPresent
          command: ["sleep", "86400"]
`, m3Workload, m3WorkloadImage)
}
