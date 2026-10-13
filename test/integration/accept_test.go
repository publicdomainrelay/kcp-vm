//go:build integration

package integration

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

const (
	acceptRequestTimeout = 5 * time.Minute
	acceptReplay         = 90 * time.Second
	bidderBack           = 2 * time.Minute
)

func (n *Network) bidderDir(i int) string {
	return filepath.Join(n.StateDir, "apps", "dev", fmt.Sprintf("%s-bidder-%d", n.Instance, i+1))
}

func (n *Network) bidderProcess(i int) (int, string, error) {
	r := n.h.As(context.Background(), "sh", "-c", `for d in "$1"/denokcp/runs/pod-*; do
  [ -e "$d/done.json" ] && continue
  grep -q -s -F '"bidder ready"' "$d/stdout.txt" "$d/stderr.txt" || continue
  pid=$(sed -n 's/.*"pid":\([0-9]*\).*/\1/p' "$d/state.json")
  [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null && echo "$pid $d"
done; true`, "sh", n.bidderDir(i))
	if r.Err != nil {
		return 0, "", r.Err
	}
	lines := strings.Fields(strings.TrimSpace(r.Stdout))
	if len(lines) != 2 {
		return 0, "", fmt.Errorf("want one live bidder run under %s, found %q", n.bidderDir(i), r.Stdout)
	}
	pid, err := strconv.Atoi(lines[0])
	return pid, lines[1], err
}

func (n *Network) waitBidderBack(i, oldPID int, timeout time.Duration) (int, string, error) {
	deadline := time.Now().Add(timeout)
	for {
		pid, dir, err := n.bidderProcess(i)
		if err == nil && pid != oldPID {
			return pid, dir, nil
		}
		if err := n.health(); err != nil {
			return 0, "", fmt.Errorf("waiting for the bidder to come back: %w", err)
		}
		if time.Now().After(deadline) {
			return 0, "", fmt.Errorf("no new bidder process inside %s (last: %v)", timeout, err)
		}
		time.Sleep(time.Second)
	}
}

func (n *Network) signal(sig string, pid int) error {
	r := n.h.As(context.Background(), "kill", "-"+sig, "--", "-"+strconv.Itoa(pid))
	if r.Err != nil {
		return fmt.Errorf("kill -%s %d: %v %s", sig, pid, r.Err, r.Stderr)
	}
	return nil
}

func (n *Network) grepCount(pattern string, globs ...string) int {
	r := n.h.As(context.Background(), append([]string{"sh", "-c", `cat "$@" 2>/dev/null | grep -c -a -F -e "$0"; true`, pattern}, globs...)...)
	c, _ := strconv.Atoi(strings.TrimSpace(r.Stdout))
	return c
}

func runLogs(dir string) []string {
	return []string{filepath.Join(dir, "stdout.txt"), filepath.Join(dir, "stderr.txt")}
}

func wonBids(ctx context.Context, dyn dynamic.Interface) ([]v1alpha1.Bid, []v1alpha1.Bid, error) {
	bids, err := bidderBids(ctx, dyn)
	if err != nil {
		return nil, nil, err
	}
	var won []v1alpha1.Bid
	for _, b := range bids {
		if b.Status.Phase == v1alpha1.BidPhaseWon {
			won = append(won, b)
		}
	}
	return bids, won, nil
}

func rewindStreams(ctx context.Context, dyn dynamic.Interface) (int, error) {
	u, err := dyn.Resource(marketBidderGVR).Get(ctx, bidderRootName, metav1.GetOptions{})
	if err != nil {
		return 0, err
	}
	streams, _, _ := unstructured.NestedSlice(u.Object, "status", "market", "streams")
	for i, s := range streams {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		m["cursor"] = int64(1)
		streams[i] = m
	}
	if len(streams) == 0 {
		return 0, fmt.Errorf("the bidder's root object has no status.market.streams")
	}
	if err := unstructured.SetNestedSlice(u.Object, streams, "status", "market", "streams"); err != nil {
		return 0, err
	}
	_, err = dyn.Resource(marketBidderGVR).UpdateStatus(ctx, u, metav1.UpdateOptions{})
	return len(streams), err
}

func TestAcceptOfAnEndedContractStartsNoVM(t *testing.T) {
	n := StartNetwork(t, NetworkOptions{Bidders: 1})
	ctx := context.Background()
	dyn, err := n.bidderClient(0)
	if err != nil {
		t.Fatal(err)
	}
	name := "ra" + randHex(3)
	r := n.Run("run-request", acceptRequestTimeout, "run", "request", "--network", n.Instance, "--name", name, "--exec", "true",
		"--contract-timeout", "3m", "--network-timeout", "2m", "--state-dir", n.StateRoot)
	if r.Err != nil {
		t.Fatalf("run request: %v\n%s", r.Err, r.Tail(15))
	}
	var s requestSummary
	if err := lastJSON(r.Stdout, &s); err != nil {
		t.Fatalf("run request summary: %v", err)
	}
	if s.Phase != "Released" || s.Contract == nil || s.Contract.ReceiptUri == "" {
		t.Fatalf("the contract did not end Released with a receipt: phase %s: %s", s.Phase, s.Message)
	}
	if err := n.WaitNoGuests(time.Minute); err != nil {
		t.Fatal(err)
	}
	bids, won, err := wonBids(ctx, dyn)
	if err != nil {
		t.Fatal(err)
	}
	if len(won) != 1 || won[0].Status.Accept == nil || won[0].Status.Accept.Uri == "" {
		t.Fatalf("want one Won Bid that names its accept:%s", describeBids(bids))
	}
	accept := won[0].Status.Accept.Uri
	t.Logf("contract released: receipt %s, accept %s", s.Contract.ReceiptUri, accept)

	pid, _, err := n.bidderProcess(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.signal("STOP", pid); err != nil {
		t.Fatal(err)
	}
	rewound, err := rewindStreams(ctx, dyn)
	if err != nil {
		n.signal("KILL", pid)
		t.Fatalf("rewinding the bidder's stream cursors: %v", err)
	}
	if rm := n.h.As(ctx, "find", n.bidderDir(0), "-path", "*/contracts/tombstones/*.json", "-print", "-delete"); rm.Err != nil {
		n.signal("KILL", pid)
		t.Fatalf("removing the bidder's tombstones: %v", rm.Err)
	} else {
		t.Logf("removed the bidder's local contract records: %s", strings.Join(strings.Fields(rm.Stdout), " "))
	}
	if err := n.signal("KILL", pid); err != nil {
		t.Fatal(err)
	}
	t.Logf("bidder %d killed with %d stream cursor(s) rewound to 1: the restart replays the firehose and the accept of the ended contract", pid, rewound)

	newPID, dir, err := n.waitBidderBack(0, pid, bidderBack)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(acceptReplay)
	for {
		if vms, err := n.BidderVMs(ctx); err == nil && len(vms) > 0 {
			t.Fatalf("the replayed accept of an ended contract started a ComputeVM: %s", describeVM(vms[0]))
		}
		if left := n.guestsLeft(); len(left) > 0 {
			t.Fatalf("the replayed accept of an ended contract started a guest: %s", strings.Join(left, "; "))
		}
		if n.grepCount("bidder received accept for VM", runLogs(dir)...) > 0 {
			t.Fatalf("the restarted bidder %d took the replayed accept %s", newPID, accept)
		}
		if c := n.grepCount("accept_contract_ended", runLogs(dir)...); c > 0 && n.grepCount(accept, runLogs(dir)...) > 0 {
			t.Logf("the restarted bidder %d refused the replayed accept: receipt in its own repo", newPID)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the restarted bidder %d did not see the accept %s again inside %s", newPID, accept, acceptReplay)
		}
		time.Sleep(time.Second)
	}
	if vms, err := n.BidderVMs(ctx); err != nil || len(vms) > 0 {
		t.Fatalf("ComputeVMs after the replay: %d (%v)", len(vms), err)
	}
	after, wonAfter, err := wonBids(ctx, dyn)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(bids) || len(wonAfter) != 1 || wonAfter[0].Name != won[0].Name {
		t.Fatalf("the Bids changed across the replay:%s", describeBids(after))
	}
}
