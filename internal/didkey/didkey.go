package didkey

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

const Prefix = "did:key:"

const MultibaseBase58BTC = 'z'

var Ed25519PubMulticodec = []byte{0xed, 0x01}

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var (
	ErrNotDidKey = errors.New("didkey: not a did:key")

	ErrNotEd25519 = errors.New("didkey: the multicodec prefix is not ed25519-pub (0xed 0x01)")

	ErrBadBase58 = errors.New("didkey: not base58btc")
)

func FromEd25519(pub ed25519.PublicKey) (string, error) {
	if len(pub) != ed25519.PublicKeySize {
		return "", fmt.Errorf("didkey: an ed25519 public key is %d bytes, this one is %d",
			ed25519.PublicKeySize, len(pub))
	}
	body := append(append([]byte{}, Ed25519PubMulticodec...), pub...)
	return Prefix + string(MultibaseBase58BTC) + EncodeBase58(body), nil
}

func ParseEd25519(did string) (ed25519.PublicKey, error) {
	rest, ok := strings.CutPrefix(did, Prefix)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotDidKey, did)
	}
	if rest == "" || rest[0] != MultibaseBase58BTC {
		return nil, fmt.Errorf("%w: %q is not multibase base58btc (no %q prefix)", ErrNotDidKey, did,
			MultibaseBase58BTC)
	}
	body, err := DecodeBase58(rest[1:])
	if err != nil {
		return nil, err
	}
	if len(body) < len(Ed25519PubMulticodec) || body[0] != Ed25519PubMulticodec[0] || body[1] != Ed25519PubMulticodec[1] {
		return nil, fmt.Errorf("%w: %q", ErrNotEd25519, did)
	}
	pub := body[len(Ed25519PubMulticodec):]
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("didkey: %q carries %d key bytes, an ed25519 public key is %d",
			did, len(pub), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(pub), nil
}

func EndpointID(pub ed25519.PublicKey) (string, error) {
	if len(pub) != ed25519.PublicKeySize {
		return "", fmt.Errorf("didkey: an iroh endpoint id is a %d byte ed25519 public key, this one is %d",
			ed25519.PublicKeySize, len(pub))
	}
	return hex.EncodeToString(pub), nil
}

func EncodeBase58(b []byte) string {
	zeros := 0
	for zeros < len(b) && b[zeros] == 0 {
		zeros++
	}
	n := new(big.Int).SetBytes(b)
	radix := big.NewInt(58)
	mod := new(big.Int)
	var out []byte
	for n.Sign() > 0 {
		n.DivMod(n, radix, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for i := 0; i < zeros; i++ {
		out = append(out, base58Alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func DecodeBase58(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("%w: empty", ErrBadBase58)
	}
	n := new(big.Int)
	radix := big.NewInt(58)
	for _, c := range s {
		i := strings.IndexRune(base58Alphabet, c)
		if i < 0 {
			return nil, fmt.Errorf("%w: %q holds %q", ErrBadBase58, s, c)
		}
		n.Mul(n, radix)
		n.Add(n, big.NewInt(int64(i)))
	}
	zeros := 0
	for zeros < len(s) && s[zeros] == base58Alphabet[0] {
		zeros++
	}
	return append(make([]byte, zeros), n.Bytes()...), nil
}
