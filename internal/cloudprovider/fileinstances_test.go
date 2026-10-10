package cloudprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAMissingInstanceFileIsAnErrorNotAnAbsence(t *testing.T) {
	f := NewFileInstances(filepath.Join(t.TempDir(), "nodes.json"))
	inst, known, err := f.Get(context.Background(), "c1-node1")
	if err == nil {
		t.Fatalf("Get on a missing file = (known=%v, %+v, nil), want an error: the provider writes "+
			"the snapshot on every provision, teardown and reap and once at startup, so a missing "+
			"file is a state dir removed under a running controller, and reading it as \"no "+
			"instance\" deletes every Node of the cluster", known, inst)
	}
	if !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("Get on a missing file = %v, want it to wrap ErrNoSnapshot", err)
	}
	if known {
		t.Fatal("Get on a missing file reported the instance as known")
	}
}

func TestAMalformedInstanceFileIsAnErrorNotAnAbsence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, known, err := NewFileInstances(path).Get(context.Background(), "c1-node1")
	if err == nil {
		t.Fatalf("Get on a malformed file returned known=%v and no error; an unparseable "+
			"snapshot must never read as \"the guest is gone\"", known)
	}
	if known {
		t.Fatal("Get on a malformed file reported the instance as known")
	}
}

func TestAPresentInstanceIsReturned(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.json")
	body := `[{"nodeName":"c1-node1","cluster":"c1","addresses":["10.0.0.5"],` +
		`"instanceType":"small","draining":false},` +
		`{"nodeName":"c1-node2","cluster":"c1","instanceType":"large","draining":true}]`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f := NewFileInstances(path)

	inst, known, err := f.Get(context.Background(), "c1-node2")
	if err != nil || !known {
		t.Fatalf("Get(c1-node2) = (%+v, %v, %v), want a known instance", inst, known, err)
	}
	if inst.InstanceType != "large" || !inst.Draining || !inst.Exists {
		t.Fatalf("Get(c1-node2) = %+v, want instanceType=large, draining=true, exists=true", inst)
	}

	if _, known, err := f.Get(context.Background(), "c1-node9"); err != nil || known {
		t.Fatalf("Get(c1-node9) = (known=%v, err=%v), want absent and no error", known, err)
	}
}
