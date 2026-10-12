package denokcp

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/factory/servicenames"
)

func denoPodManifests(t *testing.T, root string) map[string]v1alpha1.DenoPod {
	t.Helper()
	found := map[string]v1alpha1.DenoPod{}
	err := filepath.WalkDir(filepath.Join(root, "deploy"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || (filepath.Ext(path) != ".yaml" && filepath.Ext(path) != ".yml") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, doc := range bytes.Split(body, []byte("\n---")) {
			if !bytes.Contains(doc, []byte("kind: DenoPod\n")) {
				continue
			}
			var pod v1alpha1.DenoPod
			if err := yaml.Unmarshal(doc, &pod); err != nil {
				t.Errorf("%s document %d: %v", path, i, err)
				continue
			}
			if pod.Kind != "DenoPod" {
				continue
			}
			rel, _ := filepath.Rel(root, path)
			found[rel+"#"+pod.Name] = pod
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestNoServiceManifestNamesAPort(t *testing.T) {
	pods := denoPodManifests(t, repoRoot(t))
	if len(pods) == 0 {
		t.Fatal("no DenoPod manifest under deploy/, the scan would pass on nothing")
	}
	for where, pod := range pods {
		if pod.Annotations[v1alpha1.AnnotationEdge] != "" {
			continue
		}
		if fixed := servicenames.FixedPorts(pod.Spec.Env); len(fixed) > 0 {
			t.Errorf("%s binds a fixed port behind the edge (%s); bind 0 and let peers resolve the reported listener, "+
				"or annotate %s with the reason it is an edge entry point", where, strings.Join(fixed, ", "), v1alpha1.AnnotationEdge)
		}
	}
}
