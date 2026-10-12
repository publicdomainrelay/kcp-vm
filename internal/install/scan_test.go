package install

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostcontract"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatalf("no go.mod at %s", dir)
	}
	return dir
}

type goLiteral struct {
	file  string
	line  int
	value string
	exec  bool
}

func goLiterals(t *testing.T) []goLiteral {
	t.Helper()
	root := repoRoot(t)
	var out []goLiteral
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "kcp", "kata-containers", "streamplace", "rust", "socialweb-computer-k8s", "cluster-api":
				if p != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		execArgs := map[ast.Expr]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "exec" && len(call.Args) > 0 {
					switch sel.Sel.Name {
					case "Command", "LookPath":
						execArgs[call.Args[0]] = true
					case "CommandContext":
						if len(call.Args) > 1 {
							execArgs[call.Args[1]] = true
						}
					}
				}
			}
			return true
		})
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			out = append(out, goLiteral{file: filepath.ToSlash(rel), line: fset.Position(lit.Pos()).Line, value: v, exec: execArgs[lit]})
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("the scan found no string literal; the scanner is broken")
	}
	return out
}

func TestNoCodePathOutsideInstallCreatesAnAccount(t *testing.T) {
	creators := map[string]bool{"useradd": true, "adduser": true, "groupadd": true, "addgroup": true, "usermod": true, "gpasswd": true, "userdel": true}
	seenInInstall := false
	for _, l := range goLiterals(t) {
		if !l.exec || !creators[path.Base(l.value)] {
			continue
		}
		if strings.HasPrefix(l.file, "internal/install/") {
			seenInInstall = true
			continue
		}
		t.Errorf("%s:%d execs %s: Unix accounts and groups are changed only by `socialweb-computer install`", l.file, l.line, l.value)
	}
	if !seenInInstall {
		t.Error("the scan did not see install's own useradd exec; the scanner is broken")
	}
}

var unitLiteral = regexp.MustCompile(`^socialweb[a-z0-9-]*(\.service|\.slice|-)$`)

func TestEveryUnitPrefixTheCodeStartsIsEnumeratedForUninstall(t *testing.T) {
	n := 0
	for _, l := range goLiterals(t) {
		v := strings.TrimSuffix(strings.SplitN(l.value, "%", 2)[0], "@")
		if !unitLiteral.MatchString(v) {
			continue
		}
		n++
		if !OwnsUnit(v) && !OwnsUnit(v+"x.service") {
			t.Errorf("%s:%d names unit %q, which uninstall's prefix %s does not stop", l.file, l.line, l.value, RuntimePrefix)
		}
	}
	if n == 0 {
		t.Fatal("no unit-name literal found; the scanner is broken")
	}
	for _, name := range []string{"socialweb-vm-0123456789-a.service", "socialweb-bao-root.service", "socialweb-cluster-k.slice",
		"socialweb-kcp-gw@a.service", "socialweb.slice", "socialweb-probe-x.service"} {
		if !OwnsUnit(name) {
			t.Errorf("uninstall would leave %s running", name)
		}
	}
	for _, name := range []string{"app.slice", "dbus.service", "socialwebby.service"} {
		if OwnsUnit(name) {
			t.Errorf("uninstall would stop %s, which is not ours", name)
		}
	}
}

func TestEveryInstalledUnitFileVerifies(t *testing.T) {
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze not on PATH: the rendered unit files are not verified on this host")
	}
	for _, role := range []hostcontract.Role{hostcontract.RoleGateway, hostcontract.RoleBidder} {
		state := t.TempDir()
		bin := filepath.Join(state, "bin", "current")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, CoreName), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		units, err := RenderUnits(role, state)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		var files []string
		for name, b := range units {
			if strings.HasSuffix(name, "@.service") {
				name = strings.TrimSuffix(name, "@.service") + "@alice.example.com.service"
			}
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
			files = append(files, p)
		}
		args := append([]string{"--user", "verify"}, files...)
		if out, err := exec.Command(analyze, args...).CombinedOutput(); err != nil || len(strings.TrimSpace(string(out))) > 0 {
			t.Errorf("%s: systemd-analyze --user verify: %v\n%s", role, err, out)
		}
	}
	drop, err := DropIn()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "socialweb-dropin-check.service")
	content := "[Unit]\nDescription=the delegation drop-in's directives\n\n" + string(drop) + "ExecStart=/bin/true\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(analyze, "verify", p).CombinedOutput(); err != nil || len(strings.TrimSpace(string(out))) > 0 {
		t.Errorf("the delegation drop-in: systemd-analyze verify: %v\n%s", err, out)
	}
}
