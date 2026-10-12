package apigen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

const (
	computeVMFixtureDir  = "../../testdata/computevm"
	computeVMSchemaName  = "v1alpha1-1.computevms.socialweb.computer"
	computeVMSchemaFile  = "deploy/computevm-apiresourceschema.yaml"
	computeVMExportFile  = "deploy/computevm-apiexport.yaml"
	bidderWorkspaceType  = "deploy/workspacetype-bidder.yaml"
	computeVMFinalizer   = "computevm.socialweb.computer/release"
	computeVMTypeScript  = "../hono-compute-provider/lib/common/compute-provider-kcp-common/mod.ts"
	computeVMSiblingRepo = "../hono-compute-provider"
)

func newComputeVM(t *testing.T) runtime.Object {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	obj, err := scheme.New(v1alpha1.GroupVersion.WithKind("ComputeVM"))
	if err != nil {
		t.Fatalf("api/v1alpha1 registers no ComputeVM: %v", err)
	}
	return obj
}

func strictDecodeComputeVM(t *testing.T, raw []byte) (runtime.Object, error) {
	t.Helper()
	obj := newComputeVM(t)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func computeVMFixtures(t *testing.T, prefix string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(computeVMFixtureDir, prefix+"*.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(matches)
	return matches
}

func TestComputeVMRoundTripsThroughTheSharedFixtures(t *testing.T) {
	model := mustModel(t)
	valid := computeVMFixtures(t, "valid-")
	if len(valid) < 4 {
		t.Fatalf("expected the shared valid fixtures in %s, found %d", computeVMFixtureDir, len(valid))
	}
	for _, path := range valid {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			vm, err := strictDecodeComputeVM(t, raw)
			if err != nil {
				t.Fatalf("the Go type refuses a shared fixture: %v", err)
			}
			out, err := json.Marshal(vm)
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			for _, lost := range lostLeaves(want, got, "") {
				t.Errorf("the Go type dropped or changed a field: %s", lost)
			}
			if issues := model.Validate("ComputeVM", want); len(issues) != 0 {
				t.Errorf("the generated schema rejects a valid fixture: %v", issues)
			}
			if issues := model.Validate("ComputeVM", got); len(issues) != 0 {
				t.Errorf("the generated schema rejects what the Go type writes: %v", issues)
			}
			again, err := strictDecodeComputeVM(t, out)
			if err != nil {
				t.Fatalf("the Go type refuses its own output: %v", err)
			}
			if !reflect.DeepEqual(vm, again) {
				t.Errorf("a second round trip changed the object:\n%+v\n%+v", vm, again)
			}
		})
	}
	invalid := computeVMFixtures(t, "invalid-")
	if len(invalid) < 7 {
		t.Fatalf("expected the shared invalid fixtures in %s, found %d", computeVMFixtureDir, len(invalid))
	}
	for _, path := range invalid {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var fx invalidFixture
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatal(err)
			}
			if fx.ExpectError == "" || len(fx.Object) == 0 {
				t.Fatal("an invalid fixture names expectError and carries object")
			}
			var obj any
			if err := json.Unmarshal(fx.Object, &obj); err != nil {
				t.Fatal(err)
			}
			found := false
			issues := model.Validate("ComputeVM", obj)
			for _, i := range issues {
				if i.Path == fx.ExpectError {
					found = true
				}
			}
			if !found {
				t.Errorf("expected an issue at %s, got %v", fx.ExpectError, issues)
			}
			_, decodeErr := strictDecodeComputeVM(t, fx.Object)
			if fx.GoDecodeRejects && decodeErr == nil {
				t.Errorf("the strict Go decode accepted a fixture it must refuse at %s", fx.ExpectError)
			}
			if !fx.GoDecodeRejects && decodeErr != nil {
				t.Errorf("the strict Go decode refused a fixture only the schema refuses: %v", decodeErr)
			}
		})
	}
}

func generatedOutput(t *testing.T, path string) []byte {
	t.Helper()
	outputs, err := Generate(repoRoot)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for _, o := range outputs {
		if o.Path == path {
			return o.Data
		}
	}
	t.Fatalf("apigen generates no %s", path)
	return nil
}

func TestTheComputeVMSchemaIsRevisionLocked(t *testing.T) {
	data := generatedOutput(t, computeVMSchemaFile)
	name, hash, err := SpecHash(data)
	if err != nil {
		t.Fatal(err)
	}
	if name != computeVMSchemaName {
		t.Fatalf("the ComputeVM schema is %s, want the first revision %s", name, computeVMSchemaName)
	}
	lockData, err := os.ReadFile(filepath.Join(repoRoot, RevisionLockFile))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := ParseRevisionLock(lockData)
	if err != nil {
		t.Fatal(err)
	}
	if lock[name] != hash {
		t.Fatalf("%s locks %s at %q, the generated spec hashes %s", RevisionLockFile, name, lock[name], hash)
	}
	committed, err := os.ReadFile(filepath.Join(repoRoot, computeVMSchemaFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, committedHash, err := SpecHash(committed); err != nil || committedHash != hash {
		t.Fatalf("the committed %s hashes %s (%v), the lock holds %s", computeVMSchemaFile, committedHash, err, hash)
	}
	if err := lock.Admit(name, "sha256:changed"); err == nil {
		t.Fatal("the lock admits a changed ComputeVM spec under the first revision's name")
	}
	export := string(generatedOutput(t, computeVMExportFile))
	if !strings.Contains(export, "name: computevms\n") || !strings.Contains(export, "schema: "+computeVMSchemaName+"\n") {
		t.Fatalf("the computevms APIExport does not serve %s:\n%s", computeVMSchemaName, export)
	}
}

type namedProperty struct {
	path   string
	schema *Schema
}

func properties(s *Schema, path string, seen map[*Schema]bool) []namedProperty {
	if s == nil || seen[s] {
		return nil
	}
	seen[s] = true
	var out []namedProperty
	for _, p := range s.Properties {
		child := joinPath(path, p.Name)
		out = append(out, namedProperty{path: child, schema: p.Schema})
		out = append(out, properties(p.Schema, child, seen)...)
	}
	if s.Items != nil {
		out = append(out, properties(s.Items, path+"[]", seen)...)
	}
	return out
}

func TestTheComputeVMSchemaHasNoBackendEnumAndNoNetworkIndex(t *testing.T) {
	m := mustModel(t)
	root, ok := m.Named["ComputeVM"]
	if !ok {
		t.Fatal("the model has no ComputeVM")
	}
	props := properties(root, "", map[*Schema]bool{})
	byPath := map[string]*Schema{}
	for _, p := range props {
		byPath[p.path] = p.schema
	}
	backend, ok := byPath["status.runtime.backend"]
	if !ok {
		t.Fatal("status.runtime.backend is missing; the placement backend is recorded as a free string")
	}
	if backend.Type != TypeString || len(backend.Enum) != 0 || backend.Ref != "" {
		t.Errorf("status.runtime.backend must be a free string with no enum, got %+v", backend)
	}
	for _, e := range m.Enums {
		if strings.Contains(strings.ToLower(e.Name), "backend") {
			t.Errorf("enum %s names backends; a backend is a free string so plans 0011 and 0012 add none", e.Name)
		}
	}
	index := regexp.MustCompile(`(?i)index|idx|slot`)
	linuxOnly := regexp.MustCompile(`(?i)unit|pasta|socket|sock$|invocation|ticks|cgroup|systemd|tap|netns|uid|gid`)
	for _, p := range props {
		leaf := p.path[strings.LastIndex(p.path, ".")+1:]
		if index.MatchString(leaf) {
			t.Errorf("%s: every guest gets the identical address plan, so no network index field may exist", p.path)
		}
		if linuxOnly.MatchString(leaf) {
			t.Errorf("%s: a Linux-only field in an OS-neutral schema", p.path)
		}
	}
	for _, path := range []string{
		"spec.vm.cpus", "spec.vm.mem", "spec.vm.disk", "spec.vm.role", "spec.owner.handle", "spec.userDataSecret",
		"spec.requester.did", "spec.requester.plc", "spec.contract.receiptUri", "spec.contract.receiptCid",
		"spec.contract.acceptUri", "spec.contract.acceptCid", "spec.contract.submitEventUrl", "spec.contract.acceptedAt",
		"spec.image.reuseStale", "spec.stopGraceSeconds",
		"status.phase", "status.lostReason", "status.observedGeneration",
		"status.network.guestIP", "status.network.hostAddress", "status.network.prefix", "status.network.mac",
		"status.image.ref", "status.image.sha256", "status.image.hypervisor",
		"status.runtime.ref", "status.runtime.instance", "status.runtime.pid", "status.runtime.startTime",
		"status.runtime.hostBootID", "status.runtime.vmmID",
		"status.workDir", "status.bootedAt", "status.message", "status.conditions",
		"status.market.onNetworkUri", "status.market.address", "status.market.hostKeys",
		"status.market.hostKeys[].type", "status.market.hostKeys[].publicKey", "status.market.hostKeys[].fingerprint",
		"status.market.hostKeysUri", "status.market.reportedAt",
	} {
		if _, ok := byPath[path]; !ok {
			t.Errorf("the ComputeVM schema has no %s", path)
		}
	}
	if s := byPath["status.runtime.startTime"]; s != nil && s.Type != TypeString {
		t.Errorf("status.runtime.startTime is an opaque string, got %s", s.Type)
	}
	if s := byPath["status.phase"]; s != nil && !reflect.DeepEqual(s.Enum, []string{"Pending", "Preparing", "Booting", "Running", "Releasing", "Lost", "Failed"}) {
		t.Errorf("status.phase enum = %v; Unknown is a condition, never a phase", s.Enum)
	}
}

func TestTheComputeVMSchemaCarriesItsPrinterColumnsAndFinalizer(t *testing.T) {
	var doc struct {
		Spec struct {
			Versions []struct {
				Columns []struct {
					Name     string `yaml:"name"`
					Type     string `yaml:"type"`
					JSONPath string `yaml:"jsonPath"`
				} `yaml:"additionalPrinterColumns"`
			} `yaml:"versions"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(generatedOutput(t, computeVMSchemaFile), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Spec.Versions) != 1 {
		t.Fatalf("want one version, got %d", len(doc.Spec.Versions))
	}
	var got []string
	for _, c := range doc.Spec.Versions[0].Columns {
		got = append(got, c.Name+" "+c.Type+" "+c.JSONPath)
	}
	want := []string{
		"Phase string .status.phase",
		"GuestIP string .status.network.guestIP",
		"Requester string .spec.requester.did",
		"Age date .metadata.creationTimestamp",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("printer columns:\n got %v\nwant %v", got, want)
	}
	found := false
	for _, c := range mustModel(t).Constants {
		if c.Value == computeVMFinalizer {
			found = c.Name == "FinalizerComputeVM"
		}
	}
	if !found {
		t.Errorf("api/v1alpha1 declares no FinalizerComputeVM = %q", computeVMFinalizer)
	}
}

func TestTheBidderWorkspaceTypeBindsDenoruntimeComputeVMsAndHostProcesses(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, bidderWorkspaceType))
	if err != nil {
		t.Fatal(err)
	}
	var wt struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec struct {
			Bindings []struct {
				Export string `yaml:"export"`
				Path   string `yaml:"path"`
			} `yaml:"defaultAPIBindings"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(raw, &wt); err != nil {
		t.Fatal(err)
	}
	if wt.Kind != "WorkspaceType" || wt.Metadata.Name != "bidder" {
		t.Fatalf("%s is %s %s, want WorkspaceType bidder", bidderWorkspaceType, wt.Kind, wt.Metadata.Name)
	}
	var got []string
	for _, b := range wt.Spec.Bindings {
		got = append(got, b.Export+"@"+b.Path)
	}
	want := []string{"denoruntime@root:deno-provider", "computevms@root:socialweb-provider", "hostprocesses@root:socialweb-provider"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bidder bindings:\n got %v\nwant %v", got, want)
	}
}

func TestTheComputeVMTypeScriptLandsInTheComputeProviderWhenTheSiblingIsPresent(t *testing.T) {
	sibling := filepath.Join(repoRoot, computeVMSiblingRepo)
	if _, err := os.Stat(sibling); err != nil {
		t.Skipf("%s is not checked out beside this repo, so the second TypeScript target is not generated", sibling)
	}
	ts := string(generatedOutput(t, computeVMTypeScript))
	for _, want := range []string{
		"export interface ComputeVM {",
		"export function parseComputeVM(",
		"export const FINALIZER_COMPUTE_VM = \"computevm.socialweb.computer/release\";",
		"export const COMPUTE_VM_RESOURCE: ApiResource",
	} {
		if !strings.Contains(ts, want) {
			t.Errorf("the compute provider's TypeScript has no %q", want)
		}
	}
	for _, absent := range []string{"NodeClaim", "MarketIdentity", "FINALIZER_CLUSTER"} {
		if strings.Contains(ts, absent) {
			t.Errorf("the compute provider's TypeScript carries %s, which only the door uses", absent)
		}
	}
	door := string(generatedOutput(t, TSPackageDir+"/mod.ts"))
	if strings.Contains(door, "ComputeVM") || strings.Contains(door, "COMPUTE_VM") {
		t.Error("the door's TypeScript carries ComputeVM; a gateway never reads a bidder's ComputeVMs")
	}
	fixtures := computeVMFixtures(t, "")
	for _, path := range fixtures {
		mirrored := "../hono-compute-provider/test/fixtures/computevm/" + filepath.Base(path)
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := generatedOutput(t, mirrored); !bytes.Equal(got, want) {
			t.Errorf("%s is not a copy of %s", mirrored, path)
		}
	}
}
