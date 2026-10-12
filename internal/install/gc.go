package install

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type GCReport struct {
	Kept    map[string]string `json:"kept"`
	Removed []string          `json:"removed"`
}

func (o UpgradeOptions) binRef() *regexp.Regexp {
	return regexp.MustCompile(regexp.QuoteMeta(o.bin()) + `/([0-9a-f]{64})/`)
}

func (o UpgradeOptions) pinnedByProcesses(ref *regexp.Regexp, kept map[string]string) {
	entries, err := os.ReadDir(o.procRoot())
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.Trim(e.Name(), "0123456789") != "" {
			continue
		}
		exe, err := os.Readlink(filepath.Join(o.procRoot(), e.Name(), "exe"))
		if err != nil {
			continue
		}
		if m := ref.FindStringSubmatch(exe); m != nil {
			if _, ok := kept[m[1]]; !ok {
				kept[m[1]] = "the running process " + e.Name() + " executes it"
			}
		}
	}
}

func pinnedByUnitFiles(dir string, ref *regexp.Regexp, kept map[string]string) {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, m := range ref.FindAllStringSubmatch(string(b), -1) {
			if _, ok := kept[m[1]]; !ok {
				kept[m[1]] = "the unit file " + p + " names it"
			}
		}
		return nil
	})
}

func (o UpgradeOptions) procRoot() string {
	if o.Proc != "" {
		return o.Proc
	}
	return "/proc"
}

func GC(o UpgradeOptions) (GCReport, error) {
	rep := GCReport{Kept: map[string]string{}}
	for _, link := range []string{"current", "previous"} {
		if t := readTarget(filepath.Join(o.bin(), link)); t != "" {
			rep.Kept[t] = "bin/" + link
		}
	}
	ref := o.binRef()
	o.pinnedByProcesses(ref, rep.Kept)
	dirs := []string{filepath.Join(o.Home, UserUnitDir)}
	if o.RuntimeDir != "" {
		dirs = append(dirs, filepath.Join(o.RuntimeDir, "systemd"))
	}
	for _, d := range dirs {
		pinnedByUnitFiles(d, ref, rep.Kept)
	}
	entries, err := os.ReadDir(o.bin())
	if err != nil {
		return rep, err
	}
	for _, e := range entries {
		if !e.IsDir() || !digestName.MatchString(e.Name()) {
			continue
		}
		if _, ok := rep.Kept[e.Name()]; ok {
			continue
		}
		if err := os.RemoveAll(filepath.Join(o.bin(), e.Name())); err != nil {
			return rep, err
		}
		rep.Removed = append(rep.Removed, e.Name())
	}
	sort.Strings(rep.Removed)
	return rep, nil
}

func (o UpgradeOptions) collect() {
	rep, err := GC(o)
	if err != nil {
		fmt.Fprintf(o.out(), "gc: %v\n", err)
		return
	}
	keys := make([]string, 0, len(rep.Kept))
	for k := range rep.Kept {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(o.out(), "gc: keep bin/%s (%s)\n", k, rep.Kept[k])
	}
	for _, r := range rep.Removed {
		fmt.Fprintf(o.out(), "gc: removed bin/%s\n", r)
	}
}
