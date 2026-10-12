package revocation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

type fakeDeny struct {
	state DenyState
	err   error
	reads int
}

func (f *fakeDeny) Lookup(_ context.Context, _ string) (DenyState, error) {
	f.reads++
	return f.state, f.err
}

type fakeTenant struct {
	sa      string
	binding string
	creates int
}

func saName(did string) string { return "did-" + did }

func (t *fakeTenant) createOnRead(_ context.Context) error {
	t.creates++
	t.sa = saName("plc:abc")
	if t.binding == "" {
		t.binding = t.sa
	}
	return nil
}

func (t *fakeTenant) armed() bool { return t.sa != "" && t.binding == t.sa }

func TestRevokedDIDIsRefusedAndNothingIsCreated(t *testing.T) {
	ctx := context.Background()
	deny := &fakeDeny{state: DenyGranted}
	tenant := &fakeTenant{}

	if _, err := CreateOnRead(ctx, deny, "plc:abc", tenant.createOnRead); err != nil {
		t.Fatalf("first login refused: %v", err)
	}
	if tenant.creates != 1 {
		t.Fatalf("first login created %d times, want 1", tenant.creates)
	}

	deny.state = DenyRevoked

	if _, err := CreateOnRead(ctx, deny, "plc:abc", tenant.createOnRead); !errors.Is(err, ErrRevoked) {
		t.Fatalf("getKubeconfig after revoke: err = %v, want ErrRevoked", err)
	}
	if tenant.creates != 1 {
		t.Fatalf("the revoked DID reached the create path: creates = %d, want 1", tenant.creates)
	}
	if deny.reads != 2 {
		t.Fatalf("the deny was consulted %d times, want 2 -- it must be read on EVERY call", deny.reads)
	}
}

func TestTheDoorDoesNotRearmAStaleRoleBinding(t *testing.T) {
	ctx := context.Background()
	deny := &fakeDeny{state: DenyGranted}
	tenant := &fakeTenant{}

	if _, err := CreateOnRead(ctx, deny, "plc:abc", tenant.createOnRead); err != nil {
		t.Fatalf("first login refused: %v", err)
	}
	if !tenant.armed() {
		t.Fatal("precondition: the first login left the identity unarmed")
	}
	surviving := tenant.binding

	tenant.sa = ""
	if tenant.armed() {
		t.Fatal("precondition: the identity is still armed with its ServiceAccount deleted, so " +
			"this fixture cannot tell an armed identity from an unarmed one")
	}

	deny.state = DenyRevoked
	if _, err := CreateOnRead(ctx, deny, "plc:abc", tenant.createOnRead); !errors.Is(err, ErrRevoked) {
		t.Fatalf("a revoked DID reopened the door: err = %v, want ErrRevoked", err)
	}
	if tenant.creates != 1 {
		t.Fatalf("creates = %d, want 1: the revoke did not stop the create-on-read", tenant.creates)
	}
	if tenant.armed() {
		t.Fatalf("the identity is armed again after the revoke: sa = %q, binding = %q",
			tenant.sa, tenant.binding)
	}

	recreated := &fakeTenant{binding: surviving}
	if err := recreated.createOnRead(ctx); err != nil {
		t.Fatalf("control: createOnRead: %v", err)
	}
	if !recreated.armed() {
		t.Fatal("control: a recreated ServiceAccount did NOT resume the RoleBinding that outlived " +
			"it, so this fixture cannot represent the hazard the test above is about. RBAC " +
			"subjects are keyed by NAME and the name is DERIVED, which is why ADR 0024 says a " +
			"revocation that only deletes objects re-arms itself on the next login")
	}
}

func TestAdmitRefusesEveryNonAdmittingState(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		deny *fakeDeny
		want error
	}{
		{"record deleted under a live account", &fakeDeny{state: DenyRecordMissing}, ErrDenyRecordMissing},
		{"kcp unreadable", &fakeDeny{err: errors.New("connection refused")}, ErrDenyUnreadable},
		{"unrecognised state", &fakeDeny{state: DenyState("Quarantined")}, ErrDenyUnreadable},
		{"revoked", &fakeDeny{state: DenyRevoked}, ErrRevoked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Admit(ctx, tc.deny, "plc:abc")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if p != nil {
				t.Fatalf("a permit was issued for a refused identity: %q", p.Admitted())
			}
		})
	}
}

func TestAdmitRefusesAnEmptyDID(t *testing.T) {
	if _, err := Admit(context.Background(), &fakeDeny{state: DenyGranted}, ""); !errors.Is(err, ErrNoDID) {
		t.Fatalf("err = %v, want ErrNoDID", err)
	}
}

func TestFanoutCoversAColdClusterAndCoversItOnWake(t *testing.T) {
	cold := ClusterView{Name: "cold", ServiceAccountPresent: true, Cold: true}
	warm := ClusterView{Name: "warm", ServiceAccountPresent: true}
	fresh := ClusterView{Name: "created-after-the-revoke"}

	got := Plan([]ClusterView{cold, warm, fresh})
	want := []Effect{
		{Cluster: "cold", State: v1alpha1.IdentityColdDeferred, DeleteSA: false},
		{Cluster: "warm", State: v1alpha1.IdentityRevocationPending, DeleteSA: true},
		{Cluster: "created-after-the-revoke", State: v1alpha1.IdentityRevoked, DeleteSA: false},
	}
	if len(got) != len(want) {
		t.Fatalf("planned %d clusters, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("plan[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	woken := ClusterView{Name: "cold", ServiceAccountPresent: true}
	if e := Plan([]ClusterView{woken})[0]; e.DeleteSA != true || e.State != v1alpha1.IdentityRevocationPending {
		t.Fatalf("woken cluster: %+v, want a pending delete", e)
	}
	deleted := ClusterView{Name: "cold"}
	if e := Plan([]ClusterView{deleted})[0]; e.State != v1alpha1.IdentityRevoked {
		t.Fatalf("after the delete: %+v, want Revoked", e)
	}
}

func TestReadyRefusesUntilTheReconcileIsSettled(t *testing.T) {
	for _, e := range []Effect{
		{Cluster: "c", State: v1alpha1.IdentityColdDeferred},
		{Cluster: "c", State: v1alpha1.IdentityRevocationPending},
		{Cluster: "c", State: v1alpha1.IdentityActive},
	} {
		if s, err := WakeSettled(e); err == nil {
			t.Fatalf("phase=Ready was allowed with %s outstanding", e.State)
		} else if s != nil {
			t.Fatalf("a refusal returned a Settled proof: %v", s)
		}
	}

	s, err := WakeSettled(Effect{Cluster: "c", State: v1alpha1.IdentityRevoked})
	if err != nil {
		t.Fatalf("a completed reconcile was refused: %v", err)
	}
	phase, err := Ready(s)
	if err != nil || phase != v1alpha1.ClusterReady {
		t.Fatalf("Ready(settled) = %q, %v; want Ready, nil", phase, err)
	}

	phase, err = Ready(nil)
	if err == nil {
		t.Fatal("Ready(nil) succeeded: a wake without the reconcile would serve")
	}
	if phase == v1alpha1.ClusterReady {
		t.Fatalf("Ready(nil) returned %q", phase)
	}
}

func TestSweepRevokesOnAbsenceAndKeylessnessButNeverOnAnOutage(t *testing.T) {
	revoke, why := Sweep(PLCResolution{Err: errors.New("dial tcp: i/o timeout")})
	if revoke {
		t.Fatalf("a PLC outage revoked an identity: %s", why)
	}
	if !strings.Contains(why, "not a deletion") {
		t.Errorf("the reason must say an outage is not a deletion, got %q", why)
	}

	if revoke, _ := Sweep(PLCResolution{Found: false}); !revoke {
		t.Error("a 404 at PLC did not revoke")
	}
	if revoke, _ := Sweep(PLCResolution{Found: true}); !revoke {
		t.Error("a keyless PLC document did not revoke")
	}
	if revoke, why := Sweep(PLCResolution{Found: true, Keys: []string{"#atproto"}}); revoke {
		t.Errorf("a live document was revoked: %s", why)
	}
	if revoke, _ := Sweep(PLCResolution{Found: false, Err: errors.New("dns")}); revoke {
		t.Error("Err must be checked BEFORE Found: a failed fetch revoked")
	}
}

func TestReplyCarriesTheAccountEntryAndPerClusterState(t *testing.T) {
	got := Reply(Plan([]ClusterView{
		{Name: "cold", ServiceAccountPresent: true, Cold: true},
		{Name: "warm", ServiceAccountPresent: true},
	}))
	want := []ReplyEntry{
		{Name: "", State: "RevocationPending"},
		{Name: "cold", State: "ColdDeferred"},
		{Name: "warm", State: "RevocationPending"},
	}
	if len(got) != len(want) {
		t.Fatalf("reply = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("reply[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	if got := Reply(nil); len(got) != 1 || got[0].State != "Revoked" {
		t.Fatalf("reply with no clusters = %+v, want one Revoked account entry", got)
	}
}
