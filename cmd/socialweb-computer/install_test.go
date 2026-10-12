package main

import (
	"os"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
)

func TestInstallRefusesTheProcBackendBeforeAnythingElse(t *testing.T) {
	for _, args := range [][]string{{"install", "gateway", "--host-backend", "proc"}, {"install", "bidder", "--handle", "a.example.com", "--host-backend", "proc", "--dry-run"}} {
		code, _, stderr := runT(t, clicfg.MapEnv{}, "linux", args...)
		if code != exitRefused || !strings.Contains(stderr, "systemd-user backend only") {
			t.Errorf("%v: exit %d, %q", args, code, stderr)
		}
	}
}

func TestInstallAndUninstallRefuseANonRootCaller(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	for _, args := range [][]string{{"install", "gateway"}, {"uninstall", "bidder", "--purge"}} {
		code, _, stderr := runT(t, clicfg.MapEnv{}, "linux", args...)
		if code != exitRefused || !strings.Contains(stderr, "root") {
			t.Errorf("%v: exit %d, %q", args, code, stderr)
		}
	}
}
