package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/xrpcserver"
)

const validCredential = `{"apiVersion":"client.authentication.k8s.io/v1",` +
	`"kind":"ExecCredential","status":{"token":"sa-token","expirationTimestamp":"2030-01-01T00:00:00Z"}}`

func TestTheMinterSpeaksWhatTheServerReads(t *testing.T) {
	var gotPath, gotAuth, gotCT, gotBody, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, validCredential)
	}))
	defer srv.Close()

	const jwt = "the.signed.service-auth.jwt"
	m := &httpMinter{URL: srv.URL + xrpcserver.MintPath, Client: srv.Client()}
	cred, err := m.Mint(context.Background(), jwt)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if cred.Status.Token != "sa-token" {
		t.Fatalf("token = %q, want the body returned UNCHANGED -- TTL, audience and renewal "+
			"policy live at the endpoint, not in a binary on every user's machine", cred.Status.Token)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotPath != xrpcserver.MintPath {
		t.Errorf("path = %q, want %q -- taken from the server's own constant, so a rename "+
			"on either side fails HERE rather than on a user's machine", gotPath, xrpcserver.MintPath)
	}
	if gotAuth != "Bearer "+jwt {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer "+jwt)
	}
	if !strings.Contains(gotCT, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
	if strings.Contains(gotBody, jwt) || strings.Contains(gotBody, "did:") {
		t.Errorf("the request body carries the credential: %q. The token must travel ONLY "+
			"in the header -- a body-carried identity is the defect ADR 0020 records", gotBody)
	}
}

func TestANonOKMintYieldsNoCredential(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		m := &httpMinter{URL: srv.URL + xrpcserver.MintPath, Client: srv.Client()}
		cred, err := m.Mint(context.Background(), "jwt")
		srv.Close()

		if err == nil {
			t.Errorf("%d: Mint returned no error", code)
		}
		if cred.Status.Token != "" {
			t.Errorf("%d: a failed mint produced a credential with token %q", code, cred.Status.Token)
		}
	}
}

func TestABodyThatIsNotAnExecCredentialIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "<html>a proxy's error page, with a 200</html>")
	}))
	defer srv.Close()

	m := &httpMinter{URL: srv.URL + xrpcserver.MintPath, Client: srv.Client()}
	if _, err := m.Mint(context.Background(), "jwt"); err == nil {
		t.Fatal("a body that is not an ExecCredential was accepted as one")
	}
}
