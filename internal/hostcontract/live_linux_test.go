//go:build linux

package hostcontract

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[len(os.Args)-1] {
		case ProbeUsernsFlag:
			os.Exit(UsernsChild(os.Stdin, os.Stdout))
		case "-probe-exec":
			fmt.Println("probe exec")
			os.Exit(0)
		case ProbeHoldFlag:
			time.Sleep(time.Minute)
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func self(t *testing.T) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{exe}
}

func TestTheProbeReadsBackEveryRuntimePropertyFromThisUserManager(t *testing.T) {
	livegate.Short(t, "this test starts a socialweb-probe-* transient service in this account's systemd user manager")
	socket := hostsvc.PrivateSocketPath()
	if _, err := os.Stat(socket); err != nil {
		livegate.RequireLocal(t, "the user manager's private socket %s is missing: %v", socket, err)
	}
	f := Facts{Role: RoleGateway}
	gatherProbe(context.Background(), &f, Options{Role: RoleGateway, StateDir: t.TempDir(), Self: self(t)}, socket)
	if f.ProbeErr != "" {
		t.Fatalf("probe: %s", f.ProbeErr)
	}
	if bad := PropertyMismatches(f.ProbeSent, f.ProbeReadBack); len(bad) > 0 {
		t.Fatalf("this user manager did not keep %v", bad)
	}
	if r := fdStore(f); r.Status != StatusOK {
		t.Fatalf("fd store: %+v", r)
	}
	t.Logf("read back %d properties", len(f.ProbeSent))
}
