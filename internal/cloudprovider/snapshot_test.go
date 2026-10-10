package cloudprovider

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestTheSnapshotWriterWritesAnEmptyArrayAndNotAnAbsentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	if err := WriteSnapshot(path, nil); err != nil {
		t.Fatalf("WriteSnapshot(empty): %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the snapshot for an empty node set is ABSENT: %v. The provider must always write "+
			"the file, because an absent snapshot makes the cloud controller manager refuse to judge "+
			"any Node, so a provider whose write is skipped leaves a cluster with no guests holding "+
			"Nodes nothing removes", err)
	}
	if string(raw) != "[]" {
		t.Fatalf("the snapshot for an empty node set is %q, want an empty JSON array", string(raw))
	}
}

func TestTheSnapshotWriterRoundTripsANonEmptySet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	want := []Instance{
		{NodeName: "c1-node1", Cluster: "c1", Exists: true},
		{NodeName: "c1-node2", Cluster: "c1", InstanceType: "large", Exists: true},
	}
	if err := WriteSnapshot(path, want); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	f := NewFileInstances(path)
	for _, w := range want {
		got, known, err := f.Get(context.Background(), w.NodeName)
		if err != nil || !known {
			t.Fatalf("Get(%s) = (%+v, %v, %v), want a known instance", w.NodeName, got, known, err)
		}
		if got.Cluster != w.Cluster || got.InstanceType != w.InstanceType || !got.Exists {
			t.Fatalf("Get(%s) = %+v, want cluster=%q instanceType=%q exists=true",
				w.NodeName, got, w.Cluster, w.InstanceType)
		}
	}
}

func TestAnAbsentSnapshotIsRefusedAndAnEmptyOneIsEmpty(t *testing.T) {
	dir := t.TempDir()
	absent := filepath.Join(dir, "absent.json")
	_, known, err := NewFileInstances(absent).Get(context.Background(), "c1-node1")
	if err == nil || known {
		t.Fatalf("an absent snapshot = (known=%v, err=%v), want an error: absent is not empty, "+
			"and only the empty array the provider writes may mean that no guest is behind a Node",
			known, err)
	}

	empty := filepath.Join(dir, "nodes.json")
	if err := WriteSnapshot(empty, nil); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	_, known, err = NewFileInstances(empty).Get(context.Background(), "c1-node1")
	if err != nil || known {
		t.Fatalf("an empty-array snapshot = (known=%v, err=%v), want (false, nil): a present, "+
			"empty snapshot is the provider saying the cluster has no guest", known, err)
	}

	malformed := filepath.Join(dir, "malformed.json")
	if err := os.WriteFile(malformed, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, known, err := NewFileInstances(malformed).Get(context.Background(), "c1-node1"); err == nil {
		t.Fatalf("an unparseable snapshot returned known=%v and no error; an unreadable source is "+
			"NOT an absent instance and must never become the delete path", known)
	}
}

func TestTheControllerDeletesNoNodeOverAnAbsentSnapshotAndDeletesOverAnEmptyOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.json")
	c, rec := newTestController(t, NewFileInstances(path))
	seedNode(t, c, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "c1-node1"}})

	if err := c.Reconcile(context.Background(), "c1-node1"); err == nil {
		t.Fatal("Reconcile over an absent snapshot returned nil; it must return an error so the " +
			"Node is requeued rather than judged")
	}
	if len(rec.deleted) != 0 {
		t.Fatalf("deleted = %v over an absent snapshot, want nothing: the controller fails closed "+
			"when the instance snapshot is missing", rec.deleted)
	}

	if err := WriteSnapshot(path, nil); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	if err := c.Reconcile(context.Background(), "c1-node1"); err != nil {
		t.Fatalf("Reconcile over an empty snapshot: %v", err)
	}
	if len(rec.deleted) != 1 || rec.deleted[0] != "c1-node1" {
		t.Fatalf("deleted = %v over an empty snapshot, want [c1-node1]", rec.deleted)
	}
}
