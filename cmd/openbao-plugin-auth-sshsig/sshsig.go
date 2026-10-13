package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

const (
	sshsigMagic      = "SSHSIG"
	sshsigVersion    = 1
	sshsigArmorBegin = "-----BEGIN SSH SIGNATURE-----"
	sshsigArmorEnd   = "-----END SSH SIGNATURE-----"
)

type sshsigEnvelope struct {
	Version       uint32
	PublicKey     []byte
	Namespace     string
	Reserved      string
	HashAlgorithm string
	Signature     []byte
}

type sshsigSignedData struct {
	Namespace     string
	Reserved      string
	HashAlgorithm string
	Hash          []byte
}

func parseEd25519AuthorizedKey(text string) (ssh.PublicKey, error) {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(text)))
	if err != nil {
		return nil, fmt.Errorf("host key: %w", err)
	}
	if key.Type() != ssh.KeyAlgoED25519 {
		return nil, fmt.Errorf("host key type %s: only %s is accepted", key.Type(), ssh.KeyAlgoED25519)
	}
	return key, nil
}

func dearmorSSHSig(armored string) ([]byte, error) {
	text := strings.TrimSpace(armored)
	if !strings.HasPrefix(text, sshsigArmorBegin) || !strings.HasSuffix(text, sshsigArmorEnd) {
		return nil, errors.New("signature: not an armored SSH SIGNATURE")
	}
	body := strings.Join(strings.Fields(text[len(sshsigArmorBegin):len(text)-len(sshsigArmorEnd)]), "")
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("signature: %w", err)
	}
	return raw, nil
}

func verifySSHSig(armored string, namespace string, message []byte) (ssh.PublicKey, error) {
	raw, err := dearmorSSHSig(armored)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(raw, []byte(sshsigMagic)) {
		return nil, errors.New("signature: missing SSHSIG magic")
	}
	var env sshsigEnvelope
	if err := ssh.Unmarshal(raw[len(sshsigMagic):], &env); err != nil {
		return nil, fmt.Errorf("signature: %w", err)
	}
	if env.Version != sshsigVersion {
		return nil, fmt.Errorf("signature: version %d, want %d", env.Version, sshsigVersion)
	}
	if env.Namespace != namespace {
		return nil, fmt.Errorf("signature: namespace %q, want %q", env.Namespace, namespace)
	}
	signer, err := ssh.ParsePublicKey(env.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("signature: public key: %w", err)
	}
	if signer.Type() != ssh.KeyAlgoED25519 {
		return nil, fmt.Errorf("signature: key type %s: only %s is accepted", signer.Type(), ssh.KeyAlgoED25519)
	}
	var digest []byte
	switch env.HashAlgorithm {
	case "sha512":
		sum := sha512.Sum512(message)
		digest = sum[:]
	case "sha256":
		sum := sha256.Sum256(message)
		digest = sum[:]
	default:
		return nil, fmt.Errorf("signature: hash algorithm %q", env.HashAlgorithm)
	}
	var sig ssh.Signature
	if err := ssh.Unmarshal(env.Signature, &sig); err != nil {
		return nil, fmt.Errorf("signature: blob: %w", err)
	}
	if sig.Format != ssh.KeyAlgoED25519 || len(sig.Rest) != 0 {
		return nil, fmt.Errorf("signature: format %s: only %s is accepted", sig.Format, ssh.KeyAlgoED25519)
	}
	signed := append([]byte(sshsigMagic), ssh.Marshal(sshsigSignedData{
		Namespace:     env.Namespace,
		Reserved:      env.Reserved,
		HashAlgorithm: env.HashAlgorithm,
		Hash:          digest,
	})...)
	if err := signer.Verify(signed, &sig); err != nil {
		return nil, fmt.Errorf("signature: %w", err)
	}
	return signer, nil
}

func sameKey(a, b ssh.PublicKey) bool {
	return bytes.Equal(a.Marshal(), b.Marshal())
}
