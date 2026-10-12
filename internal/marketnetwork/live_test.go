package marketnetwork

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

var firecrackerInputs = []struct{ env, flag string }{
	{"SOCIALWEB_FIRECRACKER_NODEIMAGE", "FC_NODEIMAGE"},
	{"SOCIALWEB_FIRECRACKER_CONFIG", "FC_CONFIG"},
	{"SOCIALWEB_FIRECRACKER_REPO_DIR", "FC_REPO_DIR"},
}

func newNetworkID(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(buf)
}

func slotDir() string {
	if dir := os.Getenv("MARKET_NETWORK_SLOT_DIR"); dir != "" {
		return dir
	}
	return DefaultSlotDir
}

func slotCount() int {
	if n, err := strconv.Atoi(os.Getenv("SWC_MARKET_NETWORK_SLOTS")); err == nil && n > 0 {
		return n
	}
	return DefaultSlots
}

func testNetworkEnvFor(t *testing.T, networkID string, guests int) (string, string, []string) {
	t.Helper()
	for _, bin := range []string{"kcp", "kine", "kubectl", "go", "python3", "curl", "openssl", "flock", "ss"} {
		if _, err := exec.LookPath(bin); err != nil {
			livegate.RequireLocal(t, "%s is not on PATH; the market network's kcp and its scripts need it", bin)
		}
	}
	for _, bin := range []string{"bao", "deno", "docker"} {
		if _, err := exec.LookPath(bin); err != nil {
			livegate.Require(t, "%s is not on PATH; the market network's OpenBao, services or firecracker runner need it", bin)
		}
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		livegate.Require(t, "no /dev/kvm on this host, so no firecracker guest can boot: %v", err)
	}

	root := repoRoot(t)
	var denoKcpEnv []string
	if os.Getenv("DENO_KCP_PROVIDER") == "" {
		dir, ok := FindDenoKcp(root)
		if !ok {
			livegate.Require(t, "no deno-kcp checkout to build the provider from above %s; set DENO_KCP_DIR or DENO_KCP_PROVIDER", root)
		}
		denoKcpEnv = append(denoKcpEnv, "DENO_KCP_DIR="+dir)
	}

	meminfo, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckMemory(string(meminfo), guests); err != nil {
		livegate.Require(t, "not enough memory for another market network: %v", err)
	}
	slot, err := TakeSlot(slotDir(), slotCount(), networkID)
	if err != nil {
		livegate.Require(t, "no market network slot free: %v", err)
	}
	t.Cleanup(func() { _ = slot.Release() })

	workRoot := FCWorkRootFor(networkID)
	state := os.Getenv("MARKET_NETWORK_STATE")
	if state == "" {
		state = t.TempDir()
	}
	env := append(append(os.Environ(), denoKcpEnv...),
		"MARKET_NETWORK_ID="+networkID,
		"MARKET_NETWORK_STATE="+state,
		"MARKET_NETWORK_SLOT_HELD=1",
		"MARKET_NETWORK_SLOT="+strconv.Itoa(slot.Index),
		"MARKET_NETWORK_GUESTS="+strconv.Itoa(guests),
		"FC_WORK_ROOT="+workRoot,
	)
	for _, in := range firecrackerInputs {
		v := os.Getenv(in.env)
		if v == "" {
			livegate.Require(t, "%s is unset; the market network's bidder runs --compute-provider-firecracker and has "+
				"no default for it on this host", in.env)
		}
		env = append(env, in.flag+"="+v)
	}
	if v := os.Getenv("SOCIALWEB_FIRECRACKER_PREINSTALL"); v != "" {
		env = append(env, "FC_PREINSTALL="+v)
	}
	if v := os.Getenv("SOCIALWEB_FIRECRACKER_RUNNER_IMAGE"); v != "" {
		if err := exec.Command("docker", "image", "inspect", v).Run(); err != nil {
			livegate.Require(t, "the firecracker runner image %s is not in the local docker store", v)
		}
		env = append(env, "FC_RUNNER_IMAGE="+v)
	}
	return root, workRoot, env
}

func testNetworkEnv(t *testing.T) (string, string, []string) {
	t.Helper()
	return testNetworkEnvFor(t, newNetworkID(t), 1)
}

func liveBudget(t *testing.T, need time.Duration) context.Context {
	t.Helper()
	deadline, hasDeadline := t.Deadline()
	budget := need + 20*time.Minute
	if hasDeadline {
		budget = time.Until(deadline) - 30*time.Second
	}
	if budget < need {
		t.Fatalf("this test needs up to %s and has %s; run go test with -timeout 60m (deploy/check.sh does)", need, budget)
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	t.Cleanup(cancel)
	return ctx
}

type networkRun struct {
	id      string
	log     string
	record  string
	err     error
	elapsed time.Duration
	results []string
	verdict string
}

func runAccept(ctx context.Context, t *testing.T, root string, env []string, id string, logDir string) *networkRun {
	r := &networkRun{id: id, log: filepath.Join(logDir, "accept-"+id+".log"), record: filepath.Join(logDir, "market-network-"+id+".json")}
	logFile, err := os.Create(r.log)
	if err != nil {
		r.err = err
		return r
	}
	defer logFile.Close()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(root, "deploy/market-network/accept.sh"))
	cmd.Env = append(env, "MARKET_NETWORK_RECORD_OUT="+r.record)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.WaitDelay = 2 * time.Minute
	started := time.Now()
	r.err = cmd.Run()
	r.elapsed = time.Since(started)
	r.results, r.verdict = readResults(t, r.log)
	return r
}

func logDir(t *testing.T) string {
	if dir := os.Getenv("MARKET_NETWORK_LOG_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	return t.TempDir()
}

func requireMarketPass(t *testing.T, r *networkRun) {
	t.Helper()
	for _, line := range r.results {
		t.Logf("market network %s: %s", r.id, line)
	}
	t.Logf("market network %s: accept.sh took %s, verdict %q, log %s", r.id, r.elapsed.Round(time.Second), r.verdict, r.log)
	if r.err != nil || r.verdict != "accept: pass" {
		tail, _ := exec.Command("tail", "-n", "80", r.log).Output()
		t.Fatalf("market network %s did not pass (%v):\n%s", r.id, r.err, tail)
	}
	for _, want := range []string{"market guest onNetwork", "market contract released", "market guest gone"} {
		found := false
		for _, line := range r.results {
			if strings.HasPrefix(strings.TrimSpace(line), want) && strings.Contains(line, " PASS ") {
				found = true
			}
		}
		if !found {
			t.Errorf("market network %s passed without a PASS line for %q; the firecracker market check did not run", r.id, want)
		}
	}
}

func TestTheMarketNetworkRentsAFirecrackerGuestThroughTheRFPFlow(t *testing.T) {
	livegate.Short(t, "this test brings up kcp, OpenBao, the deno-kcp provider and the whole market network, "+
		"and rents a firecracker guest through it: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	id := newNetworkID(t)
	root, _, env := testNetworkEnvFor(t, id, 1)
	ctx := liveBudget(t, 35*time.Minute)
	r := runAccept(ctx, t, root, env, id, logDir(t))
	requireMarketPass(t, r)
}

func TestTwoNetworksOnOneHostEachRentAGuestThroughTheRFPFlow(t *testing.T) {
	livegate.Short(t, "this test brings up several whole market networks on this host at once, each renting a "+
		"firecracker guest through its own RFP flow: a skip is NOT a pass")
	networks := 2
	if n, err := strconv.Atoi(os.Getenv("MARKET_NETWORK_COUNT")); err == nil && n >= 2 {
		networks = n
	}
	bidders := "2"
	if v := os.Getenv("MARKET_NETWORK_BIDDERS"); v != "" {
		bidders = v
	}
	ctx := liveBudget(t, 40*time.Minute)
	dir := logDir(t)
	type prepared struct {
		id   string
		root string
		env  []string
	}
	var ready []prepared
	for i := 0; i < networks; i++ {
		id := newNetworkID(t)
		root, _, env := testNetworkEnvFor(t, id, 1)
		ready = append(ready, prepared{id: id, root: root, env: append(env, "MARKET_NETWORK_BIDDERS="+bidders)})
	}
	runs := make([]*networkRun, len(ready))
	var wg sync.WaitGroup
	for i, p := range ready {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runs[i] = runAccept(ctx, t, p.root, p.env, p.id, dir)
		}()
	}
	wg.Wait()
	var records []Network
	for _, r := range runs {
		requireMarketPass(t, r)
		network, err := LoadNetwork(r.record)
		if err != nil {
			t.Fatalf("market network %s left no record: %v", r.id, err)
		}
		if err := network.Validate(); err != nil {
			t.Errorf("market network %s record: %v", r.id, err)
		}
		if len(network.Bidders) < 2 {
			t.Errorf("market network %s ran %d bidders, want at least 2", r.id, len(network.Bidders))
		}
		t.Logf("market network %s: slot %d ports %v bidders %v readings %v", network.ID, network.Slot, network.Ports(), network.Bidders, network.Readings)
		records = append(records, network)
	}
	for i := range records {
		for j := i + 1; j < len(records); j++ {
			if overlap := OverlappingPorts(records[i], records[j]); len(overlap) > 0 {
				t.Errorf("market networks %s and %s both recorded ports %v", records[i].ID, records[j].ID, overlap)
			}
		}
	}
}

func readResults(t *testing.T, path string) ([]string, string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Sprintf("no log: %v", err)
	}
	defer f.Close()
	var results []string
	verdict := ""
	in := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "results:"):
			in = true
		case strings.HasPrefix(line, "accept: "):
			verdict = line
			in = false
		case in && strings.HasPrefix(line, "  "):
			results = append(results, line)
		case in && strings.TrimSpace(line) == "":
			in = false
		}
	}
	return results, verdict
}
