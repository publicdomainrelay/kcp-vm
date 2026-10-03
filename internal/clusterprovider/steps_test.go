//go:build linux

package clusterprovider

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

func spec() clusterstack.Spec {
	return clusterstack.Spec{
		DataDir:                       "/tmp/data/liveweb",
		PostgresPort:                  20001,
		OpenBaoConfig:                 "/tmp/data/liveweb/openbao.hcl",
		StoreEndpoint:                 "postgres://postgres@127.0.0.1:5432/k3s?sslmode=disable",
		DatastoreEndpoint:             "http://127.0.0.1:2379",
		KineListen:                    "127.0.0.1:20002",
		AdvertiseAddress:              portallocator.LoopbackAdvertiseAddress,
		BindAddress:                   portallocator.LoopbackAdvertiseAddress,
		AdvertisePort:                 20003,
		K3sSecurePort:                 20003,
		K3sControllerManagerPort:      20009,
		K3sSchedulerPort:              20010,
		K3sCloudControllerManagerPort: 20011,
		ServiceAccountIssuer:          "https://kubernetes.default.svc",
		APIAudiences:                  []string{"https://kubernetes.default.svc"},
		TokenFile:                     "/tmp/data/liveweb/token",
		Audit: clusterstack.Audit{
			PolicyPath: "/tmp/data/liveweb/audit-policy.yaml",
			LogPath:    "/tmp/data/liveweb/audit.log",
		},
	}
}

func leases() materialiser.Leases {
	return materialiser.Leases{
		portallocator.PurposeK3sSecure: {Port: 20003, Width: 1, Purpose: portallocator.PurposeK3sSecure},
		portallocator.PurposeOpenBao:   {Port: 20004, Width: 1, Purpose: portallocator.PurposeOpenBao},
		portallocator.PurposeKine:      {Port: 20002, Width: 1, Purpose: portallocator.PurposeKine},
	}
}

func TestProvenStackIsOneStepAtTheApiserver(t *testing.T) {
	steps, err := StepsFrom(spec(), materialiser.Leases{}, ProvenStack)
	if err != nil {
		t.Fatalf("StepsFrom(ProvenStack): %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("proven stack has %d steps, want 1: %v", len(steps), names(steps))
	}
	if steps[0].Name != procgroup.ComponentK3s {
		t.Fatalf("the one step is %q, want %q", steps[0].Name, procgroup.ComponentK3s)
	}
	argv := steps[0].Spec.Argv
	if len(argv) < 2 || argv[0] != "k3s" || argv[1] != "server" {
		t.Fatalf("argv does not start a k3s server: %v", argv)
	}
	if !has(argv, "--disable-agent") {
		t.Error("--disable-agent is missing: without it the server is a node and zero nodes stops being a real state (ADR 0012)")
	}
	if !has(argv, "--datastore-endpoint") {
		t.Error("--datastore-endpoint is missing: the proven shape IS an external datastore")
	}
	if steps[0].Ready == nil {
		t.Fatal("the k3s step has no readiness gate: a step without a gate lets the next component start against a port nothing listens on")
	}
}

func TestProvenStackNeedsNoOpenBaoLease(t *testing.T) {
	if _, err := StepsFrom(spec(), materialiser.Leases{}, ProvenStack); err != nil {
		t.Fatalf("the proven stack demanded something of the leases: %v", err)
	}
	if _, err := StepsFrom(spec(), materialiser.Leases{}, DesignedStack); !errors.Is(err, ErrNoLease) {
		t.Fatalf("the designed stack started without an openbao lease: err=%v, want ErrNoLease", err)
	}
}

func TestDesignedStackKeepsProcgroupsOrder(t *testing.T) {
	steps, err := StepsFrom(spec(), leases(), DesignedStack)
	if err != nil {
		t.Fatalf("StepsFrom(DesignedStack): %v", err)
	}
	want := []procgroup.Component{procgroup.ComponentOpenBao, procgroup.ComponentKine, procgroup.ComponentK3s}
	if got := names(steps); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order is %v, want %v (procgroup.StackOrder). This step list is the whole opt-in "+
			"path for a component the default set does not bring up: read against "+
			"procgroup.DefaultOrder, which is the default set and no longer contains openbao, this "+
			"same walk builds kine and k3s and SILENTLY DROPS the vault -- the port is leased, the "+
			"config is rendered and nothing runs", got, want)
	}
}

func TestPostgresIsNeverAStep(t *testing.T) {
	_, err := StepsFrom(spec(), leases(), Stack{procgroup.ComponentPostgres, procgroup.ComponentK3s})
	if err == nil {
		t.Fatal("a Stack naming ComponentPostgres was accepted; embedded-postgres is a Go library and cannot be a Step")
	}
}

func TestEmptyStackAndInvalidSpecAreRefused(t *testing.T) {
	if _, err := StepsFrom(spec(), leases(), nil); err == nil {
		t.Error("an empty Stack was accepted")
	}
	bad := spec()
	bad.AdvertisePort = 0
	if _, err := StepsFrom(bad, leases(), ProvenStack); !errors.Is(err, clusterstack.ErrMissingAdvertise) {
		t.Errorf("an incomplete Spec reached the argv: %v", err)
	}
}

func TestTheK3sGateOpensOnA401(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer up.Close()

	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatalf("parse %s: %v", up.URL, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("port of %s: %v", up.URL, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitK3s(ctx, "127.0.0.1", port); err != nil {
		t.Fatalf("a 401-answering apiserver was refused: %v\n\nThe gate must treat 401 as "+
			"SERVING. If this fails, k3s answers 401 on the target and the gate can never "+
			"open -- every cluster would fail with a message about the apiserver not "+
			"answering, which is the opposite of what is true", err)
	}
}

func TestTheK3sGateWaitsOutA500(t *testing.T) {
	livegate.Short(t, "-short skips this test because it waits out a real timeout; drop -short to run it, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer up.Close()

	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatalf("parse %s: %v", up.URL, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("port of %s: %v", up.URL, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	err = waitK3s(ctx, "127.0.0.1", port)
	var notReady *errNotReady
	if !errors.As(err, &notReady) {
		t.Fatalf("a 500-answering apiserver was accepted: %v", err)
	}
	if notReady.last != "HTTP 500" {
		t.Errorf("the refusal did not carry the code it saw: %q", notReady.last)
	}
}

func TestTheK3sGateAcceptsAServingApiserverAndRefusesAClosedPort(t *testing.T) {
	livegate.Short(t, "-short skips this test because it waits out a real timeout; drop -short to run it, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatalf("parse %s: %v", up.URL, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("port of %s: %v", up.URL, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitK3s(ctx, "127.0.0.1", port); err != nil {
		t.Fatalf("a serving apiserver was not accepted: %v", err)
	}

	dead, cancel2 := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel2()
	err = waitK3s(dead, "127.0.0.1", freePort(t))
	var notReady *errNotReady
	if !errors.As(err, &notReady) {
		t.Fatalf("a closed port was not refused with the last observation: %v", err)
	}
	if notReady.last == "" || notReady.last == "no answer" {
		t.Errorf("the refusal did not carry what it saw: %q", notReady.last)
	}
}

func TestOpenBaoGateRefusesSealedAndUninitialized(t *testing.T) {
	livegate.Short(t, "-short skips this test because it waits out a real timeout; drop -short to run it, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	for _, tc := range []struct {
		code int
		want bool
	}{
		{http.StatusOK, true},
		{http.StatusTooManyRequests, true},
		{473, true},
		{http.StatusServiceUnavailable, false},
		{501, false},
	} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.code)
		}))
		pool := x509.NewCertPool()
		pool.AddCert(srv.Certificate())
		ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
		err := waitOpenBaoTLS(ctx, srv.URL, pool)
		cancel()
		srv.Close()
		sealed := func() bool { var e *errNotReady; return errors.As(err, &e) }
		switch {
		case tc.want && err != nil:
			t.Errorf("HTTP %d was refused and is a serving vault: %v", tc.code, err)
		case !tc.want && !sealed():
			t.Errorf("HTTP %d was accepted and is NOT a serving vault (err=%v)", tc.code, err)
		}
	}
}

func TestKineGateDialsTheAddressItsOwnArgvNamed(t *testing.T) {
	livegate.Short(t, "-short skips this test because it waits out a real timeout; drop -short to run it, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := waitDial(ctx, ln.Addr().String()); err != nil {
		t.Fatalf("a listening kine was not accepted: %v", err)
	}
	s := spec()
	s.KineListen = fmt.Sprintf("127.0.0.1:%d", freePort(t))
	if err := gate(procgroup.ComponentKine, s, portallocator.Lease{}, portallocator.Lease{})(ctx, nil); err == nil {
		t.Fatal("a kine that is not listening passed its gate")
	}
}

func TestK3sGateRefusesAnAdvertisePortThatIsNotThisClustersLease(t *testing.T) {
	s := spec()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	ours := portallocator.Lease{Port: s.AdvertisePort, Width: 1, Purpose: portallocator.PurposeK3sSecure, Cluster: "liveweb"}
	theirs := portallocator.Lease{Port: s.AdvertisePort + 1, Width: 1, Purpose: portallocator.PurposeK3sSecure, Cluster: "other"}
	none := portallocator.Lease{}

	if err := gate(procgroup.ComponentK3s, s, none, none)(ctx, nil); !errors.Is(err, ErrNoLease) {
		t.Fatalf("a k3s step with no apiserver lease must refuse with ErrNoLease, got %v", err)
	}

	err := gate(procgroup.ComponentK3s, s, none, theirs)(ctx, nil)
	if err == nil || !strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("an --advertise-port leased to another cluster must refuse and name the symptom, got %v", err)
	}

	err = gate(procgroup.ComponentK3s, s, none, ours)(ctx, nil)
	if err == nil || !strings.Contains(err.Error(), "never became ready") {
		t.Fatalf("a matching lease must reach the readiness wait, got %v", err)
	}
}

func names(steps []procgroup.Step) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, string(s.Name))
	}
	return out
}

func has(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}

func writeFakeAdminKubeconfig(t *testing.T, dataDir, server string) {
	t.Helper()
	path := clusterstack.AdminKubeconfig(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("creating the directory a k3s server writes its admin kubeconfig into: %v", err)
	}
	body := "apiVersion: v1\nkind: Config\n" +
		"clusters:\n- name: fake\n  cluster:\n    server: " + server + "\n    insecure-skip-tls-verify: true\n" +
		"contexts:\n- name: fake\n  context:\n    cluster: fake\n    user: fake\n" +
		"current-context: fake\nusers:\n- name: fake\n  user: {}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func fakeAddonAPIServer(t *testing.T, emptyFor int32) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/apps/v1/namespaces/kube-system/deployments" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if atomic.AddInt32(&calls, 1) <= emptyFor {
			io.WriteString(w, `{"kind":"DeploymentList","apiVersion":"apps/v1","items":[]}`)
			return
		}
		io.WriteString(w, `{"kind":"DeploymentList","apiVersion":"apps/v1","items":`+
			`[{"metadata":{"name":"coredns","namespace":"kube-system"}}]}`)
	}))
	t.Cleanup(up.Close)
	return up, &calls
}

func TestTheControllersGateOpensOnlyOnceAnAddonExists(t *testing.T) {
	livegate.Short(t, "-short skips this test because it waits out a real timeout; drop -short to run it, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	up, calls := fakeAddonAPIServer(t, 3)
	dir := t.TempDir()
	writeFakeAdminKubeconfig(t, dir, up.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitK3sControllers(ctx, dir); err != nil {
		t.Fatalf("a cluster whose controllers had written an addon was refused: %v\n\nThe gate "+
			"waits for the k3s controllers' first write, because the apiserver answers /readyz "+
			"before k3s registers the node-password validator that a node's serving certificate "+
			"comes from. A gate that refuses this is a gate that never opens", err)
	}
	if n := atomic.LoadInt32(calls); n < 3 {
		t.Errorf("the gate opened after %d observations, so it accepted an empty kube-system: an "+
			"apiserver that is up and holds no addon is exactly the window a node must not be "+
			"given a guest in", n)
	}
}

func TestTheControllersGateRefusesAClusterWhoseControllersNeverWrite(t *testing.T) {
	livegate.Short(t, "-short skips this test because it waits out a real timeout; drop -short to run it, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	up, _ := fakeAddonAPIServer(t, 1<<30)
	dir := t.TempDir()
	writeFakeAdminKubeconfig(t, dir, up.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	err := waitK3sControllers(ctx, dir)
	var notReady *errNotReady
	if !errors.As(err, &notReady) {
		t.Fatalf("a cluster that never wrote an addon was accepted: %v", err)
	}
	for _, want := range []string{"node-password", "kube-system", "503"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q, so a reader cannot tell what it waited for: %v",
				want, err)
		}
	}
}

func TestTheControllersGateTreatsAnAbsentKubeconfigAsAnObservation(t *testing.T) {
	livegate.Short(t, "-short skips this test because it waits out a real timeout; drop -short to run it, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	err := waitK3sControllers(ctx, t.TempDir())
	var notReady *errNotReady
	if !errors.As(err, &notReady) {
		t.Fatalf("a kubeconfig that does not exist yet was not waited for: %v\n\nThe k3s server "+
			"writes this file as it starts, so its absence is an observation and not a failure", err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}
