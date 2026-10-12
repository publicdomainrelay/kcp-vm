//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devsup"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tsbundle"
)

const workspacesPath = "/clusters/root/apis/tenancy.kcp.io/v1alpha1/workspaces"

func (d *liveDev) runIn(dir string, extraEnv []string, out any, args ...string) error {
	cmd := exec.Command(d.bin, append(args, "--state-dir", d.state)...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "SOCIALWEB_KCP_PATH="+d.kcp, "SOCIALWEB_KINE_PATH="+d.kine, "SOCIALWEB_DENO_PATH="+d.deno, tsbundle.OrgRootEnv+"="+d.org), extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v in %s: %v\n%s", args, dir, err, stderr.String())
	}
	if out == nil {
		return nil
	}
	if b, ok := out.(*[]byte); ok {
		*b = stdout.Bytes()
		return nil
	}
	if err := json.Unmarshal(stdout.Bytes(), out); err != nil {
		return fmt.Errorf("%v: %v\n%s", args, err, stdout.String())
	}
	return nil
}

func markerWorkspace(id string) string {
	return strings.TrimRight(("swc-" + id)[:min(63, len("swc-"+id))], "-")
}

func kcpCall(t *testing.T, kubeconfig, method, path string, body any) (int, []byte) {
	t.Helper()
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	hc, err := rest.HTTPClientFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	u, err := url.Parse(cfg.Host)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(method, u.Scheme+"://"+u.Host+path, rd)
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func workspaceNames(t *testing.T, kubeconfig string) []string {
	t.Helper()
	code, b := kcpCall(t, kubeconfig, "GET", workspacesPath, nil)
	if code != 200 {
		t.Fatalf("list workspaces: %d %s", code, b)
	}
	var l struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	json.Unmarshal(b, &l)
	var out []string
	for _, i := range l.Items {
		out = append(out, i.Metadata.Name)
	}
	return out
}

func TestThreeWorktreesEachReachTheirOwnKcpThroughDevEnv(t *testing.T) {
	d := openLiveDev(t)
	if d == nil {
		return
	}
	base := t.TempDir()
	xdg := []string{"XDG_STATE_HOME=" + filepath.Join(base, "xdg-state")}
	var trees []string
	for _, parent := range []string{"one", "two", "three"} {
		wt := filepath.Join(base, parent, "socialweb-computer-kcp")
		if err := os.MkdirAll(filepath.Join(wt, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Join(wt, "internal", "x"), 0o755)
		trees = append(trees, wt)
	}
	t.Cleanup(func() {
		for _, wt := range trees {
			d.runIn(wt, xdg, nil, "dev", "down")
		}
	})
	ups := make([]upReport, len(trees))
	errs := make([]error, len(trees))
	var wg sync.WaitGroup
	for i, wt := range trees {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dir := wt
			if i == 2 {
				dir = filepath.Join(wt, "internal", "x")
			}
			errs[i] = d.runIn(dir, xdg, &ups[i], "dev", "up", "--profile", "minimal")
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]bool{}
	var peers []string
	for i, u := range ups {
		if ids[u.Instance] || !strings.HasPrefix(u.Instance, "socialweb-comput-") {
			t.Fatalf("instance id %q (duplicate or not derived from the worktree)", u.Instance)
		}
		ids[u.Instance] = true
		peers = append(peers, u.Instance)
		if got, _ := os.Readlink(filepath.Join(trees[i], devinstance.Marker)); got != u.StateDir {
			t.Fatalf("%s marker -> %q, want %s", trees[i], got, u.StateDir)
		}
		if filepath.Base(filepath.Dir(u.StateDir)) != "dev" {
			t.Fatalf("state %s is not under dev/", u.StateDir)
		}
		t.Logf("reading %s up %s in %.1fs: kcp %s kine %s socket %s", time.Now().UTC().Format(time.RFC3339), u.Instance, u.Seconds, u.KcpURL, u.KineURL, u.Socket)
	}

	leasePath := filepath.Join(base, "xdg-state", "socialweb-computer", "ports", "leases.json")
	leased := map[int]string{}
	for _, u := range ups {
		al, err := devinstance.OpenLeases(leasePath, u.Instance)
		if err != nil {
			t.Fatal(err)
		}
		l, err := al.Allocate(portallocator.Request{Cluster: "front", Purpose: portallocator.PurposeApiserverLink})
		if err != nil {
			t.Fatal(err)
		}
		if prev, dup := leased[l.Port]; dup {
			t.Fatalf("%s and %s leased %d", prev, u.Instance, l.Port)
		}
		leased[l.Port] = u.Instance
	}

	envs := make([]map[string]string, len(trees))
	for i, wt := range trees {
		if err := d.runIn(wt, xdg, &envs[i], "dev", "env", "--json"); err != nil {
			t.Fatal(err)
		}
		e := envs[i]
		if e["SOCIALWEB_INSTANCE"] != ups[i].Instance || e["SOCIALWEB_KCP_HOST"] != ups[i].KcpURL || e["SOCIALWEB_DEV_LEASE_FRONT_APISERVER_LINK"] == "" {
			t.Fatalf("dev env of %s: %v", ups[i].Instance, e)
		}
		code, b := kcpCall(t, e["KUBECONFIG"], "GET", "/readyz", nil)
		if code != 200 {
			t.Fatalf("%s /readyz through its KUBECONFIG: %d %s", ups[i].Instance, code, b)
		}
		code, b = kcpCall(t, e["KUBECONFIG"], "POST", workspacesPath, map[string]any{"apiVersion": "tenancy.kcp.io/v1alpha1",
			"kind": "Workspace", "metadata": map[string]any{"name": markerWorkspace(ups[i].Instance)}})
		if code != 201 && code != 409 {
			t.Fatalf("%s create marker: %d %s", ups[i].Instance, code, b)
		}
	}
	for i := range trees {
		names := strings.Join(workspaceNames(t, envs[i]["KUBECONFIG"]), ",")
		for j := range trees {
			has := strings.Contains(names, markerWorkspace(ups[j].Instance))
			if has != (i == j) {
				t.Fatalf("%s lists %s; marker of %s present=%v", ups[i].Instance, names, ups[j].Instance, has)
			}
		}
	}
	if deno, err := exec.LookPath("deno"); err == nil {
		k8s, _ := filepath.Abs(filepath.Join("..", "..", "socialweb-computer-k8s"))
		for i := range trees {
			cmd := exec.Command(deno, "test", "-A", "--node-modules-dir=none", "test/dev_env_kcp_test.ts")
			cmd.Dir = k8s
			cmd.Env = append(os.Environ(), "NO_COLOR=1", "SWC_DEV_PEERS="+strings.Join(peers, ","))
			for k, v := range envs[i] {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "ok | 1 passed") {
				t.Fatalf("TS client of %s: %v\n%s", ups[i].Instance, err, out)
			}
			t.Logf("TS client %s: %s", ups[i].Instance, strings.TrimSpace(string(out)))
		}
	}

	var ls []lsEntry
	if err := d.runIn(base, xdg, &ls, "dev", "ls"); err != nil {
		t.Fatal(err)
	}
	reach := 0
	for _, e := range ls {
		if ids[e.Instance] && e.Reachable {
			reach++
		}
	}
	if reach != 3 {
		t.Fatalf("dev ls: %+v", ls)
	}

	var logs []byte
	if err := d.runIn(trees[0], xdg, &logs, "dev", "logs", "kcp-a", "--tail", "3"); err != nil || len(bytes.Split(bytes.TrimSpace(logs), []byte("\n"))) != 3 {
		t.Fatalf("dev logs kcp-a --tail 3: %v %q", err, logs)
	}

	var rr restartReport
	if err := d.runIn(trees[1], xdg, &rr, "dev", "restart", "core", "--binary", d.bin); err != nil {
		t.Fatal(err)
	}
	if !rr.UnitsUnchanged || rr.After.Generation != rr.Before.Generation+1 || rr.After.PID == rr.Before.PID || devsup.Alive(rr.Before.PID) {
		t.Fatalf("dev restart core: %+v", rr)
	}
	t.Logf("reading %s restart core of %s: gen %d pid %d -> gen %d pid %d in %.2fs, adoptions %+v", time.Now().UTC().Format(time.RFC3339),
		rr.Instance, rr.Before.Generation, rr.Before.PID, rr.After.Generation, rr.After.PID, rr.Seconds, rr.After.Adoptions)

	downs := make([]downReport, len(trees))
	for i, wt := range trees {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = d.runIn(wt, xdg, &downs[i], "dev", "down")
		}()
	}
	wg.Wait()
	for i, r := range downs {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if !r.DevsupStopped || len(r.Remaining) > 0 || !r.RemovedState || !r.MarkerRemoved || r.LeasesReleased != 1 {
			t.Fatalf("down %+v", r)
		}
		if _, err := os.Lstat(filepath.Join(trees[i], devinstance.Marker)); !os.IsNotExist(err) {
			t.Fatalf("marker of %s left: %v", trees[i], err)
		}
		if left := devsup.InstanceProcesses(r.Instance); len(left) > 0 {
			t.Fatalf("processes of %s left: %v", r.Instance, left)
		}
		t.Logf("reading %s down %s in %.2fs", time.Now().UTC().Format(time.RFC3339), r.Instance, r.Seconds)
	}
	al, _ := devinstance.OpenLeases(leasePath, "")
	if all := al.AllLeases(); len(all) != 0 {
		t.Fatalf("leases left after dev down: %+v", all)
	}
}

type fgRun struct {
	cmd    *exec.Cmd
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

func (d *liveDev) startFg(t *testing.T, id string, extraEnv []string) (fgRun, upReport) {
	t.Helper()
	r := fgRun{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	r.cmd = exec.Command(d.bin, "dev", "up", "--fg", "--instance", id, "--profile", "minimal", "--state-dir", d.state)
	r.cmd.Env = append(append(os.Environ(), "SOCIALWEB_KCP_PATH="+d.kcp, "SOCIALWEB_KINE_PATH="+d.kine, "SOCIALWEB_DENO_PATH="+d.deno, tsbundle.OrgRootEnv+"="+d.org), extraEnv...)
	r.cmd.Stdout, r.cmd.Stderr = r.stdout, r.stderr
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var up upReport
		if dec := json.NewDecoder(bytes.NewReader(r.stdout.Bytes())); dec.Decode(&up) == nil && up.Instance == id {
			return r, up
		}
		if time.Now().After(deadline) || r.cmd.ProcessState != nil {
			r.cmd.Process.Kill()
			t.Fatalf("dev up --fg never reported ready:\n%s\n%s", r.stdout, r.stderr)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestDevUpInTheForegroundEndsWithItsTerminal(t *testing.T) {
	d := openLiveDev(t)
	if d == nil {
		return
	}
	xdg := []string{"XDG_STATE_HOME=" + filepath.Join(t.TempDir(), "xdg-state")}
	id := "tfg-" + strings.ToLower(fmt.Sprintf("%x", time.Now().UnixNano()&0xffffff))
	t.Cleanup(func() { exec.Command(d.bin, "dev", "down", "--instance", id, "--state-dir", d.state).Run() })

	t.Run("CtrlCIsDevDown", func(t *testing.T) {
		r, up := d.startFg(t, id, xdg)
		if !up.Foreground {
			t.Fatalf("up %+v", up)
		}
		var again bytes.Buffer
		second := exec.Command(d.bin, "dev", "up", "--fg", "--instance", id, "--profile", "minimal", "--state-dir", d.state)
		second.Env = append(os.Environ(), "SOCIALWEB_KCP_PATH="+d.kcp, "SOCIALWEB_KINE_PATH="+d.kine, "SOCIALWEB_DENO_PATH="+d.deno, tsbundle.OrgRootEnv+"="+d.org)
		second.Stderr = &again
		if err := second.Run(); second.ProcessState.ExitCode() != 2 || !strings.Contains(again.String(), "already up") {
			t.Fatalf("a second dev up --fg of a running instance: %v %q", err, again.String())
		}
		t0 := time.Now()
		r.cmd.Process.Signal(os.Interrupt)
		if err := r.cmd.Wait(); err != nil {
			t.Fatalf("dev up --fg after Ctrl-C: %v\n%s", err, r.stderr)
		}
		if left := devsup.InstanceProcesses(id); len(left) > 0 {
			t.Fatalf("processes left after Ctrl-C: %v", left)
		}
		if _, err := os.Stat(up.StateDir); !os.IsNotExist(err) {
			t.Fatalf("state left after Ctrl-C: %v", err)
		}
		t.Logf("reading %s Ctrl-C of dev up --fg %s: down in %s", time.Now().UTC().Format(time.RFC3339), id, time.Since(t0).Round(time.Millisecond))
	})

	t.Run("KillingTheTerminalProcessKillsTheInstance", func(t *testing.T) {
		r, up := d.startFg(t, id, xdg)
		pids := []int{up.Devsup.PID, up.Core.PID}
		for _, u := range up.Units {
			pids = append(pids, u.Identity.PID)
		}
		t0 := time.Now()
		r.cmd.Process.Kill()
		r.cmd.Wait()
		alive := func() []int {
			var out []int
			for _, p := range pids {
				if devsup.Alive(p) {
					out = append(out, p)
				}
			}
			return append(out, devsup.InstanceProcesses(id)...)
		}
		deadline := time.Now().Add(30 * time.Second)
		for len(alive()) > 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if left := alive(); len(left) > 0 {
			t.Fatalf("pids %v outlived kill -9 of dev up --fg by 30s", left)
		}
		t.Logf("reading %s kill -9 of dev up --fg %s: devsup %d, the core and %d units gone in %s", time.Now().UTC().Format(time.RFC3339), id,
			up.Devsup.PID, len(up.Units), time.Since(t0).Round(time.Millisecond))
	})
}
