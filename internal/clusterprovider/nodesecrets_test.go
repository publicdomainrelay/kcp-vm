package clusterprovider

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodemarket"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbao"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

const devRootToken = "node-vault-test-root"

const devBaoAttempts = 3

func startDevBao(t *testing.T) (*openbao.Client, string) {
	t.Helper()
	livegate.Short(t, "this test starts a real openbao and -short was asked for: a skip is not a pass")
	if _, err := exec.LookPath("bao"); err != nil {
		livegate.RequireLocal(t, "bao is not on PATH, so the node JWT login is not exercised against a real vault")
		return nil, ""
	}
	var last error
	for attempt := 1; attempt <= devBaoAttempts; attempt++ {
		client, addr, err := startDevBaoOnce(t)
		if err == nil {
			return client, addr
		}
		last = err
	}
	t.Fatalf("bao -dev did not answer after %d attempts on fresh ports; the port is read from a "+
		"closed listener, so another process can take it before the vault binds it: %v",
		devBaoAttempts, last)
	return nil, ""
}

func startDevBaoOnce(t *testing.T) (*openbao.Client, string, error) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("borrowing a port for the test vault: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "bao", "server", "-dev",
		"-dev-root-token-id="+devRootToken, "-dev-listen-address="+addr)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("starting bao -dev: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stop := func() {
		cancel()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
		}
	}
	client := openbao.New("http://"+addr, devRootToken)
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-exited:
			cancel()
			return nil, addr, fmt.Errorf("bao -dev exited before it answered on %s: %v\n%s", addr, err, stderr.String())
		default:
		}
		if h, err := client.Health(context.Background()); err == nil && h.Initialized && !h.Sealed {
			t.Cleanup(stop)
			return client, "http://" + addr, nil
		}
		if time.Now().After(deadline) {
			stop()
			return nil, addr, fmt.Errorf("bao -dev never answered on %s within 30s:\n%s", addr, stderr.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

type testIssuer struct {
	url string
	key *ecdsa.PrivateKey
}

func startTestIssuer(t *testing.T) *testIssuer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating the issuer key: %v", err)
	}
	issuer := &testIssuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer.url,
			"jwks_uri":                              issuer.url + "/jwks",
			"id_token_signing_alg_values_supported": []string{"ES256"},
			"response_types_supported":              []string{"id_token"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pad := func(b []byte) string {
			out := make([]byte, 32)
			copy(out[32-len(b):], b)
			return base64.RawURLEncoding.EncodeToString(out)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": "k1",
			"x": pad(key.X.Bytes()), "y": pad(key.Y.Bytes()),
		}}})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	issuer.url = server.URL
	return issuer
}

func (i *testIssuer) token(t *testing.T, subject, audience string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": i.url,
		"sub": subject,
		"aud": audience,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Minute).Unix(),
	})
	tok.Header["kid"] = "k1"
	signed, err := tok.SignedString(i.key)
	if err != nil {
		t.Fatalf("signing the workload token: %v", err)
	}
	return signed
}

func TestANodeLogsInWithItsWorkloadTokenAndReadsOnlyItsOwnSecret(t *testing.T) {
	root, addr := startDevBao(t)
	issuer := startTestIssuer(t)
	ctx := context.Background()
	vault := NewNodeVaultWith(func() (*openbao.Client, error) { return root, nil })

	const node = "c0ffee-node1"
	const other = "c0ffee-node2"
	secret := nodemarket.NodeSecret{
		Token:            "K10abc::server:pw",
		NodePassword:     "node-password",
		ServerCA:         "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----\n",
		ApiserverTicket:  "nodeticket",
		AdvertiseAddress: "100.127.255.254",
		ApiserverPort:    24001,
	}
	for _, n := range []string{node, other} {
		if err := vault.EnsurePolicy(ctx, n); err != nil {
			t.Fatalf("EnsurePolicy %s: %v", n, err)
		}
		if err := vault.WriteSecret(ctx, n, secret); err != nil {
			t.Fatalf("WriteSecret %s: %v", n, err)
		}
	}
	mounts, err := root.SecretMounts(ctx)
	if err != nil {
		t.Fatalf("SecretMounts: %v", err)
	}
	if mounts["socialweb-nodes"].Type != "kv" {
		t.Fatalf("no kv mount at socialweb-nodes: %v", mounts)
	}
	stored, err := root.GetKV(ctx, "socialweb-nodes/data/"+node)
	if err != nil {
		t.Fatalf("reading socialweb-nodes/data/%s as root: %v", node, err)
	}
	data := stored
	for field, want := range map[string]any{
		"token": secret.Token, "nodePassword": secret.NodePassword, "serverCA": secret.ServerCA,
		"apiserverTicket": secret.ApiserverTicket, "advertiseAddress": secret.AdvertiseAddress,
		"apiserverPort": json.Number("24001"),
	} {
		got := data[field]
		if n, ok := got.(float64); ok {
			got = json.Number(fmt.Sprint(n))
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("socialweb-nodes/data/%s field %s = %v, want %v", node, field, got, want)
		}
	}
	policy, err := root.GetPolicy(ctx, "node-"+node)
	if err != nil {
		t.Fatalf("reading policy node-%s: %v", node, err)
	}
	if !strings.Contains(policy, `path "socialweb-nodes/data/`+node+`"`) || strings.Contains(policy, other) {
		t.Fatalf("policy node-%s reads %q, want read on socialweb-nodes/data/%s only", node, policy, node)
	}

	audience := "socialweb.computer/node/c0ffee/" + node
	grant := v1alpha1.NodeClaimGrant{Issuer: issuer.url, Subject: "vm:" + node, Audience: audience}
	login, err := vault.EnsureLogin(ctx, node, grant)
	if err != nil {
		t.Fatalf("EnsureLogin: %v", err)
	}
	if again, err := vault.EnsureLogin(ctx, node, grant); err != nil || again != login {
		t.Fatalf("a second EnsureLogin over the same grant: %+v, %v; want %+v and no error", again, err, login)
	}
	wantMount := vaultlayout.NodeJWTMount(issuer.url)
	if login.Mount != wantMount || login.Role != node || login.Policy != "node-"+node {
		t.Fatalf("EnsureLogin answered %+v, want mount %s role %s policy node-%s", login, wantMount, node, node)
	}
	config, err := root.Read(ctx, "auth/"+wantMount+"/config")
	if err != nil {
		t.Fatalf("reading auth/%s/config: %v", wantMount, err)
	}
	if config["oidc_discovery_url"] != issuer.url || config["bound_issuer"] != issuer.url {
		t.Fatalf("auth/%s/config is %v, want discovery url and bound issuer %s", wantMount, config, issuer.url)
	}
	role, err := root.Read(ctx, "auth/"+wantMount+"/role/"+node)
	if err != nil {
		t.Fatalf("reading the role: %v", err)
	}
	if role["role_type"] != "jwt" || role["bound_subject"] != "vm:"+node || role["user_claim"] != "sub" ||
		fmt.Sprint(role["bound_audiences"]) != "["+audience+"]" ||
		fmt.Sprint(role["token_policies"]) != "[node-"+node+"]" || fmt.Sprint(role["token_ttl"]) != "300" {
		t.Fatalf("role %s reads %v", node, role)
	}

	guest := openbao.New(addr, "")
	if _, err := guest.LoginJWT(ctx, wantMount, node, issuer.token(t, "vm:"+other, audience)); err == nil {
		t.Fatal("a workload token for another subject logged in to this node's role")
	}
	if _, err := guest.LoginJWT(ctx, wantMount, node, issuer.token(t, "vm:"+node, "socialweb.computer/node/c0ffee/"+other)); err == nil {
		t.Fatal("a workload token for another audience logged in to this node's role")
	}
	nodeToken, err := guest.LoginJWT(ctx, wantMount, node, issuer.token(t, "vm:"+node, audience))
	if err != nil {
		t.Fatalf("the node's own workload token did not log in: %v", err)
	}
	asNode := openbao.New(addr, nodeToken)
	own, err := asNode.GetKV(ctx, "socialweb-nodes/data/"+node)
	if err != nil {
		t.Fatalf("the node could not read its own secret: %v", err)
	}
	if own["apiserverTicket"] != "nodeticket" {
		t.Fatalf("the node read %v from its own path", own)
	}
	if _, err := asNode.GetKV(ctx, "socialweb-nodes/data/"+other); !errors.Is(err, openbao.ErrForbidden) {
		t.Fatalf("the node read another node's secret: %v, want a 403", err)
	}

	if err := vault.Revoke(ctx, node); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := root.Read(ctx, "auth/"+wantMount+"/role/"+node); !errors.Is(err, openbao.ErrNotFound) {
		t.Fatalf("the role survived the release: %v", err)
	}
	if _, err := root.GetKV(ctx, "socialweb-nodes/data/"+node); !errors.Is(err, openbao.ErrNotFound) {
		t.Fatalf("the secret survived the release: %v", err)
	}
	policies, err := root.List(ctx, "sys/policies/acl")
	if err != nil {
		t.Fatalf("listing policies: %v", err)
	}
	if slices.Contains(policies, "node-"+node) || !slices.Contains(policies, "node-"+other) {
		t.Fatalf("after releasing %s the policies are %v; want its policy gone and %s's kept", node, policies, other)
	}
	if _, err := guest.LoginJWT(ctx, wantMount, node, issuer.token(t, "vm:"+node, audience)); err == nil {
		t.Fatal("the released node's workload token still logs in")
	}
	auths, err := root.AuthMounts(ctx)
	if err != nil {
		t.Fatalf("AuthMounts: %v", err)
	}
	if auths[wantMount].Type != "jwt" {
		t.Fatalf("the issuer's mount %s was removed with its last role; it is kept by design: %v", wantMount, auths)
	}
	if err := vault.Revoke(ctx, node); err != nil {
		t.Fatalf("a second Revoke of a released node: %v", err)
	}
}
