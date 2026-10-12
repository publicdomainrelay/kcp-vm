//go:build release

package assets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEveryEmbeddedAssetExtractsAndVerifies(t *testing.T) {
	if err := Ready(); err != nil {
		t.Fatal(err)
	}
	pins, err := platformPins(platform)
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(t.TempDir(), "runtime")
	for _, p := range pins {
		for exe := range p.Executables {
			e, err := Resolve(Options{Cache: cache}, exe)
			if err != nil {
				t.Fatalf("%s/%s: %v", p.Name, exe, err)
			}
			st, err := os.Stat(e.Path)
			if err != nil || st.Mode().Perm()&0o111 == 0 {
				t.Fatalf("%s/%s at %s is not an executable file: %v", p.Name, exe, e.Path, err)
			}
			if e.SHA256 != p.TreeSHA256 || filepath.Base(filepath.Dir(e.Path)) == "" {
				t.Fatalf("%s/%s resolved %+v", p.Name, exe, e)
			}
			t.Logf("%s %s %s -> %s", p.Name, p.Version, exe, e.Path)
		}
		got, err := TreeDigest(filepath.Join(cache, p.TreeSHA256))
		if err != nil || got != p.TreeSHA256 {
			t.Fatalf("%s: extracted tree %s, pin %s (%v)", p.Name, got, p.TreeSHA256, err)
		}
	}
}
