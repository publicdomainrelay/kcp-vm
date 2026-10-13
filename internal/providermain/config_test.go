//go:build linux

package providermain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

func envFor(extra map[string]string) func(string) string {
	env := map[string]string{
		"SOCIALWEB_KCP_HOST":                   "https://127.0.0.1:6443",
		"SOCIALWEB_KCP_KUBECONFIG":             "/tmp/live-kcp.yaml",
		"SOCIALWEB_CLUSTERPROVIDER_LEASE_PATH": "/tmp/ports.json",
	}
	for k, v := range extra {
		env[k] = v
	}
	return func(k string) string { return env[k] }
}

func thisHost(t *testing.T) string {
	t.Helper()
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	return host
}

func marketPlacementEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"SOCIALWEB_CLUSTERPROVIDER_HOST": thisHost(t),
	}
}

func joinEnv(t *testing.T, extra map[string]string) map[string]string {
	t.Helper()
	env := map[string]string{
		"SOCIALWEB_KCP_HOST":                                 "https://127.0.0.1:6443",
		"SOCIALWEB_KCP_KUBECONFIG":                           "/tmp/live-kcp.yaml",
		"SOCIALWEB_CLUSTERPROVIDER_LEASE_PATH":               "/tmp/ports.json",
		"SOCIALWEB_CLUSTERPROVIDER_JOIN":                     "liveweb",
		"SOCIALWEB_GATEWAY_MINT_KUBECONFIGS":                 t.TempDir(),
		"SOCIALWEB_CLUSTERPROVIDER_DATASTORE_HAS_ROW":        "true",
		"SOCIALWEB_CLUSTERPROVIDER_STATE_DIR":                t.TempDir(),
		"SOCIALWEB_CLUSTERPROVIDER_CLOUD_CONTROLLER_MANAGER": "/usr/local/bin/socialweb-cloud-controller-manager",
		"SOCIALWEB_CLUSTERPROVIDER_EXTERNAL_SECRETS":         "/usr/local/bin/external-secrets",
		"SOCIALWEB_CLUSTERPROVIDER_ESO_BUNDLE":               "/opt/socialweb/eso/external-secrets.yaml",
	}
	for k, v := range marketPlacementEnv(t) {
		env[k] = v
	}
	for k, v := range extra {
		if v == "" {
			delete(env, k)
			continue
		}
		env[k] = v
	}
	return env
}

func loadJoin(t *testing.T, extra map[string]string) (Config, error) {
	t.Helper()
	env := joinEnv(t, extra)
	return load(func(k string) string { return env[k] })
}

func TestAJoinWithoutTheCloudControllerManagerBinaryIsRefused(t *testing.T) {
	_, err := loadJoin(t, map[string]string{"SOCIALWEB_CLUSTERPROVIDER_CLOUD_CONTROLLER_MANAGER": ""})
	if err == nil {
		t.Fatal("a join with no cloud controller manager binary was accepted: the process group " +
			"would lease and spawn every other component and silently drop the one that gives a " +
			"joined Node its cloud identity")
	}
	if !strings.Contains(err.Error(), "SOCIALWEB_CLUSTERPROVIDER_CLOUD_CONTROLLER_MANAGER") {
		t.Fatalf("the refusal does not name the setting an operator has to supply: %v", err)
	}
}

func TestObserveOnlyNeedsNoPlacement(t *testing.T) {
	cfg, err := load(envFor(nil))
	if err != nil {
		t.Fatalf("observe-only load with no placement at all: %v -- the placement decides where a "+
			"node runs, and observe-only never runs one", err)
	}
	if !cfg.ObserveOnly() {
		t.Fatal("an unset Join must be observe-only")
	}
	if cfg.Host != "" {
		t.Fatalf("observe-only invented a placement: %+v", cfg)
	}
}

func openFrom(t *testing.T, cfg Config, leasePath string) *portallocator.Allocator {
	t.Helper()
	none := portallocator.Range{}
	a, err := portallocator.Open(portallocator.Config{
		LeasePath: leasePath,
		Range:     cfg.PortRange,
		Ephemeral: &none,
		Probe:     func(int) bool { return false },
	})
	if err != nil {
		t.Fatalf("portallocator.Open(%s): %v", leasePath, err)
	}
	return a
}

func TestCarveUnsetIsTheMeasuredDefault(t *testing.T) {
	cfg, err := load(envFor(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PortRange != (portallocator.Range{}) {
		t.Fatalf("an unset carve must stay the ZERO Range so portallocator's own default applies, got %s",
			cfg.PortRange)
	}
	a := openFrom(t, cfg, filepath.Join(t.TempDir(), "ports.json"))
	want := portallocator.Range{First: portallocator.DefaultFirst, Last: portallocator.DefaultLast}
	if got := a.Range(); got != want {
		t.Fatalf("an unset carve resolved to %s, want the measured default %s", got, want)
	}
}

func TestCarveIsHonouredAndMakesTwoInstancesDisjoint(t *testing.T) {
	dir := t.TempDir()
	alpha, err := load(envFor(map[string]string{
		"SOCIALWEB_CLUSTERPROVIDER_PORT_RANGE": "20000-20999",
	}))
	if err != nil {
		t.Fatalf("load(alpha): %v", err)
	}
	beta, err := load(envFor(map[string]string{
		"SOCIALWEB_CLUSTERPROVIDER_PORT_RANGE": "21000-21999",
	}))
	if err != nil {
		t.Fatalf("load(beta): %v", err)
	}
	if alpha.PortRange != (portallocator.Range{First: 20000, Last: 20999}) {
		t.Fatalf("alpha's carve was not carried into the Config: %s", alpha.PortRange)
	}

	aA := openFrom(t, alpha, filepath.Join(dir, "alpha.json"))
	aB := openFrom(t, beta, filepath.Join(dir, "beta.json"))
	lA, err := aA.Allocate(portallocator.Request{Cluster: "alpha", Instance: "1", Purpose: portallocator.PurposeKine})
	if err != nil {
		t.Fatalf("alpha Allocate: %v", err)
	}
	lB, err := aB.Allocate(portallocator.Request{Cluster: "beta", Instance: "1", Purpose: portallocator.PurposeKine})
	if err != nil {
		t.Fatalf("beta Allocate: %v", err)
	}
	if lA.Port == lB.Port {
		t.Fatalf("two disjoint carves still collided on %d", lA.Port)
	}
	if !alpha.PortRange.Contains(lA.Port) || !beta.PortRange.Contains(lB.Port) {
		t.Fatalf("a carve was not honoured: %d for %s and %d for %s", lA.Port, alpha.PortRange, lB.Port, beta.PortRange)
	}
}

func TestCarveMalformedIsARefusalAndNotADefault(t *testing.T) {
	for _, raw := range []string{
		"20000",
		"20000-",
		"-29999",
		"twenty-twenty",
		"20000-29999-1",
		"29999-20000",
	} {
		_, err := load(envFor(map[string]string{"SOCIALWEB_CLUSTERPROVIDER_PORT_RANGE": raw}))
		if err == nil {
			t.Errorf("%q was accepted as a carve; a malformed carve must fail at startup, not "+
				"resolve to the default", raw)
			continue
		}
		if !strings.Contains(err.Error(), "SOCIALWEB_CLUSTERPROVIDER_PORT_RANGE") {
			t.Errorf("%q: the refusal must name the variable an operator set, got %v", raw, err)
		}
	}
}

func TestAZeroScaleHoldMeansNoHoldNotTheDefault(t *testing.T) {
	cfg, err := load(envFor(map[string]string{"SOCIALWEB_CLUSTERPROVIDER_SCALE_HOLD": "0s"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	now := time.Date(2026, 10, 7, 23, 30, 0, 0, time.UTC)
	st := scale.State{Nodes: 1, IdleSince: now}
	if d := scale.Decide(scale.Observation{}, st, now.Add(time.Second), scale.Options{Hold: cfg.ScaleHold}); d.Action != scale.Teardown {
		t.Fatalf("SCALE_HOLD=0s decided %s %q one second into idleness, want Teardown: scale.Options reads a "+
			"zero Hold as unset and waits the 2m default, so the operator's 0s silently became 2m "+
			"(user 2026-10-07: the hold is killing the dev loop)", d.Action, d.Reason)
	}
}
