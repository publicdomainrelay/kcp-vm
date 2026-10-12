package devinstance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mapEnv map[string]string

func (m mapEnv) LookupEnv(k string) (string, bool) {
	v, ok := m[k]
	return v, ok
}

func worktree(t *testing.T, parent, name string) string {
	t.Helper()
	d := filepath.Join(parent, name)
	if err := os.MkdirAll(filepath.Join(d, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(d, "sub", "dir"), 0o755)
	return d
}

func TestTwoWorktreesGetDistinctInstanceIDsAndShortSocketPaths(t *testing.T) {
	base := t.TempDir()
	a := worktree(t, filepath.Join(base, "one"), "socialweb-computer-kcp")
	b := worktree(t, filepath.Join(base, "two"), "socialweb-computer-kcp")
	ida, err := ID("", filepath.Join(a, "sub", "dir"))
	if err != nil {
		t.Fatal(err)
	}
	idb, err := ID("", b)
	if err != nil {
		t.Fatal(err)
	}
	if ida == idb || !strings.HasPrefix(ida, "socialweb-comput-") {
		t.Fatalf("ids %q and %q", ida, idb)
	}
	la, err := NewLayout(ida, "/home/u/.local/state/socialweb-computer", "/run/user/1000/swc")
	if err != nil {
		t.Fatal(err)
	}
	lb, _ := NewLayout(idb, "/home/u/.local/state/socialweb-computer", "/run/user/1000/swc")
	if la.Socket == lb.Socket || la.StateDir == lb.StateDir || la.KcpUnit() == lb.KcpUnit() {
		t.Fatalf("two instances share a path or unit: %+v %+v", la, lb)
	}
	if len(la.Socket) > MaxSocketPath {
		t.Fatalf("socket %s", la.Socket)
	}
}

func TestTheSocketPathOfTheLongestWorktreeIsUnder108Bytes(t *testing.T) {
	long := "/" + strings.Repeat("d", 200)
	id, err := ID("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l, err := NewLayout(id, long, "/run/user/4294967294/swc")
	if err != nil {
		t.Fatalf("a long state root must not lengthen the socket: %v", err)
	}
	if len(l.Socket) >= 108 {
		t.Fatalf("socket %d bytes", len(l.Socket))
	}
	if _, err := NewLayout(id, "/s", "/"+strings.Repeat("r", 120)); err == nil {
		t.Fatalf("a socket path over the limit was accepted")
	}
}

func TestDevRefusesRoot(t *testing.T) {
	if RefuseRoot(0) == nil || RefuseRoot(1000) != nil {
		t.Fatal("RefuseRoot")
	}
}

func TestANamedInstanceIsValidated(t *testing.T) {
	if _, err := ID("Bad Name", "/"); err == nil {
		t.Fatal("accepted")
	}
	if id, err := ID("proof-a", "/"); err != nil || id != "proof-a" {
		t.Fatal(id, err)
	}
}

func TestTheStateRootFollowsTheTaskThenXDGThenHome(t *testing.T) {
	cases := []struct {
		opt  string
		env  mapEnv
		want string
	}{
		{"/x", mapEnv{"TASK_STATE_DIR": "/t"}, "/x"},
		{"", mapEnv{"TASK_STATE_DIR": "/t", "XDG_STATE_HOME": "/s"}, "/t/socialweb-computer"},
		{"", mapEnv{"XDG_STATE_HOME": "/s", "HOME": "/h"}, "/s/socialweb-computer"},
		{"", mapEnv{"HOME": "/h"}, "/h/.local/state/socialweb-computer"},
	}
	for _, c := range cases {
		got, err := StateRoot(c.opt, c.env)
		if err != nil || got != c.want {
			t.Errorf("%v: %q %v, want %q", c.env, got, err, c.want)
		}
	}
}
