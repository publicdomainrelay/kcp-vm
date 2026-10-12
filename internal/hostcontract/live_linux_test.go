//go:build linux

package hostcontract

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
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

func TestTheEmbeddedPastaCreatesAUserNamespaceHere(t *testing.T) {
	livegate.Short(t, "this test runs pasta, which creates a user and network namespace")
	pasta := os.Getenv("SOCIALWEB_PASTA_PATH")
	if pasta == "" {
		livegate.Require(t, "SOCIALWEB_PASTA_PATH names no pasta executable (a dev build resolves it only by explicit path)")
	}
	f := Facts{Role: RoleBidder}
	gatherPasta(context.Background(), &f, Options{Role: RoleBidder, PastaPath: pasta, Self: self(t)})
	if r := pastaUserns(f); r.Status != StatusOK {
		t.Fatalf("pasta: %+v", r)
	}
}

func TestNewuidmapMapsAThrowawayNamespaceIntoTheFirstVMSubRange(t *testing.T) {
	livegate.Short(t, "this test maps a throwaway user namespace through the setuid newuidmap/newgidmap")
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := os.ReadFile("/etc/subuid")
	sg, _ := os.ReadFile("/etc/subgid")
	uid, _ := strconv.Atoi(u.Uid)
	ur, okU := AccountRange(string(sub), u.Username, uid)
	gr, okG := AccountRange(string(sg), u.Username, uid)
	if !okU || !okG || ur.Count < VMSlotIDs || gr.Count < VMSlotIDs {
		livegate.Require(t, "%s has no subuid/subgid range of %d ids in /etc/subuid and /etc/subgid (install bidder reserves one)", u.Username, VMSlotIDs)
	}
	up, err1 := exec.LookPath("newuidmap")
	gp, err2 := exec.LookPath("newgidmap")
	if err1 != nil || err2 != nil {
		livegate.RequireLocal(t, "newuidmap/newgidmap not on PATH")
	}
	if msg := MapThrowaway(context.Background(), self(t), up, gp, ur.Start, gr.Start); msg != "" {
		t.Fatal(msg)
	}
}
