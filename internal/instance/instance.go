package instance

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

type Token string

const Redacted = "[redacted]"

func (Token) String() string   { return Redacted }
func (Token) GoString() string { return Redacted }

const TokenBytes = 32

func New(r io.Reader) (Token, error) {
	if r == nil {
		r = rand.Reader
	}
	buf := make([]byte, TokenBytes)
	if _, err := r.Read(buf); err != nil {
		return "", fmt.Errorf("instance: mint token: %w", err)
	}
	return Token("k10" + hex.EncodeToString(buf)), nil
}

var ErrEmptyToken = errors.New("instance: empty token")

type Plan struct {
	Token Token

	DeleteBootstrapRow bool

	ReSeedBundle bool

	Reason string
}

func PlanFor(token Token, datastoreHasRow bool) (Plan, error) {
	if token == "" {
		return Plan{}, ErrEmptyToken
	}
	if !datastoreHasRow {
		return Plan{
			Token:              token,
			DeleteBootstrapRow: false,
			ReSeedBundle:       false,
			Reason: "no row to reconcile against; k3s authors the bundle itself on a fresh " +
				"datastore, and there is nothing for it to disagree with",
		}, nil
	}
	return Plan{
		Token:              token,
		DeleteBootstrapRow: true,
		ReSeedBundle:       true,
		Reason: "the row is encrypted with the PREVIOUS INSTANCE's token, so k3s would exit " +
			"with 'bootstrap data already found and encrypted with different token' -- and " +
			"genUsers would then force cred/passwd to the new token AFTER the reconcile, " +
			"making the NEXT start fatal as well. Deleting the row is what makes the " +
			"rotation possible, and having no row to reconcile against is what makes " +
			"pre-seeding the 13 entries safe",
	}, nil
}

func WriteFile(path string, t Token) error {
	if t == "" {
		return ErrEmptyToken
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("instance: opening %s: %w", path, err)
	}
	if _, err := f.WriteString(string(t)); err != nil {
		f.Close()
		return fmt.Errorf("instance: writing %s: %w", path, err)
	}
	return f.Close()
}
