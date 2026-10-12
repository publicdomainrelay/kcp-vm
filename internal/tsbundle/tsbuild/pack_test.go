package tsbuild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denorun"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tsbundle"
)

func TestAnExtractedTreeWhoseFileChangedIsRefused(t *testing.T) {
	tree := t.TempDir()
	put(t, filepath.Join(tree, "src", "a", "mod.ts"), "console.log(1);\n")
	put(t, filepath.Join(tree, "deno", "remote", "x"), "cached\n")
	archive, m, err := Pack(tree, 1791000000)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := WriteBundle(out, archive, m); err != nil {
		t.Fatal(err)
	}
	src := tsbundle.Embedded{FS: os.DirFS(out), Dir: "."}
	state := writableTempDir(t)
	p, err := src.Prepare(state)
	if err != nil {
		t.Fatal(err)
	}
	if p.Root != filepath.Join(state, "ts", m.TreeSHA256, "src") || p.Key != m.TreeSHA256 || p.Mode != denorun.Production {
		t.Fatalf("prepared %+v", p)
	}
	if b, err := os.ReadFile(filepath.Join(p.DenoDirs, p.Key, "remote", "x")); err != nil || string(b) != "cached\n" {
		t.Fatalf("the DENO_DIR was not seeded from the closure: %q %v", b, err)
	}
	if err := os.WriteFile(filepath.Join(p.Root, "a", "mod.ts"), []byte("x"), 0o644); err == nil {
		t.Fatal("an extracted file is writable")
	}
	target := filepath.Join(p.Root, "a", "mod.ts")
	os.Chmod(filepath.Dir(target), 0o755)
	os.Chmod(target, 0o644)
	if err := os.WriteFile(target, []byte("console.log(2);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = src.Prepare(state)
	if err == nil || !strings.Contains(err.Error(), "src/a/mod.ts") {
		t.Fatalf("a changed extracted file was not refused by name: %v", err)
	}
	tampered := t.TempDir()
	bad := append([]byte{}, archive...)
	bad[len(bad)/2] ^= 0xff
	if err := WriteBundle(tampered, bad, m); err != nil {
		t.Fatal(err)
	}
	if _, err := (tsbundle.Embedded{FS: os.DirFS(tampered), Dir: "."}).Prepare(writableTempDir(t)); err == nil {
		t.Fatal("an archive whose bytes differ from its manifest was extracted")
	}
}
