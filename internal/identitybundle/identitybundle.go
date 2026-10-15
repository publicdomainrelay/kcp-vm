package identitybundle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var Paths = []string{
	"tls/server-ca.crt",
	"tls/server-ca.key",
	"tls/client-ca.crt",
	"tls/client-ca.key",
	"tls/request-header-ca.crt",
	"tls/request-header-ca.key",
	"tls/etcd/server-ca.crt",
	"tls/etcd/server-ca.key",
	"tls/etcd/peer-ca.crt",
	"tls/etcd/peer-ca.key",
	"tls/service.key",
	"cred/passwd",
	"cred/ipsec.psk",
}

type Entry struct {
	Path    string
	Bytes   []byte
	ModTime time.Time
}

type Bundle struct {
	Entries []Entry
}

var ErrIncomplete = errors.New("identitybundle: incomplete")

var ErrMismatch = errors.New("identitybundle: mismatch")

func Harvest(dataDir string) (*Bundle, error) {
	b := &Bundle{Entries: make([]Entry, 0, len(Paths))}
	for _, rel := range Paths {
		full := filepath.Join(dataDir, rel)
		info, err := os.Stat(full)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrIncomplete, rel, err)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrIncomplete, rel, err)
		}
		b.Entries = append(b.Entries, Entry{Path: rel, Bytes: data, ModTime: info.ModTime()})
	}
	return b, nil
}

func (b *Bundle) Verify(dataDir string) error {
	if len(b.Entries) != len(Paths) {
		return fmt.Errorf("%w: bundle has %d entries, want %d", ErrIncomplete, len(b.Entries), len(Paths))
	}
	for _, e := range b.Entries {
		got, err := os.ReadFile(filepath.Join(dataDir, e.Path))
		if err != nil {
			return fmt.Errorf("%w: %s: %w", ErrMismatch, e.Path, err)
		}
		if !equal(got, e.Bytes) {
			return fmt.Errorf("%w: %s differs (on disk %s, expected %s)",
				ErrMismatch, e.Path, Sum(got), Sum(e.Bytes))
		}
	}
	return nil
}

func (b *Bundle) Seed(dataDir string) error {
	if len(b.Entries) != len(Paths) {
		return fmt.Errorf("%w: bundle has %d entries, want %d", ErrIncomplete, len(b.Entries), len(Paths))
	}
	for _, e := range b.Entries {
		full := filepath.Join(dataDir, e.Path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return fmt.Errorf("identitybundle: mkdir for %s: %w", e.Path, err)
		}
		if err := os.WriteFile(full, e.Bytes, 0o600); err != nil {
			return fmt.Errorf("identitybundle: write %s: %w", e.Path, err)
		}
		if err := os.Chtimes(full, e.ModTime, e.ModTime); err != nil {
			return fmt.Errorf("identitybundle: chtimes %s: %w", e.Path, err)
		}
	}
	return b.Verify(dataDir)
}

func Sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

const ClusterUIDFile = "cluster-uid"

var ErrEarlierCreation = errors.New("identitybundle: identity bundle belongs to an earlier creation of this cluster")

func ReadClusterUID(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ClusterUIDFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("identitybundle: reading %s: %w", ClusterUIDFile, err)
	}
	return string(raw), nil
}

func WriteClusterUID(dir, uid string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("identitybundle: mkdir for %s: %w", ClusterUIDFile, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ClusterUIDFile), []byte(uid), 0o600); err != nil {
		return fmt.Errorf("identitybundle: write %s: %w", ClusterUIDFile, err)
	}
	return nil
}

func BindClusterUID(dir, uid string) (adopted bool, err error) {
	have, err := ReadClusterUID(dir)
	if err != nil {
		return false, err
	}
	switch have {
	case uid:
		return false, nil
	case "":
		return true, WriteClusterUID(dir, uid)
	}
	return false, fmt.Errorf("%w: it is bound to cluster uid %s and this creation is %s", ErrEarlierCreation, have, uid)
}

func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
