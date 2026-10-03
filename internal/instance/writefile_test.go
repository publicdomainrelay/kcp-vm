package instance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheFileCarriesTheTokenAndNotTheRedaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	tok, err := New(strings.NewReader(strings.Repeat("A", TokenBytes)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := WriteFile(path, tok); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if string(got) == Redacted {
		t.Fatalf("the file contains %q: the redaction was written instead of the credential, so "+
			"k3s would hold a token its bootstrap row is not keyed with", Redacted)
	}
	if string(got) != string(tok) {
		t.Fatalf("the file holds %d bytes, want the token's %d", len(got), len(tok))
	}
	if strings.Contains(string(got), Redacted) {
		t.Fatalf("the file contains the redaction string: %q", got)
	}
}

func TestTheModeIsSetAtOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	tok, err := New(strings.NewReader(strings.Repeat("B", TokenBytes)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := WriteFile(path, tok); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 600: this file holds the credential k3s derives its bootstrap "+
			"storage key from", perm)
	}
}

func TestReMaterialisationOverwritesThePreviousToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	first, err := New(strings.NewReader(strings.Repeat("C", TokenBytes)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	second, err := New(strings.NewReader(strings.Repeat("D", TokenBytes)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := WriteFile(path, first); err != nil {
		t.Fatalf("WriteFile first: %v", err)
	}
	if err := WriteFile(path, second); err != nil {
		t.Fatalf("WriteFile second refused to overwrite: %v -- a re-materialisation must rotate "+
			"the token, which is the whole point of ADR 0016", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if string(got) != string(second) {
		t.Fatalf("the file holds the FIRST token: rotation did not take")
	}
	if strings.Contains(string(got), string(first)) {
		t.Fatal("the file is a mixture of two tokens")
	}
}

func TestWriteFileRefusesAnEmptyToken(t *testing.T) {
	if err := WriteFile(filepath.Join(t.TempDir(), "token"), ""); !errors.Is(err, ErrEmptyToken) {
		t.Fatalf("WriteFile(\"\") = %v, want ErrEmptyToken -- an empty token is a credential "+
			"nobody generated, and k3s would derive its storage key from nothing", err)
	}
}
