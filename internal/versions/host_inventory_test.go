package versions

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const (
	thisRepo          = "socialweb-computer-kcp"
	hostInventoryPath = "docs/host-inventory.yaml"
)

var inventoryFates = []string{
	"linked",
	"embedded-server",
	"embedded-child",
	"embedded-tool",
	"replaced-by-go",
	"download-asset",
	"host-file",
	"host-prerequisite",
	"build-host",
	"removed-legacy",
}

var inventoryKinds = []string{"executable", "package", "file"}

type inventoryEntry struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Fate     string   `json:"fate"`
	DrivenBy string   `json:"driven_by,omitempty"`
	Owner    string   `json:"owner"`
	Why      string   `json:"why"`
	From     []string `json:"from,omitempty"`
}

type indirectSite struct {
	Site     string   `json:"site"`
	Resolves []string `json:"resolves"`
}

type hostInventory struct {
	Note     string            `json:"note"`
	Fates    map[string]string `json:"fates"`
	Entries  []inventoryEntry  `json:"entries"`
	Indirect []indirectSite    `json:"indirect"`
}

type execSite struct {
	where string
	name  string
	key   string
}

func loadHostInventory(t *testing.T) hostInventory {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, hostInventoryPath))
	if err != nil {
		t.Fatalf("read %s: %v -- 0006 M1-HOST-INVENTORY: every executable the code execs needs a fate "+
			"recorded there", hostInventoryPath, err)
	}
	var inv hostInventory
	if err := yaml.UnmarshalStrict(raw, &inv); err != nil {
		t.Fatalf("parse %s: %v", hostInventoryPath, err)
	}
	return inv
}

func goExecSites(t *testing.T) []execSite {
	t.Helper()
	var out []execSite
	for _, file := range nonTestGoFiles(t, repoRoot) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		rel, _ := filepath.Rel(repoRoot, file)
		rel = filepath.ToSlash(rel)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			arg := -1
			switch pkg.Name + "." + sel.Sel.Name {
			case "exec.Command", "exec.LookPath", "syscall.Exec":
				arg = 0
			case "exec.CommandContext":
				arg = 1
			}
			if arg < 0 || len(call.Args) <= arg {
				return true
			}
			where := thisRepo + "/" + rel + ":" + strconv.Itoa(fset.Position(call.Pos()).Line)
			if lit, ok := call.Args[arg].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s: unquote %s: %v", where, lit.Value, err)
				}
				out = append(out, execSite{where: where, name: path.Base(s)})
				return true
			}
			var buf bytes.Buffer
			if err := printer.Fprint(&buf, fset, call.Args[arg]); err != nil {
				t.Fatalf("%s: print argument: %v", where, err)
			}
			out = append(out, execSite{where: where, key: thisRepo + "/" + rel + ": " + buf.String()})
			return true
		})
	}
	return out
}

var denoCommand = regexp.MustCompile(`new\s+Deno\.Command\(\s*([^,)]+?)\s*[,)]`)

var tsLiteral = regexp.MustCompile("^(?:\"([^\"]*)\"|'([^']*)'|`([^`$]*)`)$")

func tsExecSites(t *testing.T, repo, root string) []execSite {
	t.Helper()
	var out []execSite
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "test" ||
				name == "tests" || name == "testdata" || name == "third_party" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, "_test.ts") ||
			strings.HasSuffix(name, ".test.ts") || strings.HasSuffix(name, ".d.ts") {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		body := string(raw)
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		for _, m := range denoCommand.FindAllStringSubmatchIndex(body, -1) {
			expr := body[m[2]:m[3]]
			where := repo + "/" + rel + ":" + strconv.Itoa(strings.Count(body[:m[0]], "\n")+1)
			if lit := tsLiteral.FindStringSubmatch(expr); lit != nil {
				out = append(out, execSite{where: where, name: path.Base(lit[1] + lit[2] + lit[3])})
				continue
			}
			out = append(out, execSite{where: where, key: repo + "/" + rel + ": " + expr})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

var dnfInstall = regexp.MustCompile(`dnf install((?:\s+[A-Za-z0-9_.+=-]+)+)`)

var copyToBin = regexp.MustCompile(`(?m)^COPY\s.*\s/usr/local/bin/([A-Za-z0-9_.-]+)\s*$`)

func imageContents(t *testing.T, dockerfile string) (packages, executables []string) {
	t.Helper()
	body := strings.ReplaceAll(mustRead(t, dockerfile), "\\\n", " ")
	for _, m := range dnfInstall.FindAllStringSubmatch(body, -1) {
		for _, f := range strings.Fields(m[1]) {
			if !strings.HasPrefix(f, "-") {
				packages = append(packages, f)
			}
		}
	}
	for _, m := range copyToBin.FindAllStringSubmatch(body, -1) {
		executables = append(executables, m[1])
	}
	if len(packages) == 0 || len(executables) == 0 {
		t.Fatalf("%s: read %d dnf packages and %d /usr/local/bin copies; the parse no longer "+
			"matches the file", dockerfile, len(packages), len(executables))
	}
	return packages, executables
}

func TestEveryExecutableTheCodeExecsHasAFateInTheHostInventory(t *testing.T) {
	inv := loadHostInventory(t)

	var gotFates []string
	for f, desc := range inv.Fates {
		gotFates = append(gotFates, f)
		if strings.TrimSpace(desc) == "" {
			t.Errorf("fate %q has no description", f)
		}
	}
	sort.Strings(gotFates)
	wantFates := append([]string(nil), inventoryFates...)
	sort.Strings(wantFates)
	if strings.Join(gotFates, ",") != strings.Join(wantFates, ",") {
		t.Fatalf("fates = %v, want exactly %v", gotFates, wantFates)
	}

	byKind := map[string]map[string]inventoryEntry{}
	for _, k := range inventoryKinds {
		byKind[k] = map[string]inventoryEntry{}
	}
	for _, e := range inv.Entries {
		kind, ok := byKind[e.Kind]
		if !ok {
			t.Errorf("%s: kind %q is not one of %v", e.Name, e.Kind, inventoryKinds)
			continue
		}
		if _, dup := kind[e.Name]; dup {
			t.Errorf("%s %s is inventoried twice; every item has exactly one fate", e.Kind, e.Name)
		}
		kind[e.Name] = e
		if _, ok := inv.Fates[e.Fate]; !ok {
			t.Errorf("%s %s: fate %q is not one of %v", e.Kind, e.Name, e.Fate, inventoryFates)
		}
		if e.Name == "" || strings.TrimSpace(e.Owner) == "" || strings.TrimSpace(e.Why) == "" {
			t.Errorf("%s %q needs a name, an owner (the plan item that carries out its fate) and a why", e.Kind, e.Name)
		}
		if e.Fate == "embedded-server" && strings.TrimSpace(e.DrivenBy) == "" {
			t.Errorf("%s is an embedded server with no driven_by: name the CRD (or the core's local "+
				"state) that starts it", e.Name)
		}
	}

	sites := goExecSites(t)
	scanned := map[string]bool{thisRepo: true}
	sites = append(sites, tsExecSites(t, thisRepo, repoRoot)...)
	if len(sites) == 0 {
		t.Fatal("the scan found no exec call site at all; the scanner is broken, not the code clean")
	}

	indirect := map[string]indirectSite{}
	for _, s := range inv.Indirect {
		if _, dup := indirect[s.Site]; dup {
			t.Errorf("indirect site %q is listed twice", s.Site)
		}
		indirect[s.Site] = s
		if len(s.Resolves) == 0 {
			t.Errorf("indirect site %q resolves to nothing", s.Site)
		}
		for _, name := range s.Resolves {
			if _, ok := byKind["executable"][name]; !ok {
				t.Errorf("indirect site %q resolves to %s, which has no fate in %s", s.Site, name, hostInventoryPath)
			}
		}
	}

	seenKeys := map[string]bool{}
	for _, s := range sites {
		if s.key == "" {
			if _, ok := byKind["executable"][s.name]; !ok {
				t.Errorf("%s execs %s, which has no fate in %s", s.where, s.name, hostInventoryPath)
			}
			continue
		}
		seenKeys[s.key] = true
		if _, ok := indirect[s.key]; !ok {
			t.Errorf("%s execs a computed name; list %q under indirect with the executables it resolves to",
				s.where, s.key)
		}
	}
	for key := range indirect {
		repo, _, _ := strings.Cut(key, "/")
		if scanned[repo] && !seenKeys[key] {
			t.Errorf("indirect site %q matches no call site any more; remove it", key)
		}
	}

	images := []string{
		filepath.Join(repoRoot, "deploy", "control-plane-container", "Dockerfile"),
		filepath.Join(repoRoot, "ts", "hono-compute-provider", "hono-firecracker-standalone", "Dockerfile"),
	}
	for _, image := range images {
		packages, executables := imageContents(t, image)
		for _, p := range packages {
			if _, ok := byKind["package"][p]; !ok {
				t.Errorf("%s installs package %s, which has no fate in %s", image, p, hostInventoryPath)
			}
		}
		for _, x := range executables {
			if _, ok := byKind["executable"][x]; !ok {
				t.Errorf("%s copies %s into /usr/local/bin, and it has no fate in %s", image, x, hostInventoryPath)
			}
		}
	}

	for _, e := range inv.Entries {
		if e.Fate == "host-prerequisite" && e.Kind == "package" {
			t.Errorf("package %s is a host prerequisite: name the executable or file the host must "+
				"supply instead, so nothing reads as 'from the package manager'", e.Name)
		}
	}
}
