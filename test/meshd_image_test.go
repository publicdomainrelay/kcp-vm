package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readRepo(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestTheDoorImageShipsBothMeshdBuildsBesideTheirSidecarsAndChecksThem(t *testing.T) {
	dockerfile := readRepo(t, "deploy/control-plane-container/Dockerfile")
	k8s := dockerfile[strings.Index(dockerfile, "FROM controlplane AS k8s"):]
	if next := strings.Index(k8s[1:], "\nFROM "); next >= 0 {
		k8s = k8s[:next+1]
	}
	downloads := "/opt/org-root/socialweb-computer-kcp/socialweb-computer-k8s/web/downloads/"
	for _, arch := range []string{"x86_64", "aarch64"} {
		for _, f := range []string{"socialweb-meshd-" + arch, "socialweb-meshd-" + arch + ".sha256"} {
			if !strings.Contains(k8s, " "+f+" ") {
				t.Fatalf("the k8s stage does not copy %s from the meshd context: the door would offer no "+
					"pinned meshd and every market node would join without the pod mesh", f)
			}
		}
		if !strings.Contains(k8s, "socialweb-meshd-"+arch+".sha256") || !strings.Contains(k8s, "sha256sum -c") {
			t.Fatalf("the k8s stage does not check socialweb-meshd-%s against its sidecar", arch)
		}
	}
	if !strings.Contains(k8s, "COPY --from=meshd ") || !strings.Contains(k8s, downloads) {
		t.Fatalf("the meshd pair must land in the door's downloads dir %s", downloads)
	}
	build := readRepo(t, "deploy/control-plane-container/build.sh")
	if !strings.Contains(build, `--build-context "meshd=$MESHD"`) || !strings.Contains(build, "rust/build.sh\" x86_64 aarch64") {
		t.Fatal("build.sh k8s must build both meshd arches with rust/build.sh (or take KCP_MESHD_DIST) and pass them as the meshd context")
	}
}
