package deploy_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/manifests"
)

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
