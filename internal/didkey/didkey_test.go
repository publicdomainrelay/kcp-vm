package didkey

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"testing"
)

const specPublicKeyHex = "3b6a27bcceb6a42d62a3a8d02a6f0d73653215771de243a63ac048a18b59da29"

const specDid = "did:key:z6MkiTBz1ymuepAQ4HEHYSF1H8quG5GLVVQR3djdX3mDooWp"

const dumbpipeSecretHex = "f8555129991792f402f21f6a01b0c027744b0b02c4b5d1f0e1bf979f722991d8"

const dumbpipeEndpointID = "cb6686656591d6e18858e0262a30c92700543f3230d3d483a4976cbfcbfc693c"

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTheDidKeySpecVectorEncodesAnEd25519PublicKey(t *testing.T) {
	got, err := FromEd25519(ed25519.PublicKey(mustHex(t, specPublicKeyHex)))
	if err != nil {
		t.Fatal(err)
	}
	if got != specDid {
		t.Fatalf("FromEd25519(%s) = %s, the did:key spec vector is %s", specPublicKeyHex, got, specDid)
	}
}

func TestTheDidKeySpecVectorDecodesBackToItsPublicKey(t *testing.T) {
	pub, err := ParseEd25519(specDid)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pub, mustHex(t, specPublicKeyHex)) {
		t.Fatalf("ParseEd25519(%s) = %x, want %s", specDid, pub, specPublicKeyHex)
	}
}

func TestTheEndpointIDDumbpipeDerivesFromIrohSecretIsTheSeedsPublicKeyInHex(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(mustHex(t, dumbpipeSecretHex))
	got, err := EndpointID(priv.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if got != dumbpipeEndpointID {
		t.Fatalf("EndpointID = %s, dumbpipe v0.39.0 printed a ticket naming %s for IROH_SECRET=%s",
			got, dumbpipeEndpointID, dumbpipeSecretHex)
	}
}

func TestADidKeyOfAnotherKeyTypeIsRefused(t *testing.T) {
	_, err := ParseEd25519("did:key:zQ3shokFTS3brHcDQrn82RUDfCZESWL1ZdCEJwekUDPQiYBme")
	if !errors.Is(err, ErrNotEd25519) {
		t.Fatalf("a secp256k1 did:key parsed as ed25519: %v", err)
	}
}

func TestNotADidKeyIsRefused(t *testing.T) {
	for _, did := range []string{"did:web:example.com", "did:key:f00", "did:key:z0OIl"} {
		if _, err := ParseEd25519(did); err == nil {
			t.Fatalf("%s parsed", did)
		}
	}
}

func TestBase58KeepsLeadingZeroBytes(t *testing.T) {
	in := []byte{0, 0, 1, 2, 3}
	out, err := DecodeBase58(EncodeBase58(in))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(in, out) {
		t.Fatalf("round trip %x -> %x", in, out)
	}
}
