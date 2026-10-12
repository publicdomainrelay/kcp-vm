//go:build linux

package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc/hostsvctest"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpserver"
)

func utc() string { return time.Now().UTC().Format("15:04:05.000Z") }

type frontClient struct {
	front string
	token string
	http  *http.Client
	watch *http.Client
}

func newFrontClient(t *testing.T, front string, a kcpserver.Admin) *frontClient {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(a.CA) {
		t.Fatal("the shared CA does not parse")
	}
	tr := func() *http.Transport {
		return &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"}, DisableKeepAlives: true}
	}
	return &frontClient{front: front, token: a.Token, http: &http.Client{Timeout: 2 * time.Second, Transport: tr()},
		watch: &http.Client{Transport: tr()}}
}

func (c *frontClient) get(ctx context.Context, cl *http.Client, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.front+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	return cl.Do(req)
}

func (c *frontClient) readyz(ctx context.Context) error {
	resp, err := c.get(ctx, c.http, "/readyz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("readyz %d", resp.StatusCode)
	}
	return nil
}

type poller struct {
	ok, failed atomic.Int64
	mu         sync.Mutex
	failures   []string
	stop       chan struct{}
	done       chan struct{}
}

func startPoller(c *frontClient, every time.Duration) *poller {
	p := &poller{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-t.C:
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err := c.readyz(ctx)
			cancel()
			if err != nil {
				p.failed.Add(1)
				p.mu.Lock()
				p.failures = append(p.failures, utc()+" "+err.Error())
				p.mu.Unlock()
				continue
			}
			p.ok.Add(1)
		}
	}()
	return p
}

func (p *poller) halt() { close(p.stop); <-p.done }

type watcher struct {
	mu        sync.Mutex
	streams   int
	ends      []string
	events    []string
	lastRV    string
	stop      context.CancelFunc
	done      chan struct{}
	relists   int
	openedAt  []string
	watchErrs []string
}

const workspacesPath = "/clusters/root/apis/tenancy.kcp.io/v1alpha1/workspaces"

func (c *frontClient) listRV(ctx context.Context) (string, error) {
	resp, err := c.get(ctx, c.http, workspacesPath)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var l struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("list %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&l); err != nil {
		return "", err
	}
	return l.Metadata.ResourceVersion, nil
}

func startWatcher(c *frontClient) *watcher {
	ctx, cancel := context.WithCancel(context.Background())
	w := &watcher{stop: cancel, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		for ctx.Err() == nil {
			w.mu.Lock()
			rv := w.lastRV
			w.mu.Unlock()
			if rv == "" {
				var err error
				lctx, lcancel := context.WithTimeout(ctx, 3*time.Second)
				rv, err = c.listRV(lctx)
				lcancel()
				if err != nil || rv == "" {
					time.Sleep(200 * time.Millisecond)
					continue
				}
				w.mu.Lock()
				w.relists++
				w.lastRV = rv
				w.mu.Unlock()
			}
			resp, err := c.get(ctx, c.watch, workspacesPath+"?watch=1&resourceVersion="+rv)
			if err != nil {
				w.mu.Lock()
				w.watchErrs = append(w.watchErrs, utc()+" "+err.Error())
				w.mu.Unlock()
				time.Sleep(200 * time.Millisecond)
				continue
			}
			if resp.StatusCode != http.StatusOK {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
				resp.Body.Close()
				w.mu.Lock()
				w.watchErrs = append(w.watchErrs, fmt.Sprintf("%s %d %s", utc(), resp.StatusCode, b))
				if resp.StatusCode == http.StatusGone {
					w.lastRV = ""
				}
				w.mu.Unlock()
				time.Sleep(200 * time.Millisecond)
				continue
			}
			w.mu.Lock()
			w.streams++
			w.openedAt = append(w.openedAt, utc())
			w.mu.Unlock()
			sc := bufio.NewScanner(resp.Body)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			lines := 0
			opened := time.Now()
			for sc.Scan() {
				lines++
				var ev struct {
					Type   string          `json:"type"`
					Object json.RawMessage `json:"object"`
				}
				var obj struct {
					Message  string `json:"message"`
					Metadata struct {
						Name            string `json:"name"`
						ResourceVersion string `json:"resourceVersion"`
					} `json:"metadata"`
					Status json.RawMessage `json:"status"`
				}
				var phase struct {
					Phase string `json:"phase"`
				}
				if err := json.Unmarshal(sc.Bytes(), &ev); err != nil || json.Unmarshal(ev.Object, &obj) != nil {
					continue
				}
				if ev.Type != "ERROR" {
					json.Unmarshal(obj.Status, &phase)
				}
				w.mu.Lock()
				if ev.Type == "ERROR" {
					w.lastRV = ""
					w.watchErrs = append(w.watchErrs, utc()+" ERROR "+obj.Message)
				} else {
					w.lastRV = obj.Metadata.ResourceVersion
					w.events = append(w.events, ev.Type+" "+obj.Metadata.Name+" "+phase.Phase)
				}
				w.mu.Unlock()
			}
			resp.Body.Close()
			w.mu.Lock()
			w.ends = append(w.ends, utc())
			if lines == 0 && time.Since(opened) < time.Second && len(w.watchErrs) < 5 {
				w.watchErrs = append(w.watchErrs, fmt.Sprintf("%s empty stream at rv %s: %v proto %s ct %s", utc(), rv, sc.Err(), resp.Proto, resp.Header.Get("Content-Type")))
			}
			w.mu.Unlock()
			time.Sleep(50 * time.Millisecond)
		}
	}()
	return w
}

func (w *watcher) saw(want string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Contains(w.events, want)
}

func (w *watcher) snapshot() (int, []string, []string, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.streams, append([]string(nil), w.ends...), append([]string(nil), w.watchErrs...), w.relists
}

func waitFor(t *testing.T, what string, d time.Duration, f func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for !f() {
		if time.Since(start) > d {
			t.Fatalf("timed out after %s waiting for %s", d, what)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return time.Since(start)
}

func dialLoop(addr string, stop <-chan struct{}) (*atomic.Int64, *atomic.Int64, *atomic.Int64, chan struct{}) {
	var connected, refused, other atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			c, err := net.DialTimeout("tcp", addr, 3*time.Second)
			switch {
			case err == nil:
				connected.Add(1)
				c.Close()
			case errors.Is(err, syscall.ECONNREFUSED):
				refused.Add(1)
			default:
				other.Add(1)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	return &connected, &refused, &other, done
}

func TestTheKcpFrontFlipsWithZeroFailedReadyzPolls(t *testing.T) {
	d := openLiveDev(t)
	if d == nil {
		return
	}
	id := "t" + hostsvctest.RandomKey() + "-roll"
	t.Cleanup(func() { exec.Command(d.bin, "dev", "down", "--instance", id, "--state-dir", d.state).Run() })
	up := d.up(t, id)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, f := range []string{"core.log", "front.log"} {
			b, _ := os.ReadFile(filepath.Join(up.StateDir, "logs", f))
			if len(b) > 6000 {
				b = b[len(b)-6000:]
			}
			t.Logf("%s:\n%s", f, b)
		}
	})
	t.Logf("reading %s dev up %s: front %s, %.1f s", utc(), id, up.KcpURL, up.Seconds)
	frontPort, err := kcpserver.PortOf(up.KcpURL)
	if err != nil {
		t.Fatal(err)
	}
	kcpDir := filepath.Join(up.StateDir, "kcp")
	a, err := kcpserver.ReadSharedAdmin(filepath.Join(kcpDir, "shared"))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := kcpserver.NewHTTPAdmin(a, up.KcpURL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, u := range up.Units {
		if u.Role == "kcp-a" && u.Port == frontPort {
			t.Fatalf("the instance's kcp URL is the slot port %d, not a front", u.Port)
		}
	}
	if err := admin.CreateWorkspace(ctx, "root", "before-roll"); err != nil {
		t.Fatal(err)
	}
	rctx, rcancel := context.WithTimeout(ctx, 30*time.Second)
	if err := admin.WaitWorkspaceReady(rctx, "root", "before-roll"); err != nil {
		t.Fatal(err)
	}
	rcancel()
	c := newFrontClient(t, up.KcpURL, a)

	t.Run("DevRollKcpFailsNoPollAndHandsTheLeaseOver", func(t *testing.T) {
		w := startWatcher(c)
		defer func() { w.stop(); <-w.done }()
		waitFor(t, "the watch to open", 15*time.Second, func() bool { s, _, _, _ := w.snapshot(); return s >= 1 })
		p := startPoller(c, 200*time.Millisecond)
		time.Sleep(time.Second)
		started := utc()
		var rep rollReport
		err := d.runE(&rep, "dev", "roll", "kcp", "--instance", id)
		ended := utc()
		time.Sleep(2 * time.Second)
		p.halt()
		if err != nil {
			t.Fatal(err)
		}
		r := rep.Roll
		t.Logf("reading roll (%s..%s UTC): polls ok %d failed %d; slot %s:%d -> %s:%d; start-to-ready %.2fs, old exit %.2fs, lease %q -> %q in %.2fs, probe Ready %.2fs, total %.2fs, open on old at exit %d, drained %d",
			started, ended, p.ok.Load(), p.failed.Load(), r.From, r.OldPort, r.To, r.NewPort, r.StartToReadySeconds,
			r.OldExitSeconds, r.OldHolder, r.NewHolder, r.LeaseHandoverSeconds, r.ProbeSeconds, r.TotalSeconds,
			r.OpenOnOldAtExit, r.Drained)
		if p.failed.Load() != 0 {
			t.Fatalf("%d of %d /readyz polls through the front failed: %v", p.failed.Load(), p.ok.Load()+p.failed.Load(), p.failures)
		}
		if p.ok.Load() < 50 {
			t.Fatalf("only %d polls ran across the roll", p.ok.Load())
		}
		if r.From != "a" || r.To != "b" || r.NewHolder == "" || r.NewHolder == r.OldHolder || r.LeaseHandoverSeconds > 10 {
			t.Fatalf("roll report %+v", r)
		}
		if l, err := admin.Lease(ctx); err != nil || l.Holder != r.NewHolder {
			t.Fatalf("lease holder after the roll %q (%v), want the new slot's %q", l.Holder, err, r.NewHolder)
		}
		if err := admin.CreateWorkspace(ctx, "root", "after-roll"); err != nil {
			t.Fatal(err)
		}
		readyIn := waitFor(t, "after-roll Ready", 30*time.Second, func() bool {
			ph, _ := admin.WorkspacePhase(ctx, "root", "after-roll")
			return ph == "Ready"
		})
		streams, ends, werrs, relists := w.snapshot()
		if !w.saw("MODIFIED after-roll Ready") {
			time.Sleep(5 * time.Second)
		}
		w.mu.Lock()
		evs := append([]string(nil), w.events...)
		w.mu.Unlock()
		if !w.saw("MODIFIED after-roll Ready") {
			t.Fatalf("the watch never saw after-roll Ready: streams %d ends %v errors %v relists %d events %v", streams, ends, werrs, relists, evs)
		}
		t.Logf("reading watch: %d streams, ends %v, relists %d, errors %v; after-roll Ready in %s", streams, ends, relists, werrs, readyIn.Round(time.Millisecond))
		if streams < 2 {
			t.Fatalf("the watch never reconnected across the roll: %d streams", streams)
		}
		names, err := admin.ListWorkspaces(ctx, "root")
		if err != nil || !slices.Contains(names, "before-roll") || !slices.Contains(names, "after-roll") {
			t.Fatalf("workspaces after the roll %v (%v)", names, err)
		}
		if ph, _ := admin.WorkspacePhase(ctx, "root", "before-roll"); ph != "Ready" {
			t.Fatalf("before-roll is %q after the roll", ph)
		}
		var st statusReport
		d.run(t, &st, "dev", "status", "--instance", id)
		roles := map[string]bool{}
		for _, u := range st.Record.Units {
			roles[u.Role] = true
		}
		if !roles["kcp-b"] || roles["kcp-a"] || !roles["front"] {
			t.Fatalf("record units after the roll %v", roles)
		}
	})

	t.Run("AFrontRestartRefusesNoConnection", func(t *testing.T) {
		var rec devinstance.Record
		if err := devinstance.ReadJSON(filepath.Join(up.StateDir, "instance.json"), &rec); err != nil {
			t.Fatal(err)
		}
		front := rec.Unit("front")
		stop := make(chan struct{})
		connected, refused, other, done := dialLoop("127.0.0.1:"+strconv.Itoa(frontPort), stop)
		p := startPoller(c, 200*time.Millisecond)
		time.Sleep(500 * time.Millisecond)
		killed := utc()
		if err := syscall.Kill(front.Identity.PID, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Second)
		close(stop)
		<-done
		p.halt()
		t.Logf("reading front kill -9 of pid %d at %s: connects %d refused %d other %d; readyz ok %d failed %d %v",
			front.Identity.PID, killed, connected.Load(), refused.Load(), other.Load(), p.ok.Load(), p.failed.Load(), p.failures)
		if refused.Load() != 0 || other.Load() != 0 {
			t.Fatalf("%d connections refused, %d failed across the front restart", refused.Load(), other.Load())
		}
		if p.failed.Load() > 1 {
			t.Fatalf("%d readyz polls failed across the front restart (the one in flight at the kill may be reset)", p.failed.Load())
		}
		pid := 0
		waitFor(t, "a new front pid on the same port", 10*time.Second, func() bool {
			var st statusReport
			if d.runE(&st, "dev", "status", "--instance", id) != nil {
				return false
			}
			for _, u := range st.Devsup.Units {
				if u.Name == front.Unit && u.MainPID > 0 && u.MainPID != front.Identity.PID {
					pid = u.MainPID
					return true
				}
			}
			return false
		})
		if got, err := listenPortOf(pid); err != nil || got != frontPort {
			t.Fatalf("the restarted front %d listens on %d (%v), want %d", pid, got, err, frontPort)
		}
	})

	t.Run("AFrontKeepsItsAddressAcrossAKcpSlotRestart", func(t *testing.T) {
		s, err := kcpserver.ReadSlots(filepath.Join(kcpDir, "kcp-slots.json"))
		if err != nil {
			t.Fatal(err)
		}
		slot := s.Slots[s.Active]
		p := startPoller(c, 200*time.Millisecond)
		killed := time.Now()
		if err := syscall.Kill(slot.PID, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		back := waitFor(t, "readyz through the front after a slot kill -9", 90*time.Second, func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return time.Since(killed) > time.Second && c.readyz(ctx) == nil
		})
		p.halt()
		after, err := kcpserver.ReadSlots(filepath.Join(kcpDir, "kcp-slots.json"))
		if err != nil {
			t.Fatal(err)
		}
		if after.Front.Port != frontPort {
			t.Fatalf("the front moved from %d to %d", frontPort, after.Front.Port)
		}
		if err := admin.CreateWorkspace(ctx, "root", "after-restart"); err != nil {
			t.Fatal(err)
		}
		readyIn := waitFor(t, "after-restart Ready", 90*time.Second, func() bool {
			ph, _ := admin.WorkspacePhase(ctx, "root", "after-restart")
			return ph == "Ready"
		})
		t.Logf("reading slot kill -9 of pid %d at %s: kcp back through the front %d after %s (slot port %d -> %d); polls ok %d failed %d; after-restart Ready %s later",
			slot.PID, killed.UTC().Format("15:04:05.000Z"), frontPort, back.Round(time.Millisecond), slot.Port, after.Slots[after.Active].Port,
			p.ok.Load(), p.failed.Load(), readyIn.Round(time.Millisecond))
	})

	if t.Failed() {
		return
	}
	r := d.down(t, id)
	if !r.DevsupStopped || len(r.Remaining) > 0 {
		t.Fatalf("down %+v", r)
	}
}

func listenPortOf(pid int) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return waitListen(ctx, pid)
}

func waitListen(ctx context.Context, pid int) (int, error) {
	return hostsvc.WaitListenPort(ctx, pid, 20*time.Millisecond)
}
