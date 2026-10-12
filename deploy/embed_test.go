package deploy_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/deploy"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/manifests"
)

func yamlOnDisk(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if ext := filepath.Ext(path); ext == ".yaml" || ext == ".yml" {
			out = append(out, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func TestEveryYAMLUnderDeployIsInExactlyOneSetOrListedAsUnapplied(t *testing.T) {
	ix, err := manifests.Load()
	if err != nil {
		t.Fatal(err)
	}
	homes := map[string][]string{}
	for _, s := range ix.Sets {
		seen := map[string]bool{}
		for _, p := range s.Phases {
			for _, f := range p.Files {
				if !seen[f.Path] {
					seen[f.Path] = true
					homes[f.Path] = append(homes[f.Path], "set "+s.Name)
				}
			}
		}
	}
	for _, u := range ix.Unapplied {
		if strings.TrimSpace(u.Why) == "" {
			t.Errorf("%s is listed as unapplied with no reason", u.Path)
		}
		homes[u.Path] = append(homes[u.Path], "unapplied")
	}
	disk := yamlOnDisk(t)
	if len(disk) == 0 {
		t.Fatal("no YAML found under deploy")
	}
	onDisk := map[string]bool{}
	for _, path := range disk {
		onDisk[path] = true
		if _, err := fs.Stat(deploy.FS, path); err != nil {
			t.Errorf("%s is under deploy and not in deploy.FS: %v", path, err)
		}
		if got := homes[path]; len(got) != 1 {
			t.Errorf("%s is in %d places %v; every YAML under deploy is in exactly one set or listed as unapplied", path, len(got), got)
		}
	}
	for path := range homes {
		if !onDisk[path] {
			t.Errorf("sets.yaml names %s, which is not a YAML under deploy", path)
		}
	}
}

func TestEverySetDecodesStrictlyIntoKcpAndKubernetesTypes(t *testing.T) {
	ix, err := manifests.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.Sets) == 0 {
		t.Fatal("sets.yaml names no set")
	}
	for _, s := range ix.Sets {
		values, err := manifests.NewValues(s.Values)
		if err != nil {
			t.Errorf("set %s: %v", s.Name, err)
			continue
		}
		fill(reflect.ValueOf(values).Elem(), s.Name)
		rendered, err := s.Render(values)
		if err != nil {
			t.Errorf("set %s: %v", s.Name, err)
			continue
		}
		objects := 0
		for _, r := range rendered {
			if r.Workspace == "" || !strings.HasPrefix(r.Workspace, "root") {
				t.Errorf("set %s phase %s renders workspace %q", s.Name, r.Phase.Name, r.Workspace)
			}
			for _, o := range r.Objects {
				if o.Typed == nil {
					t.Errorf("set %s phase %s: %s %s decoded into no typed value", s.Name, r.Phase.Name, o.Unstructured.GetKind(), o.Unstructured.GetName())
				}
				objects++
			}
		}
		if objects == 0 {
			t.Errorf("set %s renders no object", s.Name)
		}
	}
}

func fill(v reflect.Value, seed string) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("root:" + seed)
	case reflect.Int:
		v.SetInt(40000)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			fill(v.Field(i), seed+"-"+strings.ToLower(v.Type().Field(i).Name))
		}
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 2, 2)
		for i := 0; i < 2; i++ {
			fill(s.Index(i), seed+string(rune('a'+i)))
		}
		v.Set(s)
	}
}
