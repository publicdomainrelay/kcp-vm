package identitybundle

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func seedDir(t *testing.T, dir string) {
	t.Helper()
	for i, rel := range Paths {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		body := []byte("entry-" + rel + "-" + string(rune('a'+i%26)))
		if err := os.WriteFile(full, body, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func TestHarvestRefusesAPartialBundle(t *testing.T) {
	dir := t.TempDir()
	seedDir(t, dir)
	if _, err := Harvest(dir); err != nil {
		t.Fatalf("Harvest on a full dir: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "tls/service.key")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	_, err := Harvest(dir)
	if err == nil {
		t.Fatal("Harvest returned a bundle missing service.key -- A SHORT BUNDLE IS MORE " +
			"DANGEROUS THAN NO BUNDLE, because it looks like success")
	}
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("want ErrIncomplete, got %v", err)
	}
}

func TestSeedRoundTripsBytesAndMtimes(t *testing.T) {
	src := t.TempDir()
	seedDir(t, src)
	stamp := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	for _, rel := range Paths {
		full := filepath.Join(src, rel)
		if err := os.Chtimes(full, stamp, stamp); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}
	b, err := Harvest(src)
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}

	dst := t.TempDir()
	if err := b.Seed(dst); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	info, err := os.Stat(filepath.Join(dst, "tls/service.key"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.ModTime().Equal(stamp) {
		t.Fatalf("mtime not restored: got %s want %s -- k3s compares bytes first, but "+
			"when they differ a >=3s newer entry is a FATAL and an older one is silently "+
			"overwritten, so the mtime is load-bearing", info.ModTime(), stamp)
	}
}

func TestVerifyCatchesASingleTamperedByte(t *testing.T) {
	dir := t.TempDir()
	seedDir(t, dir)
	b, err := Harvest(dir)
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	if err := b.Verify(dir); err != nil {
		t.Fatalf("Verify on an untouched dir: %v", err)
	}

	target := filepath.Join(dir, "tls/service.key")
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body[0] ^= 0x01
	if err := os.WriteFile(target, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	err = b.Verify(dir)
	if err == nil {
		t.Fatal("Verify accepted a tampered service.key -- A WRONG service.key FAILS " +
			"SILENTLY AND PERMANENTLY, because k3s never overwrites an existing one, so " +
			"every SA token stops validating with no error. This check is the ONLY guard")
	}
	if !errors.Is(err, ErrMismatch) {
		t.Fatalf("want ErrMismatch, got %v", err)
	}
}

func TestSeedRefusesAShortBundle(t *testing.T) {
	dir := t.TempDir()
	seedDir(t, dir)
	b, err := Harvest(dir)
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	b.Entries = b.Entries[:11]
	if err := b.Seed(t.TempDir()); err == nil {
		t.Fatal("Seed wrote a partial bundle; it must refuse, because that is exactly " +
			"the state that makes k3s regenerate the missing CAs")
	}
}

func TestSumsAreStableAndDistinct(t *testing.T) {
	a := Sum([]byte("one"))
	if a != Sum([]byte("one")) {
		t.Fatal("Sum is not deterministic")
	}
	if a == Sum([]byte("two")) {
		t.Fatal("Sum collided on different input")
	}
	if len(a) != 64 {
		t.Fatalf("want a hex sha256, got %d chars", len(a))
	}
}
