package test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNoCommentsAndASCII(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	root := filepath.Dir(filepath.Dir(file))

	for _, dir := range []string{"api", "internal", "cmd", "test"} {
		base := filepath.Join(root, dir)
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || filepath.Ext(path) != ".go" {
				return nil
			}
			checkComments(t, root, path)
			checkASCII(t, root, path)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", base, err)
		}
	}
}

func checkComments(t *testing.T, root, path string) {
	t.Helper()
	rel := relative(t, root, path)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Errorf("%s: parse: %v", rel, err)
		return
	}

	for _, group := range f.Comments {
		if commentGroupAllowed(group) {
			continue
		}
		for _, c := range group.List {
			pos := fset.Position(c.Pos())
			t.Errorf("%s:%d:%d: comment not in a permitted form: %s", rel, pos.Line, pos.Column, c.Text)
		}
	}
}

func commentGroupAllowed(group *ast.CommentGroup) bool {
	for _, c := range group.List {
		if strings.HasPrefix(c.Text, "//go:") {
			return true
		}
		if strings.Contains(c.Text, "ponytail:") {
			return true
		}
	}
	return false
}

func checkASCII(t *testing.T, root, path string) {
	t.Helper()
	rel := relative(t, root, path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("%s: read: %v", rel, err)
		return
	}
	for offset, b := range data {
		if b > 0x7F {
			t.Errorf("%s: non-ASCII byte 0x%02X at offset %d", rel, b, offset)
		}
	}
}

func relative(t *testing.T, root, path string) string {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}
