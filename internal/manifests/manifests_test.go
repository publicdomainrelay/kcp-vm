package manifests

import (
	"errors"
	"strings"
	"testing"
)

func goldenNetwork() *MarketNetwork {
	return &MarketNetwork{
		NetworkID:           "network1",
		StateDir:            "/market-network/state",
		GuestTLSDir:         "/market-network/guest-tls",
		PDSPrivateKeyHex:    strings.Repeat("1", 64),
		BobPDSPrivateKeyHex: strings.Repeat("2", 64),
		Bidders: []Bidder{
			{Name: "bidder-1", Handle: "bidder-1.bob.test", PrivateKeyHex: strings.Repeat("3", 64)},
			{Name: "bidder-2", Handle: "bidder-2.bob.test", PrivateKeyHex: strings.Repeat("4", 64)},
		},
		DispatcherPort: 40001,
		GuestTLSPort:   40002,
		Firecracker: Firecracker{
			NodeImage:   "/fc/nodeimage.json",
			NodeBoot:    "/usr/local/bin/socialweb-nodeboot",
			Config:      "/fc/config.json",
			RepoDir:     "/fc/repo",
			Preinstall:  "/fc/preinstall.json",
			VMM:         "/usr/local/bin/firecracker",
			RunnerImage: "runner@sha256:abc",
			WorkRoot:    "/fc/work",
		},
	}
}

func mustSet(t *testing.T, name string) Set {
	t.Helper()
	ix, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	s, ok := ix.Set(name)
	if !ok {
		t.Fatalf("sets.yaml names no set %s", name)
	}
	return s
}

func TestAPhaseRenderedBeforeItsCaptureIsAMissingValue(t *testing.T) {
	s := mustSet(t, "market-network")
	values := goldenNetwork()
	values.GuestTLSPort = 0
	for _, p := range s.Phases {
		_, _, err := s.RenderText(p, values)
		switch p.Name {
		case "bidders":
			if !errors.Is(err, ErrMissingValue) {
				t.Errorf("phase bidders rendered with no GuestTLSPort captured: %v", err)
			}
		default:
			if err != nil {
				t.Errorf("phase %s: %v", p.Name, err)
			}
		}
	}
}

func TestSetsYAMLDecodesStrictly(t *testing.T) {
	good := "sets:\n  - name: a\n    summary: s\n    values: None\n    phases:\n      - name: p\n        workspace: root\n        files:\n          - path: workspace-deno-provider.yaml\n"
	if _, err := Parse([]byte(good)); err != nil {
		t.Fatalf("a well-formed index: %v", err)
	}
	bad := map[string]string{
		"an unknown field":          strings.Replace(good, "summary: s", "summary: s\n    sumary: s", 1),
		"an unknown values type":    strings.Replace(good, "values: None", "values: Nope", 1),
		"a file not in deploy.FS":   strings.Replace(good, "workspace-deno-provider.yaml", "nope.yaml", 1),
		"a phase with no workspace": strings.Replace(good, "        workspace: root\n", "", 1),
		"an unknown wait kind":      good + "        after:\n          - kind: Pod\n            for: Ready\n",
		"a wait for its kind lacks": good + "        after:\n          - kind: Workspace\n            for: Address\n",
		"a capture into no field":   good + "        after:\n          - kind: Workspace\n            for: Ready\n            capture:\n              into: Nope\n              from: LogicalCluster\n",
		"each over no field":        strings.Replace(good, "path: workspace-deno-provider.yaml", "path: workspace-deno-provider.yaml\n            each: Nope\n            as: X", 1),
	}
	for what, doc := range bad {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("an index with %s parsed", what)
		}
	}
}

func TestDecodeIsStrict(t *testing.T) {
	ws := "apiVersion: tenancy.kcp.io/v1alpha1\nkind: Workspace\nmetadata:\n  name: a\nspec:\n  type:\n    name: universal\n    path: root\n"
	if objs, err := Decode("ws.yaml", []byte(ws)); err != nil || len(objs) != 1 || objs[0].Typed == nil {
		t.Fatalf("a valid Workspace: %v %v", objs, err)
	}
	bad := map[string]string{
		"an unknown field":    strings.Replace(ws, "spec:\n", "spec:\n  bogus: 1\n", 1),
		"a duplicate key":     strings.Replace(ws, "  name: a\n", "  name: a\n  name: b\n", 1),
		"an unknown kind":     strings.Replace(ws, "kind: Workspace", "kind: Gadget", 1),
		"an unknown version":  strings.Replace(ws, "v1alpha1", "v9", 1),
		"no name":             strings.Replace(ws, "  name: a\n", "  labels: {}\n", 1),
		"an unknown metadata": strings.Replace(ws, "  name: a\n", "  name: a\n  nmae: b\n", 1),
	}
	for what, doc := range bad {
		if _, err := Decode("bad.yaml", []byte(doc)); err == nil {
			t.Errorf("a manifest with %s decoded", what)
		}
	}
}

func TestKindsSelectWhatAFileContributes(t *testing.T) {
	s := mustSet(t, "permission-inventory")
	values := &PermissionInventory{ProviderCluster: "abc123"}
	kinds := map[string]map[string]int{}
	for _, p := range s.Phases {
		r, err := s.RenderPhase(p, values)
		if err != nil {
			t.Fatalf("phase %s: %v", p.Name, err)
		}
		kinds[p.Name] = map[string]int{}
		for _, o := range r.Objects {
			kinds[p.Name][o.Unstructured.GetKind()]++
		}
	}
	if kinds["subjects"]["ServiceAccount"] != 4 || len(kinds["subjects"]) != 1 {
		t.Errorf("subjects applies %v; want the four ServiceAccounts and nothing else", kinds["subjects"])
	}
	if kinds["grants"]["ServiceAccount"] != 0 || kinds["grants"]["ClusterRole"] != 4 || kinds["grants"]["ClusterRoleBinding"] != 4 {
		t.Errorf("grants applies %v; want four ClusterRoles and four ClusterRoleBindings", kinds["grants"])
	}
}

func TestCaptureSetsTheTypedField(t *testing.T) {
	b := goldenNetwork()
	if err := SetCaptured(b, Capture{Into: "DispatcherPort", From: CaptureAddressPort}, "127.0.0.1:51234"); err != nil {
		t.Fatal(err)
	}
	if b.DispatcherPort != 51234 {
		t.Fatalf("DispatcherPort = %d", b.DispatcherPort)
	}
	p := &PermissionInventory{}
	if err := SetCaptured(p, Capture{Into: "ProviderCluster", From: CaptureLogicalCluster}, "2x9k"); err != nil || p.ProviderCluster != "2x9k" {
		t.Fatalf("ProviderCluster = %q, %v", p.ProviderCluster, err)
	}
	if err := SetCaptured(b, Capture{Into: "DispatcherPort", From: CaptureAddressPort}, "no-port"); err == nil {
		t.Fatal("an address with no port was captured")
	}
}

func TestAManifestTakesFieldReferencesOnly(t *testing.T) {
	data := map[string]any{"A": map[string]any{"B": "x"}, "N": 3}
	got, err := execute("t", "a={{.A.B}} n={{ .N }}\n", data)
	if err != nil || string(got) != "a=x n=3\n" {
		t.Fatalf("%q, %v", got, err)
	}
	for _, text := range []string{"{{if .N}}y{{end}}", "{{range .A}}{{end}}", "{{.A.B | printf \"%s\"}}", "{{define \"x\"}}{{end}}", "{{.A}}", "{{$x := .N}}"} {
		if _, err := execute("t", text, data); err == nil {
			t.Errorf("%s rendered", text)
		}
	}
	if _, err := execute("t", "{{.A.C}}", data); !errors.Is(err, ErrMissingValue) {
		t.Errorf("a missing nested key: %v", err)
	}
}
