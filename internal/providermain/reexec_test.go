//go:build linux

package providermain

import (
	"slices"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/foldmain"
)

func TestTheClusterProviderSpawnsTheCCMAsAReExecOfItsResolvedPath(t *testing.T) {
	env := joinEnv(t, map[string]string{"SOCIALWEB_CLUSTERPROVIDER_CLOUD_CONTROLLER_MANAGER": ""})
	get := func(k string) string { return env[k] }
	exe := "/home/swc/.local/state/socialweb-computer/bin/0123abcd/socialweb-computer"

	cfg, err := loadFor(get, foldmain.Invocation{Exe: exe, Verb: []string{"serve", "provider"}})
	if err != nil {
		t.Fatalf("serve provider with no CCM path: %v; the multi-call binary carries the CCM itself", err)
	}
	want := []string{exe, "internal", "cloud-controller-manager"}
	if !slices.Equal(cfg.CloudControllerManagerExec, want) {
		t.Fatalf("CCM argv = %v, want the resolved bin/<sha>/ path re-exec %v", cfg.CloudControllerManagerExec, want)
	}

	_, err = loadFor(get, foldmain.Invocation{Exe: "/usr/local/bin/socialweb-clusterprovider"})
	if err == nil || !strings.Contains(err.Error(), "SOCIALWEB_CLUSTERPROVIDER_CLOUD_CONTROLLER_MANAGER") {
		t.Fatalf("the standalone shim with no CCM path: %v; it has no internal verb to re-exec and must refuse as before", err)
	}

	env["SOCIALWEB_CLUSTERPROVIDER_CLOUD_CONTROLLER_MANAGER"] = "/opt/ccm"
	cfg, err = loadFor(get, foldmain.Invocation{Exe: exe, Verb: []string{"serve", "provider"}})
	if err != nil || !slices.Equal(cfg.CloudControllerManagerExec, []string{"/opt/ccm"}) {
		t.Fatalf("an explicit CCM path is an override: %v %v", cfg.CloudControllerManagerExec, err)
	}
}
