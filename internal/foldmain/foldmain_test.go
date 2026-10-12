package foldmain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveFollowsBinCurrentToTheContentAddressedPath(t *testing.T) {
	root := t.TempDir()
	sha := filepath.Join(root, "bin", "0123abcd")
	if err := os.MkdirAll(sha, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(sha, "socialweb-computer")
	if err := os.WriteFile(exe, []byte("#!/bin/true\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("0123abcd"), filepath.Join(root, "bin", "current")); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(filepath.Join(root, "bin", "current", "socialweb-computer"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(exe)
	if got != want {
		t.Fatalf("Resolve = %s, want the bin/<sha>/ path %s: a member started through bin/current would change code on the next upgrade", got, want)
	}
}

func TestAStandaloneBinaryHasNoReexec(t *testing.T) {
	inv := Invocation{Exe: "/x/socialweb-clusterprovider"}
	if got := inv.Reexec("internal", "cloud-controller-manager"); got != nil {
		t.Fatalf("a standalone binary re-execs as %v; only the multi-call binary carries the internal verbs", got)
	}
	inv = Invocation{Exe: "/s/bin/ab/socialweb-computer", Verb: []string{"serve", "provider"}}
	got := inv.Reexec("internal", "cloud-controller-manager")
	if len(got) != 3 || got[0] != inv.Exe || got[1] != "internal" || got[2] != "cloud-controller-manager" {
		t.Fatalf("Reexec = %v", got)
	}
	if self := inv.Self(); len(self) != 3 || self[1] != "serve" {
		t.Fatalf("Self = %v", self)
	}
}
