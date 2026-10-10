package apigen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

const fixtureDir = "../../testdata/nodeclaim"

type invalidFixture struct {
	ExpectError     string          `json:"expectError"`
	GoDecodeRejects bool            `json:"goDecodeRejects"`
	Object          json.RawMessage `json:"object"`
}

func fixtureFiles(t *testing.T, prefix string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(fixtureDir, prefix+"*.json"))
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	sort.Strings(matches)
	return matches
}

func strictDecode(raw []byte) (*v1alpha1.NodeClaim, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var claim v1alpha1.NodeClaim
	if err := dec.Decode(&claim); err != nil {
		return nil, err
	}
	return &claim, nil
}

func lostLeaves(want, got any, path string) []string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: want an object, got %v", path, got)}
		}
		var out []string
		for k, v := range w {
			out = append(out, lostLeaves(v, g[k], joinPath(path, k))...)
		}
		return out
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return []string{fmt.Sprintf("%s: want %v, got %v", path, w, got)}
		}
		var out []string
		for i := range w {
			out = append(out, lostLeaves(w[i], g[i], fmt.Sprintf("%s[%d]", path, i))...)
		}
		return out
	default:
		if !reflect.DeepEqual(want, got) {
			return []string{fmt.Sprintf("%s: want %v, got %v", path, want, got)}
		}
		return nil
	}
}

func TestEveryValidNodeClaimFixtureRoundTripsThroughTheGoType(t *testing.T) {
	model := mustModel(t)
	files := fixtureFiles(t, "valid-")
	if len(files) < 4 {
		t.Fatalf("expected the shared valid fixtures in %s, found %d", fixtureDir, len(files))
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := strictDecode(raw)
			if err != nil {
				t.Fatalf("the Go type refuses a fixture the door accepts: %v", err)
			}
			out, err := json.Marshal(claim)
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
				t.Errorf("the Go type dropped or changed a field the door wrote: %s", lost)
			}
			if issues := model.Validate("NodeClaim", want); len(issues) != 0 {
				t.Errorf("the generated schema rejects a valid fixture: %v", issues)
			}
			if issues := model.Validate("NodeClaim", got); len(issues) != 0 {
				t.Errorf("the generated schema rejects what the Go type writes: %v", issues)
			}
			again, err := strictDecode(out)
			if err != nil {
				t.Fatalf("the Go type refuses its own output: %v", err)
			}
			if !reflect.DeepEqual(claim, again) {
				t.Errorf("a second round trip changed the claim:\n%+v\n%+v", claim, again)
			}
		})
	}
}

func TestEveryInvalidNodeClaimFixtureIsRejectedWhereTheDoorRejectsIt(t *testing.T) {
	model := mustModel(t)
	files := fixtureFiles(t, "invalid-")
	if len(files) < 8 {
		t.Fatalf("expected the shared invalid fixtures in %s, found %d", fixtureDir, len(files))
	}
	for _, path := range files {
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
			issues := model.Validate("NodeClaim", obj)
			found := false
			for _, i := range issues {
				if i.Path == fx.ExpectError {
					found = true
				}
			}
			if !found {
				t.Errorf("expected an issue at %s, got %v", fx.ExpectError, issues)
			}
			_, decodeErr := strictDecode(fx.Object)
			if fx.GoDecodeRejects && decodeErr == nil {
				t.Errorf("the strict Go decode accepted a fixture it must refuse at %s", fx.ExpectError)
			}
			if !fx.GoDecodeRejects && decodeErr != nil {
				t.Errorf("the strict Go decode refused a fixture only the schema refuses: %v", decodeErr)
			}
		})
	}
}
