//go:build linux

package clusterprovider

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

func TestTheCloudControllerManagerKubeconfigIsWrittenAtomicallyAndRefusesAPathItCannotUse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cloud-controller-manager.kubeconfig")
	cfg := &rest.Config{
		Host: "https://127.0.0.1:20003",
		TLSClientConfig: rest.TLSClientConfig{
			CAData:   []byte("ca"),
			CertData: []byte("cert"),
			KeyData:  []byte("key"),
		},
	}
	if err := WriteCloudControllerManagerKubeconfig(path, cfg); err != nil {
		t.Fatalf("WriteCloudControllerManagerKubeconfig: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Mode().Perm() != cloudControllerManagerKubeconfigMode {
		t.Fatalf("mode = %o, want %o: the file carries a client key",
			info.Mode().Perm(), cloudControllerManagerKubeconfigMode)
	}
	parsed, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatalf("the written kubeconfig does not parse: %v", err)
	}
	if parsed.Host != cfg.Host {
		t.Fatalf("server = %q, want %q", parsed.Host, cfg.Host)
	}

	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = WriteCloudControllerManagerKubeconfig(filepath.Join(blocker, "ccm.kubeconfig"), cfg)
	if !errors.Is(err, ErrCloudControllerManagerKubeconfigUnwritable) {
		t.Fatalf("writing the credential somewhere it cannot be written = %v, want "+
			"ErrCloudControllerManagerKubeconfigUnwritable", err)
	}
	if !strings.Contains(err.Error(), "cloud controller manager") {
		t.Fatalf("the refusal does not name the component whose credential failed: %v", err)
	}
}

func TestTheCloudControllerManagerStepCarriesItsConfigInEnvNotArgv(t *testing.T) {
	s := specWithCCM()
	steps, err := StepsFrom(s, leases(), Stack{procgroup.ComponentK3s, procgroup.ComponentCloudControllerManager})
	if err != nil {
		t.Fatalf("StepsFrom: %v", err)
	}
	var ccm *procgroup.Step
	for i := range steps {
		if steps[i].Name == procgroup.ComponentCloudControllerManager {
			ccm = &steps[i]
		}
	}
	if ccm == nil {
		t.Fatalf("the stack names the cloud controller manager and no step was built: %v", names(steps))
	}
	if len(ccm.Spec.Argv) != 1 || ccm.Spec.Argv[0] != s.CloudControllerManagerBinary {
		t.Fatalf("argv = %v, want only the binary: the credential path is a secret and no secret "+
			"rides argv (invariant 3)", ccm.Spec.Argv)
	}
	for _, want := range []string{
		EnvCloudControllerManagerKubeconfig + "=" + clusterstack.CloudControllerManagerKubeconfig(s.DataDir),
		EnvCloudControllerManagerCluster + "=" + s.Cluster,
		EnvCloudControllerManagerInstances + "=" + clusterstack.NodeSnapshotPath(s.DataDir),
	} {
		if !envHas(ccm.Spec.Env, want) {
			t.Fatalf("the step's Env does not carry %q, so the controller cannot read its cluster, "+
				"its credential or its instance file: %v", want, ccm.Spec.Env)
		}
	}
	for _, arg := range ccm.Spec.Argv {
		if strings.Contains(arg, "kubeconfig") {
			t.Fatalf("argv %q names the credential: invariants 3 and 13 forbid it", arg)
		}
	}
	if ccm.Ready == nil {
		t.Fatal("the cloud controller manager step has no readiness gate")
	}
}

func envHas(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func specWithCCM() clusterstack.Spec {
	s := spec()
	s.Cluster = "liveweb"
	s.CloudControllerManagerBinary = "/usr/local/bin/socialweb-cloud-controller-manager"
	return s
}
