//go:build linux

package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scaledriver"
)

const marketTick = 5 * time.Second

type marketBackoffLoop struct {
	t      *testing.T
	ctx    context.Context
	now    time.Time
	start  time.Time
	key    string
	claims *marketClaims
	client kubernetes.Interface
	p      *Provider
	logs   *bytes.Buffer
	writes []time.Duration
}

func newMarketBackoffLoop(t *testing.T, maxNodes int) *marketBackoffLoop {
	t.Helper()
	l := &marketBackoffLoop{
		t: t, ctx: context.Background(), now: time.Unix(1700000000, 0).UTC(),
		claims: &marketClaims{}, logs: &bytes.Buffer{}, key: clusterKey(t, "liveweb"),
	}
	l.start = l.now
	tenant, client := intentTenant(t)
	l.client = client
	log := slog.New(slog.NewTextHandler(l.logs, nil))
	opts := marketOptions(t, l.claims, tenant)
	opts.Log = log
	hosts, err := newNodeHosts(opts)
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	observer := clusterprovider.NodeObserver{Client: tenant}
	driver, err := scaledriver.New(hosts.forCluster, scaledriver.Options{
		Observe: func(ctx context.Context, cluster string) (scale.Observation, int, error) {
			observed, nodes, err := observer.Observe(ctx, cluster)
			if err != nil {
				return observed, nodes, err
			}
			return observed, nodes + hosts.inFlight(ctx, cluster), nil
		},
		Scale: scale.Options{MaxNodes: maxNodes, PodsPerNode: 1},
		Now:   func() time.Time { return l.now },
		Log:   log,
	})
	if err != nil {
		t.Fatalf("scaledriver.New: %v", err)
	}
	l.p = &Provider{nodeScale: driver, nodes: hosts, log: log}
	return l
}

func (l *marketBackoffLoop) pass() []string {
	l.t.Helper()
	before := l.claims.writes
	l.p.scale(l.ctx, l.key, "liveweb", true)
	var fresh []string
	if l.claims.writes > before {
		l.claims.mu.Lock()
		for key, claim := range l.claims.claims {
			if claim.Status.Phase == "" {
				fresh = append(fresh, key)
			}
		}
		l.claims.mu.Unlock()
		for range l.claims.writes - before {
			l.writes = append(l.writes, l.now.Sub(l.start))
		}
	}
	l.now = l.now.Add(marketTick)
	return fresh
}

func (l *marketBackoffLoop) answerNoBids(keys []string) {
	l.claims.mu.Lock()
	defer l.claims.mu.Unlock()
	for _, key := range keys {
		claim := l.claims.claims[key]
		claim.Status.Phase = v1alpha1.NodeClaimFailed
		claim.Status.Message = "no bids arrived for this node"
		claim.Status.Contract = v1alpha1.NodeClaimContract{
			VMUri:  "at://did:web:example.com/vm/" + claim.Name,
			RFPUri: "at://did:web:example.com/rfp/" + claim.Name,
		}
		l.claims.claims[key] = claim
	}
}

func (l *marketBackoffLoop) noBidsUntil(writes int) {
	l.t.Helper()
	for i := 0; len(l.writes) < writes; i++ {
		if i > 10_000 {
			l.t.Fatalf("only %d claims written in %d passes", len(l.writes), i)
		}
		l.answerNoBids(l.pass())
	}
}

func (l *marketBackoffLoop) setIntent(name string, on bool) {
	l.t.Helper()
	pods := l.client.CoreV1().Pods("default")
	if !on {
		if err := pods.Delete(l.ctx, name, metav1.DeleteOptions{}); err != nil {
			l.t.Fatalf("deleting the pod %s: %v", name, err)
		}
		return
	}
	if _, err := pods.Create(l.ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}, metav1.CreateOptions{}); err != nil {
		l.t.Fatalf("seeding the pod %s: %v", name, err)
	}
}

func TestNoBidsEndsBackTheMarketOffAtGrowingIntervalsCappedAtTenMinutes(t *testing.T) {
	l := newMarketBackoffLoop(t, 1)
	l.noBidsUntil(8)
	want := []time.Duration{
		marketTick + 30*time.Second,
		marketTick + time.Minute,
		marketTick + 2*time.Minute,
		marketTick + 4*time.Minute,
		marketTick + 8*time.Minute,
		marketTick + 10*time.Minute,
		marketTick + 10*time.Minute,
	}
	got := make([]time.Duration, 0, len(l.writes)-1)
	for i := 1; i < len(l.writes); i++ {
		got = append(got, l.writes[i]-l.writes[i-1])
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("the gaps between the claims written for one missing node were %v, want %v. "+
				"MEASURED on prod 2026-10-07 15:43-16:48 UTC: the door's fulfiller got no_bids after "+
				"its 30 s bid window, the claim ended Failed, the confirm pass gave the node up and "+
				"the next scale pass claimed again -- a new public market.rfp about every 50 s, 49 "+
				"RFPs and 86 no_bids in one hour", got, want)
		}
	}
	logs := l.logs.String()
	var line string
	for _, candidate := range strings.Split(logs, "\n") {
		if strings.Contains(candidate, "node scale") && strings.Contains(candidate, "backing off") {
			line = candidate
			break
		}
	}
	if line == "" {
		t.Fatalf("no node-scale decision line says the cluster is backing off:\n%s", logs)
	}
	for _, part := range []string{"backoffUntil=", "no bids arrived for this node", "until "} {
		if !strings.Contains(line, part) {
			t.Errorf("the decision line %q does not carry %q: it has to say the cluster is backing "+
				"off, until when, and why", line, part)
		}
	}
}

func TestAMarketNodeThatJoinsResetsTheBackoff(t *testing.T) {
	l := newMarketBackoffLoop(t, 2)
	l.noBidsUntil(4)
	var joining []string
	for len(joining) == 0 {
		joining = l.pass()
	}
	for _, key := range joining {
		claim := l.claims.claims[key]
		l.claims.setPhase(testHomePrefix+"liveweb", claim.Name, v1alpha1.NodeClaimRunning)
		if _, err := l.client.CoreV1().Nodes().Create(l.ctx, &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: claim.Name},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding the Node the cluster registers once the agent has joined: %v", err)
		}
	}
	l.pass()
	if st := l.p.nodeScale.State(l.key); st.Backoff != (scale.Backoff{}) {
		t.Fatalf("a market node joined and the back-off is still %+v", st.Backoff)
	}
	l.setIntent("work-b", true)
	from := len(l.writes)
	l.noBidsUntil(from + 2)
	if gap := l.writes[from+1] - l.writes[from]; gap != marketTick+30*time.Second {
		t.Fatalf("the first no_bids end after a node joined waited %s, want the base wait %s: "+
			"without the reset it is the fifth end in a row and waits eight minutes",
			gap, marketTick+30*time.Second)
	}
}

func TestIntentGoneResetsTheMarketBackoff(t *testing.T) {
	l := newMarketBackoffLoop(t, 1)
	l.noBidsUntil(4)
	l.setIntent("work", false)
	l.pass()
	if st := l.p.nodeScale.State(l.key); st.Backoff != (scale.Backoff{}) {
		t.Fatalf("a pass with no intent left the back-off at %+v", st.Backoff)
	}
	l.setIntent("work", true)
	from := len(l.writes)
	l.answerNoBids(l.pass())
	if len(l.writes) != from+1 {
		t.Fatal("intent came back after the back-off was reset and the pass claimed no node")
	}
	l.noBidsUntil(from + 2)
	if gap := l.writes[from+1] - l.writes[from]; gap != marketTick+30*time.Second {
		t.Fatalf("the first no_bids end after the intent came back waited %s, want the base wait %s",
			gap, marketTick+30*time.Second)
	}
}

func TestAFirstNodeClaimedAgainAfterItWasGivenUpIsHeld(t *testing.T) {
	claims := &marketClaims{}
	tenant, _ := intentTenant(t)
	hosts, err := newNodeHosts(marketOptions(t, claims, tenant))
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	ctx := context.Background()
	key := clusterKey(t, "liveweb")
	nodes, err := hosts.forCluster(key)
	if err != nil {
		t.Fatalf("forCluster: %v", err)
	}
	if err := nodes.Provision(ctx); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	claims.setPhase(testHomePrefix+"liveweb", key+"-node1", v1alpha1.NodeClaimFailed)
	if _, err := hosts.confirm(ctx, key); err == nil {
		t.Fatal("confirming a node whose claim failed reported no failure")
	}
	if err := nodes.Provision(ctx); err != nil {
		t.Fatalf("second Provision: %v", err)
	}
	if _, ok := claims.claims[testHomePrefix+"liveweb/"+key+"-node1"]; !ok {
		t.Fatalf("the second provision did not claim the freed first name; claims: %v", keysOf(claims.claims))
	}
	if got := hosts.inFlight(ctx, key); got != 1 {
		t.Fatalf("the first node claimed again after the first one was given up counted %d nodes in "+
			"flight, want 1. A claim this process wrote and does not hold is one no confirm pass "+
			"reads: its end is never seen, so it is never released and never backs the market off, "+
			"and every pass claims it again because nothing counts it", got)
	}
	claims.setPhase(testHomePrefix+"liveweb", key+"-node1", v1alpha1.NodeClaimFailed)
	if _, err := hosts.confirm(ctx, key); err == nil {
		t.Fatal("the second claim of the first node ended Failed and the confirm pass did not see it")
	}
	if got := claims.held(); got != 0 {
		t.Fatalf("the second claim of the first node ended Failed and %d claims are left, want it "+
			"released like the first", got)
	}
}

func TestAFailureAfterABidWasAcceptedDoesNotBackOff(t *testing.T) {
	l := newMarketBackoffLoop(t, 1)
	fresh := l.pass()
	if len(fresh) != 1 {
		t.Fatalf("the first pass wrote %d claims, want one", len(fresh))
	}
	l.claims.mu.Lock()
	claim := l.claims.claims[fresh[0]]
	claim.Status.Phase = v1alpha1.NodeClaimFailed
	claim.Status.Message = "the node's agent exited"
	claim.Status.Contract = v1alpha1.NodeClaimContract{RFPUri: "at://o/rfp/1", AcceptUri: "at://o/accept/1"}
	l.claims.claims[fresh[0]] = claim
	l.claims.mu.Unlock()
	l.pass()
	if got := len(l.writes); got != 2 {
		t.Fatalf("a node that failed after a bid was accepted left %d claims written by the next "+
			"pass, want two: the market did answer, so that end keeps today's behaviour", got)
	}
	if st := l.p.nodeScale.State(l.key); st.Backoff != (scale.Backoff{}) {
		t.Fatalf("a failure after an accept started a back-off %+v", st.Backoff)
	}
}
