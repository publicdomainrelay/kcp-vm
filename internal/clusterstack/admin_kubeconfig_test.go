package clusterstack

import (
	"path/filepath"
	"strings"
	"testing"
)

func adminSpec() Spec {
	return Spec{
		DataDir:                       "/var/lib/socialweb/clusters/pdr-a",
		PostgresPort:                  15432,
		OpenBaoConfig:                 "/var/lib/socialweb/clusters/pdr-a/openbao.hcl",
		StoreEndpoint:                 "postgres://postgres@127.0.0.1:5432/k3s?sslmode=disable",
		KineListen:                    "127.0.0.1:2379",
		DatastoreEndpoint:             "http://127.0.0.1:2379",
		AdvertiseAddress:              "127.0.0.1",
		BindAddress:                   "127.0.0.1",
		AdvertisePort:                 16443,
		K3sSecurePort:                 16446,
		K3sControllerManagerPort:      16447,
		K3sSchedulerPort:              16448,
		K3sCloudControllerManagerPort: 16449,
		TokenFile:                     "/var/lib/socialweb/clusters/pdr-a/token",
		Audit: Audit{
			PolicyPath: "/var/lib/socialweb/clusters/pdr-a/audit-policy.yaml",
			LogPath:    "/var/lib/socialweb/clusters/pdr-a/audit.log",
		},
	}
}

func argvValue(argv []string, flag string) (string, bool) {
	for i, a := range argv {
		if a == flag {
			if i+1 < len(argv) {
				return argv[i+1], true
			}
			return "", true
		}
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v, true
		}
	}
	return "", false
}

func TestTheAdminKubeconfigIsWrittenPerClusterUnderTheDataDir(t *testing.T) {
	s := adminSpec()
	argv, err := s.ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	got, ok := argvValue(argv, "--write-kubeconfig")
	if !ok {
		t.Fatalf("argv = %v, want --write-kubeconfig. k3s writes its admin kubeconfig to "+
			"/etc/rancher/k3s/k3s.yaml BY DEFAULT, and that path is SHARED between two clusters "+
			"on one host: the second cluster overwrites the first cluster's admin credential, "+
			"and the provider then holds a credential for whichever cluster started last", argv)
	}
	want := AdminKubeconfig(s.DataDir)
	if got != want {
		t.Fatalf("--write-kubeconfig = %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, filepath.Join("server", "cred", AdminKubeconfigName)) {
		t.Fatalf("the admin credential lands at %q, want it under the k3s data dir's own cred "+
			"directory: the provider reads it per cluster, so a shared path is a cross-tenant "+
			"credential", got)
	}

	other := adminSpec()
	other.DataDir = "/var/lib/socialweb/clusters/pdr-b"
	otherArgv, err := other.ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	otherPath, ok := argvValue(otherArgv, "--write-kubeconfig")
	if !ok {
		t.Fatalf("argv = %v, want --write-kubeconfig", otherArgv)
	}
	if otherPath == got {
		t.Fatalf("two clusters were told to write their admin credential to the same file %q", got)
	}
}
