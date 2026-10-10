package clusterlink

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

const fixtureSecret = "f8555129991792f402f21f6a01b0c027744b0b02c4b5d1f0e1bf979f722991d8"

const fixtureEndpointID = "cb6686656591d6e18858e0262a30c92700543f3230d3d483a4976cbfcbfc693c"

const fixtureOutput = `Forwarding incoming requests to '127.0.0.1:9'.
To connect, use e.g.:
dumbpipe connect-tcp endpointadfwnbtfmwi5nymildqcmkrqzetqavb7giynhveduslwzp6l7rutybiaenuhi5dqom5c6l3von3tcljrfzzgk3dbpexg4mbonfzg62bonruw42zof4aqayltuyk3xcqdaeagiwullc5yuaybacwbcaabxofagaiaycuaaff3ribq
`

const fixtureTicket = "endpointadfwnbtfmwi5nymildqcmkrqzetqavb7giynhveduslwzp6l7rutybiaenuhi5dqom5c6l3von3tcljrfzzgk3dbpexg4mbonfzg62bonruw42zof4aqayltuyk3xcqdaeagiwullc5yuaybacwbcaabxofagaiaycuaaff3ribq"

const fixtureOfflineOutput = `using secret key 30b0a6d2e8c91c167a71bfa88b1f05a1775a84144ce0f3a3f3a65650ef5a67c4
Warning: Failed to connect to the home relay
Forwarding incoming requests to '127.0.0.1:9'.
To connect, use e.g.:
dumbpipe connect-tcp endpointabqxdprknrzh52p6hssdgbbu2ost4ax2zqh3fljn5o7plucisqcukaa
`

type memStore struct {
	data    map[string]map[string]any
	creates int
	refuse  bool
}

func (m *memStore) Read(_ context.Context, path string) (map[string]any, bool, error) {
	d, ok := m.data[path]
	return d, ok, nil
}

func (m *memStore) Create(_ context.Context, path string, data map[string]any) (bool, error) {
	m.creates++
	if m.refuse {
		return false, nil
	}
	if _, ok := m.data[path]; ok {
		return false, nil
	}
	m.data[path] = data
	return true, nil
}

func TestTheTicketDumbpipePrintsIsFoundAndNamesTheEndpointItsSecretDerives(t *testing.T) {
	ticket, ok := TicketIn(fixtureOutput)
	if !ok || ticket != fixtureTicket {
		t.Fatalf("TicketIn = %q %v, want %q", ticket, ok, fixtureTicket)
	}
	id, err := ParseTicket(ticket)
	if err != nil {
		t.Fatal(err)
	}
	if id != fixtureEndpointID {
		t.Fatalf("ParseTicket = %s, want %s", id, fixtureEndpointID)
	}
	seed, _ := hex.DecodeString(fixtureSecret)
	k, err := KeyFromSeed(v1alpha1.ClusterLinkApiserver, seed)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Listening(k, ticket)
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase != v1alpha1.ClusterLinkListening || st.EndpointID != fixtureEndpointID || st.Ticket != ticket ||
		!strings.HasPrefix(st.Did, "did:key:z6Mk") {
		t.Fatalf("Listening = %+v", st)
	}
}

func TestATicketPrintedWithNoRelayIsStillFound(t *testing.T) {
	ticket, ok := TicketIn(fixtureOfflineOutput)
	if !ok {
		t.Fatal("no ticket found in the output of a listener that could not reach its relay")
	}
	seed, _ := hex.DecodeString("30b0a6d2e8c91c167a71bfa88b1f05a1775a84144ce0f3a3f3a65650ef5a67c4")
	k, err := KeyFromSeed(v1alpha1.ClusterLinkOpenBao, seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Listening(k, ticket); err != nil {
		t.Fatal(err)
	}
}

func TestATicketForAnotherKeyIsNotListening(t *testing.T) {
	k, err := KeyFromSeed(v1alpha1.ClusterLinkApiserver, bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Listening(k, fixtureTicket); err == nil {
		t.Fatal("a ticket naming another endpoint was reported Listening")
	}
}

func TestNoTicketBeforeDumbpipePrintsOne(t *testing.T) {
	if ticket, ok := TicketIn("Forwarding incoming requests to '127.0.0.1:9'.\n"); ok {
		t.Fatalf("TicketIn = %q before any connect line", ticket)
	}
	for _, bad := range []string{"", "endpoint", "nodeabc", "endpoint!!!!", "endpointaaaa"} {
		if _, err := ParseTicket(bad); !errors.Is(err, ErrBadTicket) {
			t.Fatalf("ParseTicket(%q) = %v", bad, err)
		}
	}
}

func TestTheKeyIsGeneratedOnceAndAProviderRestartReadsTheSameOneBack(t *testing.T) {
	store := &memStore{data: map[string]map[string]any{}}
	first, err := EnsureKey(context.Background(), store, v1alpha1.ClusterLinkApiserver, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	stored := store.data[string(v1alpha1.ClusterLinkApiserver)]
	if stored[FieldSecret] == "" || stored[FieldEndpointID] != first.EndpointID || stored[FieldDid] != first.Did {
		t.Fatalf("stored record = %v", stored)
	}

	restarted, err := EnsureKey(context.Background(), store, v1alpha1.ClusterLinkApiserver, bytes.NewReader(bytes.Repeat([]byte{9}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if restarted.EndpointID != first.EndpointID || restarted.Did != first.Did || restarted.Env() != first.Env() {
		t.Fatalf("after a restart the link is endpoint %s, before it was %s: every ticket a node "+
			"holds would name an endpoint nobody listens as", restarted.EndpointID, first.EndpointID)
	}
	if store.creates != 1 {
		t.Fatalf("the key was written %d times, want once", store.creates)
	}
	if !strings.HasPrefix(first.Env(), EnvSecret+"=") || len(first.Env()) != len(EnvSecret)+1+64 {
		t.Fatalf("Env = %q, want IROH_SECRET=<64 hex>", first.Env())
	}
}

func TestEachServiceHasItsOwnKey(t *testing.T) {
	store := &memStore{data: map[string]map[string]any{}}
	a, err := EnsureKey(context.Background(), store, v1alpha1.ClusterLinkApiserver, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := EnsureKey(context.Background(), store, v1alpha1.ClusterLinkOpenBao, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if a.EndpointID == b.EndpointID {
		t.Fatal("two services share one endpoint id")
	}
}

func TestALosingFirstWriteTakesTheKeyThatWon(t *testing.T) {
	seed, _ := hex.DecodeString(fixtureSecret)
	won, err := KeyFromSeed(v1alpha1.ClusterLinkOpenBao, seed)
	if err != nil {
		t.Fatal(err)
	}
	store := &racingStore{won: won.record()}
	got, err := EnsureKey(context.Background(), store, v1alpha1.ClusterLinkOpenBao, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if got.EndpointID != fixtureEndpointID {
		t.Fatalf("EnsureKey = %s, want the stored %s", got.EndpointID, fixtureEndpointID)
	}
}

type racingStore struct {
	won   map[string]any
	reads int
}

func (r *racingStore) Read(context.Context, string) (map[string]any, bool, error) {
	r.reads++
	if r.reads == 1 {
		return nil, false, nil
	}
	return r.won, true, nil
}

func (r *racingStore) Create(context.Context, string, map[string]any) (bool, error) {
	return false, nil
}

func TestAnUnreadableStoredKeyIsNeverRegeneratedOver(t *testing.T) {
	store := &memStore{data: map[string]map[string]any{"apiserver": {FieldSecret: "nothex"}}}
	_, err := EnsureKey(context.Background(), store, v1alpha1.ClusterLinkApiserver, rand.Reader)
	if !errors.Is(err, ErrStoredKeyUnreadable) {
		t.Fatalf("EnsureKey = %v", err)
	}
	if store.creates != 0 {
		t.Fatal("a fresh key was written over the unreadable one")
	}
	store.data["apiserver"] = map[string]any{FieldSecret: fixtureSecret, FieldEndpointID: strings.Repeat("0", 64)}
	if _, err := EnsureKey(context.Background(), store, v1alpha1.ClusterLinkApiserver, rand.Reader); !errors.Is(err, ErrStoredKeyUnreadable) {
		t.Fatalf("a record whose endpoint id disagrees with its secret was accepted: %v", err)
	}
}

func TestTheListenerForwardsToTheTargetOnLoopbackAndHoldsItsLeasedPort(t *testing.T) {
	got := Argv("/usr/local/bin/dumbpipe", 20010, 20012)
	want := []string{"/usr/local/bin/dumbpipe", "listen-tcp", "--host", "127.0.0.1:20010", "--ipv4-addr", "0.0.0.0:20012"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Argv = %v, want %v", got, want)
	}
}

func TestTheObjectIsNamedForItsClusterAndService(t *testing.T) {
	o := Object("abc123-dev", v1alpha1.ClusterLinkOpenBao, 20003, v1alpha1.ClusterLinkStatus{Phase: v1alpha1.ClusterLinkPending})
	if o.Name != "abc123-dev-openbao" || o.Spec.Cluster != "abc123-dev" || o.Spec.Service != v1alpha1.ClusterLinkOpenBao ||
		o.Spec.TargetPort != 20003 || o.Kind != "ClusterLink" || o.APIVersion != v1alpha1.GroupVersion.String() {
		t.Fatalf("Object = %+v", o)
	}
}
