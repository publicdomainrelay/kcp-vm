package versions

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const repoRoot = "../.."

var unbuiltStoreName = regexp.MustCompile(`(?i)\b(rustfs|t4)\b|central.?store`)

func nonTestGoFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "testdata" || name == "docs" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

func declarationSpan(t *testing.T, path, name string) (int, int) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, n := range vs.Names {
				if n.Name == name {
					return fset.Position(vs.Pos()).Offset, fset.Position(vs.End()).Offset
				}
			}
		}
	}
	t.Fatalf("%s declares no %s, so the store measurement this scan allows is gone or renamed", path, name)
	return 0, 0
}

func TestNoRustfsOrT4NameOutsideTheStoreMeasurement(t *testing.T) {
	measurement := filepath.Join(repoRoot, "internal", "clusterprovider", "stack.go")
	from, to := declarationSpan(t, measurement, "ErrNoStore")

	files := append(nonTestGoFiles(t, repoRoot), filepath.Join(repoRoot, "internal", "versions", "versions.json"))
	var found []string
	for _, path := range files {
		body := mustRead(t, path)
		for _, loc := range unbuiltStoreName.FindAllStringIndex(body, -1) {
			if path == measurement && loc[0] >= from && loc[1] <= to {
				continue
			}
			line := strings.Count(body[:loc[0]], "\n") + 1
			rel, _ := filepath.Rel(repoRoot, path)
			found = append(found, rel+":"+strconv.Itoa(line)+": "+body[loc[0]:loc[1]])
		}
	}
	if len(found) > 0 {
		t.Fatalf("the rustfs/t4 central store was never built (each cluster's store is kine over its "+
			"own postgres), yet its names survive outside stack.go's ErrNoStore measurement:\n%s",
			strings.Join(found, "\n"))
	}
}

func TestTheKineInvariantNamesNoBucket(t *testing.T) {
	kine, err := Get("kine")
	if err != nil {
		t.Fatalf("Get(kine): %v", err)
	}
	if strings.Contains(strings.ToLower(kine.Invariant), "bucket") {
		t.Fatalf("the kine invariant still speaks of a bucket, but kine's endpoint is the cluster's "+
			"own postgres: %s", kine.Invariant)
	}
}
