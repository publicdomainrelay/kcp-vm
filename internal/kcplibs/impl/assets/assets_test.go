package assets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaterialiseWritesAndCaches(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".kcpdns")
	set := &Set{Dir: dir, Files: map[string][]byte{
		"shim.ts":  []byte("export {}"),
		"probe.ts": []byte("Deno.exit(0)"),
	}}
	paths, err := set.Materialise()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths = %v", paths)
	}
	body, err := os.ReadFile(paths["shim.ts"])
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "export {}" {
		t.Fatalf("shim = %q", body)
	}
	if err := os.Remove(paths["shim.ts"]); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Materialise(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths["shim.ts"]); err == nil {
		t.Fatal("a second materialise is cached and must not rewrite")
	}
}

func TestRewriteReplacesTheFilesMaterialiseCached(t *testing.T) {
	dir := t.TempDir()
	set := &Set{Dir: dir, Files: map[string][]byte{"shim.ts": []byte("v1")}}
	paths, err := set.Materialise()
	if err != nil {
		t.Fatal(err)
	}
	set.Files["shim.ts"] = []byte("v2")
	if _, err := set.Materialise(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(paths["shim.ts"])
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "v1" {
		t.Fatalf("a second materialise is cached, so the file still reads %q", body)
	}
	if _, err := set.Rewrite(); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(paths["shim.ts"])
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "v2" {
		t.Fatalf("a rewrite must replace the file, it reads %q", body)
	}
}

func TestPathNamesAMissingFile(t *testing.T) {
	set := &Set{Dir: t.TempDir(), Files: map[string][]byte{"a.ts": []byte("x")}}
	if _, err := set.Path("b.ts"); err == nil {
		t.Fatal("an unknown asset must be an error")
	}
	path, err := set.Path("a.ts")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "a.ts" {
		t.Fatalf("path = %q", path)
	}
}

func TestMaterialiseWithoutADirectory(t *testing.T) {
	set := &Set{Files: map[string][]byte{"a.ts": []byte("x")}}
	if _, err := set.Materialise(); err != ErrNoDirectory {
		t.Fatalf("err = %v", err)
	}
}

func TestAFailedMaterialiseIsRetried(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, ".kcpdns")
	if err := os.WriteFile(dir, []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}
	set := &Set{Dir: dir, Files: map[string][]byte{"shim.ts": []byte("export {}")}}
	if _, err := set.Materialise(); err == nil {
		t.Fatal("a write that cannot create its directory must fail")
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	paths, err := set.Materialise()
	if err != nil {
		t.Fatalf("a retry after the obstacle is gone must succeed: %v", err)
	}
	body, err := os.ReadFile(paths["shim.ts"])
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "export {}" {
		t.Fatalf("shim = %q", body)
	}
}
