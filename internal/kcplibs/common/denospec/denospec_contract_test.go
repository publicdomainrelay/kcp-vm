package denospec

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	structOpen = regexp.MustCompile(`^type (\w+) struct \{`)

	field = regexp.MustCompile("^(\\w+)\\s+([^\\s`]+)\\s+`json:\"([^\"]+)\"`")
)

type shape struct {
	typ string

	tag string
}

func TestTheShapesAreTheOnesTheConsumerDeclares(t *testing.T) {
	dir := consumerAPIDir(t)
	if dir == "" {
		return
	}
	declared := consumerShapes(t, dir)
	for _, pair := range []struct {
		shape any
		name  string
	}{
		{ServiceAccountRef{}, "ServiceAccountRef"},
		{Permission{}, "DenoPermission"},
		{Permissions{}, "DenoPermissions"},
		{PodTemplate{}, "DenoPodTemplate"},
		{ExecProbe{}, "ExecProbe"},
	} {
		want, ok := declared[pair.name]
		if !ok {
			t.Fatalf("the consumer declares no %s, so the shape here is invented", pair.name)
		}
		got := ownShapes(pair.shape)
		if !slices.Equal(got, want) {
			t.Fatalf("%s carries %v, the consumer declares %v, so a value written from one would not decode as the other", pair.name, got, want)
		}
	}
}

func ownShapes(value any) []shape {
	out := []shape{}
	kind := reflect.TypeOf(value)
	for i := 0; i < kind.NumField(); i++ {
		tag, ok := kind.Field(i).Tag.Lookup("json")
		if !ok {
			continue
		}
		out = append(out, shape{typ: normalize(kind.Field(i).Type.String()), tag: tag})
	}
	return out
}

func normalize(typ string) string {
	typ = strings.ReplaceAll(typ, "denospec.", "")
	typ = strings.ReplaceAll(typ, "DenoPermission", "Permission")
	typ = strings.ReplaceAll(typ, "DenoPermissions", "Permissions")
	return typ
}

func consumerShapes(t *testing.T, dir string) map[string][]shape {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]shape{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		current := ""
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if match := structOpen.FindStringSubmatch(line); match != nil {
				current = match[1]
				out[current] = []shape{}
				continue
			}
			if current == "" {
				continue
			}
			if line == "}" {
				current = ""
				continue
			}
			if strings.HasPrefix(line, "//") {
				continue
			}
			if match := field.FindStringSubmatch(line); match != nil {
				out[current] = append(out[current], shape{typ: normalize(match[2]), tag: match[3]})
			}
		}
	}
	return out
}

func consumerAPIDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Join(root, "internal", "denokcp", "api", "v1alpha1")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(root)
		if parent == root {
			break
		}
		root = parent
	}
	if os.Getenv("KCP_LIBS_REQUIRE_CONSUMER") == "1" {
		t.Fatal("the consumer's api/v1alpha1 was not found; this test reads it as the source of truth, so set KCP_LIBS_REQUIRE_CONSUMER only where the checkout is present")
	}
	t.Skip("the consumer checkout is not next to this module, so there is nothing to compare against")
	return ""
}
