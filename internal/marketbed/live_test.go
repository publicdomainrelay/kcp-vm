package marketbed

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

var testBedPorts = []int{2583, 2584, 2585, 2586, 2587, 2588, 2589}

var firecrackerInputs = []struct{ env, flag string }{
	{"SOCIALWEB_FIRECRACKER_NODEIMAGE", "FC_NODEIMAGE"},
	{"SOCIALWEB_FIRECRACKER_CONFIG", "FC_CONFIG"},
	{"SOCIALWEB_FIRECRACKER_REPO_DIR", "FC_REPO_DIR"},
	{"SOCIALWEB_FIRECRACKER_RUNNER_IMAGE", "FC_RUNNER_IMAGE"},
}

var orgSiblings = []string{"deno-kcp", "kcp-libs", "atproto-market", "atproto-relay", "hono-pds",
	"typescript-helpers", "did-key-ingress-proxy", "hono-compute-provider"}

func findOrgRoot(start string) (string, bool) {
	if dir := os.Getenv("ORG_ROOT"); dir != "" {
		return dir, isDir(filepath.Join(dir, "atproto-market"))
	}
	dir := start
	for {
		if isDir(filepath.Join(dir, "atproto-market")) && isDir(filepath.Join(dir, "deno-kcp")) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func TestTheMarketTestBedRentsAFirecrackerGuestThroughTheRFPFlow(t *testing.T) {
	livegate.Short(t, "this test brings up kcp, OpenBao, the deno-kcp provider and the whole market test bed, "+
		"and rents a firecracker guest through it: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")

	for _, bin := range []string{"kcp", "kine", "kubectl", "go", "python3", "curl", "openssl"} {
		if _, err := exec.LookPath(bin); err != nil {
			livegate.RequireLocal(t, "%s is not on PATH; the test bed's kcp and its scripts need it", bin)
		}
	}
	for _, bin := range []string{"bao", "deno", "docker"} {
		if _, err := exec.LookPath(bin); err != nil {
			livegate.Require(t, "%s is not on PATH; the test bed's OpenBao, services or firecracker runner need it", bin)
		}
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		livegate.Require(t, "no /dev/kvm on this host, so no firecracker guest can boot: %v", err)
	}

	root := repoRoot(t)
	org, ok := findOrgRoot(root)
	if !ok {
		livegate.Require(t, "no org root holding atproto-market and deno-kcp above %s; set ORG_ROOT", root)
	}
	for _, s := range orgSiblings {
		st, err := os.Lstat(filepath.Join(org, s))
		if err != nil || !st.IsDir() {
			livegate.Require(t, "%s/%s is not a real directory; the bidder serves its siblings to the guest from "+
				"the org root and a symlinked sibling reads as absent", org, s)
		}
	}

	env := append(os.Environ(),
		"ORG_ROOT="+org,
		"ACCEPT_ROOT="+filepath.Join(t.TempDir(), "accept"),
	)
	for _, in := range firecrackerInputs {
		v := os.Getenv(in.env)
		if v == "" {
			livegate.Require(t, "%s is unset; the test bed's bidder runs --compute-provider-firecracker and has "+
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
	}

	for _, port := range testBedPorts {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			livegate.Require(t, "port %d is held by another process; the market example binds 2583-2589 and "+
				"cannot share them with a second test bed: %v", port, err)
		}
		_ = l.Close()
	}

	deadline, hasDeadline := t.Deadline()
	budget := 55 * time.Minute
	if hasDeadline {
		budget = time.Until(deadline) - 30*time.Second
	}
	if budget < 35*time.Minute {
		t.Fatalf("this test needs up to 35 minutes and has %s; run go test with -timeout 60m (deploy/check.sh does)", budget)
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	logPath := filepath.Join(t.TempDir(), "accept.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	cmd := exec.CommandContext(ctx, "bash", filepath.Join(root, "deploy/examples/atproto/market/accept.sh"))
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.WaitDelay = 2 * time.Minute
	started := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(started)

	results, verdict := readResults(t, logPath)
	for _, line := range results {
		t.Log(line)
	}
	t.Logf("accept.sh took %s, verdict %q", elapsed.Round(time.Second), verdict)
	if runErr != nil || verdict != "accept: pass" {
		tail, _ := exec.Command("tail", "-n", "80", logPath).Output()
		t.Fatalf("the market test bed did not pass (%v):\n%s", runErr, tail)
	}
	for _, want := range []string{"market guest onNetwork", "market contract released", "market guest gone"} {
		found := false
		for _, line := range results {
			if strings.HasPrefix(strings.TrimSpace(line), want) && strings.Contains(line, " PASS ") {
				found = true
			}
		}
		if !found {
			t.Errorf("accept.sh passed without a PASS line for %q; the firecracker market check did not run", want)
		}
	}
}

func readResults(t *testing.T, path string) ([]string, string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
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
