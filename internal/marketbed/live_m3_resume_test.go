package marketbed

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

const (
	m3ColdStartDown   = 120 * time.Second
	m3NodeLossGrace   = 90 * time.Second
	m3ClockWatchAfter = m3NodeLossGrace + 60*time.Second
)

func runningGuest(workRoot string) string {
	entries, _ := os.ReadDir(workRoot)
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "fc-") && guestRunning(e.Name()) {
			return e.Name()
		}
	}
	return ""
}

func guestDirs(workRoot string) []string {
	var dirs []string
	entries, _ := os.ReadDir(workRoot)
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "fc-") {
			dirs = append(dirs, e.Name())
		}
	}
	return dirs
}

func (b *m3Bed) requireDurableClaimKeys() {
	if strings.Contains(b.logs(), "claim_keys_in_memory") {
		b.t.Fatalf("the door keeps its claim keys in memory, so a restart cannot resume an in-flight claim: %s",
			b.logLine("claim_keys_in_memory", ""))
	}
	b.reading("door keeps each claim's secrets-server key in the bed's OpenBao (secret/socialweb-door/claims/*, its own token)")
}

func (b *m3Bed) keepEvidence(workRoot, guest string) {
	t := b.t
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
}

func (b *m3Bed) nodeReady(node string) string {
	out, err := cmdRun{args: append([]string{"docker", "exec", "-i", b.name, "kubectl", "--kubeconfig=" + b.tenantKC},
		"get", "node", node, "-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status} {.status.conditions[?(@.type=="Ready")].lastTransitionTime}`)}.exec()
	if err != nil {
		return "unreachable"
	}
	return out
}

func (b *m3Bed) waitNodeReady(node string, within time.Duration) {
	b.waitTenant("node "+node+" Ready", within, func() bool {
		return strings.HasPrefix(b.nodeReady(node), "True")
	})
}

func (b *m3Bed) releaseWorkload(node, guest string) {
	t := b.t
	b.tenant("delete", "deployment", m3Workload, "-n", "default").must()
	b.waitLog("node_claim_released", node, 8*time.Minute)
	if !strings.Contains(b.logLine("node_claim_released", node), `"vmReleased":true`) {
		t.Fatalf("the door released %s without a vm.delete: %s", node, b.logLine("node_claim_released", node))
	}
	goneBy := time.Now().Add(m3GuestGoneWithin)
	for guestRunning(guest) {
		if time.Now().After(goneBy) {
			t.Fatalf("guest %s still running %s after its release", guest, m3GuestGoneWithin)
		}
		time.Sleep(2 * time.Second)
	}
	b.reading("workload deleted, claim %s released with vm.delete, guest %s gone", node, guest)
}

func TestADoorRestartMidProvisioningStillJoinsTheNode(t *testing.T) {
	livegate.Short(t, "this test brings up the market test bed and the real control-plane container, rents a "+
		"firecracker guest through the door, replaces the door after accept and before onNetwork, and watches the "+
		"node join anyway: a skip is NOT a pass")
	b, workRoot, _ := newM3Bed(t)
	b.requireDurableClaimKeys()

	b.tenant("apply", "-f", "-").stdin(workloadManifest()).must()
	b.reading("workload %s applied (intent, zero nodes)", m3Workload)
	node := b.waitForClaim(5 * time.Minute)
	uid := b.claimField(node, "{.metadata.uid}")

	var guest string
	b.waitTenant("claim "+node+" Provisioning with its guest booting and no onNetwork", 15*time.Minute, func() bool {
		if b.claimField(node, "{.status.phase}") != "Provisioning" || b.claimField(node, "{.status.fqdn}") != "" {
			return false
		}
		guest = runningGuest(workRoot)
		return guest != ""
	})
	b.keepEvidence(workRoot, guest)
	if fqdn := b.claimField(node, "{.status.fqdn}"); fqdn != "" {
		t.Fatalf("the guest reported onNetwork (%s) before the door could be replaced mid-provisioning", fqdn)
	}
	rfp := b.claimField(node, "{.status.contract.rfpUri}")
	accept := b.claimField(node, "{.status.contract.acceptUri}")
	secretsURL := b.claimField(node, "{.status.secrets.url}")
	if rfp == "" || accept == "" || secretsURL == "" {
		t.Fatalf("claim %s in Provisioning holds rfp %q accept %q secrets url %q; the restart would not be after accept", node, rfp, accept, secretsURL)
	}
	b.reading("claim %s Provisioning, guest %s booting, no onNetwork: rfp %s accept %s secrets url %s", node, guest, rfp, accept, secretsURL)

	restartAt := time.Now()
	b.startControlPlane()
	b.reading("control plane container replaced (door, provider and kcp restarted) %s after accept, before onNetwork",
		time.Since(restartAt).Round(time.Second))
	b.waitLog("node_claim_resumed", node, 5*time.Minute)
	b.reading("door resumed the claim: %s", b.logLine("node_claim_resumed", node))
	served := b.logLine("node_claim_secrets_served", node)
	if !strings.Contains(served, `"resumed":true`) || !strings.Contains(served, secretsURL) {
		b.waitLog("node_claim_secrets_served", node, 2*time.Minute)
		served = b.logLine("node_claim_secrets_served", node)
	}
	if !strings.Contains(served, secretsURL) {
		t.Fatalf("the restarted door serves %s, not the secrets url %s the guest's user_data names", served, secretsURL)
	}
	b.reading("restarted door re-serves the same secrets url from the stored key: %s", served)

	b.waitNodeReady(node, 20*time.Minute)
	b.reading("tenant Node %s Ready after the mid-provisioning restart", node)
	b.waitTenant("door phase Joining", 2*time.Minute, func() bool {
		return b.claimField(node, "{.status.phase}") == "Joining"
	})

	if got := b.claimField(node, "{.metadata.uid}"); got != uid {
		t.Fatalf("claim uid %q after the restart; want %q, the same claim", got, uid)
	}
	if claims := strings.Fields(b.kcp(b.home, "get", "nodeclaims", "-o", "jsonpath={.items[*].metadata.name}").out()); len(claims) != 1 {
		t.Fatalf("nodeclaims %v after the restart; want exactly %s", claims, node)
	}
	if got := b.claimField(node, "{.status.contract.rfpUri}"); got != rfp {
		t.Fatalf("rfp %q after the restart; want %q, no second RFP", got, rfp)
	}
	if got := b.claimField(node, "{.status.contract.acceptUri}"); got != accept {
		t.Fatalf("accept %q after the restart; want %q", got, accept)
	}
	receipt := b.claimField(node, "{.status.contract.receiptUri}")
	if receipt == "" {
		t.Fatalf("claim %s holds no receipt after it joined", node)
	}
	since := b.logsSince(restartAt)
	for _, bad := range []string{`"message":"rfp_created"`, `"message":"accept_created"`, `"message":"submitting_vm_delete"`} {
		if strings.Contains(since, bad) {
			t.Fatalf("after the restart the door logged %s", bad)
		}
	}
	if dirs := guestDirs(workRoot); len(dirs) != 1 || dirs[0] != guest {
		t.Fatalf("guests under %s: %v; want only %s", workRoot, dirs, guest)
	}
	b.reading("one claim (uid %s), one RFP, one accept, one receipt %s, one guest %s, no vm.delete; joined %s after the restart",
		uid, receipt, guest, time.Since(restartAt).Round(time.Second))

	b.releaseWorkload(node, guest)
}

type readySample struct {
	at    time.Time
	value string
}

func (b *m3Bed) watchNodeKept(node, guest, uid string, until time.Time) []readySample {
	t := b.t
	var samples []readySample
	last := ""
	for time.Now().Before(until) {
		v := b.nodeReady(node)
		if v != last {
			samples = append(samples, readySample{time.Now(), v})
			b.reading("node %s Ready condition: %s", node, v)
			last = v
		}
		if got := b.claimField(node, "{.metadata.uid}"); got != "" && got != uid {
			t.Fatalf("claim uid %q; want %q, the node kept", got, uid)
		}
		if del := b.claimField(node, "{.metadata.deletionTimestamp}"); del != "" {
			t.Fatalf("claim %s is being deleted at %s; our outage was counted against the node", node, del)
		}
		if !guestRunning(guest) {
			t.Fatalf("guest %s stopped; our outage was counted against the node", guest)
		}
		time.Sleep(3 * time.Second)
	}
	return samples
}

func notReadyIn(samples []readySample) string {
	for _, s := range samples {
		if !strings.HasPrefix(s.value, "True") && s.value != "unreachable" {
			return s.at.UTC().Format(time.RFC3339) + " " + s.value
		}
	}
	return "none"
}

func TestOurOwnOutageNeverCountsAgainstATenantNode(t *testing.T) {
	livegate.Short(t, "this test brings up the market test bed and the real control-plane container, joins a market "+
		"node, then takes our side of its path down (a cold start longer than the loss grace, and a link restart) and "+
		"reads the NotReady clock: a skip is NOT a pass")
	b, workRoot, _ := newM3Bed(t)
	b.tenant("apply", "-f", "-").stdin(workloadManifest()).must()
	node := b.waitForClaim(5 * time.Minute)
	guest := waitNewGuest(t, workRoot, 10*time.Minute)
	b.keepEvidence(workRoot, guest)
	b.waitNodeReady(node, 20*time.Minute)
	uid := b.claimField(node, "{.metadata.uid}")
	b.reading("node %s Ready on guest %s (claim uid %s)", node, guest, uid)

	run(t, "docker", "stop", "-t", "30", b.name)
	downAt := time.Now()
	b.reading("cold start: control plane container stopped, held down %s (longer than the %s loss grace)", m3ColdStartDown, m3NodeLossGrace)
	for time.Since(downAt) < m3ColdStartDown {
		if !guestRunning(guest) {
			t.Fatalf("guest %s stopped while our side was down", guest)
		}
		time.Sleep(5 * time.Second)
	}
	b.startControlPlane()
	pathUp := b.waitLinksListening(10 * time.Minute)
	b.reading("cold start: path up (apiserver Ready, both ClusterLinks Listening) %s after the stop", pathUp.Sub(downAt).Round(time.Second))
	samples := b.watchNodeKept(node, guest, uid, pathUp.Add(m3ClockWatchAfter))
	if strings.Contains(b.logsSince(downAt), "NotReady past the loss grace") {
		t.Fatalf("the provider released a node for NotReady time that ran while our side was down: %s",
			b.logLine("NotReady past the loss grace", node))
	}
	b.waitNodeReady(node, 3*time.Minute)
	b.reading("cold start reading: down %s, path up at %s, first NotReady seen %s, node kept and Ready %s after path up",
		pathUp.Sub(downAt).Round(time.Second), pathUp.UTC().Format(time.RFC3339), notReadyIn(samples), m3ClockWatchAfter)

	apiserverPort := b.kcp(b.home, "get", "nodeclaim", node, "-o", "jsonpath={.spec.apiserverPort}").out()
	if _, err := strconv.Atoi(apiserverPort); err != nil {
		t.Fatalf("claim %s names no apiserver port: %q", node, apiserverPort)
	}
	pids := strings.Fields(cmdRun{t: t, args: []string{"docker", "exec", b.name, "pgrep", "-f", "listen-tcp --host 127.0.0.1:" + apiserverPort + " "}}.out())
	if len(pids) == 0 {
		t.Fatalf("no apiserver link process (dumbpipe listen-tcp --host 127.0.0.1:%s) in the control plane", apiserverPort)
	}
	linkDownAt := time.Now()
	run(t, append([]string{"docker", "exec", b.name, "kill"}, pids...)...)
	b.reading("link restart: killed the apiserver ClusterLink process %v (127.0.0.1:%s)", pids, apiserverPort)
	link := b.key + "-apiserver"
	sawDown := ""
	end := time.Now().Add(5 * time.Minute)
	for {
		phase := b.kcp(b.home, "get", "clusterlinks", link, "-o", "jsonpath={.status.phase}").out()
		if phase != "Listening" && sawDown == "" {
			sawDown = phase + " at " + time.Now().UTC().Format(time.RFC3339)
		}
		if phase == "Listening" && (sawDown != "" || time.Since(linkDownAt) > time.Minute) {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("ClusterLink %s not Listening again within 5m of its process being killed", link)
		}
		time.Sleep(2 * time.Second)
	}
	linkUp := b.waitLinksListening(5 * time.Minute)
	b.reading("link restart: %s left Listening (%s), path up again %s after the kill", link, orNone(sawDown), linkUp.Sub(linkDownAt).Round(time.Second))
	samples = b.watchNodeKept(node, guest, uid, linkUp.Add(m3ClockWatchAfter))
	if strings.Contains(b.logsSince(linkDownAt), "NotReady past the loss grace") {
		t.Fatalf("the provider released a node for NotReady time that ran while its link was down: %s",
			b.logLine("NotReady past the loss grace", node))
	}
	b.waitNodeReady(node, 3*time.Minute)
	b.reading("link restart reading: path down %s, first NotReady seen %s, node kept and Ready %s after path up",
		linkUp.Sub(linkDownAt).Round(time.Second), notReadyIn(samples), m3ClockWatchAfter)

	b.releaseWorkload(node, guest)
}

func orNone(s string) string {
	if s == "" {
		return "not observed between polls"
	}
	return s
}
