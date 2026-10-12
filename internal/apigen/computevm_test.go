package apigen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

const (
	computeVMFixtureDir = "../../testdata/computevm"
	computeVMSchemaName = "v1alpha1-1.computevms.socialweb.computer"
	computeVMSchemaFile = "deploy/computevm-apiresourceschema.yaml"
	computeVMExportFile = "deploy/computevm-apiexport.yaml"
	bidderWorkspaceType = "deploy/workspacetype-bidder.yaml"
	computeVMFinalizer  = "computevm.socialweb.computer/release"
	computeVMTypeScript = ComputeProviderTSPackageDir + "/mod.ts"
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
