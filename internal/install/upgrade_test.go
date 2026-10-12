package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostcontract"
)

type upgradeBed struct {
	o        UpgradeOptions
	restarts [][]string
	checks   []string
}

func newUpgradeBed(t *testing.T) (*upgradeBed, string) {
	t.Helper()
	sys, root := newFakeHost(t, false)
	if _, err := Install(context.Background(), opts(t, root, hostcontract.RoleBidder, "alice.example.com"), sys); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "var/lib/socialweb-computer/swc-bidder")
	b := &upgradeBed{}
	b.o = UpgradeOptions{StateDir: state, Home: state,
		SelfCheck: func(ctx context.Context, exe string, role hostcontract.Role, stateDir string) error {
			b.checks = append(b.checks, exe+" "+string(role))
			return nil
		},
		Restart: func(ctx context.Context, units []string) error {
			b.restarts = append(b.restarts, units)
			return nil
		}}
	return b, readTarget(filepath.Join(state, "bin/current"))
}

func digestOf(t *testing.T, p string) string {
	t.Helper()
	s, err := fileSHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestUpgradeRefusesABinaryWhoseManifestDoesNotMatch(t *testing.T) {
	b, before := newUpgradeBed(t)
	b.o.Binary = fakeBinary(t, "core v2")
	b.o.SHA256 = digestOf(t, fakeBinary(t, "core v2 tampered"))
	if err := Upgrade(context.Background(), b.o); !errors.Is(err, ErrManifestMismatch) {
		t.Fatalf("a mismatched binary: %v", err)
	}
	if got := readTarget(filepath.Join(b.o.StateDir, "bin/current")); got != before || len(b.restarts) != 0 || len(b.checks) != 0 {
		t.Fatalf("a refused upgrade moved current to %s, restarted %v, checked %v", got, b.restarts, b.checks)
	}
}

func TestUpgradeRefusesABinaryWhoseSelfCheckFails(t *testing.T) {
	b, before := newUpgradeBed(t)
	b.o.Binary = fakeBinary(t, "core v2")
	b.o.SHA256 = digestOf(t, b.o.Binary)
	b.o.SelfCheck = func(ctx context.Context, exe string, role hostcontract.Role, stateDir string) error {
		return errors.New("check-host: dev-kvm")
	}
	if err := Upgrade(context.Background(), b.o); !errors.Is(err, ErrSelfCheck) {
		t.Fatalf("a failing self-check: %v", err)
	}
	if got := readTarget(filepath.Join(b.o.StateDir, "bin/current")); got != before || len(b.restarts) != 0 {
		t.Fatalf("current %s, restarts %v", got, b.restarts)
	}
}

func TestUpgradeThenRollbackRetargetsCurrentToPrevious(t *testing.T) {
	b, v1 := newUpgradeBed(t)
	b.o.Binary = fakeBinary(t, "core v2")
	b.o.SHA256 = digestOf(t, b.o.Binary)
	if err := Upgrade(context.Background(), b.o); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(b.o.StateDir, "bin")
	if readTarget(filepath.Join(bin, "current")) != b.o.SHA256 || readTarget(filepath.Join(bin, "previous")) != v1 {
		t.Fatalf("after upgrade: current %s previous %s", readTarget(filepath.Join(bin, "current")), readTarget(filepath.Join(bin, "previous")))
	}
	if len(b.checks) != 1 || len(b.restarts) != 1 || b.restarts[0][0] != "socialweb-computer-bidder@alice.example.com.service" {
		t.Fatalf("checks %v restarts %v", b.checks, b.restarts)
	}
	if err := Upgrade(context.Background(), b.o); err != nil || len(b.restarts) != 1 {
		t.Fatalf("a repeated upgrade to the same binary: %v, restarts %v", err, b.restarts)
	}
	if err := Rollback(context.Background(), b.o); err != nil {
		t.Fatal(err)
	}
	if readTarget(filepath.Join(bin, "current")) != v1 || readTarget(filepath.Join(bin, "previous")) != b.o.SHA256 || len(b.restarts) != 2 {
		t.Fatalf("after rollback: current %s previous %s restarts %v", readTarget(filepath.Join(bin, "current")), readTarget(filepath.Join(bin, "previous")), b.restarts)
	}
	if _, err := os.Stat(filepath.Join(bin, v1, CoreName)); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackWithoutAPreviousBinaryIsRefused(t *testing.T) {
	b, before := newUpgradeBed(t)
	if err := Rollback(context.Background(), b.o); !errors.Is(err, ErrNoPrevious) {
		t.Fatalf("rollback with no previous: %v", err)
	}
	if readTarget(filepath.Join(b.o.StateDir, "bin/current")) != before {
		t.Fatal("a refused rollback moved current")
	}
}
