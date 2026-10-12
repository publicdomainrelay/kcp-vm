package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

func TestTheBootstrapOrderIsStoreFrontSlot(t *testing.T) {
	l, err := devinstance.NewLayout("proof-a", "/s", "/run/user/1000/swc")
	if err != nil {
		t.Fatal(err)
	}
	got := Table(l)
	if len(got) != 3 || got[0].Role != RoleKine || got[1].Role != RoleFront || got[2].Role != RoleKcp {
		t.Fatalf("order %+v", got)
	}
	if got[1].Unit != l.FrontUnit() || got[2].Unit != l.SlotUnit("a") {
		t.Fatalf("units %+v", got)
	}
}

func TestTheDevSlotAdvertisesTheFrontAndRunsLeaderElection(t *testing.T) {
	l, err := devinstance.NewLayout("proof-a", "/s", "/run/user/1000/swc")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Layout: l, KcpPath: "/opt/kcp", KinePath: "/opt/kine", Exe: "/opt/swc"}
	spec, root, err := cfg.slotSpec("b", "http://127.0.0.1:41000", "https://127.0.0.1:45000")
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(spec.Exec, " ")
	for _, want := range []string{"--shard-base-url=https://127.0.0.1:45000", "--enable-leader-election",
		"--etcd-servers=http://127.0.0.1:41000", "--root-directory=" + l.SlotRoot("b")} {
		if !strings.Contains(argv, want) {
			t.Errorf("slot b argv lacks %s: %s", want, argv)
		}
	}
	if root != l.SlotRoot("b") || spec.Name != l.SlotUnit("b") {
		t.Fatalf("root %s unit %s", root, spec.Name)
	}
	front, err := cfg.frontSpec()
	if err != nil {
		t.Fatal(err)
	}
	if front.FDStore != 1 || front.Restart != "always" || !strings.Contains(strings.Join(front.Exec, " "), "--kcp-front-address 127.0.0.1:0") {
		t.Fatalf("front spec %+v", front)
	}
}

func TestOnlyTheMinimalProfileIsBuiltAndTheOthersNameTheirItem(t *testing.T) {
	if err := CheckProfile(ProfileMinimal); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"quick", "bed", "prodlike"} {
		err := CheckProfile(p)
		if !errors.Is(err, ErrUnbuiltProfile) || !strings.Contains(err.Error(), "M2-BOOTSTRAP-LAYER") {
			t.Errorf("%s: %v", p, err)
		}
	}
	if err := CheckProfile("nope"); err == nil || errors.Is(err, ErrUnbuiltProfile) {
		t.Errorf("unknown profile: %v", err)
	}
}

type inPlaceManager struct {
	hostsvc.Manager
	st      hostsvc.UnitStatus
	checked bool
}

func (m *inPlaceManager) Inspect(ctx context.Context, name string) (hostsvc.UnitStatus, error) {
	return m.st, nil
}

func (m *inPlaceManager) Check(ctx context.Context, want hostsvc.Identity) (hostsvc.Observation, error) {
	m.checked = true
	return hostsvc.Observation{Verdict: hostsvc.VerdictLost, Reason: hostsvc.LostIdentityMismatch, Stopped: true}, nil
}

func TestAUnitRestartedInPlaceIsReidentifiedNeverCheckedToDeath(t *testing.T) {
	m := &inPlaceManager{st: hostsvc.UnitStatus{Name: "socialweb-kcp-x-front.service", State: hostsvc.Active, MainPID: 42, InvocationID: "new"}}
	obs, restarted, err := Observe(context.Background(), m, hostsvc.Identity{Unit: "socialweb-kcp-x-front.service", InvocationID: "old", PID: 41})
	if err != nil || !restarted || m.checked || obs.Verdict != hostsvc.VerdictRunning {
		t.Fatalf("restarted=%v checked=%v obs=%+v err=%v: a unit its manager restarted in place must be re-identified, not Checked (Check stops a mismatch)",
			restarted, m.checked, obs, err)
	}
	m.st = hostsvc.UnitStatus{Name: "socialweb-kcp-x-front.service", State: hostsvc.Activating, SubState: "auto-restart", InvocationID: "old"}
	start := time.Now()
	if obs, restarted, _ := Observe(context.Background(), m, hostsvc.Identity{Unit: "socialweb-kcp-x-front.service", InvocationID: "old", PID: 41}); restarted || m.checked || obs.Verdict != hostsvc.VerdictUnknown || time.Since(start) < RestartSettle {
		t.Fatalf("a unit in its manager's auto-restart window was judged: %+v checked=%v", obs, m.checked)
	}
	m.st = hostsvc.UnitStatus{Name: "socialweb-kcp-x-front.service", State: hostsvc.Active, MainPID: 41, InvocationID: "old"}
	if _, restarted, _ := Observe(context.Background(), m, hostsvc.Identity{Unit: "socialweb-kcp-x-front.service", InvocationID: "old", PID: 41}); restarted || !m.checked {
		t.Fatalf("an unchanged invocation must go through Check")
	}
}
