package xosgate

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/johnandersen777/socialweb-computer-kcp"

var osNeutralPackages = []string{
	module + "/api/...",
	module + "/internal/clusterprovider",
	module + "/internal/cloudprovider",
	module + "/internal/nodeprovider",
	module + "/internal/nodemarket",
	module + "/internal/materialiser",
	module + "/internal/kcpclient",
	module + "/internal/tenantclient",
	module + "/internal/gateway",
}

var linuxBackends = []string{
	"github.com/coreos/go-systemd",
	"github.com/vishvananda/netlink",
	"github.com/firecracker-microvm",
	module + "/internal/nodefirecracker",
	module + "/internal/fcapi",
}

var targets = []string{
	"linux/amd64",
	"linux/arm64",
	"darwin/amd64",
	"darwin/arm64",
	"windows/amd64",
	"windows/arm64",
}

type listedPackage struct {
	ImportPath string
	DepOnly    bool
	Deps       []string
	Error      *struct{ Err string }
}

type violation struct {
	target  string
	pkg     string
	backend string
}

func backendOf(dep string) (string, bool) {
	for _, backend := range linuxBackends {
		if dep == backend || strings.HasPrefix(dep, backend+"/") {
			return backend, true
		}
	}
	return "", false
}

func listRoots(t *testing.T, target string, patterns []string) []listedPackage {
	t.Helper()
	goos, goarch, ok := strings.Cut(target, "/")
	if !ok {
		t.Fatalf("target %q is not GOOS/GOARCH", target)
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("the import boundary is read from `go list -deps -json`, and no go is on PATH: %v", err)
	}
	args := append([]string{"list", "-e", "-deps", "-json"}, patterns...)
	cmd := exec.Command(gobin, args...)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "GOFLAGS=-mod=readonly")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list for %s: %v\n%s", target, err, stderr.String())
	}
	roots := []listedPackage{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listedPackage
		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decoding go list for %s: %v", target, err)
		}
		if p.DepOnly {
			continue
		}
		if p.Error != nil {
			t.Fatalf("%s does not load for %s, so its imports cannot be checked: %s", p.ImportPath, target, p.Error.Err)
		}
		roots = append(roots, p)
	}
	if len(roots) == 0 {
		t.Fatalf("go list for %s matched no package in %v", target, patterns)
	}
	return roots
}

func boundaryViolations(t *testing.T, target string, patterns []string) []violation {
	t.Helper()
	found := []violation{}
	for _, root := range listRoots(t, target, patterns) {
		for _, dep := range root.Deps {
			if backend, ok := backendOf(dep); ok {
				found = append(found, violation{target: target, pkg: root.ImportPath, backend: backend})
			}
		}
	}
	return found
}

func TestOSNeutralPackagesImportNoLinuxBackend(t *testing.T) {
	for _, target := range targets {
		for _, v := range boundaryViolations(t, target, osNeutralPackages) {
			t.Errorf("%s: %s is OS-neutral and imports %s, a Linux backend; the backend belongs behind a "+
				"_linux.go file in its own package, reached through an interface", v.target, v.pkg, v.backend)
		}
	}
}

func TestBoundaryFlagsAGoSystemdImportInAFixture(t *testing.T) {
	for _, target := range targets {
		flagged := false
		for _, v := range boundaryViolations(t, target, []string{"./testdata/systemdleak"}) {
			if v.backend == "github.com/coreos/go-systemd" {
				flagged = true
			}
		}
		if !flagged {
			t.Errorf("%s: testdata/systemdleak imports go-systemd and the boundary check did not flag it, "+
				"so TestOSNeutralPackagesImportNoLinuxBackend passing proves nothing", target)
		}
	}
}
