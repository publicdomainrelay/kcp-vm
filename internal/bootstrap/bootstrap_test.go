package bootstrap

import (
	"errors"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
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
