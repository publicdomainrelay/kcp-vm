package assets

import (
	"go/build/constraint"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/versions"
)

var knownTargets = []string{
	"linux/amd64", "linux/arm64", "linux/riscv64", "linux/386",
	"darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64", "freebsd/amd64",
}

func buildLine(t *testing.T, file string) (constraint.Expr, string) {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if constraint.IsGoBuild(line) {
			e, err := constraint.Parse(line)
			if err != nil {
				t.Fatal(err)
			}
			return e, string(b)
		}
	}
	return nil, string(b)
}

func selects(e constraint.Expr, target string, release bool) bool {
	goos, goarch, _ := strings.Cut(target, "/")
	return e.Eval(func(tag string) bool {
		switch tag {
		case "release":
			return release
		case goos, goarch:
			return true
		case "unix":
			return goos != "windows"
		}
		return false
	})
}

func TestEveryReleaseEmbedFileNamesExactlyItsPlatformsAssetSet(t *testing.T) {
	ps, err := versions.Platforms()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]constraint.Expr{}
	embedRe := regexp.MustCompile(`(?m)^//go:embed (\S+)$`)
	platRe := regexp.MustCompile(`platform = "([^"]+)"`)
	for plat, p := range ps {
		if p.Status != versions.PlatformRelease {
			continue
		}
		u := strings.ReplaceAll(plat, "/", "_")
		file := "assets_" + u + ".go"
		e, src := buildLine(t, file)
		if e == nil {
			t.Fatalf("%s: no //go:build line", file)
		}
		if want := "release && " + strings.ReplaceAll(plat, "/", " && "); e.String() != want {
			t.Errorf("%s: //go:build %s, want %s", file, e, want)
		}
		if m := embedRe.FindStringSubmatch(src); m == nil || m[1] != "blobs/"+u {
			t.Errorf("%s embeds %v, want exactly blobs/%s", file, m, u)
		}
		if m := platRe.FindStringSubmatch(src); m == nil || m[1] != plat {
			t.Errorf("%s names platform %v, want %s", file, m, plat)
		}
		if _, err := os.Stat(filepath.Join("blobs", u, "placeholder")); err != nil {
			t.Errorf("blobs/%s/placeholder is missing; a release build with no staged blob must still compile and then refuse to start", u)
		}
		files[file] = e
		pins, err := versions.PlatformPins(plat)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{}
		for _, pin := range pins {
			want[BlobName(pin.Name)] = true
		}
		staged, _ := filepath.Glob(filepath.Join("blobs", u, "*"+blobSuffix))
		for _, s := range staged {
			if !want[filepath.Base(s)] {
				t.Errorf("blobs/%s holds %s, which is not in the %s asset set; it would be embedded unpinned", u, filepath.Base(s), plat)
			}
		}
		all, _ := versions.Assets()
		var expect []string
		for name, a := range all {
			if a.Pending == "" {
				if _, ok := a.Platforms[plat]; ok {
					expect = append(expect, name)
				}
			}
		}
		sort.Strings(expect)
		got := append([]string(nil), p.Assets...)
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(expect, ",") {
			t.Errorf("%s asset set %v, want every non-pending asset pinned for it %v", plat, got, expect)
		}
	}
	for _, f := range []string{"assets_release_unsupported.go", "assets_dev.go", "release.go"} {
		e, _ := buildLine(t, f)
		if e == nil {
			t.Fatalf("%s: no //go:build line", f)
		}
		files[f] = e
	}
	for _, target := range knownTargets {
		for _, release := range []bool{true, false} {
			var platformFiles []string
			for f, e := range files {
				if f == "release.go" || f == "assets_dev.go" {
					continue
				}
				if selects(e, target, release) {
					platformFiles = append(platformFiles, f)
				}
			}
			switch {
			case release && len(platformFiles) != 1:
				t.Errorf("%s release: %v supply the embed; want exactly one", target, platformFiles)
			case !release && len(platformFiles) != 0:
				t.Errorf("%s dev: %v embed blobs; a dev build embeds none", target, platformFiles)
			}
			if selects(files["release.go"], target, release) == selects(files["assets_dev.go"], target, release) {
				t.Errorf("%s release=%v: exactly one of release.go and assets_dev.go must build", target, release)
			}
		}
	}
}
