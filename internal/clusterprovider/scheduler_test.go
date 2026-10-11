package clusterprovider

import (
	"errors"
	"io/fs"
	"os"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
)

func marketSpecForSchedulerConfig(t *testing.T) clusterstack.Spec {
	t.Helper()
	return clusterstack.Spec{DataDir: t.TempDir(), MarketDialled: true}
}

func TestTheSchedulerConfigIsWrittenWhereTheArgvReadsIt(t *testing.T) {
	s := marketSpecForSchedulerConfig(t)
	if err := WriteKubeSchedulerConfig(s); err != nil {
		t.Fatalf("WriteKubeSchedulerConfig: %v", err)
	}

	path := clusterstack.KubeSchedulerConfigPath(s.DataDir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s is mode %04o: k3s hands this path to kube-scheduler as it stands, and the "+
			"file names the cluster's apiserver through its kubeconfig", path, info.Mode().Perm())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	rendered, err := s.KubeSchedulerConfig()
	if err != nil {
		t.Fatalf("KubeSchedulerConfig: %v", err)
	}
	if string(body) != string(rendered) {
		t.Fatalf("the file on disk is not what the renderer produces:\n%s", body)
	}
}

func TestWritingTheSchedulerConfigTwiceChangesNothing(t *testing.T) {
	s := marketSpecForSchedulerConfig(t)
	path := clusterstack.KubeSchedulerConfigPath(s.DataDir)
	if err := WriteKubeSchedulerConfig(s); err != nil {
		t.Fatalf("first write: %v", err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := WriteKubeSchedulerConfig(s); err != nil {
		t.Fatalf("second write: %v", err)
	}
	second, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !os.SameFile(first, second) {
		t.Fatal("the second write replaced the file. Every reconcile pass writes, so a rewrite per " +
			"pass is a new inode per pass for a file k3s reads by path")
	}
}

func TestAPlainClusterWritesNoSchedulerConfig(t *testing.T) {
	s := marketSpecForSchedulerConfig(t)
	s.MarketDialled = false
	if err := WriteKubeSchedulerConfig(s); err != nil {
		t.Fatalf("WriteKubeSchedulerConfig on a plain cluster: %v", err)
	}
	if _, err := os.Stat(clusterstack.KubeSchedulerConfigPath(s.DataDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a plain cluster's argv names no scheduler config, and one was written anyway: %v", err)
	}
}
