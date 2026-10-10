package xrpcserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bluesky-social/indigo/atproto/atcrypto"
	"github.com/bluesky-social/indigo/atproto/auth"
	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/lexicon"
	"github.com/bluesky-social/indigo/atproto/syntax"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/atprotoauth"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/gateway"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/mint"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/revocation"
)

const (
	ourHost    = "gateway.example.com"
	ourService = "socialweb_computer"
	callerDID  = "did:plc:abcdefghijklmnopqrstuvwx"
	attacker   = "did:plc:zyxwvutsrqponmlkjihgfedc"
)

func audiences() []string {
	return []string{
		"did:web:" + ourHost,
		"did:web:" + ourHost + "#" + ourService,
	}
}

type fakeDirectory struct{ keys map[string]atcrypto.PublicKey }

func (f *fakeDirectory) LookupDID(_ context.Context, did syntax.DID) (*identity.Identity, error) {
	pk, ok := f.keys[did.String()]
	if !ok {
		return nil, identity.ErrDIDNotFound
	}
	return &identity.Identity{DID: did, Keys: map[string]identity.VerificationMethod{
		"atproto": {Type: "Multikey", PublicKeyMultibase: pk.Multibase()},
	}}, nil
}

func (f *fakeDirectory) LookupHandle(context.Context, syntax.Handle) (*identity.Identity, error) {
	return nil, identity.ErrHandleNotFound
}
func (f *fakeDirectory) Lookup(context.Context, syntax.AtIdentifier) (*identity.Identity, error) {
	return nil, identity.ErrDIDNotFound
}
func (f *fakeDirectory) Purge(context.Context, syntax.AtIdentifier) error { return nil }

func granted() *fakeDeny { return &fakeDeny{state: revocation.DenyGranted} }

type fakeDeny struct{ state revocation.DenyState }

func (f *fakeDeny) Lookup(context.Context, string) (revocation.DenyState, error) {
	return f.state, nil
}

type fakeStore struct {
	views    map[string]gateway.ClusterView
	callers  []gateway.Caller
	names    []string
	ttls     []time.Duration
	wakes    int
	failWith error
	failVerb string
}

func newStore() *fakeStore {
	return &fakeStore{views: map[string]gateway.ClusterView{}}
}

func (s *fakeStore) saw(c gateway.Caller, name string) {
	s.callers = append(s.callers, c)
	s.names = append(s.names, name)
}

func (s *fakeStore) Ensure(_ context.Context, c gateway.Caller, name string) error {
	s.saw(c, name)
	if s.failVerb == "Ensure" {
		return s.failWith
	}
	if _, ok := s.views[name]; !ok {
		s.views[name] = gateway.ClusterView{Name: name, Phase: "Initializing", Instance: "1"}
	}
	return nil
}

func (s *fakeStore) Kubeconfig(_ context.Context, c gateway.Caller, name string, ttl time.Duration) (string, error) {
	s.saw(c, name)
	s.ttls = append(s.ttls, ttl)
	return "server: https://kcp.invalid/clusters/" + name, nil
}

func (s *fakeStore) Get(_ context.Context, c gateway.Caller, name string) (gateway.ClusterView, bool, error) {
	s.saw(c, name)
	if s.failVerb == "Get" {
		return gateway.ClusterView{}, false, s.failWith
	}
	v, ok := s.views[name]
	return v, ok, nil
}

func (s *fakeStore) List(_ context.Context, c gateway.Caller) ([]gateway.ClusterView, error) {
	s.saw(c, "")
	if s.failVerb == "List" {
		return nil, s.failWith
	}
	out := make([]gateway.ClusterView, 0, len(s.views))
	for _, v := range s.views {
		out = append(out, v)
	}
	return out, nil
}

func (s *fakeStore) SetRunning(_ context.Context, c gateway.Caller, name string, running bool) (gateway.ClusterView, error) {
	s.saw(c, name)
	s.wakes++
	v, ok := s.views[name]
	if !ok {
		return gateway.ClusterView{}, gateway.ErrClusterNotFound
	}
	v.Running = running
	if !running {
		v.Phase = "Unavailable"
	}
	s.views[name] = v
	return v, nil
}

func (s *fakeStore) Delete(_ context.Context, c gateway.Caller, name string) (gateway.ClusterView, error) {
	s.saw(c, name)
	v, ok := s.views[name]
	if !ok {
		return gateway.ClusterView{}, gateway.ErrClusterNotFound
	}
	v.Phase = "Draining"
	delete(s.views, name)
	return v, nil
}

type fakeRevoker struct {
	writes   []string
	clusters []revocation.ClusterView
	fail     error
	order    *[]string
}

func (f *fakeRevoker) WriteDeny(_ context.Context, did string) error {
	f.writes = append(f.writes, did)
	if f.order != nil {
		*f.order = append(*f.order, "write")
	}
	if f.fail != nil {
		return f.fail
	}
	return nil
}

func (f *fakeRevoker) FanOut(_ context.Context, did string) ([]revocation.ClusterView, error) {
	if f.order != nil {
		*f.order = append(*f.order, "observe")
	}
	if f.fail != nil {
		return nil, f.fail
	}
	return f.clusters, nil
}

type fakeMintStore struct {
	binding mint.Binding
	issued  int
}

func (f *fakeMintStore) Resolve(_ context.Context, c mint.DeviceCredential) (mint.Binding, error) {
	if c.Token == "" {
		return mint.Binding{}, mint.ErrBindingUnknown
	}
	return f.binding, nil
}

func (f *fakeMintStore) Issue(_ context.Context, _ mint.Binding, ttl time.Duration) (string, time.Time, error) {
	f.issued++
	return "sa-token", time.Now().Add(ttl), nil
}

type rig struct {
	t     *testing.T
	srv   *Server
	h     http.Handler
	store *fakeStore
	mints *fakeMintStore
	rev   *fakeRevoker
	priv  atcrypto.PrivateKey
}

func newRig(t *testing.T) *rig {
	t.Helper()
	priv, err := atcrypto.GeneratePrivateKeyP256()
	if err != nil {
		t.Fatalf("GeneratePrivateKeyP256: %v", err)
	}
	pub, err := priv.PublicKey()
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}

	store := newStore()
	rev := &fakeRevoker{}
	ms := &fakeMintStore{binding: mint.Binding{
		DID: callerDID, Cluster: "c", SA: "sa", SAExists: true, Instance: "1",
	}}
	srv, err := New(Options{
		Gateway: &gateway.Server{
			Deny: granted(), Store: store, Revoker: rev,
			MaxDeviceKeyTTL: time.Hour,
		},
		Mint:      mint.New(ms, mint.Options{}),
		Audiences: audiences(),
		Directory: &fakeDirectory{keys: map[string]atcrypto.PublicKey{callerDID: pub}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &rig{t: t, srv: srv, h: srv.Handler(), store: store, mints: ms, rev: rev, priv: priv}
}

func (r *rig) token(aud string, ttl time.Duration, lxm *syntax.NSID) string {
	r.t.Helper()
	iss, err := syntax.ParseDID(callerDID)
	if err != nil {
		r.t.Fatalf("ParseDID: %v", err)
	}
	tok, err := auth.SignServiceAuth(iss, aud, ttl, lxm, r.priv)
	if err != nil {
		r.t.Fatalf("SignServiceAuth: %v", err)
	}
	return tok
}

func nsid(t *testing.T, s string) *syntax.NSID {
	t.Helper()
	n, err := syntax.ParseNSID(s)
	if err != nil {
		t.Fatalf("ParseNSID(%s): %v", s, err)
	}
	return &n
}

func (r *rig) do(method, path, token string) *httptest.ResponseRecorder {
	r.t.Helper()
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(`{"name":"c"}`)
	}
	req := httptest.NewRequest(method, path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	return w
}

func (r *rig) procedure(verb, body string) *httptest.ResponseRecorder {
	r.t.Helper()
	n := NSIDPrefix + verb
	req := httptest.NewRequest(http.MethodPost, XRPCPrefix+n, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+r.token(audiences()[0], atprotoauth.MaxServiceAuthLifetime, nsid(r.t, n)))
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
	}
	return out
}

func (r *rig) validateOutput(nsid string, w *httptest.ResponseRecorder) {
	r.t.Helper()
	sch, err := r.srv.catalog.Resolve(nsid)
	if err != nil {
		r.t.Fatalf("Resolve(%s): %v", nsid, err)
	}
	var out *lexicon.SchemaBody
	switch d := sch.Def.(type) {
	case lexicon.SchemaProcedure:
		out = d.Output
	case lexicon.SchemaQuery:
		out = d.Output
	default:
		r.t.Fatalf("%s is a %T", nsid, sch.Def)
	}
	obj, err := objectOf(nsid+" output", out.Schema)
	if err != nil {
		r.t.Fatalf("%v", err)
	}
	var got map[string]any
	dec := json.NewDecoder(bytes.NewReader(w.Body.Bytes()))
	dec.UseNumber()
	if err := dec.Decode(&got); err != nil {
		r.t.Fatalf("response is not JSON: %v", err)
	}
	if err := validate(nsid, r.srv.catalog, obj, numbersAsInts(got)); err != nil {
		r.t.Fatalf("the response does not satisfy the lexicon: %v\n%s", err, w.Body.String())
	}
}

func TestAVerifiedCallerReachesTheRightVerb(t *testing.T) {
	r := newRig(t)

	t.Run("getKubeconfig", func(t *testing.T) {
		w := r.procedure("getKubeconfig", `{"name":"c"}`)
		if w.Code != 200 {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		r.validateOutput(nsidGetKubeconfig, w)
		got := decode(t, w)
		if got["instance"] != "1" || got["kubeconfig"] == "" {
			t.Fatalf("body = %v", got)
		}
		if len(r.store.callers) == 0 || r.store.callers[len(r.store.callers)-1] != callerDID {
			t.Fatalf("the store acted for %v, want %q", r.store.callers, callerDID)
		}
	})

	t.Run("createCluster", func(t *testing.T) {
		r.store.views["c"] = gateway.ClusterView{Name: "c", Phase: "Ready", Instance: "1"}
		w := r.procedure("createCluster", `{"name":"c"}`)
		if w.Code != 200 {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		r.validateOutput(nsidCreateCluster, w)
		if !r.store.views["c"].Running {
			t.Fatalf("createCluster answered a phase but left the cluster not running: %+v",
				r.store.views["c"])
		}
	})

	t.Run("controlPlaneUp", func(t *testing.T) {
		r.store.views["c"] = gateway.ClusterView{Name: "c", Phase: "Ready", Instance: "1"}
		w := r.procedure("controlPlaneUp", `{"name":"c"}`)
		if w.Code != 200 {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		r.validateOutput(nsidControlPlaneUp, w)
	})

	t.Run("controlPlaneDown", func(t *testing.T) {
		w := r.procedure("controlPlaneDown", `{"name":"c"}`)
		if w.Code != 200 {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		r.validateOutput(nsidControlPlaneDown, w)
	})

	t.Run("deleteCluster", func(t *testing.T) {
		w := r.procedure("deleteCluster", `{"name":"c"}`)
		if w.Code != 200 {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		r.validateOutput(nsidDeleteCluster, w)
	})

	t.Run("listClusters is a query, so it is a GET", func(t *testing.T) {
		n := nsidListClusters
		req := httptest.NewRequest(http.MethodGet, XRPCPrefix+n, nil)
		req.Header.Set("Authorization", "Bearer "+r.token(audiences()[0], atprotoauth.MaxServiceAuthLifetime, nsid(t, n)))
		w := httptest.NewRecorder()
		r.h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		r.validateOutput(n, w)
	})
}

func TestTheRequestedDeviceKeyLifetimeIsCheckedAtTheDoor(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want int
		ttl  time.Duration
	}{
		{"absent keeps the deployment's lifetime", `{"name":"c"}`, 200, 0},
		{"the declared floor", `{"name":"c","ttlSeconds":60}`, 200, time.Minute},
		{"the configured ceiling", `{"name":"c","ttlSeconds":3600}`, 200, time.Hour},
		{"one second below the floor", `{"name":"c","ttlSeconds":59}`, 400, 0},
		{"one second above the ceiling", `{"name":"c","ttlSeconds":3601}`, 400, 0},
		{"zero", `{"name":"c","ttlSeconds":0}`, 400, 0},
		{"negative", `{"name":"c","ttlSeconds":-1}`, 400, 0},
		{"not an integer at all", `{"name":"c","ttlSeconds":"60"}`, 400, 0},
		{"a fractional second", `{"name":"c","ttlSeconds":60.5}`, 400, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			w := r.procedure("getKubeconfig", tc.body)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
			if tc.want == 400 {
				if got := decode(t, w)["error"]; got != "InvalidRequest" {
					t.Fatalf("error = %v, want InvalidRequest -- an out-of-window lifetime is "+
						"REFUSED, and the response must not read as a cluster problem", got)
				}
				if len(r.store.ttls) != 0 {
					t.Fatalf("a refused lifetime still reached the store as %v", r.store.ttls)
				}
				return
			}
			if len(r.store.ttls) != 1 || r.store.ttls[0] != tc.ttl {
				t.Fatalf("the store was handed lifetimes %v, want [%s]", r.store.ttls, tc.ttl)
			}
		})
	}
}

func TestEveryRefusalIsRefused(t *testing.T) {
	r := newRig(t)
	path := XRPCPrefix + nsidGetKubeconfig
	other := nsidGetKubeconfig + ".other"

	cases := []struct {
		name  string
		token string
	}{
		{"no header at all", ""},
		{"not a JWT", "not.a.token"},
		{"expired", r.token(audiences()[0], -atprotoauth.MaxServiceAuthLifetime, nsid(t, nsidGetKubeconfig))},
		{"another lxm", r.token(audiences()[0], atprotoauth.MaxServiceAuthLifetime, nsid(t, other))},
		{"no lxm claim", r.token(audiences()[0], atprotoauth.MaxServiceAuthLifetime, nil)},
		{"another audience", r.token("did:web:somewhere.else.example", atprotoauth.MaxServiceAuthLifetime, nsid(t, nsidGetKubeconfig))},
		{"another service id", r.token("did:web:"+ourHost+"#other", atprotoauth.MaxServiceAuthLifetime, nsid(t, nsidGetKubeconfig))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := r.do(http.MethodPost, path, tc.token)
			if w.Code != 401 {
				t.Fatalf("status = %d, want 401: %s", w.Code, w.Body.String())
			}
			if got := decode(t, w)["error"]; got != "Unauthorized" {
				t.Fatalf("error = %v, want Unauthorized", got)
			}
			if len(r.store.callers) != 0 {
				t.Fatalf("an unverified token reached the store: %v", r.store.callers)
			}
		})
	}
}

func TestATokenSignedByAnotherKeyIsRefused(t *testing.T) {
	r := newRig(t)
	other, err := atcrypto.GeneratePrivateKeyP256()
	if err != nil {
		t.Fatalf("GeneratePrivateKeyP256: %v", err)
	}
	iss, err := syntax.ParseDID(callerDID)
	if err != nil {
		t.Fatalf("ParseDID: %v", err)
	}
	forged, err := auth.SignServiceAuth(iss, audiences()[0], atprotoauth.MaxServiceAuthLifetime, nsid(t, nsidGetKubeconfig), other)
	if err != nil {
		t.Fatalf("SignServiceAuth: %v", err)
	}

	w := r.do(http.MethodPost, XRPCPrefix+nsidGetKubeconfig, forged)
	if w.Code != 401 {
		t.Fatalf("status = %d, want 401: %s", w.Code, w.Body.String())
	}
}

func TestABodyCannotNameThePrincipal(t *testing.T) {
	r := newRig(t)
	w := r.procedure("getKubeconfig", fmt.Sprintf(
		`{"name":"c","did":%q,"iss":%q,"caller":%q,"sub":%q}`, attacker, attacker, attacker, attacker))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	for _, c := range r.store.callers {
		if string(c) != callerDID {
			t.Fatalf("the store acted for %q; a body field named the principal", c)
		}
	}
	if len(r.store.callers) == 0 {
		t.Fatal("the verb never reached the store")
	}
	if strings.Contains(w.Body.String(), attacker) {
		t.Fatalf("the attacker's DID appeared in the response: %s", w.Body.String())
	}
}

func TestTheBodyIsValidatedAgainstTheLexicon(t *testing.T) {
	r := newRig(t)
	for _, body := range []string{
		`{}`,
		`{"name":5}`,
		`{"name":null}`,
		`["c"]`,
		`{"name":"c"`,
		``,
	} {
		w := r.procedure("getKubeconfig", body)
		if w.Code != 400 {
			t.Errorf("body %q: status = %d, want 400: %s", body, w.Code, w.Body.String())
			continue
		}
		if got := decode(t, w)["error"]; got != "InvalidRequest" {
			t.Errorf("body %q: error = %v, want InvalidRequest", body, got)
		}
	}
	if len(r.store.callers) != 0 {
		t.Fatalf("an invalid body reached the store: %v", r.store.callers)
	}
}

func TestANameThatIsNotALabelIsRefusedAtTheDoor(t *testing.T) {
	for _, tc := range []struct{ what, name string }{
		{"colon", "a:b:clusters"},
		{"space", "a b"},
		{"traversal", "../../root"},
		{"uppercase", "MyCluster"},
		{"leading-hyphen", "-lead"},
		{"toolong", strings.Repeat("a", 300)},
	} {
		t.Run(tc.what, func(t *testing.T) {
			r := newRig(t)
			w := r.procedure("getKubeconfig", fmt.Sprintf(`{"name":%q}`, tc.name))
			if w.Code != 400 {
				t.Fatalf("name %q: status = %d, want 400: %s", tc.name, w.Code, w.Body.String())
			}
			if got := decode(t, w)["error"]; got != "InvalidRequest" {
				t.Fatalf("name %q: error = %v, want InvalidRequest", tc.name, got)
			}
			if len(r.store.names) != 0 {
				t.Fatalf("name %q reached the verb as %q -- it is not a label, so no kcp "+
					"path should ever have been built from it", tc.name, r.store.names)
			}
		})
	}

	t.Run("revokeIdentity keeps its optional name", func(t *testing.T) {
		r := newRig(t)
		if w := r.procedure("revokeIdentity", `{}`); w.Code != 200 {
			t.Fatalf("an absent name is legal here: status = %d: %s", w.Code, w.Body.String())
		}
		if w := r.procedure("revokeIdentity", `{"name":"a:b"}`); w.Code != 400 {
			t.Fatalf("an illegal name must be refused: status = %d: %s", w.Code, w.Body.String())
		}
	})

	for _, tc := range []struct {
		chars int
		want  int
	}{{63, 200}, {64, 400}} {
		t.Run(fmt.Sprintf("boundary-%d", tc.chars), func(t *testing.T) {
			r := newRig(t)
			name := strings.Repeat("a", tc.chars)
			w := r.procedure("getKubeconfig", fmt.Sprintf(`{"name":%q}`, name))
			if w.Code != tc.want {
				t.Fatalf("%d characters: status = %d, want %d: %s",
					tc.chars, w.Code, tc.want, w.Body.String())
			}
			if tc.want == 200 {
				if got := r.store.names; len(got) == 0 || got[len(got)-1] != name {
					t.Fatalf("a legal label never reached the verb: %v", got)
				}
			}
		})
	}
}

func TestListClustersDeclaresNoPaginationItCannotHonour(t *testing.T) {
	r := newRig(t)

	sch, err := r.srv.catalog.Resolve(nsidListClusters)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	q, ok := sch.Def.(lexicon.SchemaQuery)
	if !ok {
		t.Fatalf("%s is a %T, want a query", nsidListClusters, sch.Def)
	}
	params := map[string]bool{}
	if q.Parameters != nil {
		for name := range q.Parameters.Properties {
			params[name] = true
		}
	}
	for _, p := range []string{"limit", "cursor"} {
		if params[p] {
			t.Errorf("%s declares the parameter %q and internal/gateway.ListClusters takes "+
				"none: a declared parameter that reaches no reader is worse than absent -- "+
				"a client counts on it and is silently given everything", nsidListClusters, p)
		}
	}
	if out, err := objectOf(nsidListClusters+" output", q.Output.Schema); err == nil {
		if _, ok := out.Properties["cursor"]; ok {
			t.Errorf("%s declares an output cursor, and nothing can produce one", nsidListClusters)
		}
	}

	tok := r.token(audiences()[0], atprotoauth.MaxServiceAuthLifetime, nsid(t, nsidListClusters))
	for _, qs := range []string{"", "?limit=0", "?limit=201", "?limit=50", "?cursor=abc"} {
		req := httptest.NewRequest(http.MethodGet, XRPCPrefix+nsidListClusters+qs, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		r.h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Errorf("%s: status = %d, want 200: %s", qs, w.Code, w.Body.String())
			continue
		}
		r.validateOutput(nsidListClusters, w)
		if _, ok := decode(t, w)["cursor"]; ok {
			t.Errorf("%s: the response carries a cursor", qs)
		}
	}
}

func TestInitializingIs429WithRetryAfterAndDrainingIsNot(t *testing.T) {
	r := newRig(t)

	r.store.views["init"] = gateway.ClusterView{Name: "init", Phase: "Initializing"}
	w := r.procedure("controlPlaneUp", `{"name":"init"}`)
	if w.Code != 429 {
		t.Fatalf("Initializing: status = %d, want 429: %s", w.Code, w.Body.String())
	}
	if got := decode(t, w)["error"]; got != "ClusterInitializing" {
		t.Fatalf("Initializing: error = %v", got)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("Initializing must carry Retry-After: client-go retries on it automatically")
	}

	r.store.views["drain"] = gateway.ClusterView{Name: "drain", Phase: "Draining"}
	w = r.procedure("controlPlaneUp", `{"name":"drain"}`)
	if w.Code != 409 {
		t.Fatalf("Draining: status = %d, want 409: %s", w.Code, w.Body.String())
	}
	if got := decode(t, w)["error"]; got != "ClusterDraining" {
		t.Fatalf("Draining: error = %v", got)
	}
	if h := w.Header().Get("Retry-After"); h != "" {
		t.Fatalf("Draining must NOT carry Retry-After (got %q): a cancellable gate is a gate an operator clears", h)
	}
}

func TestAnUnmappedErrorBecomesAGenericFiveHundred(t *testing.T) {
	r := newRig(t)
	const secret = "kcp-object-" + "5f3a9" + "-must-not-leak"
	r.store.failVerb = "Get"
	r.store.failWith = errors.New(secret)

	w := r.procedure("getKubeconfig", `{"name":"c"}`)
	if w.Code != 500 {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
	if got := decode(t, w)["error"]; got != "InternalError" {
		t.Fatalf("error = %v, want a named InternalError", got)
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Fatalf("the internal error string reached the client: %s", w.Body.String())
	}
}

func TestTheMintIsNotUnderXrpc(t *testing.T) {
	r := newRig(t)

	if strings.HasPrefix(MintPath, XRPCPrefix) {
		t.Fatalf("%s must not be under %s", MintPath, XRPCPrefix)
	}
	if got := r.do(http.MethodPost, XRPCPrefix+NSIDPrefix+"credential", "x").Code; got != 404 {
		t.Fatalf("an XRPC-shaped path reached the mint: status = %d, want 404", got)
	}
	if got := r.do(http.MethodPost, MintPath, "").Code; got != 401 {
		t.Fatalf("the mint without a credential: status = %d, want 401", got)
	}

	r.mints.binding = mint.Binding{DID: callerDID, Cluster: "c", SA: "sa", SAExists: true, Instance: "1"}
	w := r.do(http.MethodPost, MintPath, "device-credential")
	if w.Code != 200 {
		t.Fatalf("the mint: status = %d, want 200: %s", w.Code, w.Body.String())
	}
	got := decode(t, w)
	if got["apiVersion"] != mint.APIVersion || got["kind"] != mint.Kind {
		t.Fatalf("the mint must return the ExecCredential VERBATIM: %v", got)
	}
	status, _ := got["status"].(map[string]any)
	if status["expirationTimestamp"] == nil || status["expirationTimestamp"] == "" {
		t.Fatalf("the ExecCredential carries no expirationTimestamp, so client-go never renews: %v", got)
	}

	if r.store.wakes != 0 {
		t.Fatalf("the mint woke the control plane %d time(s)", r.store.wakes)
	}
	if len(r.store.callers) != 0 {
		t.Fatalf("the mint reached a cluster verb: %v", r.store.callers)
	}
}

func TestTheMintDoesNotRequireALxm(t *testing.T) {
	r := newRig(t)
	w := r.do(http.MethodPost, MintPath, r.token(audiences()[0], atprotoauth.MaxServiceAuthLifetime, nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: the mint has no lxm to check: %s", w.Code, w.Body.String())
	}
}

func TestTheMintRefusesWhatTheStoreRefuses(t *testing.T) {
	r := newRig(t)

	r.mints.binding.SAExists = false
	w := r.do(http.MethodPost, MintPath, "device-credential")
	if w.Code != 403 {
		t.Fatalf("a deleted ServiceAccount: status = %d, want 403 (the revocation signal): %s", w.Code, w.Body.String())
	}
	if got := decode(t, w)["error"]; got != "IdentityRevoked" {
		t.Fatalf("error = %v, want IdentityRevoked", got)
	}

	r.mints.binding.SAExists = true
	r.mints.binding.Instance = ""
	w = r.do(http.MethodPost, MintPath, "another-device-credential")
	if w.Code != 401 {
		t.Fatalf("an unbound credential: status = %d, want 401: %s", w.Code, w.Body.String())
	}
}

func TestTheMintHoldsAFailingCredential(t *testing.T) {
	r := newRig(t)
	r.mints.binding.SAExists = false

	if w := r.do(http.MethodPost, MintPath, "device-credential"); w.Code != 403 {
		t.Fatalf("first call: status = %d, want 403", w.Code)
	}
	w := r.do(http.MethodPost, MintPath, "device-credential")
	if w.Code != 429 {
		t.Fatalf("held call: status = %d, want 429: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("a hold without Retry-After is a bare refusal")
	}
}

func TestEveryProcedureRefusesTheWrongMethod(t *testing.T) {
	r := newRig(t)
	tok := r.token(audiences()[0], atprotoauth.MaxServiceAuthLifetime, nsid(t, nsidGetKubeconfig))

	req := httptest.NewRequest(http.MethodGet, XRPCPrefix+nsidGetKubeconfig, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET on a procedure: status = %d, want 405", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, XRPCPrefix+nsidListClusters, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	w = httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST on a query: status = %d, want 405", w.Code)
	}
}

func TestConstructionRefusesAnUncheckableSurface(t *testing.T) {
	base := func() Options {
		return Options{
			Gateway:   &gateway.Server{Deny: granted(), Store: newStore()},
			Mint:      mint.New(&fakeMintStore{}, mint.Options{}),
			Audiences: audiences(),
			Directory: &fakeDirectory{keys: map[string]atcrypto.PublicKey{}},
		}
	}
	for name, mutate := range map[string]func(*Options){
		"no gateway":   func(o *Options) { o.Gateway = nil },
		"no mint":      func(o *Options) { o.Mint = nil },
		"no audiences": func(o *Options) { o.Audiences = nil },
		"no directory": func(o *Options) { o.Directory = nil },
	} {
		o := base()
		mutate(&o)
		if _, err := New(o); err == nil {
			t.Errorf("%s: New returned no error", name)
		}
	}
}

func TestEveryLexiconErrorNameIsOneGatewayDeclares(t *testing.T) {
	known := map[string]int{
		gateway.ErrInvalidRequest.Name:      gateway.ErrInvalidRequest.Status,
		gateway.ErrUnauthorized.Name:        gateway.ErrUnauthorized.Status,
		gateway.ErrForbidden.Name:           gateway.ErrForbidden.Status,
		gateway.ErrIdentityRevoked.Name:     gateway.ErrIdentityRevoked.Status,
		gateway.ErrClusterNotFound.Name:     gateway.ErrClusterNotFound.Status,
		gateway.ErrClusterInitializing.Name: gateway.ErrClusterInitializing.Status,
		gateway.ErrClusterDraining.Name:     gateway.ErrClusterDraining.Status,
		gateway.ErrClusterUnavailable.Name:  gateway.ErrClusterUnavailable.Status,
	}

	r := newRig(t)
	for _, rt := range r.srv.routes {
		if len(rt.errors) == 0 {
			t.Errorf("%s declares no errors", rt.nsid)
		}
		for _, e := range rt.errors {
			if _, ok := known[e.Name]; !ok {
				t.Errorf("%s declares error %q, which internal/gateway has no name for", rt.nsid, e.Name)
			}
		}
	}
	declared := map[string]bool{}
	for _, rt := range r.srv.routes {
		for _, e := range rt.errors {
			declared[e.Name] = true
		}
	}
	if declared[gateway.ErrClusterUnavailable.Name] {
		t.Fatal("ClusterUnavailable is now declared by a lexicon, and the transport owes it a " +
			"status the lexicon does not fix")
	}
}

func TestEveryTrackedLexiconIsRouted(t *testing.T) {
	embedded := embeddedIDs(t)
	routed := map[string]bool{}
	for _, rt := range newRig(t).srv.routes {
		routed[rt.nsid] = true
	}

	if len(embedded) != 7 {
		t.Fatalf("the embedded lexicon set has %d methods, want 7: %v", len(embedded), embedded)
	}
	for _, id := range embedded {
		if !routed[id] {
			t.Errorf("%s has a tracked lexicon and no route, so a generated client gets a 404", id)
		}
	}
	if len(routed) != len(embedded) {
		t.Fatalf("%d methods are routed and %d are tracked", len(routed), len(embedded))
	}
}

func TestATeardownBlockedClusterIsVisibleThroughTheList(t *testing.T) {
	r := newRig(t)
	r.store.views["blocked"] = gateway.ClusterView{
		Name: "blocked", Phase: "Draining", TeardownBlocked: true,
	}
	r.store.views["fine"] = gateway.ClusterView{Name: "fine", Phase: "Draining"}

	n := nsidListClusters
	req := httptest.NewRequest(http.MethodGet, XRPCPrefix+n, nil)
	req.Header.Set("Authorization", "Bearer "+r.token(audiences()[0], atprotoauth.MaxServiceAuthLifetime, nsid(t, n)))
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	r.validateOutput(n, w)

	items, _ := decode(t, w)["clusters"].([]any)
	if len(items) != 2 {
		t.Fatalf("the list returned %d clusters, want 2: %s", len(items), w.Body.String())
	}
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		name, _ := item["name"].(string)
		blocked, present := item["teardownBlocked"]
		switch name {
		case "blocked":
			if !present || blocked != true {
				t.Fatalf("a teardown-blocked cluster did not surface the flag: %v", item)
			}
		case "fine":
			if present && blocked == true {
				t.Fatalf("a cluster whose teardown was not blocked reported teardownBlocked: %v", item)
			}
		default:
			t.Fatalf("unexpected cluster %q in the list", name)
		}
	}
}

func TestRevokeIdentityWritesTheDenyForTheCallerAndForNobodyElse(t *testing.T) {
	r := newRig(t)
	r.rev.clusters = []revocation.ClusterView{
		{Name: "warm", ServiceAccountPresent: true},
		{Name: "cold", ServiceAccountPresent: true, Cold: true},
	}

	t.Run("the caller's own did is revoked", func(t *testing.T) {
		w := r.procedure("revokeIdentity", fmt.Sprintf(`{"did":%q}`, callerDID))
		if w.Code != 200 {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		r.validateOutput(nsidRevokeIdentity, w)
		if len(r.rev.writes) != 1 || r.rev.writes[0] != callerDID {
			t.Fatalf("the deny was written for %v, want exactly [%q]", r.rev.writes, callerDID)
		}
		revoked, _ := decode(t, w)["revoked"].([]any)
		if len(revoked) != 3 {
			t.Fatalf("revoked[] = %v, want the account entry plus one per cluster", revoked)
		}
		account, _ := revoked[0].(map[string]any)
		if account["name"] != "" || account["state"] != "RevocationPending" {
			t.Fatalf("account entry = %v, want an empty name and RevocationPending", account)
		}
	})

	t.Run("an absent did means the caller revokes itself", func(t *testing.T) {
		r.rev.writes = nil
		w := r.procedure("revokeIdentity", `{}`)
		if w.Code != 200 {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		if len(r.rev.writes) != 1 || r.rev.writes[0] != callerDID {
			t.Fatalf("the deny was written for %v, want exactly [%q]", r.rev.writes, callerDID)
		}
	})

	t.Run("another did is refused and nothing is written", func(t *testing.T) {
		r.rev.writes = nil
		w := r.procedure("revokeIdentity", fmt.Sprintf(`{"did":%q}`, attacker))
		if w.Code != 403 {
			t.Fatalf("status = %d, want 403: %s", w.Code, w.Body.String())
		}
		if got := decode(t, w)["error"]; got != "Forbidden" {
			t.Fatalf("error = %v, want Forbidden", got)
		}
		if len(r.rev.writes) != 0 {
			t.Fatalf("a body named %q and the deny was written for %v", attacker, r.rev.writes)
		}
	})
}

func TestRevokeIdentityWritesTheDenyBeforeItObservesAnything(t *testing.T) {
	r := newRig(t)
	var order []string
	r.rev.order = &order

	if w := r.procedure("revokeIdentity", `{}`); w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if len(order) != 2 || order[0] != "write" || order[1] != "observe" {
		t.Fatalf("the writer was called in the order %v, want [write observe]", order)
	}
}

func TestRevokeIdentityWithoutAWriterRefusesRatherThanVanishing(t *testing.T) {
	r := newRig(t)
	r.srv.gateway.Revoker = nil

	w := r.procedure("revokeIdentity", `{}`)
	if w.Code != 503 {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
	}
	if got := decode(t, w)["error"]; got != "ClusterUnavailable" {
		t.Fatalf("error = %v, want ClusterUnavailable", got)
	}

	r.store.views["c"] = gateway.ClusterView{Name: "c", Phase: "Ready"}
	if other := r.procedure("controlPlaneDown", `{"name":"c"}`); other.Code != 200 {
		t.Fatalf("controlPlaneDown: status = %d, want 200: %s", other.Code, other.Body.String())
	}
}

func embeddedIDs(t *testing.T) []string {
	t.Helper()
	var ids []string
	err := fs.WalkDir(lexiconFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := lexiconFS.ReadFile(p)
		if err != nil {
			return err
		}
		var sf lexicon.SchemaFile
		if err := json.Unmarshal(b, &sf); err != nil {
			return err
		}
		ids = append(ids, sf.ID)
		return nil
	})
	if err != nil {
		t.Fatalf("walking the embedded lexicons: %v", err)
	}
	sort.Strings(ids)
	return ids
}

func TestTheEmbeddedLexiconsWereLoaded(t *testing.T) {
	r := newRig(t)
	for _, id := range embeddedIDs(t) {
		if _, err := r.srv.catalog.Resolve(id); err != nil {
			t.Errorf("Resolve(%s): %v", id, err)
		}
	}
	for _, rt := range r.srv.routes {
		if _, ok := r.srv.inputs[rt.nsid]; !ok {
			if _, ok := r.srv.params[rt.nsid]; !ok {
				t.Errorf("%s has neither an input schema nor parameters", rt.nsid)
			}
		}
	}
}

func TestEmbeddedLexiconsMatchTheTrackedOnes(t *testing.T) {
	const tracked = "../../lexicons/com/publicdomainrelay/temp/cluster"
	found := 0
	err := fs.WalkDir(lexiconFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		got, err := lexiconFS.ReadFile(p)
		if err != nil {
			return err
		}
		want, err := os.ReadFile(filepath.Join(tracked, d.Name()))
		if err != nil {
			return fmt.Errorf("%s is embedded here but not tracked: %w", d.Name(), err)
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("%s differs from %s/%s; the embedded copy has drifted",
				p, tracked, d.Name())
		}
		found++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found != 7 {
		t.Fatalf("%d lexicons compared, want 7", found)
	}
}
