package test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	edgePortsFile    = "deploy/edge-ports.yaml"
	portRatchetFile  = "deploy/fixed-ports-ratchet.yaml"
	optionDefaults   = "internal/clicfg/options.json"
	implicitPortKind = "implicit"
)

var (
	argPortPattern      = regexp.MustCompile(`--(?:[a-z0-9]+-)*port(?:=|\s+|["']\s*,\s*)["']?(\d{1,5})\b`)
	envPortPattern      = regexp.MustCompile(`\b(?:[A-Z0-9]+_)*PORT["']?\s*(?::-|:=|=|:)\s*["']?(\d{1,5})(?:$|[^.\d])`)
	loopbackPortPattern = regexp.MustCompile(`(?:\b127\.0\.0\.1|\b0\.0\.0\.0|\[::1?\]|\blocalhost|\.localhost):(\d{1,5})\b`)
	explicitZeroPattern = regexp.MustCompile(`(?:\b(?:[A-Z0-9]+_)*PORT["']?\s*[:=]\s*["']?0\b|--(?:[a-z0-9]+-)*port(?:=|\s+|["']\s*,\s*)["']?0\b)`)
	serviceKindPattern  = regexp.MustCompile(`(?m)^kind:\s*(DenoPod|HostProcess)\s*$`)
	restartAlwaysRegexp = regexp.MustCompile(`(?m)^\s*restartPolicy:\s*Always\s*$`)
	documentSeparator   = regexp.MustCompile(`(?m)^---\s*$`)
)

type fixedPort struct {
	File string `yaml:"file"`
	Port int    `yaml:"port"`
	Kind string `yaml:"kind,omitempty"`
	Line int    `yaml:"-"`
	Text string `yaml:"-"`
}

type edgePort struct {
	Name   string   `yaml:"name"`
	Port   int      `yaml:"port"`
	Files  []string `yaml:"files"`
	Reason string   `yaml:"reason"`
}

type edgePorts struct {
	EdgePorts []edgePort `yaml:"edge_ports"`
}

type portRatchet struct {
	StartedWith int         `yaml:"started_with"`
	Entries     []fixedPort `yaml:"entries"`
}

func repoRootDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	return filepath.Dir(filepath.Dir(file))
}

func scannedFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(filepath.Join(root, "deploy"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == edgePortsFile || rel == portRatchetFile {
			return nil
		}
		switch {
		case strings.HasSuffix(rel, ".yaml"), strings.HasSuffix(rel, ".yml"), strings.HasSuffix(rel, ".sh"),
			filepath.Base(rel) == "config.json", filepath.Base(rel) == "cli-args-env.json":
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(root, optionDefaults)); err == nil {
		files = append(files, optionDefaults)
	}
	sort.Strings(files)
	return files, nil
}

func literalPorts(rel string, body string) []fixedPort {
	var found []fixedPort
	for i, line := range strings.Split(body, "\n") {
		seen := map[int]bool{}
		for kind, pattern := range map[string]*regexp.Regexp{"arg": argPortPattern, "env": envPortPattern, "loopback": loopbackPortPattern} {
			for _, m := range pattern.FindAllStringSubmatch(line, -1) {
				port, err := strconv.Atoi(m[1])
				if err != nil || port == 0 || port > 65535 || seen[port] {
					continue
				}
				seen[port] = true
				found = append(found, fixedPort{File: rel, Port: port, Kind: kind, Line: i + 1, Text: strings.TrimSpace(line)})
			}
		}
	}
	return found
}

func defaultPorts(rel string, body []byte) ([]fixedPort, error) {
	var doc struct {
		Options map[string]struct {
			Default any `json:"default"`
		} `json:"options"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc.Options == nil {
		var flat map[string]any
		if ferr := json.Unmarshal(body, &flat); ferr != nil {
			return nil, fmt.Errorf("%s: %w", rel, ferr)
		}
		doc.Options = map[string]struct {
			Default any `json:"default"`
		}{}
		for k, v := range flat {
			doc.Options[k] = struct {
				Default any `json:"default"`
			}{Default: v}
		}
	}
	var found []fixedPort
	for name, opt := range doc.Options {
		if name != "port" && !strings.HasSuffix(name, "-port") {
			continue
		}
		port, ok := opt.Default.(float64)
		if !ok || port == 0 {
			continue
		}
		found = append(found, fixedPort{File: rel, Port: int(port), Kind: "default", Text: name})
	}
	return found, nil
}

func implicitPorts(rel string, body string) []fixedPort {
	var found []fixedPort
	for _, doc := range documentSeparator.Split(body, -1) {
		kind := serviceKindPattern.FindStringSubmatch(doc)
		if kind == nil {
			continue
		}
		if kind[1] == "DenoPod" && !restartAlwaysRegexp.MatchString(doc) {
			continue
		}
		if explicitZeroPattern.MatchString(doc) {
			continue
		}
		found = append(found, fixedPort{File: rel, Port: 0, Kind: implicitPortKind, Text: kind[1] + " names no port, so it inherits its CLI's default"})
	}
	return found
}

func scanFixedPorts(root string) ([]fixedPort, error) {
	files, err := scannedFiles(root)
	if err != nil {
		return nil, err
	}
	var found []fixedPort
	for _, rel := range files {
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(rel, ".json") {
			defaults, err := defaultPorts(rel, body)
			if err != nil {
				return nil, err
			}
			found = append(found, defaults...)
		}
		found = append(found, literalPorts(rel, string(body))...)
		if strings.HasSuffix(rel, ".yaml") || strings.HasSuffix(rel, ".yml") {
			found = append(found, implicitPorts(rel, string(body))...)
		}
	}
	return found, nil
}

func readYAMLFile(t *testing.T, path string, into any) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(body, into); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func edgeAllows(edges []edgePort, v fixedPort) bool {
	for _, e := range edges {
		if e.Port != v.Port {
			continue
		}
		for _, f := range e.Files {
			if ok, _ := filepath.Match(f, v.File); ok {
				return true
			}
		}
	}
	return false
}

func ratchetKey(file string, port int) string {
	return fmt.Sprintf("%s:%d", file, port)
}

func unexplainedFixedPorts(root string, edges []edgePort, ratchet []fixedPort) (violations []fixedPort, stale []fixedPort, err error) {
	found, err := scanFixedPorts(root)
	if err != nil {
		return nil, nil, err
	}
	listed := map[string]bool{}
	for _, r := range ratchet {
		listed[ratchetKey(r.File, r.Port)] = true
	}
	matched := map[string]bool{}
	for _, v := range found {
		if edgeAllows(edges, v) {
			continue
		}
		key := ratchetKey(v.File, v.Port)
		if listed[key] {
			matched[key] = true
			continue
		}
		violations = append(violations, v)
	}
	for _, r := range ratchet {
		if !matched[ratchetKey(r.File, r.Port)] {
			stale = append(stale, r)
		}
	}
	return violations, stale, nil
}

func loadPortPolicy(t *testing.T, root string) ([]edgePort, portRatchet) {
	t.Helper()
	var edges edgePorts
	readYAMLFile(t, filepath.Join(root, edgePortsFile), &edges)
	var ratchet portRatchet
	readYAMLFile(t, filepath.Join(root, portRatchetFile), &ratchet)
	return edges.EdgePorts, ratchet
}

func describeFixedPort(v fixedPort) string {
	if v.Line > 0 {
		return fmt.Sprintf("%s:%d port %d (%s): %s", v.File, v.Line, v.Port, v.Kind, v.Text)
	}
	return fmt.Sprintf("%s port %d (%s): %s", v.File, v.Port, v.Kind, v.Text)
}

func TestNoFixedPortBehindTheEdge(t *testing.T) {
	root := repoRootDir(t)
	edges, ratchet := loadPortPolicy(t, root)
	violations, _, err := unexplainedFixedPorts(root, edges, ratchet.Entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Errorf("%s: a listener behind the edge binds port 0 and reports its address; a fixed port belongs in %s only by a user decision", describeFixedPort(v), edgePortsFile)
	}
}

func TestTheRatchetListsOnlyViolationsThatStillExist(t *testing.T) {
	root := repoRootDir(t)
	edges, ratchet := loadPortPolicy(t, root)
	_, stale, err := unexplainedFixedPorts(root, edges, ratchet.Entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range stale {
		t.Errorf("%s lists %s port %d, which the tree no longer names; delete the line so the ratchet only shrinks", portRatchetFile, r.File, r.Port)
	}
	seen := map[string]bool{}
	for _, r := range ratchet.Entries {
		key := ratchetKey(r.File, r.Port)
		if seen[key] {
			t.Errorf("%s lists %s twice", portRatchetFile, key)
		}
		seen[key] = true
	}
	if len(ratchet.Entries) > ratchet.StartedWith {
		t.Errorf("%s holds %d entries but started with %d; it only shrinks", portRatchetFile, len(ratchet.Entries), ratchet.StartedWith)
	}
}

func TestEveryEdgePortHasAReason(t *testing.T) {
	root := repoRootDir(t)
	edges, _ := loadPortPolicy(t, root)
	if len(edges) == 0 {
		t.Fatalf("%s lists no edge port", edgePortsFile)
	}
	for _, e := range edges {
		if e.Name == "" || e.Port <= 0 || e.Port > 65535 || strings.TrimSpace(e.Reason) == "" || len(e.Files) == 0 {
			t.Errorf("%s: edge port %+v needs a name, a port, the files it is allowed in and a reason", edgePortsFile, e)
		}
		for _, f := range e.Files {
			if _, err := filepath.Match(f, ""); err != nil {
				t.Errorf("%s: edge port %s file pattern %q: %v", edgePortsFile, e.Name, f, err)
			}
		}
	}
}

func writeFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func fixtureViolations(t *testing.T, files map[string]string, ratchet []fixedPort) ([]fixedPort, []fixedPort) {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, files)
	edges := []edgePort{{Name: "caddy", Port: 443, Files: []string{"deploy/caddy/*.sh"}, Reason: "public https"}}
	violations, stale, err := unexplainedFixedPorts(root, edges, ratchet)
	if err != nil {
		t.Fatal(err)
	}
	return violations, stale
}

func TestAPlantedFixedPortBeyondTheRatchetFails(t *testing.T) {
	files := map[string]string{
		"deploy/svc/run.sh":       "deno run mod.ts --port 0\n",
		"deploy/legacy/start.sh":  "KCP_PORT=${KCP_PORT:-6443}\n",
		"deploy/caddy/serve.sh":   "caddy reverse-proxy --from :443 --to 127.0.0.1:443\n",
		"deploy/svc/planted.sh":   "curl http://127.0.0.1:2583/xrpc/_health\n",
		"deploy/svc/flag.yaml":    "args: [\"--serve-port\", \"2590\"]\n",
		"deploy/svc/config.json":  "{\"port\": 2591}\n",
		"deploy/svc/envfile.yaml": "env:\n  TLS_PORT: \"2592\"\n",
	}
	ratchet := []fixedPort{{File: "deploy/legacy/start.sh", Port: 6443}}
	violations, stale := fixtureViolations(t, files, ratchet)
	if len(stale) != 0 {
		t.Fatalf("the ratchet entry still matches the fixture but was reported stale: %+v", stale)
	}
	got := map[string]bool{}
	for _, v := range violations {
		got[ratchetKey(v.File, v.Port)] = true
	}
	for _, want := range []string{"deploy/svc/planted.sh:2583", "deploy/svc/flag.yaml:2590", "deploy/svc/config.json:2591", "deploy/svc/envfile.yaml:2592"} {
		if !got[want] {
			t.Errorf("planted literal %s was not reported; got %v", want, got)
		}
	}
	for _, allowed := range []string{"deploy/legacy/start.sh:6443", "deploy/caddy/serve.sh:443", "deploy/svc/run.sh:0"} {
		if got[allowed] {
			t.Errorf("%s is ratcheted, an edge port or port 0, yet was reported", allowed)
		}
	}
	if len(violations) != 4 {
		t.Errorf("want exactly the 4 planted violations, got %d: %+v", len(violations), violations)
	}
}

func TestAStaleRatchetEntryFails(t *testing.T) {
	files := map[string]string{"deploy/svc/run.sh": "deno run mod.ts --port 0\n"}
	_, stale := fixtureViolations(t, files, []fixedPort{{File: "deploy/svc/run.sh", Port: 2583}})
	if len(stale) != 1 {
		t.Fatalf("a ratchet entry the tree no longer names must be reported stale, got %+v", stale)
	}
}

func TestAServiceManifestWithoutAnExplicitPortIsAViolation(t *testing.T) {
	files := map[string]string{
		"deploy/svc/explicit.yaml": "apiVersion: deno.computer/v1alpha1\nkind: DenoPod\nspec:\n  restartPolicy: Always\n  env:\n    SERVICE_ENV: '{\"PORT\":\"0\"}'\n",
		"deploy/svc/implicit.yaml": "apiVersion: deno.computer/v1alpha1\nkind: DenoPod\nspec:\n  restartPolicy: Always\n  env:\n    SERVICE_ARGS: '[\"--hostname\",\"x\"]'\n",
		"deploy/svc/job.yaml":      "apiVersion: deno.computer/v1alpha1\nkind: DenoPod\nspec:\n  restartPolicy: Never\n",
		"deploy/svc/host.yaml":     "kind: Namespace\n---\nkind: HostProcess\nspec:\n  args: [\"--listen\"]\n",
	}
	violations, _ := fixtureViolations(t, files, nil)
	got := map[string]bool{}
	for _, v := range violations {
		if v.Kind == implicitPortKind {
			got[v.File] = true
		}
	}
	if !got["deploy/svc/implicit.yaml"] || !got["deploy/svc/host.yaml"] {
		t.Errorf("a service manifest naming no port must be a violation; got %v", got)
	}
	if got["deploy/svc/explicit.yaml"] || got["deploy/svc/job.yaml"] {
		t.Errorf("an explicit port 0 or a one-shot pod is not a violation; got %v", got)
	}
}
