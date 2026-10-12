package main

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/capabilities"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/ccmmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/checkhostmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/foldmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/gatewaymain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/providermain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tenantcredentialmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultmain"
)

const modulePath = "github.com/johnandersen777/socialweb-computer-kcp"

type foldCase struct {
	args []string
	env  clicfg.MapEnv
	code int
}

type foldedBinary struct {
	binary string
	pkg    string
	verb   []string
	prefix []string
	main   func(context.Context, foldmain.Invocation) int
	cases  []foldCase
}

func foldedBinaries() []foldedBinary {
	none := clicfg.MapEnv{}
	relativeState := clicfg.MapEnv{"SOCIALWEB_STATE_DIR": "relative/state"}
	return []foldedBinary{
		{"socialweb-gateway", "gatewaymain", []string{"serve", "gateway", "xrpc"}, nil, gatewaymain.Main,
			[]foldCase{{nil, none, 1}}},
		{"socialweb-clusterprovider", "providermain", []string{"serve", "provider"}, nil, providermain.Main,
			[]foldCase{{nil, none, 1}}},
		{"socialweb-clusterprovider", "providermain", []string{"migrate"}, []string{"migrate"}, providermain.Main,
			[]foldCase{{nil, none, 1}}},
		{"socialweb-vault", "vaultmain", []string{"vault", "prepare-root"}, []string{"prepare-root"}, vaultmain.Main,
			[]foldCase{{nil, relativeState, 1}, {[]string{"extra"}, none, 1}}},
		{"socialweb-vault", "vaultmain", []string{"vault", "apply-root"}, []string{"apply-root"}, vaultmain.Main,
			[]foldCase{{nil, relativeState, 1}}},
		{"socialweb-computer-kcp-check-host", "checkhostmain", []string{"check-host"}, nil, checkhostmain.Main,
			[]foldCase{{[]string{"-probe-exec"}, none, 0}, {[]string{"-no-such-flag"}, none, 2}, {[]string{"-h"}, none, 0}}},
		{"socialweb-cloud-controller-manager", "ccmmain", []string{"internal", "cloud-controller-manager"}, nil, ccmmain.Main,
			[]foldCase{{nil, none, 1}}},
		{"socialweb-tenantcredential", "tenantcredentialmain", []string{"internal", "tenantcredential"}, nil, tenantcredentialmain.Main,
			[]foldCase{{nil, none, 1}}},
	}
}

func TestEveryFoldedBinaryIsAVerbWithTheSameUsageAndExitCodes(t *testing.T) {
	for _, b := range foldedBinaries() {
		v, _, ok := capabilities.Find(b.verb)
		if !ok || v.Path != strings.Join(b.verb, " ") || !v.Built() {
			t.Errorf("%s: verb %q is not a built verb of the tree (%+v)", b.binary, strings.Join(b.verb, " "), v)
			continue
		}
		for _, c := range b.cases {
			args := append(append([]string(nil), b.verb...), c.args...)
			vcode, vout, verr := runT(t, c.env, "linux", args...)
			var sout, serr bytes.Buffer
			scode := b.main(context.Background(), foldmain.Invocation{
				Args:   append(append([]string(nil), b.prefix...), c.args...),
				Getenv: func(k string) string { return c.env[k] },
				Stdout: &sout, Stderr: &serr, Exe: "/standalone/" + b.binary,
			})
			if vcode != c.code || scode != c.code {
				t.Errorf("%s %v: verb exit %d, binary exit %d, want %d\nverb stderr: %s\nbinary stderr: %s",
					b.binary, c.args, vcode, scode, c.code, verr, serr.String())
			}
			if vout != sout.String() || verr != serr.String() {
				t.Errorf("%s %v: the verb and the binary differ\nverb:   %q %q\nbinary: %q %q",
					b.binary, c.args, vout, verr, sout.String(), serr.String())
			}
		}
		assertOneLineShim(t, b.binary, b.pkg)
	}
}

func assertOneLineShim(t *testing.T, binary, pkg string) {
	t.Helper()
	path := filepath.Join("..", binary, "main.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Errorf("%s: the shim %s does not parse: %v", binary, path, err)
		return
	}
	imported := false
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == modulePath+"/internal/"+pkg {
			imported = true
		}
	}
	if !imported || len(f.Decls) != 2 {
		t.Errorf("%s: %s is not a one-line shim over internal/%s (imports it: %v, %d decls)", binary, path, pkg, imported, len(f.Decls))
		return
	}
	fn, ok := f.Decls[1].(*ast.FuncDecl)
	if !ok || fn.Name.Name != "main" || len(fn.Body.List) != 1 {
		t.Errorf("%s: %s main is not one statement", binary, path)
		return
	}
	var buf bytes.Buffer
	_ = printer.Fprint(&buf, fset, fn.Body.List[0])
	if want := "os.Exit(" + pkg + ".Main(context.Background(), foldmain.OS()))"; buf.String() != want {
		t.Errorf("%s: shim body %q, want %q", binary, buf.String(), want)
	}
}

func TestNoNodebootOrNodeimageVerbInAReleaseBuild(t *testing.T) {
	for _, v := range capabilities.Tree() {
		for _, w := range v.Words() {
			if strings.Contains(w, "nodeboot") || strings.Contains(w, "nodeimage") || w == "namespace" {
				t.Errorf("verb %q: nodeboot and its namespace verb are deleted and nodeimage leaves the host binary", v.Path)
			}
		}
	}
	out, err := exec.Command("go", "list", "-tags", "release", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v: %s", err, out)
	}
	for _, p := range strings.Fields(string(out)) {
		if !strings.HasPrefix(p, modulePath+"/") || p == modulePath+"/cmd/socialweb-computer" {
			continue
		}
		if strings.HasPrefix(p, modulePath+"/cmd/") || strings.Contains(p, "nodeboot") || strings.Contains(p, "nodeimage") {
			t.Errorf("the release socialweb-computer links %s", p)
		}
	}
}
