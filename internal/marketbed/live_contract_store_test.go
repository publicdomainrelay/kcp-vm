package marketbed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

const (
	heldOnNetworkWithin = 20 * time.Minute
	bidderBackWithin    = 4 * time.Minute
	terminatedWithin    = 4 * time.Minute
	requesterDoneWithin = 3 * time.Minute
)

type heldBed struct {
	t        *testing.T
	root     string
	org      string
	workRoot string
	facts    map[string]string
}

func (h *heldBed) reading(format string, args ...any) {
	h.t.Log(time.Now().UTC().Format(time.RFC3339) + " " + fmt.Sprintf(format, args...))
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v
		}
	}
	return ""
}

func startHeldBed(t *testing.T) *heldBed {
	t.Helper()
	root, workRoot, env := testBedEnv(t)
	org := envValue(env, "ORG_ROOT")
	requireTreesUnchangedAtCleanup(t, org)
	facts := startHeldTestBed(t, root, env)
	if facts["CHECKS_FAILED"] != "0" {
		t.Fatalf("the test bed came up with failing checks: %v", facts)
	}
	h := &heldBed{t: t, root: root, org: org, workRoot: workRoot, facts: facts}
	h.reading("test bed %s held up: bidder %s, guests under %s", facts["BED_ID"], facts["BIDDER_DID"], workRoot)
	return h
}

func (h *heldBed) kc(ws string, args ...string) cmdRun {
	base := []string{"kubectl", "--kubeconfig=" + h.facts["KUBECONFIG_PATH"], "--server=" + h.facts["KCP_SERVER"] + "/clusters/root:" + ws}
	return cmdRun{t: h.t, args: append(base, args...)}
}

func (h *heldBed) podField(ws, name, field string) string {
	return h.kc(ws, "get", "denopod", name, "-o", "jsonpath={.status."+field+"}").out()
}

type bidderRun struct {
	runID    string
	restarts string
	pid      int
	stdout   string
}

func (h *heldBed) bidderRun(name string) (bidderRun, bool) {
	run := h.podField("bob", name, "runID")
	if run == "" || h.podField("bob", name, "ready") != "true" {
		return bidderRun{}, false
	}
	body, err := os.ReadFile(filepath.Join(h.facts["RUNS_DIR"], run, "state.json"))
	if err != nil {
		return bidderRun{}, false
	}
	var state struct{ PID int }
	if json.Unmarshal(body, &state) != nil || state.PID == 0 {
		return bidderRun{}, false
	}
	pid := state.PID
	if out, err := exec.Command("pgrep", "-P", strconv.Itoa(state.PID)).Output(); err == nil {
		if child, err := strconv.Atoi(strings.Fields(string(out) + " 0")[0]); err == nil && child > 0 {
			pid = child
		}
	}
	return bidderRun{
		runID:    run,
		restarts: h.podField("bob", name, "restarts"),
		pid:      pid,
		stdout:   filepath.Join(h.facts["RUNS_DIR"], run, "stdout.txt"),
	}, true
}

func (h *heldBed) restartBidder(name string) bidderRun {
	t := h.t
	t.Helper()
	before, ok := h.bidderRun(name)
	if !ok {
		t.Fatalf("bidder %s is not running and ready before its restart", name)
	}
	if err := syscall.Kill(before.pid, syscall.SIGTERM); err != nil {
		t.Fatalf("stopping bidder %s (pid %d): %v", name, before.pid, err)
	}
	h.reading("bidder %s stopped (SIGTERM to pid %d, run %s, restarts %q); its DenoPod's restartPolicy Always starts it again",
		name, before.pid, before.runID, before.restarts)
	end := time.Now().Add(bidderBackWithin)
	for {
		if after, ok := h.bidderRun(name); ok && after.runID != before.runID && fileHas(after.stdout, `"bidder_ready"`) {
			h.reading("bidder %s back: run %s, restarts %q, pid %d, same DID %s", name, after.runID, after.restarts, after.pid,
				h.facts["BIDDER_DID"])
			return after
		}
		if time.Now().After(end) {
			t.Fatalf("bidder %s did not come back ready within %s (run %q ready %q)", name, bidderBackWithin,
				h.podField("bob", name, "runID"), h.podField("bob", name, "ready"))
		}
		time.Sleep(2 * time.Second)
	}
}

func fileHas(path, want string) bool {
	data, err := os.ReadFile(path)
	return err == nil && bytes.Contains(data, []byte(want))
}

func lineWith(path, want string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.Contains(l, want) {
			return l
		}
	}
	return ""
}

type heldRequester struct {
	h       *heldBed
	control string
	result  string
	log     string
	done    chan error
}

type requesterVerdict struct {
	OK             bool   `json:"ok"`
	Reason         string `json:"reason"`
	ReceiptURI     string `json:"receiptUri"`
	WinnerDid      string `json:"winnerDid"`
	OnNetwork      string `json:"onNetwork"`
	VMDeleteStatus int    `json:"vmDeleteStatus"`
	VMDeleteOK     bool   `json:"vmDeleteOk"`
	LateDelete     bool   `json:"lateDelete"`
	Terminated     *struct {
		Reason     string `json:"reason"`
		ObservedAt string `json:"observedAt"`
		ReceivedAt string `json:"receivedAt"`
	} `json:"terminated"`
}

func (h *heldBed) startRequester() *heldRequester {
	t := h.t
	t.Helper()
	dir := t.TempDir()
	r := &heldRequester{h: h, control: filepath.Join(dir, "control"), result: filepath.Join(dir, "result.json"),
		log: filepath.Join(dir, "requester.log"), done: make(chan error, 1)}
	if err := os.MkdirAll(r.control, 0o755); err != nil {
		t.Fatal(err)
	}
	market := filepath.Join(h.org, "atproto-market")
	lock := filepath.Join(dir, "requester.lock")
	if body, err := os.ReadFile(filepath.Join(market, "deno.lock")); err == nil {
		writeFile(t, lock, string(body))
	}
	logFile, err := os.Create(r.log)
	if err != nil {
		t.Fatal(err)
	}
	f := h.facts
	kcpdns := filepath.Join(f["RUNS_DIR"], ".kcpdns")
	cmd := exec.Command("deno", "run", "-A", "--node-modules-dir=none", "--config", filepath.Join(market, "deno.json"), "--lock="+lock,
		"--preload", filepath.Join(kcpdns, "shim.ts"),
		filepath.Join(h.root, "deploy/examples/atproto/market/market-requester/mod.ts"),
		"--plc-directory-url", "https://127.0.0.1:"+f["PLC_PORT"],
		"--ingress-proxy-host", "relay.localhost:"+f["DISPATCHER_PORT"],
		"--bidder-dids", f["BIDDER_DIDS"],
		"--guest-host-aliases", f["HOST_IP"]+" relay.localhost",
		"--guest-ca-file", filepath.Join(f["GUEST_TLS_DIR"], "ca.crt"),
		"--guest-curl-home", "/root",
		"--guest-journal-to-console",
		"--vm-ready-timeout-sec", strconv.Itoa(int(heldOnNetworkWithin/time.Second)-60),
		"--result-file", r.result,
		"--control-dir", r.control)
	cmd.Dir = market
	cmd.Env = append(os.Environ(), "DENO_CERT="+f["CA_PEM"], "KCP_DNS_TABLE_FILE="+filepath.Join(kcpdns, "table.json"))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { r.done <- cmd.Wait(); _ = logFile.Close() }()
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(r.control, "release"), nil, 0o644)
		select {
		case <-r.done:
		case <-time.After(requesterDoneWithin):
			_ = cmd.Process.Kill()
		}
		if t.Failed() {
			tail, _ := exec.Command("tail", "-n", "60", r.log).Output()
			t.Logf("requester log tail:\n%s", tail)
		}
	})
	h.reading("requester started on the host: RFP to %s through the bed's dispatcher, holding the contract after onNetwork", f["BIDDER_DIDS"])
	return r
}

func (r *heldRequester) waitControl(name string, within time.Duration) map[string]any {
	t := r.h.t
	t.Helper()
	end := time.Now().Add(within)
	for {
		if data, err := os.ReadFile(filepath.Join(r.control, name)); err == nil {
			var v map[string]any
			if err := json.Unmarshal(data, &v); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			return v
		}
		select {
		case err := <-r.done:
			r.done <- err
			tail, _ := exec.Command("tail", "-n", "60", r.log).Output()
			t.Fatalf("the requester exited (%v) before writing %s:\n%s", err, name, tail)
		case <-time.After(2 * time.Second):
		}
		if time.Now().After(end) {
			t.Fatalf("the requester wrote no %s within %s", name, within)
		}
	}
}

func (r *heldRequester) release() requesterVerdict {
	t := r.h.t
	t.Helper()
	writeFile(t, filepath.Join(r.control, "release"), "")
	select {
	case <-r.done:
	case <-time.After(requesterDoneWithin):
		t.Fatalf("the requester did not finish within %s of its release", requesterDoneWithin)
	}
	r.done <- nil
	data, err := os.ReadFile(r.result)
	if err != nil {
		t.Fatalf("the requester left no verdict: %v", err)
	}
	var v requesterVerdict
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("verdict %s: %v", data, err)
	}
	return v
}

func (h *heldBed) waitGuestGone(guest string) time.Duration {
	t := h.t
	t.Helper()
	started := time.Now()
	end := started.Add(m3GuestGoneWithin)
	dir := filepath.Join(h.workRoot, guest)
	for {
		_, statErr := os.Stat(dir)
		if !guestRunning(guest) && os.IsNotExist(statErr) {
			return time.Since(started)
		}
		if time.Now().After(end) {
			t.Fatalf("guest %s still there %s on (running=%v, work dir err=%v)", guest, m3GuestGoneWithin, guestRunning(guest), statErr)
		}
		time.Sleep(2 * time.Second)
	}
}

func powerOffGuest(t *testing.T, workRoot, guest string) string {
	t.Helper()
	sock := filepath.Join(workRoot, guest, "fc.sock")
	body := `{"action_type":"SendCtrlAltDel"}`
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	req, _ := http.NewRequest(http.MethodPut, "http://localhost/actions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusNoContent {
			return "PUT /actions SendCtrlAltDel on " + sock + " from the host -> 204"
		}
		err = fmt.Errorf("status %d", resp.StatusCode)
	}
	out, cerr := exec.Command("docker", "exec", guest, "curl", "-sS", "-o", "/dev/null", "-w", "%{http_code}",
		"--unix-socket", sock, "-X", "PUT", "-H", "Content-Type: application/json", "-d", body, "http://localhost/actions").CombinedOutput()
	if cerr != nil || strings.TrimSpace(string(out)) != "204" {
		t.Fatalf("SendCtrlAltDel on %s: from the host %v; inside the guest's container %v %s", sock, err, cerr, out)
	}
	return "PUT /actions SendCtrlAltDel on " + sock + " inside the guest's container -> 204 (the host could not: " + err.Error() + ")"
}

func TestARestartedBidderReleasesTheGuestItRentedBeforeTheRestart(t *testing.T) {
	livegate.Short(t, "this test brings up a market test bed, rents a firecracker guest through the RFP flow, restarts "+
		"the bidder and releases the guest with vm.delete: a skip is NOT a pass")
	liveBudget(t, 45*time.Minute)
	h := startHeldBed(t)
	req := h.startRequester()
	on := req.waitControl("onnetwork.json", heldOnNetworkWithin)
	guest := waitNewGuest(t, h.workRoot, time.Minute)
	h.reading("guest %s onNetwork at %v after %vms: receipt %v from %v", guest, on["vmAddress"], on["onNetworkMs"], on["receiptUri"], on["winnerDid"])

	before, _ := h.bidderRun("bidder-1")
	after := h.restartBidder("bidder-1")
	if !guestRunning(guest) {
		t.Fatalf("guest %s stopped across the bidder restart", guest)
	}
	if line := lineWith(after.stdout, "microvm_adopted"); !strings.Contains(line, guest) {
		t.Fatalf("the restarted bidder did not adopt %s; its log %s has no microvm_adopted for it", guest, after.stdout)
	} else {
		h.reading("restarted bidder adopted the guest from its contract store: %s", line)
	}

	v := req.release()
	h.reading("requester vm.delete after the restart: status %d ok %v (%s)", v.VMDeleteStatus, v.VMDeleteOK, v.Reason)
	if v.VMDeleteStatus != http.StatusOK || !v.VMDeleteOK || !v.OK || v.LateDelete {
		t.Fatalf("vm.delete after the bidder restart: %+v; want 200 for a live contract", v)
	}
	t.Cleanup(func() {
		if t.Failed() {
			copyEvidence(t, "contract-store-bidder-before.txt", before.stdout)
			copyEvidence(t, "contract-store-bidder-after.txt", after.stdout)
		}
	})
	took := h.waitGuestGone(guest)
	h.reading("guest %s container and work dir %s gone %s after vm.delete", guest, filepath.Join(h.workRoot, guest), took.Round(time.Second))
	for _, want := range []string{"submitEvent: VM destroyed", "submitEvent: vm deleted"} {
		line := lineWith(after.stdout, want)
		if line == "" {
			t.Fatalf("the restarted bidder's log %s has no %q line for the vm.delete", after.stdout, want)
		}
		h.reading("restarted bidder: %s", line)
	}
}

func TestALateVmDeleteForAPoweredOffGuestAfterABidderRestartIsAlreadyTerminated(t *testing.T) {
	livegate.Short(t, "this test brings up a market test bed, rents a firecracker guest through the RFP flow, powers it "+
		"off, restarts the bidder and sends a late vm.delete: a skip is NOT a pass")
	liveBudget(t, 45*time.Minute)
	h := startHeldBed(t)
	req := h.startRequester()
	on := req.waitControl("onnetwork.json", heldOnNetworkWithin)
	guest := waitNewGuest(t, h.workRoot, time.Minute)
	h.reading("guest %s onNetwork at %v after %vms: receipt %v from %v", guest, on["vmAddress"], on["onNetworkMs"], on["receiptUri"], on["winnerDid"])

	how := powerOffGuest(t, h.workRoot, guest)
	h.reading("guest %s powered off: %s", guest, how)
	terminated := req.waitControl("terminated.json", terminatedWithin)
	h.reading("requester received vm.terminated: reason %v observedAt %v receivedAt %v", terminated["reason"],
		terminated["observedAt"], terminated["receivedAt"])
	if terminated["reason"] == "" || terminated["reason"] == nil {
		t.Fatalf("vm.terminated with no reason: %v", terminated)
	}
	took := h.waitGuestGone(guest)
	h.reading("guest %s container and work dir gone %s after the power-off", guest, took.Round(time.Second))

	before, _ := h.bidderRun("bidder-1")
	after := h.restartBidder("bidder-1")
	v := req.release()
	h.reading("requester late vm.delete after the restart: status %d ok %v late %v (%s)", v.VMDeleteStatus, v.VMDeleteOK, v.LateDelete, v.Reason)
	if v.VMDeleteStatus != http.StatusOK || !v.VMDeleteOK || !v.OK || !v.LateDelete {
		t.Fatalf("late vm.delete after the power-off and the bidder restart: %+v; want 200 already-terminated", v)
	}
	line := lineWith(after.stdout, "vm.delete for already-terminated receipt")
	if line == "" {
		t.Fatalf("the restarted bidder's log %s does not answer the late vm.delete as already-terminated (the run before was %s)",
			after.stdout, before.stdout)
	}
	h.reading("restarted bidder answered from its persisted tombstone: %s", line)
	if fileHas(after.stdout, "submitEvent: unknown receipt") {
		t.Fatalf("the restarted bidder answered unknown receipt: %s", lineWith(after.stdout, "unknown receipt"))
	}
}

func treeState(t *testing.T, org string) map[string]string {
	t.Helper()
	repos := append([]string{".", "socialweb-computer-kcp"}, orgSiblings...)
	state := map[string]string{}
	for _, repo := range repos {
		dir := filepath.Join(org, repo)
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			continue
		}
		status, err := exec.Command("git", "-C", dir, "status", "--porcelain=v1", "-uall", "--ignore-submodules=all").Output()
		if err != nil {
			t.Fatalf("git status in %s: %v", dir, err)
		}
		diff, err := exec.Command("git", "-C", dir, "diff", "HEAD", "--ignore-submodules=all").Output()
		if err != nil {
			t.Fatalf("git diff in %s: %v", dir, err)
		}
		sum := sha256.Sum256(diff)
		state[repo] = string(status) + "diff " + hex.EncodeToString(sum[:8])
	}
	return state
}

func requireTreesUnchanged(t *testing.T, org string, before map[string]string) {
	t.Helper()
	after := treeState(t, org)
	for repo, was := range before {
		if now := after[repo]; now != was {
			t.Errorf("the bed changed the working tree of %s:\nbefore:\n%s\nafter:\n%s", filepath.Join(org, repo), was, now)
		}
	}
}

func requireTreesUnchangedAtCleanup(t *testing.T, org string) {
	t.Helper()
	before := treeState(t, org)
	t.Cleanup(func() { requireTreesUnchanged(t, org, before) })
}

func TestABedRunLeavesEveryRepositoryWorkingTreeAsItFoundIt(t *testing.T) {
	livegate.Short(t, "this test runs a whole market test bed, renting a firecracker guest through the RFP flow, and "+
		"compares every org repository's working tree before and after: a skip is NOT a pass")
	id := newBedID(t)
	root, _, env := testBedEnvFor(t, id, 1)
	org := envValue(env, "ORG_ROOT")
	before := treeState(t, org)
	ctx := liveBudget(t, 35*time.Minute)
	r := runAccept(ctx, t, root, env, id, logDir(t))
	requireMarketPass(t, r)
	requireTreesUnchanged(t, org, before)
	t.Logf("%s bed %s left the working trees of %d repositories under %s as it found them",
		time.Now().UTC().Format(time.RFC3339), id, len(before), org)
}
