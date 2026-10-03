package atprotoauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bluesky-social/indigo/atproto/atcrypto"
	"github.com/bluesky-social/indigo/atproto/auth"
	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

const (
	ourHost    = "gateway.example.com"
	ourService = "socialweb_computer"
	theNSID    = "com.publicdomainrelay.temp.cluster.getKubeconfig"
	otherNSID  = "com.publicdomainrelay.temp.cluster.deleteCluster"
	issuerDID  = "did:plc:abcdefghijklmnopqrstuvwx"
	otherDID   = "did:plc:zyxwvutsrqponmlkjihgfedc"
)

func audiences() []string {
	return []string{
		"did:web:" + ourHost,
		"did:web:" + ourHost + "#" + ourService,
	}
}

type fakeDirectory struct {
	keys    map[string]atcrypto.PublicKey
	lookups int
}

func (f *fakeDirectory) LookupDID(_ context.Context, did syntax.DID) (*identity.Identity, error) {
	f.lookups++
	pk, ok := f.keys[did.String()]
	if !ok {
		return nil, identity.ErrDIDNotFound
	}
	return &identity.Identity{
		DID: did,
		Keys: map[string]identity.VerificationMethod{
			"atproto": {Type: "Multikey", PublicKeyMultibase: pk.Multibase()},
		},
	}, nil
}

func (f *fakeDirectory) LookupHandle(context.Context, syntax.Handle) (*identity.Identity, error) {
	return nil, identity.ErrHandleNotFound
}

func (f *fakeDirectory) Lookup(context.Context, syntax.AtIdentifier) (*identity.Identity, error) {
	return nil, identity.ErrDIDNotFound
}

func (f *fakeDirectory) Purge(context.Context, syntax.AtIdentifier) error { return nil }

func mint(t *testing.T, iss syntax.DID, aud string, ttl time.Duration, lxm *syntax.NSID, priv atcrypto.PrivateKey) string {
	t.Helper()
	tok, err := auth.SignServiceAuth(iss, aud, ttl, lxm, priv)
	if err != nil {
		t.Fatalf("SignServiceAuth: %v", err)
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

func mustKey(t *testing.T) atcrypto.PrivateKey {
	t.Helper()
	k, err := atcrypto.GeneratePrivateKeyP256()
	if err != nil {
		t.Fatalf("GeneratePrivateKeyP256: %v", err)
	}
	return k
}

func pub(t *testing.T, priv atcrypto.PrivateKey) atcrypto.PublicKey {
	t.Helper()
	pk, err := priv.PublicKey()
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}
	return pk
}

func did(t *testing.T, s string) syntax.DID {
	t.Helper()
	d, err := syntax.ParseDID(s)
	if err != nil {
		t.Fatalf("ParseDID(%s): %v", s, err)
	}
	return d
}

func TestRejectsATokenWithALongLifetime(t *testing.T) {
	priv := mustKey(t)
	dir := &fakeDirectory{keys: map[string]atcrypto.PublicKey{issuerDID: pub(t, priv)}}

	tok := mint(t, did(t, issuerDID), audiences()[0], 30*24*time.Hour, nsid(t, theNSID), priv)

	got, err := Verify(context.Background(), tok, theNSID, audiences(), dir)
	if !errors.Is(err, ErrLifetimeTooLong) {
		t.Fatalf("err = %v (issuer %q), want ErrLifetimeTooLong", err, got)
	}
}

func TestAcceptsATokenAtExactlyTheMaximumLifetime(t *testing.T) {
	priv := mustKey(t)
	dir := &fakeDirectory{keys: map[string]atcrypto.PublicKey{issuerDID: pub(t, priv)}}

	tok := mint(t, did(t, issuerDID), audiences()[0], MaxServiceAuthLifetime, nsid(t, theNSID), priv)

	got, err := Verify(context.Background(), tok, theNSID, audiences(), dir)
	if err != nil {
		t.Fatalf("Verify rejected a token at exactly the maximum lifetime: %v", err)
	}
	if got != issuerDID {
		t.Fatalf("issuer = %q, want %q", got, issuerDID)
	}
}

func TestVerifiesARealTokenAndReturnsTheIssuer(t *testing.T) {
	priv := mustKey(t)
	dir := &fakeDirectory{keys: map[string]atcrypto.PublicKey{issuerDID: pub(t, priv)}}

	tok := mint(t, did(t, issuerDID), audiences()[0], MaxServiceAuthLifetime, nsid(t, theNSID), priv)

	got, err := Verify(context.Background(), tok, theNSID, audiences(), dir)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got != issuerDID {
		t.Fatalf("issuer = %q, want %q", got, issuerDID)
	}
}

func TestAcceptsBothAudienceShapes(t *testing.T) {
	priv := mustKey(t)
	dir := &fakeDirectory{keys: map[string]atcrypto.PublicKey{issuerDID: pub(t, priv)}}

	for _, aud := range audiences() {
		tok := mint(t, did(t, issuerDID), aud, MaxServiceAuthLifetime, nsid(t, theNSID), priv)
		got, err := Verify(context.Background(), tok, theNSID, audiences(), dir)
		if err != nil {
			t.Errorf("aud %q: Verify: %v", aud, err)
			continue
		}
		if got != issuerDID {
			t.Errorf("aud %q: issuer = %q, want %q", aud, got, issuerDID)
		}
	}
}

func TestRejectsATokenSignedByTheWrongKey(t *testing.T) {
	attacker := mustKey(t)
	real := mustKey(t)
	dir := &fakeDirectory{keys: map[string]atcrypto.PublicKey{issuerDID: pub(t, real)}}

	tok := mint(t, did(t, issuerDID), audiences()[0], time.Hour, nsid(t, theNSID), attacker)

	_, err := Verify(context.Background(), tok, theNSID, audiences(), dir)
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature", err)
	}
}

func TestRejectsAnExpiredToken(t *testing.T) {
	priv := mustKey(t)
	dir := &fakeDirectory{keys: map[string]atcrypto.PublicKey{issuerDID: pub(t, priv)}}

	tok := mint(t, did(t, issuerDID), audiences()[0], -time.Hour, nsid(t, theNSID), priv)

	_, err := Verify(context.Background(), tok, theNSID, audiences(), dir)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("err = %v, want ErrExpired", err)
	}
}

func TestRejectsAForeignLxm(t *testing.T) {
	priv := mustKey(t)
	dir := &fakeDirectory{keys: map[string]atcrypto.PublicKey{issuerDID: pub(t, priv)}}

	tok := mint(t, did(t, issuerDID), audiences()[0], time.Hour, nsid(t, otherNSID), priv)

	_, err := Verify(context.Background(), tok, theNSID, audiences(), dir)
	if !errors.Is(err, ErrBadLxm) {
		t.Fatalf("err = %v, want ErrBadLxm", err)
	}
}

func TestRejectsATokenWithNoLxm(t *testing.T) {
	priv := mustKey(t)
	dir := &fakeDirectory{keys: map[string]atcrypto.PublicKey{issuerDID: pub(t, priv)}}

	tok := mint(t, did(t, issuerDID), audiences()[0], time.Hour, nil, priv)

	_, err := Verify(context.Background(), tok, theNSID, audiences(), dir)
	if !errors.Is(err, ErrBadLxm) {
		t.Fatalf("err = %v, want ErrBadLxm", err)
	}
}

func TestRefusesToVerifyWithoutALxmToRequire(t *testing.T) {
	priv := mustKey(t)
	dir := &fakeDirectory{keys: map[string]atcrypto.PublicKey{issuerDID: pub(t, priv)}}

	tok := mint(t, did(t, issuerDID), audiences()[0], time.Hour, nsid(t, theNSID), priv)

	_, err := Verify(context.Background(), tok, "", audiences(), dir)
	if !errors.Is(err, ErrBadLxm) {
		t.Fatalf("err = %v, want ErrBadLxm", err)
	}
}

func TestRejectsAWrongAudience(t *testing.T) {
	priv := mustKey(t)
	dir := &fakeDirectory{keys: map[string]atcrypto.PublicKey{issuerDID: pub(t, priv)}}

	for _, aud := range []string{
		"did:web:somewhere.else.example",
		"did:web:" + ourHost + "#different_service",
		"did:plc:notours",
	} {
		tok := mint(t, did(t, issuerDID), aud, time.Hour, nsid(t, theNSID), priv)
		_, err := Verify(context.Background(), tok, theNSID, audiences(), dir)
		if !errors.Is(err, ErrBadAudience) {
			t.Errorf("aud %q: err = %v, want ErrBadAudience", aud, err)
		}
	}
}

func TestADidKeyIssuerIsRefusedByName(t *testing.T) {
	priv := mustKey(t)
	dir := &fakeDirectory{keys: map[string]atcrypto.PublicKey{}}

	selfMinted, err := syntax.ParseDID(pub(t, priv).DIDKey())
	if err != nil {
		t.Fatalf("ParseDID(did:key): %v", err)
	}
	tok := mint(t, selfMinted, audiences()[0], time.Hour, nsid(t, theNSID), priv)

	got, err := Verify(context.Background(), tok, theNSID, audiences(), dir)
	if !errors.Is(err, ErrBadIssuer) {
		t.Fatalf("err = %v, want ErrBadIssuer", err)
	}
	if errors.Is(err, ErrNoKey) {
		t.Fatal("a refused method must not be reported as a key that could not be OBTAINED: " +
			"the transport answers that one 503 + Retry-After, which invites a retry that cannot work")
	}
	if errors.Is(err, ErrBadSignature) {
		t.Fatal("a refused method is not a forged signature")
	}
	if got != "" {
		t.Fatalf("a refused issuer returned %q; it must return no identity at all", got)
	}
	if dir.lookups != 0 {
		t.Fatalf("the directory was consulted %d time(s) for a refused method", dir.lookups)
	}
}

func TestPlcAndWebIssuersAreAdmitted(t *testing.T) {
	for _, iss := range []string{issuerDID, "did:web:account.example.com"} {
		priv := mustKey(t)
		dir := &fakeDirectory{keys: map[string]atcrypto.PublicKey{iss: pub(t, priv)}}

		tok := mint(t, did(t, iss), audiences()[0], MaxServiceAuthLifetime, nsid(t, theNSID), priv)

		got, err := Verify(context.Background(), tok, theNSID, audiences(), dir)
		if err != nil {
			t.Fatalf("%s: Verify: %v", iss, err)
		}
		if got != iss {
			t.Fatalf("issuer = %q, want %q", got, iss)
		}
		if dir.lookups == 0 {
			t.Fatalf("%s: resolved with ZERO directory lookups; an admitted method must be resolved, not assumed", iss)
		}
	}
}

func TestUnresolvableIssuerIsNotABadSignature(t *testing.T) {
	priv := mustKey(t)
	dir := &fakeDirectory{keys: map[string]atcrypto.PublicKey{}}

	tok := mint(t, did(t, otherDID), audiences()[0], time.Hour, nsid(t, theNSID), priv)

	_, err := Verify(context.Background(), tok, theNSID, audiences(), dir)
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("err = %v, want ErrNoKey", err)
	}
	if errors.Is(err, ErrBadSignature) {
		t.Fatal("an unresolvable issuer must not be reported as a bad signature")
	}
}

func TestIssuerIsTakenFromTheToken(t *testing.T) {
	priv := mustKey(t)
	dir := &fakeDirectory{keys: map[string]atcrypto.PublicKey{
		issuerDID: pub(t, priv),
		otherDID:  pub(t, priv),
	}}

	tok := mint(t, did(t, otherDID), audiences()[0], MaxServiceAuthLifetime, nsid(t, theNSID), priv)

	got, err := Verify(context.Background(), tok, theNSID, audiences(), dir)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got != otherDID {
		t.Fatalf("issuer = %q, want the token's %q", got, otherDID)
	}
}
