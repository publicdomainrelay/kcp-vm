package cloudprovider

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

type recorder struct {
	providerIDs map[string]string
	labels      map[string]map[string]string
	removed     map[string][]string
	deleted     []string
}

func (r *recorder) PatchProviderID(_ context.Context, nodeName, providerID string) error {
	if r.providerIDs == nil {
		r.providerIDs = map[string]string{}
	}
	r.providerIDs[nodeName] = providerID
	return nil
}

func (r *recorder) SetLabels(_ context.Context, nodeName string, set map[string]string, remove []string) error {
	if r.labels == nil {
		r.labels = map[string]map[string]string{}
		r.removed = map[string][]string{}
	}
	r.labels[nodeName] = set
	r.removed[nodeName] = remove
	return nil
}

func (r *recorder) DeleteNode(_ context.Context, nodeName string) error {
	r.deleted = append(r.deleted, nodeName)
	return nil
}

type stubInstances struct {
	inst  Instance
	known bool
	err   error
}

func (s *stubInstances) Get(context.Context, string) (Instance, bool, error) {
	return s.inst, s.known, s.err
}

func newTestController(t *testing.T, inst Instances) (*Controller, *recorder) {
	t.Helper()
	rec := &recorder{}
	c, err := New(Options{Cluster: "c1", Instances: inst, Nodes: rec}, fake.NewClientset())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, rec
}

func seedNode(t *testing.T, c *Controller, node *corev1.Node) {
	t.Helper()
	if err := c.informer.GetStore().Add(node); err != nil {
		t.Fatalf("seeding the informer store with %s: %v", node.Name, err)
	}
}

func TestReconcileStampsALiveInstance(t *testing.T) {
	c, rec := newTestController(t, &stubInstances{
		known: true,
		inst:  Instance{NodeName: "c1-node1", Cluster: "c1", InstanceType: "small"},
	})
	seedNode(t, c, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "c1-node1"}})

	if err := c.Reconcile(context.Background(), "c1-node1"); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := rec.providerIDs["c1-node1"]; got != "socialweb://c1/c1-node1" {
		t.Fatalf("providerID patched = %q, want socialweb://c1/c1-node1", got)
	}
	if rec.labels["c1-node1"][LabelCluster] != "c1" {
		t.Fatalf("labels patched = %v, want the cluster label", rec.labels["c1-node1"])
	}
	if len(rec.deleted) != 0 {
		t.Fatalf("deleted = %v, want nothing deleted for a live instance", rec.deleted)
	}
}

func TestReconcileDeletesWhenTheInstanceIsGone(t *testing.T) {
	c, rec := newTestController(t, &stubInstances{known: false})
	seedNode(t, c, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "c1-node1"}})

	if err := c.Reconcile(context.Background(), "c1-node1"); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(rec.deleted) != 1 || rec.deleted[0] != "c1-node1" {
		t.Fatalf("deleted = %v, want [c1-node1]", rec.deleted)
	}
	if len(rec.providerIDs) != 0 {
		t.Fatalf("providerIDs written = %v, want none for a deleted Node", rec.providerIDs)
	}
}

func TestReconcileDeletesWhenTheInstanceIsDraining(t *testing.T) {
	c, rec := newTestController(t, &stubInstances{
		known: true,
		inst:  Instance{NodeName: "c1-node1", Cluster: "c1", Draining: true},
	})
	seedNode(t, c, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "c1-node1"}})

	if err := c.Reconcile(context.Background(), "c1-node1"); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(rec.deleted) != 1 {
		t.Fatalf("deleted = %v, want the draining Node deleted", rec.deleted)
	}
}

func TestAnUnreadableInstanceSourceIsNotAnAbsence(t *testing.T) {
	c, rec := newTestController(t, &stubInstances{err: errors.New("the snapshot is unreadable")})
	seedNode(t, c, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "c1-node1"}})

	err := c.Reconcile(context.Background(), "c1-node1")
	if err == nil {
		t.Fatal("Reconcile over an unreadable instance source returned nil; the caller must " +
			"requeue, and it only requeues on an error")
	}
	if len(rec.deleted) != 0 {
		t.Fatalf("deleted = %v, want nothing: an unreadable source must never be read as \"the "+
			"guest is gone\", because that deletes a Node under its own running work", rec.deleted)
	}
	if len(rec.providerIDs) != 0 || len(rec.labels) != 0 {
		t.Fatalf("writes happened over an unreadable source: providerIDs=%v labels=%v",
			rec.providerIDs, rec.labels)
	}
}

func TestAKnownRecordWithNoClusterIsNotAnAbsence(t *testing.T) {
	c, rec := newTestController(t, &stubInstances{
		known: true,
		inst:  Instance{NodeName: "c1-node1", InstanceType: "small"},
	})
	seedNode(t, c, &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "c1-node1"},
		Spec:       corev1.NodeSpec{ProviderID: "socialweb://previous-life/c1-node1"},
	})

	err := c.Reconcile(context.Background(), "c1-node1")
	if err == nil {
		t.Fatal("Reconcile over a record with no cluster returned nil: the record is MALFORMED, " +
			"not absent, so the caller must requeue and take no decision")
	}
	if len(rec.deleted) != 0 || len(rec.providerIDs) != 0 || len(rec.labels) != 0 {
		t.Fatalf("a malformed record caused writes: deleted=%v providerIDs=%v labels=%v",
			rec.deleted, rec.providerIDs, rec.labels)
	}
}

func TestAKnownRecordWithNoNodeIsNotAnAbsence(t *testing.T) {
	c, rec := newTestController(t, &stubInstances{
		known: true,
		inst:  Instance{Cluster: "c1", InstanceType: "small"},
	})
	seedNode(t, c, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "c1-node1"}})

	err := c.Reconcile(context.Background(), "c1-node1")
	if err == nil {
		t.Fatal("Reconcile over a record with no node returned nil: the record is MALFORMED, " +
			"not absent")
	}
	if len(rec.deleted) != 0 || len(rec.providerIDs) != 0 || len(rec.labels) != 0 {
		t.Fatalf("a malformed record caused writes: deleted=%v providerIDs=%v labels=%v",
			rec.deleted, rec.providerIDs, rec.labels)
	}
}

func TestReconcileSkipsANodeAbsentFromTheCache(t *testing.T) {
	c, rec := newTestController(t, &stubInstances{known: true})
	if err := c.Reconcile(context.Background(), "c1-node1"); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(rec.deleted) != 0 || len(rec.providerIDs) != 0 || len(rec.labels) != 0 {
		t.Fatalf("an absent Node caused writes: deleted=%v providerIDs=%v labels=%v",
			rec.deleted, rec.providerIDs, rec.labels)
	}
}

func TestReconcileDoesNotRewriteACorrectNode(t *testing.T) {
	c, rec := newTestController(t, &stubInstances{
		known: true,
		inst:  Instance{NodeName: "c1-node1", Cluster: "c1", InstanceType: "small"},
	})
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "c1-node1",
			Labels: map[string]string{
				LabelCluster:      "c1",
				LabelInstanceType: "small",
			},
		},
		Spec: corev1.NodeSpec{ProviderID: "socialweb://c1/c1-node1"},
	}
	seedNode(t, c, node)

	if err := c.Reconcile(context.Background(), "c1-node1"); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(rec.providerIDs) != 0 || len(rec.labels) != 0 {
		t.Fatalf("a correct Node was rewritten: providerIDs=%v labels=%v",
			rec.providerIDs, rec.labels)
	}
}
