package bootstrap

import (
	"errors"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
)

func TestTheBootstrapOrderIsStoreThenSlot(t *testing.T) {
	l, err := devinstance.NewLayout("proof-a", "/s", "/run/user/1000/swc")
	if err != nil {
		t.Fatal(err)
	}
	got := Table(l)
	if len(got) != 2 || got[0].Role != RoleKine || got[1].Role != RoleKcp {
		t.Fatalf("order %+v", got)
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
