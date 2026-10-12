//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devsup"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc/hostsvctest"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tsbundle"
)

type liveDev struct {
	bin    string
	state  string
	kcp    string
	kine   string
	deno   string
	source string
}

func openLiveDev(t *testing.T) *liveDev {
	t.Helper()
	livegate.Short(t, "this test runs real kcp and kine processes under devsup (two dev instances, ~1 GB, ~20 s)")
	d := &liveDev{kcp: testExe("SOCIALWEB_KCP_PATH", "kcp"), kine: testExe("SOCIALWEB_KINE_PATH", "kine"), deno: testExe("SOCIALWEB_DENO_PATH", "deno")}
	for name, p := range map[string]string{"kcp": d.kcp, "kine": d.kine, "deno": d.deno} {
		if p == "" {
			livegate.RequireLocal(t, "no %s executable here (its SOCIALWEB_*_PATH env or PATH)", name)
			return nil
		}
	}
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		livegate.RequireLocal(t, "XDG_RUNTIME_DIR is unset")
		return nil
	}
	source, err := tsbundle.FindCheckout(".")
	if err != nil {
		livegate.RequireLocal(t, "no socialweb-computer-kcp checkout above this package: %v", err)
		return nil
	}
	d.source = source
	dir := t.TempDir()
	d.bin = filepath.Join(dir, "socialweb-computer")
	if out, err := exec.Command("go", "build", "-o", d.bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	d.state = filepath.Join(dir, "state")
	return d
}

func testExe(env, name string) string {
	if p := os.Getenv(env); p != "" {
		return p
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	abs, _ := filepath.Abs(p)
	return abs
}

func (d *liveDev) runE(out any, args ...string) error {
	cmd := exec.Command(d.bin, append(args, "--state-dir", d.state)...)
	cmd.Env = append(os.Environ(), "SOCIALWEB_KCP_PATH="+d.kcp, "SOCIALWEB_KINE_PATH="+d.kine, "SOCIALWEB_DENO_PATH="+d.deno, tsbundle.SourceRootEnv+"="+d.source)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v: %v\n%s", args, err, stderr.String())
	}
	if out != nil {
		if err := json.Unmarshal(stdout.Bytes(), out); err != nil {
			return fmt.Errorf("%v: %v\n%s", args, err, stdout.String())
		}
	}
	return nil
}

func (d *liveDev) run(t *testing.T, out any, args ...string) {
	t.Helper()
	if err := d.runE(out, args...); err != nil {
		t.Fatal(err)
	}
}

func (d *liveDev) up(t *testing.T, id string) upReport {
	var r upReport
	d.run(t, &r, "dev", "up", "--instance", id, "--profile", "quick")
	return r
}

func (d *liveDev) down(t *testing.T, id string) downReport {
	var r downReport
	d.run(t, &r, "dev", "down", "--instance", id)
	return r
}

func invocations(r upReport) map[string]string {
	out := map[string]string{}
	for _, u := range r.Units {
		out[u.Role] = u.Identity.InvocationID
	}
	return out
}

func TestTwoDevInstancesEachServeTheirOwnKcpOnPortZero(t *testing.T) {
	d := openLiveDev(t)
	if d == nil {
		return
	}
	key := hostsvctest.RandomKey()
	ids := []string{"t" + key + "-a", "t" + key + "-b"}
	t.Cleanup(func() {
		for _, id := range ids {
			exec.Command(d.bin, "dev", "down", "--instance", id, "--state-dir", d.state).Run()
		}
	})
	reports := make([]upReport, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = d.runE(&reports[i], "dev", "up", "--instance", id, "--profile", "quick")
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	a, b := reports[0], reports[1]
	seen := map[string]string{}
	for _, r := range reports {
		for k, v := range map[string]string{"socket": r.Socket, "state": r.StateDir, "kcp": r.KcpURL, "kine": r.KineURL,
			"kubeconfig": r.AdminKubeconfig} {
			if prev, dup := seen[v]; dup {
				t.Fatalf("%s %s is shared with %s", k, v, prev)
			}
			seen[v] = r.Instance + " " + k
		}
		t.Logf("reading %s: %s kcp %s kine %s seconds %.1f", time.Now().UTC().Format(time.RFC3339), r.Instance, r.KcpURL, r.KineURL, r.Seconds)
	}
	beforeB := invocations(b)

	t.Run("ACoreRestartUnderDevAdoptsEveryUnit", func(t *testing.T) {
		core := a.Core
		if err := syscall.Kill(core.PID, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		var next devinstance.Core
		deadline := time.Now().Add(30 * time.Second)
		for {
			devinstance.ReadJSON(filepath.Join(a.StateDir, "core.json"), &next)
			if next.Generation > core.Generation && !next.AdoptedAt.IsZero() {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("no new core generation after kill -9 of %d", core.PID)
			}
			time.Sleep(100 * time.Millisecond)
		}
		before := invocations(a)
		for _, ad := range next.Adoptions {
			if !ad.Adopted || ad.InvocationID != before[ad.Role] {
				t.Fatalf("core generation %d re-created %s: %+v, before %s", next.Generation, ad.Role, ad, before[ad.Role])
			}
		}
	})

	t.Run("ASecondDevUpOfOneInstanceAdoptsItsDevsup", func(t *testing.T) {
		again := d.up(t, a.Instance)
		if !again.Devsup.Adopted || again.Devsup.PID != a.Devsup.PID {
			t.Fatalf("devsup %+v, first %+v", again.Devsup, a.Devsup)
		}
		for role, inv := range invocations(again) {
			if inv != invocations(a)[role] {
				t.Fatalf("%s re-created by a second dev up", role)
			}
		}
	})

	t.Run("DevDownLeavesNoProcess", func(t *testing.T) {
		pids := []int{a.Devsup.PID, a.Core.PID}
		for _, u := range a.Units {
			pids = append(pids, u.Identity.PID)
		}
		r := d.down(t, a.Instance)
		if !r.DevsupStopped || len(r.Remaining) > 0 || !r.RemovedState || !r.RemovedRuntime {
			t.Fatalf("down %+v", r)
		}
		for _, p := range pids {
			if devsup.Alive(p) {
				t.Fatalf("pid %d outlived dev down", p)
			}
		}
		if left := devsup.InstanceProcesses(a.Instance); len(left) > 0 {
			t.Fatalf("processes of %s left: %v", a.Instance, left)
		}
		for _, p := range []string{a.StateDir, filepath.Dir(a.Socket)} {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatalf("%s left behind: %v", p, err)
			}
		}
		var st statusReport
		cmd := exec.Command(d.bin, "dev", "status", "--instance", b.Instance, "--state-dir", d.state)
		out, _ := cmd.Output()
		json.Unmarshal(out, &st)
		after := map[string]string{}
		for _, u := range st.Devsup.Units {
			after[u.Name] = u.InvocationID
		}
		for _, u := range b.Units {
			if after[u.Unit] != beforeB[u.Role] || u.Identity.Backend != hostsvc.BackendProc {
				t.Fatalf("stopping %s touched %s: %s -> %s", a.Instance, u.Unit, beforeB[u.Role], after[u.Unit])
			}
		}
		rb := d.down(t, b.Instance)
		if !rb.DevsupStopped || len(rb.Remaining) > 0 {
			t.Fatalf("down %+v", rb)
		}
		if strings.Contains(strings.Join(os.Environ(), " "), "SWC_INSTANCE") {
			t.Fatalf("the test process carries an instance marker")
		}
	})
}
