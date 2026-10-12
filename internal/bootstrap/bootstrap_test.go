package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

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
