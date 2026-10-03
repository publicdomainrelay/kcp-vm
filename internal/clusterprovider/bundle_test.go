//go:build linux

package clusterprovider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/identitybundle"
)

func authorClusterIdentity(t *testing.T, dataDir string) {
	t.Helper()
	for _, rel := range identitybundle.Paths {
		full := filepath.Join(dataDir, "server", rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte("k3s authored "+rel+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

func TestTheSeedLandsExactlyWhereK3sLooksForItsTLS(t *testing.T) {
	stateDir := t.TempDir()
	dataDir, err := ClusterDataDir(stateDir, "liveweb")
	if err != nil {
		t.Fatalf("ClusterDataDir: %v", err)
	}
	authorClusterIdentity(t, dataDir)
	if err := HarvestBundle(stateDir, "liveweb"); err != nil {
		t.Fatalf("HarvestBundle: %v", err)
	}

	fingerprint := readIdentity(t, dataDir)
	if err := os.RemoveAll(filepath.Join(dataDir, "server")); err != nil {
		t.Fatalf("wiping the server dir: %v", err)
	}
	if err := SeedBundle(stateDir, "liveweb"); err != nil {
		t.Fatalf("SeedBundle: %v", err)
	}

	tlsDir := clusterstack.TLSDir(dataDir)
	for _, rel := range identitybundle.Paths {
		full := filepath.Join(dataDir, "server", rel)
		if _, err := os.Stat(full); err != nil {
			t.Fatalf("the seed put %s somewhere k3s never reads: %v. identitybundle.Paths are "+
				"relative to a root no caller supplies, and clusterstack.TLSDir puts the same files "+
				"at %s, so a seeder rooted anywhere else writes CAs where k3s never looks and the "+
				"identical CA fingerprint holds only on paper", rel, err, tlsDir)
		}
	}
	if _, err := os.Stat(filepath.Join(tlsDir, "server-ca.crt")); err != nil {
		t.Fatalf("the seed did not land on clusterstack.TLSDir(%s): %v", dataDir, err)
	}
	if got := readIdentity(t, dataDir); got != fingerprint {
		t.Fatalf("the seeded identity differs from the harvested one:\n%s\n%s", got, fingerprint)
	}
}

func TestThePersistedBundleIsExactlyTheThirteenK3sAuthors(t *testing.T) {
	stateDir := t.TempDir()
	dataDir, err := ClusterDataDir(stateDir, "liveweb")
	if err != nil {
		t.Fatalf("ClusterDataDir: %v", err)
	}
	authorClusterIdentity(t, dataDir)
	if err := HarvestBundle(stateDir, "liveweb"); err != nil {
		t.Fatalf("HarvestBundle: %v", err)
	}
	b, err := PersistedBundle(stateDir, "liveweb")
	if err != nil {
		t.Fatalf("PersistedBundle: %v", err)
	}
	if len(b.Entries) != len(identitybundle.Paths) {
		t.Fatalf("the persisted bundle holds %d entries, want %d: a short bundle makes k3s "+
			"REGENERATE the missing CAs and invalidate every kubeconfig and ServiceAccount token",
			len(b.Entries), len(identitybundle.Paths))
	}
	dir, err := BundleDir(stateDir, "liveweb")
	if err != nil {
		t.Fatalf("BundleDir: %v", err)
	}
	if filepath.Dir(dir) != dataDir {
		t.Fatalf("the bundle is persisted at %s, want it under the cluster's own data dir %s: a "+
			"bundle root shared between clusters seeds one cluster's CA into another's", dir, dataDir)
	}
}

func readIdentity(t *testing.T, dataDir string) string {
	t.Helper()
	out := ""
	for _, rel := range identitybundle.Paths {
		body, err := os.ReadFile(filepath.Join(dataDir, "server", rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		out += rel + " " + identitybundle.Sum(body) + "\n"
	}
	return out
}
