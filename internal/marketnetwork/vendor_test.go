package marketnetwork

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/manifests"
)

const providerWorkspacePath = "root:deno-provider"

var kcpBuiltinWorkspaceTypes = map[string]bool{"universal": true, "organization": true, "team": true}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

type vendoredObject struct {
	file string
	obj  Object
}

func renderedSetFiles(t *testing.T) map[string][]byte {
	t.Helper()
	ix, err := manifests.Load()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]any{
		"denoruntime": &manifests.None{},
		"market-network": &manifests.MarketNetwork{
			NetworkID: "b", StateDir: "/s", GuestTLSDir: "/g",
			PDSPrivateKeyHex: "aa", BobPDSPrivateKeyHex: "bb",
			Bidders:        []manifests.Bidder{{Name: "bidder-1", Handle: "bidder-1.bob.test", PrivateKeyHex: "cc"}},
			DispatcherPort: 40001, GuestTLSPort: 40002,
			Firecracker: manifests.Firecracker{NodeImage: "/n", NodeBoot: "/b", Config: "/c", RepoDir: "/r",
				Preinstall: "/p", VMM: "/v", RunnerImage: "i", WorkRoot: "/w"},
		},
	}
	out := map[string][]byte{}
	for name, v := range values {
		s, ok := ix.Set(name)
		if !ok {
			t.Fatalf("sets.yaml names no set %s", name)
		}
		for _, p := range s.Phases {
			_, texts, err := s.RenderText(p, v)
			if err != nil {
				t.Fatalf("set %s phase %s: %v", name, p.Name, err)
			}
			for _, tx := range texts {
				if _, seen := out[tx.Path]; !seen {
					out[tx.Path] = tx.Body
				}
			}
		}
	}
	return out
}

func loadVendoredObjects(t *testing.T) []vendoredObject {
	t.Helper()
	root := repoRoot(t)
	tree, err := ListVendoredTree(root)
	if err != nil {
		t.Fatalf("walking the vendored tree: %v", err)
	}
	rendered := renderedSetFiles(t)
	var out []vendoredObject
	for _, path := range tree {
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			continue
		}
		var objs []Object
		var err error
		if body, ok := rendered[strings.TrimPrefix(path, "deploy/")]; ok {
			objs, err = Decode(body)
		} else {
			objs, err = DecodeFile(filepath.Join(root, path))
		}
		if err != nil {
			t.Errorf("%s does not decode as YAML: %v", path, err)
			continue
		}
		for _, o := range objs {
			out = append(out, vendoredObject{file: path, obj: o})
		}
	}
	return out
}

func TestEveryVendoredManifestDecodesAgainstTheVendoredSchemas(t *testing.T) {
	objs := loadVendoredObjects(t)

	schemas := map[string]ResourceSchema{}
	exports := map[string]apisv1alpha2.APIExport{}
	workspaceTypes := map[string]bool{}
	for _, vo := range objs {
		switch vo.obj.Kind() {
		case "APIResourceSchema":
			var typed apisv1alpha1.APIResourceSchema
			if err := StrictInto(vo.obj, &typed); err != nil {
				t.Errorf("%s: %s does not decode as a kcp APIResourceSchema: %v", vo.file, vo.obj.Name(), err)
			}
			rs, err := ParseResourceSchema(vo.obj)
			if err != nil {
				t.Errorf("%s: %v", vo.file, err)
				continue
			}
			schemas[rs.Name] = rs
		case "APIExport":
			var typed apisv1alpha2.APIExport
			if err := StrictInto(vo.obj, &typed); err != nil {
				t.Errorf("%s: %s does not decode as a kcp APIExport: %v", vo.file, vo.obj.Name(), err)
				continue
			}
			exports[typed.Name] = typed
		case "WorkspaceType":
			workspaceTypes[vo.obj.Name()] = true
		}
	}
	if len(schemas) == 0 || len(exports) == 0 || len(workspaceTypes) == 0 {
		t.Fatalf("the vendored tree carries %d APIResourceSchemas, %d APIExports and %d WorkspaceTypes; "+
			"the market example cannot be checked against nothing", len(schemas), len(exports), len(workspaceTypes))
	}

	exported := map[string]ResourceSchema{}
	for _, e := range exports {
		for _, res := range e.Spec.Resources {
			rs, ok := schemas[res.Schema]
			if !ok {
				t.Errorf("APIExport %s exports %s.%s from schema %q, which is not vendored", e.Name, res.Name, res.Group, res.Schema)
				continue
			}
			if rs.Group != res.Group || rs.Plural != res.Name {
				t.Errorf("APIExport %s names %s.%s and its schema %s describes %s.%s", e.Name, res.Name, res.Group, rs.Name, rs.Plural, rs.Group)
			}
			exported[rs.Group+"/"+rs.Kind] = rs
		}
	}

	denoPods := 0
	for _, vo := range objs {
		o := vo.obj
		switch o.Kind() {
		case "APIResourceSchema", "APIExport":
		case "WorkspaceType":
			var typed tenancyv1alpha1.WorkspaceType
			if err := StrictInto(o, &typed); err != nil {
				t.Errorf("%s: %s does not decode as a kcp WorkspaceType: %v", vo.file, o.Name(), err)
				continue
			}
			for _, b := range typed.Spec.DefaultAPIBindings {
				if _, ok := exports[b.Export]; !ok {
					t.Errorf("%s: WorkspaceType %s binds export %q, which is not vendored", vo.file, typed.Name, b.Export)
				}
				if b.Path != providerWorkspacePath {
					t.Errorf("%s: WorkspaceType %s binds %s from %q; install-provider.sh puts every export in %s",
						vo.file, typed.Name, b.Export, b.Path, providerWorkspacePath)
				}
			}
			if typed.Spec.Extend.With != nil {
				for _, w := range typed.Spec.Extend.With {
					if !workspaceTypes[string(w.Name)] && !kcpBuiltinWorkspaceTypes[string(w.Name)] {
						t.Errorf("%s: WorkspaceType %s extends %q, which is neither vendored nor a kcp built-in", vo.file, typed.Name, w.Name)
					}
				}
			}
		case "Workspace":
			var typed tenancyv1alpha1.Workspace
			if err := StrictInto(o, &typed); err != nil {
				t.Errorf("%s: %s does not decode as a kcp Workspace: %v", vo.file, o.Name(), err)
				continue
			}
			if typed.Spec.Type == nil || !workspaceTypes[string(typed.Spec.Type.Name)] {
				t.Errorf("%s: Workspace %s names a type that is not vendored", vo.file, typed.Name)
			}
		case "ServiceAccount":
			var typed corev1.ServiceAccount
			if err := StrictInto(o, &typed); err != nil {
				t.Errorf("%s: %s: %v", vo.file, o.Name(), err)
			}
		case "ClusterRole":
			var typed rbacv1.ClusterRole
			if err := StrictInto(o, &typed); err != nil {
				t.Errorf("%s: %s: %v", vo.file, o.Name(), err)
			}
		case "ClusterRoleBinding":
			var typed rbacv1.ClusterRoleBinding
			if err := StrictInto(o, &typed); err != nil {
				t.Errorf("%s: %s: %v", vo.file, o.Name(), err)
			}
		default:
			rs, ok := exported[o.Group()+"/"+o.Kind()]
			if !ok {
				t.Errorf("%s: %s %s is a kind no vendored APIExport serves", vo.file, o.APIVersion(), o.Kind())
				continue
			}
			version := strings.TrimPrefix(o.APIVersion(), o.Group()+"/")
			schema, ok := rs.Versions[version]
			if !ok {
				t.Errorf("%s: %s %s names version %q and schema %s has none", vo.file, o.Kind(), o.Name(), version, rs.Name)
				continue
			}
			var md struct {
				Metadata metav1.ObjectMeta `json:"metadata"`
			}
			if err := StrictInto(Object{"metadata": o["metadata"]}, &md); err != nil {
				t.Errorf("%s: %s %s metadata: %v", vo.file, o.Kind(), o.Name(), err)
			}
			for _, p := range ValidateObject(schema, o) {
				t.Errorf("%s: %s %s: %s", vo.file, o.Kind(), o.Name(), p)
			}
			if o.Kind() == "DenoPod" {
				denoPods++
			}
		}
	}
	if denoPods == 0 {
		t.Error("the vendored market example carries no DenoPod")
	}
}

func TestTheSchemaCheckRefusesWhatKubeWouldPruneOrReject(t *testing.T) {
	schema := &Schema{
		Type: "object",
		Properties: map[string]*Schema{
			"spec": {
				Type:     "object",
				Required: []string{"script"},
				Properties: map[string]*Schema{
					"script":        {Type: "string"},
					"restartPolicy": {Type: "string", Enum: []any{"Always", "Never"}},
					"env":           {Type: "object", AdditionalProperties: []byte(`{"type":"string"}`)},
					"permissions":   {Type: "object", PreserveUnknown: true},
				},
			},
		},
	}
	good := Object{"spec": map[string]any{"script": "x", "env": map[string]any{"A": "b"}, "permissions": map[string]any{"any": true}}}
	if p := ValidateObject(schema, good); len(p) != 0 {
		t.Fatalf("a valid object was refused: %v", p)
	}
	for name, bad := range map[string]Object{
		"missing required": {"spec": map[string]any{}},
		"enum":             {"spec": map[string]any{"script": "x", "restartPolicy": "Sometimes"}},
		"pruned field":     {"spec": map[string]any{"script": "x", "scirpt": "y"}},
		"env not string":   {"spec": map[string]any{"script": "x", "env": map[string]any{"A": float64(1)}}},
	} {
		if p := ValidateObject(schema, bad); len(p) == 0 {
			t.Errorf("%s: an invalid object passed", name)
		}
	}
}
