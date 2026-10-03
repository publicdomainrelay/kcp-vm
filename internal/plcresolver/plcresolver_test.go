package plcresolver

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/plcsweep"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/revocation"
)

const (
	plcURL = "https://plc.test"
	theDID = "did:plc:testaccount0000000000000000"
	other  = "did:plc:otheraccount0000000000000"
)

func liveDoc(did string) string {
	return fmt.Sprintf(`{
		"@context": ["https://www.w3.org/ns/did/v1"],
		"id": %q,
		"verificationMethod": [{
			"id": %q,
			"type": "Multikey",
			"controller": %q,
			"publicKeyMultibase": "zQ3shunJbLm4rT9wK6mVvHn3q8Y"
		}],
		"service": [{"id": "#atproto_pds", "type": "AtprotoPersonalDataServer"}]
	}`, did, did+"#atproto", did)
}

type fakePLC struct {
	t      *testing.T
	mu     sync.Mutex
	paths  []string
	status int
	body   string
	doErr  error
}

func (f *fakePLC) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.mu.Unlock()
	if f.doErr != nil {
		return nil, f.doErr
	}
	return &http.Response{
		StatusCode: f.status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Request:    r,
	}, nil
}

func (f *fakePLC) requested() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func newResolver(t *testing.T, f *fakePLC) *Resolver {
	t.Helper()
	r, err := New(Options{URL: plcURL, Client: &http.Client{Transport: f}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func TestAGoneAccountIsAnAbsenceAndNotAnError(t *testing.T) {
	f := &fakePLC{t: t, status: http.StatusNotFound, body: `{"error":"NotFound"}`}
	r := newResolver(t, f)

	res, err := r.Resolve(context.Background(), theDID)
	if err != nil {
		t.Fatalf("a 404 came back as an error (%v): plcsweep reads an error as SKIPPED, so a "+
			"deleted account would never be revoked", err)
	}
	if res.Found {
		t.Fatal("a 404 reported Found=true")
	}
	if res.Err != nil {
		t.Fatalf("a 404 carried Err=%v: revocation.Sweep reads ANY Err as 'plc unreachable' "+
			"and refuses to revoke, so the account would never be revoked", res.Err)
	}

	if got, want := f.requested(), []string{"/" + theDID}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("requested %v, want %v", got, want)
	}
}

func TestAFetchFailureIsNotAnAbsence(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    *fakePLC
	}{
		{"a 500 from the directory", &fakePLC{status: http.StatusInternalServerError, body: `{}`}},
		{"a 429 from the directory", &fakePLC{status: http.StatusTooManyRequests, body: `{}`}},
		{"a transport failure", &fakePLC{doErr: errors.New("dial tcp 1.2.3.4:443: connection refused")}},
		{"a body that is not a document", &fakePLC{status: http.StatusOK, body: `not json at all`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.f.t = t
			r := newResolver(t, tc.f)

			res, err := r.Resolve(context.Background(), theDID)
			if err == nil {
				t.Fatal("the fetch failed and Resolve reported success")
			}
			if res.Err == nil {
				t.Fatal("the resolution does not carry the error, so a caller that asks " +
					"revocation.Sweep about it is told 'plc unreachable: an outage is not a " +
					"deletion' for the wrong reason -- it is told nothing")
			}
			if res.Found {
				t.Fatal("a failed fetch reported Found=true, which no code path may do")
			}
			if revoke, why := revocation.Sweep(res); revoke {
				t.Fatalf("revocation.Sweep revoked on a failed fetch: %s", why)
			}
		})
	}
}

func TestADocumentWithNoKeyIsLiveAndRevokes(t *testing.T) {
	f := &fakePLC{t: t, status: http.StatusOK, body: fmt.Sprintf(`{"id":%q}`, theDID)}
	r := newResolver(t, f)

	res, err := r.Resolve(context.Background(), theDID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.Found || len(res.Keys) != 0 || res.Err != nil {
		t.Fatalf("want Found with no keys and no error, got %+v", res)
	}
	if revoke, why := revocation.Sweep(res); !revoke {
		t.Fatalf("a keyless document did not revoke: %s", why)
	}
}

func TestALiveDocumentCarriesItsVerificationMethodIDs(t *testing.T) {
	f := &fakePLC{t: t, status: http.StatusOK, body: liveDoc(theDID)}
	r := newResolver(t, f)

	res, err := r.Resolve(context.Background(), theDID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.Found || res.Err != nil {
		t.Fatalf("want Found and no error, got %+v", res)
	}
	if len(res.Keys) != 1 || res.Keys[0] != theDID+"#atproto" {
		t.Fatalf("Keys = %v, want the document's verification method ids", res.Keys)
	}
	if revoke, why := revocation.Sweep(res); revoke {
		t.Fatalf("a live document revoked: %s", why)
	}
}

func TestAMethodWithNoDocumentIsRefused(t *testing.T) {
	f := &fakePLC{t: t, status: http.StatusNotFound, body: `{}`}
	r := newResolver(t, f)

	res, err := r.Resolve(context.Background(), "did:key:zDnaeigPxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	if err == nil {
		t.Fatal("a did:key was resolved as if a document existed to resolve")
	}
	if res.Found == false && res.Err == nil {
		t.Fatal("the refusal is shaped like a 404: a did:key account would be revoked by the " +
			"first pass that reached it")
	}
	if revoke, why := revocation.Sweep(res); revoke {
		t.Fatalf("the refusal revoked: %s", why)
	}
	if n := len(f.requested()); n != 0 {
		t.Fatalf("%d request(s) were made for a DID this package refuses to resolve: %v",
			n, f.requested())
	}

	if _, err := r.Resolve(context.Background(), "not-a-did"); err == nil {
		t.Fatal("a non-DID was accepted")
	}
	if n := len(f.requested()); n != 0 {
		t.Fatalf("a request was made for a string that is not a DID: %v", f.requested())
	}
}

func TestNewRefusesWhatItCannotDoWithout(t *testing.T) {
	if _, err := New(Options{Client: &http.Client{}}); err == nil {
		t.Fatal("an empty URL was accepted, so the resolver has no directory to ask")
	}
	if _, err := New(Options{URL: plcURL}); err == nil {
		t.Fatal("a nil client was accepted: the default would be a client with no timeout and " +
			"no SSRF protection on a fetch that runs forever in a background loop")
	}
}

type recorder struct {
	mu   sync.Mutex
	dids []string
}

func (r *recorder) Revoke(_ context.Context, did, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dids = append(r.dids, did)
	return nil
}

func (r *recorder) revoked() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.dids...)
}

type list []string

func (l list) DIDs(context.Context) ([]string, error) { return l, nil }

func TestAPLCOutageRevokesNobodyThroughTheRealSweeper(t *testing.T) {
	f := &fakePLC{t: t, status: http.StatusInternalServerError, body: `{}`}
	v := &recorder{}
	s := &plcsweep.Sweeper{
		Worklist: list{theDID, other},
		Resolver: newResolver(t, f),
		Revoker:  v,
	}

	out, err := s.Pass(context.Background())
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if got := v.revoked(); len(got) != 0 {
		t.Fatalf("A PLC OUTAGE REVOKED %v: every user of the service loses access on the first "+
			"outage", got)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 outcomes, got %+v", out)
	}
	for _, o := range out {
		if !o.Skipped {
			t.Fatalf("%s was not Skipped, so an operator cannot tell 'we looked and it is fine' "+
				"from 'we could not look': %+v", o.DID, o)
		}
	}
}

func TestAGoneAccountIsRevokedThroughTheRealSweeper(t *testing.T) {
	f := &fakePLC{t: t, status: http.StatusNotFound, body: `{}`}
	v := &recorder{}
	s := &plcsweep.Sweeper{Worklist: list{theDID}, Resolver: newResolver(t, f), Revoker: v}

	out, err := s.Pass(context.Background())
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if got := v.revoked(); len(got) != 1 || got[0] != theDID {
		t.Fatalf("a 404 did not reach the revoker: %v", got)
	}
	if len(out) != 1 || !out[0].Revoke || !strings.Contains(out[0].Why, "404") {
		t.Fatalf("the outcome does not say why: %+v", out)
	}
}

const (
	goneHost = "didweb-gone.com"

	hangHost = "didweb-hang.com"

	nxdomainHost = "didweb-nxdomain.com"
)

type localWeb struct {
	srv  *httptest.Server
	addr string
}

func newLocalWeb(t *testing.T) *localWeb {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == goneHost {
			http.NotFound(w, r)
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	return &localWeb{srv: srv, addr: srv.Listener.Addr().String()}
}

func (l *localWeb) client(timeout time.Duration) *http.Client {
	tr := l.srv.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", l.addr)
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

func nxdomainClient(t *testing.T, timeout time.Duration) (*http.Client, func(name string) error) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("dns listen: %v", err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := buf[:n]
			if n < 12 {
				continue
			}
			i := 12
			for i < n && q[i] != 0 {
				i += int(q[i]) + 1
			}
			if i >= n {
				continue
			}
			i += 5
			if i > n {
				continue
			}
			resp := make([]byte, 0, i)
			resp = append(resp, q[:2]...)
			resp = binary.BigEndian.AppendUint16(resp, 0x8183)
			resp = binary.BigEndian.AppendUint16(resp, 1)
			resp = binary.BigEndian.AppendUint16(resp, 0)
			resp = binary.BigEndian.AppendUint16(resp, 0)
			resp = binary.BigEndian.AppendUint16(resp, 0)
			resp = append(resp, q[12:i]...)
			pc.WriteTo(resp, addr)
		}
	}()
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", pc.LocalAddr().String())
	}}
	return &http.Client{
			Transport: &http.Transport{DialContext: (&net.Dialer{Resolver: resolver}).DialContext},
			Timeout:   timeout,
		}, func(name string) error {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			_, err := resolver.LookupHost(ctx, name)
			return err
		}
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func resolverFor(t *testing.T, c *http.Client) *Resolver {
	t.Helper()
	r, err := New(Options{URL: plcURL, Client: c})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func TestDidWebGoneRevokesAndUnreachableDoesNot(t *testing.T) {
	web := newLocalWeb(t)
	nxClient, probe := nxdomainClient(t, 3*time.Second)

	if err := probe("example.com"); err == nil {
		t.Fatal("example.com resolved through the NXDOMAIN server: this test is talking to the " +
			"real network, and an NXDOMAIN it observes proves nothing about NXDOMAIN")
	}
	var dnsErr *net.DNSError
	if err := probe("example.com"); !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
		t.Fatalf("the probe failed for a reason that is not an NXDOMAIN, so the case below is "+
			"not measuring one: %v", err)
	}

	for _, tc := range []struct {
		name    string
		did     string
		client  *http.Client
		revokes bool
		check   func(error) bool
	}{
		{
			name:    "the document is gone",
			did:     "did:web:" + goneHost,
			client:  web.client(3 * time.Second),
			revokes: true,
		},
		{
			name:   "the name does not resolve",
			did:    "did:web:" + nxdomainHost,
			client: nxClient,
			check: func(err error) bool {
				return strings.Contains(err.Error(), "does not resolve")
			},
		},
		{
			name:   "the host never answers",
			did:    "did:web:" + hangHost,
			client: web.client(300 * time.Millisecond),
			check:  isTimeout,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := resolverFor(t, tc.client)

			res, err := r.Resolve(context.Background(), tc.did)
			if tc.revokes {
				if err != nil || res.Found || res.Err != nil {
					t.Fatalf("want the one absent answer (Found:false, no error), got %+v, %v", res, err)
				}
			} else {
				if err == nil || res.Err == nil {
					t.Fatalf("an unreachable document was reported as an ANSWER: %+v, %v -- "+
						"plcsweep reads a nil error as a decision and would revoke the account",
						res, err)
				}
				if res.Found {
					t.Fatalf("an unreachable document reported Found=true: %+v", res)
				}
				if tc.check == nil || !tc.check(err) {
					t.Fatalf("the failure is not the one this case is about, so it cannot be "+
						"told from a deletion: %v", err)
				}
			}

			v := &recorder{}
			s := &plcsweep.Sweeper{Worklist: list{tc.did}, Resolver: r, Revoker: v}
			out, err := s.Pass(context.Background())
			if err != nil {
				t.Fatalf("Pass: %v", err)
			}
			got := v.revoked()
			if tc.revokes {
				if len(got) != 1 || got[0] != tc.did {
					t.Fatalf("a deleted did:web document did not reach the revoker: %v", got)
				}
				if len(out) != 1 || out[0].Skipped {
					t.Fatalf("a deleted did:web document was reported as skipped: %+v", out)
				}
				return
			}
			if len(got) != 0 {
				t.Fatalf("AN UNREACHABLE did:web DOCUMENT REVOKED %v -- a resolver outage read "+
					"as a deletion", got)
			}
			if len(out) != 1 || !out[0].Skipped {
				t.Fatalf("an unreachable document was not reported as SKIPPED, so an operator "+
					"cannot tell 'we looked and it is fine' from 'we could not look': %+v", out)
			}
		})
	}
}

func TestALiveDidWebDocumentIsResolvedAndCarriesItsKeys(t *testing.T) {
	live := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/did.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, liveDoc("did:web:example.com"))
	}))
	t.Cleanup(live.Close)

	tr := live.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	addr := live.Listener.Addr().String()
	tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}

	r := resolverFor(t, &http.Client{Transport: tr, Timeout: 3 * time.Second})
	res, err := r.Resolve(context.Background(), "did:web:example.com")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.Found || res.Err != nil || len(res.Keys) != 1 {
		t.Fatalf("a live did:web document did not resolve to its keys: %+v", res)
	}
	if revoke, why := revocation.Sweep(res); revoke {
		t.Fatalf("a live did:web document revoked: %s", why)
	}
	if res.Keys[0] != "did:web:example.com#atproto" {
		t.Fatalf("Keys = %v, want the document's verification method ids", res.Keys)
	}
}

func TestTheResolverSatisfiesTheSeam(t *testing.T) {
	var _ plcsweep.Resolver = (*Resolver)(nil)
	r, err := New(Options{URL: plcURL, Client: &http.Client{Transport: &fakePLC{t: t}}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if r == nil || r.dir == nil {
		t.Fatal("New returned a resolver with no directory, so the seam would panic on the first sweep")
	}
}
