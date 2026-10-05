package cloudprovider

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTheSnapshotWriterWritesAnEmptyArrayAndNotAnAbsentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	if err := WriteSnapshot(path, nil); err != nil {
		t.Fatalf("WriteSnapshot(empty): %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the snapshot for an empty node set is ABSENT: %v. The provider must always write "+
			"the file, because its absence and an empty list are both read as \"no instance\", and a "+
			"provider whose write is skipped leaves the cloud controller manager unable to tell a "+
			"cluster with no nodes from one whose snapshot never ran", err)
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

func TestAbsentAndEmptySnapshotsReadTheSameAndMalformedIsRefused(t *testing.T) {
	dir := t.TempDir()
	absent := filepath.Join(dir, "absent.json")
	_, known, err := NewFileInstances(absent).Get(context.Background(), "c1-node1")
	if err != nil || known {
		t.Fatalf("an absent snapshot = (known=%v, err=%v), want (false, nil): fileinstances.go "+
			"documents an absent file as an absent instance and NOT as an unreadable source, so both "+
			"the absent file and the empty array are the delete path", known, err)
	}

	empty := filepath.Join(dir, "nodes.json")
	if err := WriteSnapshot(empty, nil); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	_, known, err = NewFileInstances(empty).Get(context.Background(), "c1-node1")
	if err != nil || known {
		t.Fatalf("an empty-array snapshot = (known=%v, err=%v), want (false, nil), the same as an "+
			"absent file -- so writing the array is what keeps the file present, and presence is not "+
			"itself a claim that the node still exists", known, err)
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
