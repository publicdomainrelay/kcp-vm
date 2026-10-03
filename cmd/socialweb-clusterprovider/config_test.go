//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
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

func placementEnv(t *testing.T) map[string]string {
	t.Helper()
	image := builtNodeImageFixture(t)
	return map[string]string{
		"SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE":     "firecracker",
		"SOCIALWEB_CLUSTERPROVIDER_HOST":          thisHost(t),
		"SOCIALWEB_CLUSTERPROVIDER_NETWORK":       "pasta",
		"SOCIALWEB_CLUSTERPROVIDER_IMAGE_OUT_DIR": image.outDir,
		"SOCIALWEB_CLUSTERPROVIDER_IMAGE_CONFIG":  image.configPath,
		"SOCIALWEB_CLUSTERPROVIDER_FIRECRACKER":   "/usr/bin/firecracker",
		"SOCIALWEB_CLUSTERPROVIDER_NODE_AGENT":    "/usr/bin/socialweb-nodeagent",
	}
}

func joinEnv(t *testing.T, extra map[string]string) map[string]string {
	t.Helper()
	env := map[string]string{
		"SOCIALWEB_KCP_HOST":                          "https://127.0.0.1:6443",
		"SOCIALWEB_KCP_KUBECONFIG":                    "/tmp/live-kcp.yaml",
		"SOCIALWEB_CLUSTERPROVIDER_LEASE_PATH":        "/tmp/ports.json",
		"SOCIALWEB_CLUSTERPROVIDER_JOIN":              "liveweb",
		"SOCIALWEB_GATEWAY_MINT_KUBECONFIGS":          t.TempDir(),
		"SOCIALWEB_CLUSTERPROVIDER_DATASTORE_HAS_ROW": "true",
		"SOCIALWEB_CLUSTERPROVIDER_STATE_DIR":         t.TempDir(),
	}
	for k, v := range placementEnv(t) {
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

func TestAPlacementIsRequiredAndRefusedRatherThanDefaulted(t *testing.T) {
	if _, err := loadJoin(t, nil); err != nil {
		t.Fatalf("a fully configured join was refused: %v", err)
	}

	for _, tc := range []struct {
		name  string
		set   map[string]string
		names []string
	}{
		{"no substrate", map[string]string{"SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE": ""},
			[]string{"SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE"}},
		{"an unknown substrate", map[string]string{"SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE": "jail"},
			[]string{"SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE", "jail", "firecracker"}},
		{"a host that is not this one", map[string]string{"SOCIALWEB_CLUSTERPROVIDER_HOST": thisHost(t) + "-not"},
			[]string{"SOCIALWEB_CLUSTERPROVIDER_HOST", "not"}},
		{"no host", map[string]string{"SOCIALWEB_CLUSTERPROVIDER_HOST": ""},
			[]string{"SOCIALWEB_CLUSTERPROVIDER_HOST"}},
		{"no network", map[string]string{"SOCIALWEB_CLUSTERPROVIDER_NETWORK": ""},
			[]string{"SOCIALWEB_CLUSTERPROVIDER_NETWORK"}},
		{"an unknown network", map[string]string{"SOCIALWEB_CLUSTERPROVIDER_NETWORK": "host"},
			[]string{"SOCIALWEB_CLUSTERPROVIDER_NETWORK", "host", "pasta"}},
		{"no image store", map[string]string{"SOCIALWEB_CLUSTERPROVIDER_IMAGE_OUT_DIR": ""},
			[]string{"SOCIALWEB_CLUSTERPROVIDER_IMAGE_OUT_DIR"}},
		{"no image configuration", map[string]string{"SOCIALWEB_CLUSTERPROVIDER_IMAGE_CONFIG": ""},
			[]string{"SOCIALWEB_CLUSTERPROVIDER_IMAGE_CONFIG"}},
		{"an image configuration that is not one the builder wrote",
			map[string]string{"SOCIALWEB_CLUSTERPROVIDER_IMAGE_CONFIG": "/nonexistent/nodeimage.json"},
			[]string{"SOCIALWEB_CLUSTERPROVIDER_IMAGE_CONFIG", "/nonexistent/nodeimage.json"}},
		{"an advertise address on a substrate whose nodes are guests",
			map[string]string{"SOCIALWEB_CLUSTERPROVIDER_ADVERTISE_ADDRESS": "192.168.5.2"},
			[]string{"SOCIALWEB_CLUSTERPROVIDER_ADVERTISE_ADDRESS", "192.168.5.2", "firecracker"}},
	} {
		_, err := loadJoin(t, tc.set)
		if err == nil {
			t.Errorf("%s was accepted, and there is no default for it: a placement that was "+
				"guessed is one this gateway cannot honour and cannot be caught not honouring", tc.name)
			continue
		}
		for _, want := range tc.names {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal does not name %q, so an operator cannot tell which "+
					"setting it is about: %v", tc.name, want, err)
			}
		}
	}
}

func TestFirecrackerNeedsItsWholePlacementAndIsNotFallenBackFrom(t *testing.T) {
	_, err := loadJoin(t, map[string]string{
		"SOCIALWEB_CLUSTERPROVIDER_FIRECRACKER": "",
		"SOCIALWEB_CLUSTERPROVIDER_NODE_AGENT":  "",
	})
	if err == nil {
		t.Fatal("the firecracker substrate was accepted with no VMM and no agent: a guest is the " +
			"VMM, the agent and the image, and a join that accepted it would report a node it " +
			"never booted")
	}
	for _, want := range []string{
		"SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE",
		"SOCIALWEB_CLUSTERPROVIDER_FIRECRACKER",
		"SOCIALWEB_CLUSTERPROVIDER_NODE_AGENT",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q, so an operator cannot tell which setting it "+
				"is about: %v", want, err)
		}
	}
	for _, other := range []string{"process", "local"} {
		if strings.Contains(err.Error(), other) {
			t.Errorf("the refusal names %q, which reads as a fallback to another substrate. "+
				"Firecracker is the only one implemented today, and a fallback would place the "+
				"node somewhere its record, its kubelet port and its teardown do not describe: %v",
				other, err)
		}
	}
}

func TestQEMUIsCheckedTheSameWayAsFirecrackerAndIsNotFallenBackFrom(t *testing.T) {
	qimage := builtNodeImageFixtureFor(t, SubstrateQEMU)
	qemu := func(extra map[string]string) map[string]string {
		env := map[string]string{
			"SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE":     "qemu",
			"SOCIALWEB_CLUSTERPROVIDER_FIRECRACKER":   "",
			"SOCIALWEB_CLUSTERPROVIDER_QEMU":          "/usr/bin/qemu-system-x86_64",
			"SOCIALWEB_CLUSTERPROVIDER_IMAGE_OUT_DIR": qimage.outDir,
			"SOCIALWEB_CLUSTERPROVIDER_IMAGE_CONFIG":  qimage.configPath,
		}
		for k, v := range extra {
			env[k] = v
		}
		return env
	}
	cfg, err := loadJoin(t, qemu(nil))
	if err != nil {
		t.Fatalf("a fully configured qemu join was refused: %v", err)
	}
	if cfg.Substrate != SubstrateQEMU || cfg.QEMU != "/usr/bin/qemu-system-x86_64" {
		t.Fatalf("the qemu placement was not carried into the Config: %+v", cfg)
	}

	for _, tc := range []struct {
		name  string
		set   map[string]string
		names []string
	}{
		{"no qemu binary", qemu(map[string]string{"SOCIALWEB_CLUSTERPROVIDER_QEMU": ""}),
			[]string{"SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE", "SOCIALWEB_CLUSTERPROVIDER_QEMU"}},
		{"an advertise address on a substrate whose nodes are guests",
			qemu(map[string]string{"SOCIALWEB_CLUSTERPROVIDER_ADVERTISE_ADDRESS": "192.168.5.2"}),
			[]string{"SOCIALWEB_CLUSTERPROVIDER_ADVERTISE_ADDRESS", "192.168.5.2",
				"SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE"}},
		{"a firecracker binary under qemu, where it cannot be in force",
			qemu(map[string]string{"SOCIALWEB_CLUSTERPROVIDER_FIRECRACKER": "/usr/bin/firecracker"}),
			[]string{"SOCIALWEB_CLUSTERPROVIDER_FIRECRACKER", "/usr/bin/firecracker"}},
	} {
		_, err := loadJoin(t, tc.set)
		if err == nil {
			t.Errorf("%s was accepted, and a placement that was guessed or partly inert is one "+
				"this gateway cannot honour and cannot be caught not honouring", tc.name)
			continue
		}
		for _, want := range tc.names {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal does not name %q, so an operator cannot tell which "+
					"setting it is about: %v", tc.name, want, err)
			}
		}
	}

	_, err = loadJoin(t, map[string]string{
		"SOCIALWEB_CLUSTERPROVIDER_QEMU": "/usr/bin/qemu-system-x86_64",
	})
	if err == nil {
		t.Fatal("a qemu binary was accepted under the firecracker substrate: it is never " +
			"executed there, and an operator who set it and saw no complaint would believe it was " +
			"carrying it")
	}
	if !strings.Contains(err.Error(), "SOCIALWEB_CLUSTERPROVIDER_QEMU") {
		t.Errorf("the refusal does not name the inert setting: %v", err)
	}
}

func TestAnImageConfigBuiltForAnotherVMMIsRefusedAtStartup(t *testing.T) {
	f := builtNodeImageFixture(t)
	_, err := loadJoin(t, map[string]string{
		"SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE":     "qemu",
		"SOCIALWEB_CLUSTERPROVIDER_FIRECRACKER":   "",
		"SOCIALWEB_CLUSTERPROVIDER_QEMU":          "/usr/bin/qemu-system-x86_64",
		"SOCIALWEB_CLUSTERPROVIDER_IMAGE_OUT_DIR": f.outDir,
		"SOCIALWEB_CLUSTERPROVIDER_IMAGE_CONFIG":  f.configPath,
	})
	if err == nil {
		t.Fatal("a qemu placement was accepted with a configuration that builds a firecracker " +
			"image: the guest booted from it would come up and join, and nothing downstream would " +
			"name the image that moved")
	}
	for _, want := range []string{"SOCIALWEB_CLUSTERPROVIDER_IMAGE_CONFIG", "firecracker", "qemu"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q, so an operator cannot see which setting or "+
				"VMM it is about: %v", want, err)
		}
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
	if cfg.Substrate != "" || cfg.Host != "" || cfg.Network != "" || cfg.ImageOutDir != "" ||
		cfg.ImageConfig != "" {
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
