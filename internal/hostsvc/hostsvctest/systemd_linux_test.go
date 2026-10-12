//go:build linux

package hostsvctest

import (
	"context"
	"os"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

func TestMain(m *testing.M) {
	if FDStoreHelper() {
		os.Exit(FDStoreHelperMain())
	}
	os.Exit(m.Run())
}

func TestTheSystemdUserBackendPassesTheContract(t *testing.T) {
	livegate.Short(t, "this suite starts stand-in transient services (sleep, sh) in this account's systemd user manager")
	if _, err := os.Stat(hostsvc.PrivateSocketPath()); err != nil {
		livegate.RequireLocal(t, "the user manager's private socket %s is missing: %v", hostsvc.PrivateSocketPath(), err)
	}
	key := RandomKey()
	Run(t, Harness{
		Open: func(t *testing.T) (hostsvc.Manager, error) {
			return hostsvc.Open(context.Background(), hostsvc.Config{Backend: hostsvc.BackendSystemdUser})
		},
		SkipManagerRestart: "systemd-user: restarting user@<uid> stops every unit of the account and the login session; the lingering-account proof is M2-HOST-CONTRACT",
		Prefix:             "socialweb-test-" + key + "-",
		SlicePrefix:        "socialweb-test" + key,
		FDStoreHelper: func(t *testing.T) ([]string, []string) {
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			return []string{exe, "-test.run=^$"}, nil
		},
	})
}
