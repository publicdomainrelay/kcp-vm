package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/mint"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/plcsweep"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/revocation"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/sweepkcp"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tenantclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/xrpcserver"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := load(env(completeEnv()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

type refusingDirectory struct {
	mu      sync.Mutex
	lookups int
}

func (d *refusingDirectory) LookupDID(context.Context, syntax.DID) (*identity.Identity, error) {
	d.mu.Lock()
	d.lookups++
	d.mu.Unlock()
	return nil, errors.New("refusingDirectory: this test does not resolve")
}

func (d *refusingDirectory) LookupHandle(context.Context, syntax.Handle) (*identity.Identity, error) {
	return nil, errors.New("refusingDirectory: this test does not resolve")
}

func (d *refusingDirectory) Lookup(context.Context, syntax.AtIdentifier) (*identity.Identity, error) {
	return nil, errors.New("refusingDirectory: this test does not resolve")
}

func (d *refusingDirectory) Purge(context.Context, syntax.AtIdentifier) error { return nil }

func (d *refusingDirectory) asked() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lookups
}

type noMintStore struct{}

func (noMintStore) Resolve(context.Context, mint.DeviceCredential) (mint.Binding, error) {
	return mint.Binding{}, mint.ErrBindingUnknown
}

func (noMintStore) Issue(context.Context, mint.Binding, time.Duration) (string, time.Time, error) {
	return "", time.Time{}, errors.New("noMintStore: Issue was reached, which this test does not expect")
}

func TestBuildRefusesANilMintStore(t *testing.T) {
	_, err := Build(testConfig(t), Deps{Directory: &refusingDirectory{}}, quiet())
	if !errors.Is(err, ErrNoMintStore) {
		t.Fatalf("want ErrNoMintStore, got %v", err)
	}
	for _, want := range []string{
		"mint.Store", "tenantclient", "buildDeps", "gwmint",
		"SOCIALWEB_GATEWAY_MINT_KUBECONFIGS", "SOCIALWEB_GATEWAY_DEVKEY_REGISTRY",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not mention %q: %v", want, err)
		}
	}
}

func TestBuildDepsBuildsTheMintStoreFromTheRealImplementation(t *testing.T) {
	cfg := testConfig(t)
	cfg.KCPKubeconfig = writeKubeconfig(t)
	cfg.MintKubeconfigs = writeMintKubeconfigs(t, "live")

	deps, err := buildDeps(cfg)
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	if deps.MintStore == nil {
		t.Fatal("buildDeps supplied no mint.Store, so Build returns ErrNoMintStore and this " +
			"binary does not start -- which is the gate, not a detail")
	}
	if _, ok := deps.MintStore.(*tenantclient.Client); !ok {
		t.Fatalf("the mint store is a %T, not internal/tenantclient's Client", deps.MintStore)
	}

	g, err := Build(cfg, deps, quiet())
	if err != nil {
		t.Fatalf("Build refused a Deps carrying a real mint store: %v", err)
	}
	if g == nil || g.Handler() == nil {
		t.Fatal("Build returned a gateway with no handler, so nothing would be served")
	}
}

func TestBuildDepsRefusesAMintKubeconfigsPathThatIsNotADirectory(t *testing.T) {
	cfg := testConfig(t)
	cfg.KCPKubeconfig = writeKubeconfig(t)

	absent := filepath.Join(t.TempDir(), "no-such-directory")
	notADir := writeKubeconfig(t)
	for _, tc := range []struct{ name, path string }{
		{"absent", absent},
		{"not a directory", notADir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg.MintKubeconfigs = tc.path
			if _, err := buildDeps(cfg); err == nil {
				t.Fatalf("%s was accepted as the mint credential directory; the mint would "+
					"then fail closed on EVERY request with an error about a missing file, "+
					"after the process had already reported itself healthy", tc.path)
			}
		})
	}
}

func TestBuildDepsRefusesNilAudiencesWithoutPanicking(t *testing.T) {
	cfg := testConfig(t)
	cfg.Audiences = nil
	cfg.KCPKubeconfig = writeKubeconfig(t)
	cfg.MintKubeconfigs = writeMintKubeconfigs(t, "live")

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("buildDeps panicked on an empty audience list: %v; the length guard runs "+
				"after the list is indexed, so it can never fire", r)
		}
	}()
	if _, err := buildDeps(cfg); err == nil {
		t.Fatal("an empty audience list was accepted; a device credential would be signed for " +
			"nothing")
	} else if !strings.Contains(err.Error(), envAudiences) {
		t.Fatalf("the refusal does not name %s: %v", envAudiences, err)
	}
}

func writeMintKubeconfigs(t *testing.T, clusters ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, cluster := range clusters {
		doc := `apiVersion: v1
kind: Config
clusters:
- name: tenant
  cluster:
    server: https://127.0.0.1:6443
users:
- name: gwmint
  user:
    token: test-only-not-a-credential
contexts:
- name: tenant
  context:
    cluster: tenant
    user: gwmint
current-context: tenant
`
		key, err := kcpclient.ClusterKey(kcpclient.HomePath(mintTestDID, cluster))
		if err != nil {
			t.Fatalf("the cluster key for %q: %v", cluster, err)
		}
		if err := os.WriteFile(filepath.Join(dir, key), []byte(doc), 0o600); err != nil {
			t.Fatalf("writing the mint kubeconfig for %q: %v", cluster, err)
		}
	}
	return dir
}

func TestBuildDepsReadsTheScopedCredentialAndKeepsIndigosDirectoryHardened(t *testing.T) {
	cfg := testConfig(t)
	cfg.KCPKubeconfig = writeKubeconfig(t)
	cfg.MintKubeconfigs = writeMintKubeconfigs(t, "live")

	deps, err := buildDeps(cfg)
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	if any(deps.Store) != any(deps.Deny) {
		t.Fatal("the store and the deny are different objects")
	}
	if deps.MintStore == nil {
		t.Fatal("buildDeps supplied no mint.Store, so Build refuses and the binary does not " +
			"start. internal/tenantclient is the implementation; what it needs is " +
			"SOCIALWEB_GATEWAY_MINT_KUBECONFIGS")
	}
	if deps.Revoker == nil {
		t.Fatal("the revoker is nil, so revokeIdentity answers 503 ClusterUnavailable and no " +
			"revocation can be performed through the gateway at all. gateway.Revoker is two " +
			"methods, WriteDeny and FanOut; *kcpclient.Client implements both, so the wiring " +
			"is `Revoker: client` -- the same object as Store and Deny, now its third seam")
	}

	if deps.Sweep == nil {
		t.Fatal("the sweep is nil, so the only way an account is ever revoked is a user " +
			"asking. Its seams exist; see internal/{plcsweep,plcresolver,sweepkcp}")
	}
	if deps.Sweep.Worklist == nil || deps.Sweep.Resolver == nil || deps.Sweep.Revoker == nil {
		t.Fatalf("the sweep is assembled with a nil seam: %+v", deps.Sweep)
	}
	if r, ok := deps.Sweep.Revoker.(*sweepkcp.Revoker); !ok {
		t.Fatalf("the sweep's revoker is a %T, not the kcp-backed one", deps.Sweep.Revoker)
	} else if any(r.Deny) != any(deps.Store) {
		t.Fatal("the sweep's deny and the gateway's store are different objects -- two " +
			"clients are two answers about which cluster a caller has")
	}

	cached, ok := deps.Directory.(*identity.CacheDirectory)
	if !ok {
		t.Fatalf("the directory is a %T; indigo's cached directory is what makes a PLC outage "+
			"survivable within a window", deps.Directory)
	}
	if cached.ErrTTL != cfg.DirectoryCacheTTL {
		t.Fatalf("the cache error TTL is %s, want the configured %s -- the directory was not "+
			"re-wrapped with this deployment's window", cached.ErrTTL, cfg.DirectoryCacheTTL)
	}
	if cached.Inner == nil {
		t.Fatal("the cached directory has no inner directory, so the hardening was dropped")
	}
}

func TestBuildDepsRefusesAKubeconfigItCannotRead(t *testing.T) {
	cfg := testConfig(t)
	cfg.KCPKubeconfig = filepath.Join(t.TempDir(), "absent.kubeconfig")
	if _, err := buildDeps(cfg); err == nil {
		t.Fatal("an unreadable kubeconfig was accepted; the credential is the whole boundary " +
			"between this process and kcp")
	}
}

func writeKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway.kubeconfig")
	doc := `apiVersion: v1
kind: Config
clusters:
- name: kcp
  cluster:
    server: https://127.0.0.1:6443
users:
- name: gateway
  user:
    token: test-only-not-a-credential
contexts:
- name: kcp
  context:
    cluster: kcp
    user: gateway
current-context: kcp
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("writing the kubeconfig: %v", err)
	}
	return path
}

func TestTheTransportsRefusalsSurfaceAtConstruction(t *testing.T) {
	cases := []struct {
		name string
		cfg  func(*Config)
		deps func(*Deps)
		want string
	}{
		{"no audiences", func(c *Config) { c.Audiences = nil }, func(*Deps) {}, "no audiences"},
		{"no directory", func(*Config) {}, func(d *Deps) { d.Directory = nil }, "no directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			tc.cfg(&cfg)
			d := Deps{MintStore: noMintStore{}, Directory: &refusingDirectory{}}
			tc.deps(&d)
			_, err := Build(cfg, d, quiet())
			if err == nil {
				t.Fatalf("%s was accepted at construction; every token would then be refused "+
					"as Unauthorized, which is indistinguishable from a bad credential", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the refusal does not say why: %v", err)
			}
		})
	}
}

func TestARouteRespondsAndTheTwoNamespacesStayApart(t *testing.T) {
	dir := &refusingDirectory{}
	g, err := Build(testConfig(t), Deps{MintStore: noMintStore{}, Directory: dir}, quiet())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	handler := g.Handler()

	const nsid = xrpcserver.NSIDPrefix
	cases := []struct {
		name       string
		method     string
		path       string
		token      string
		wantStatus int
		wantError  string
	}{
		{"an XRPC procedure with no token", http.MethodPost, "/xrpc/" + nsid + "deleteCluster", "", 401, "Unauthorized"},
		{"the XRPC query with no token", http.MethodGet, "/xrpc/" + nsid + "listClusters", "", 401, "Unauthorized"},
		{"an XRPC procedure whose token is not a JWT", http.MethodPost, "/xrpc/" + nsid + "deleteCluster", "Bearer not.a.jwt", 401, "Unauthorized"},
		{"the mint with no device credential", http.MethodPost, xrpcserver.MintPath, "", 401, "Unauthorized"},
		{"the mint with a device credential that binds nothing", http.MethodPost, xrpcserver.MintPath, "Bearer device-key", 401, "Unauthorized"},

		{"the query called as a procedure", http.MethodPost, "/xrpc/" + nsid + "listClusters", "", 405, ""},
		{"the mint called as a GET", http.MethodGet, xrpcserver.MintPath, "", 405, ""},
		{"the mint reached through /xrpc/", http.MethodPost, "/xrpc" + xrpcserver.MintPath, "", 404, ""},
		{"a method that does not exist", http.MethodPost, "/xrpc/" + nsid + "noSuchVerb", "", 404, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(t, handler, tc.method, tc.path, tc.token, "")
			if rec.Code != tc.wantStatus {
				t.Fatalf("%s %s = %d, want %d (%s)", tc.method, tc.path, rec.Code, tc.wantStatus, rec.Body)
			}
			if tc.wantError == "" {
				return
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("the refusal is not the XRPC error body: %v (%s)", err, rec.Body)
			}
			if body.Error != tc.wantError {
				t.Fatalf("error = %q, want %q", body.Error, tc.wantError)
			}
		})
	}

	if dir.asked() != 0 {
		t.Fatalf("the directory was asked to resolve %d time(s) for tokens that never parsed", dir.asked())
	}
}

func TestEveryVerbIsBound(t *testing.T) {
	g, err := Build(testConfig(t), Deps{MintStore: noMintStore{}, Directory: &refusingDirectory{}}, quiet())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, verb := range []string{
		"getKubeconfig", "controlPlaneUp", "controlPlaneDown", "deleteCluster", "listClusters", "revokeIdentity",
	} {
		method := http.MethodPost
		if verb == "listClusters" {
			method = http.MethodGet
		}
		if code := request(t, g.Handler(), method, "/xrpc/"+xrpcserver.NSIDPrefix+verb, "", "").Code; code == 404 {
			t.Fatalf("%s is not routed: the surface is missing a verb the lexicons declare", verb)
		}
	}
	if code := request(t, g.Handler(), http.MethodPost, xrpcserver.MintPath, "", "").Code; code == 404 {
		t.Fatal("the mint is not routed")
	}
}

func request(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type sweepWorklist struct {
	mu    sync.Mutex
	reads int
	dids  []string
}

func (w *sweepWorklist) DIDs(context.Context) ([]string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reads++
	return w.dids, nil
}

func (w *sweepWorklist) readCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.reads
}

type goneResolver struct{}

func (goneResolver) Resolve(context.Context, string) (revocation.PLCResolution, error) {
	return revocation.PLCResolution{Found: false}, nil
}

type sweepRevoker struct {
	mu      sync.Mutex
	revoked []string
}

func (r *sweepRevoker) Revoke(_ context.Context, did, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revoked = append(r.revoked, did)
	return nil
}

func (r *sweepRevoker) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.revoked)
}

func TestRunRunsTheSweepOnceImmediatelyAndStopsOnCancel(t *testing.T) {
	worklist := &sweepWorklist{dids: []string{"did:plc:gone"}}
	revoker := &sweepRevoker{}
	sweeper := &plcsweep.Sweeper{Worklist: worklist, Resolver: goneResolver{}, Revoker: revoker}

	g := &Gateway{sweep: sweeper, sweepInterval: time.Hour, log: quiet()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()

	if !waitFor(func() bool { return revoker.count() > 0 }) {
		t.Fatal("the sweep never ran a pass; an account deleted at PLC while the process was " +
			"down would not be noticed")
	}
	if worklist.readCount() != 1 {
		t.Fatalf("the worklist was read %d times before the first interval, want 1", worklist.readCount())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run reported a cancelled context as a failure: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

func TestRunStopsOnCancelWithNoSweepWired(t *testing.T) {
	g := &Gateway{log: quiet()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run with no sweep returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run with no sweep did not return after its context was cancelled")
	}
}

func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}
