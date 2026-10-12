package servicenames

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/ref"
)

func withStatusAddress(p *unstructured.Unstructured, address string) *unstructured.Unstructured {
	p.Object["status"] = map[string]any{"address": address}
	return p
}

func TestAPortZeroManifestAdvertisesNothingUntilBound(t *testing.T) {
	for _, tc := range []struct{ args, env string }{
		{`["--port","0"]`, ""},
		{"", `{"PORT":"0"}`},
		{`["--port","0","--hostname","relay.localhost"]`, `{"PORT":"0"}`},
	} {
		if got := AdvertisedAddress(tc.args, tc.env); got != "" {
			t.Fatalf("AdvertisedAddress(%q, %q) = %q, port 0 is not an address anyone can reach", tc.args, tc.env, got)
		}
	}
}

func TestBoundAddressKeepsTheAdvertisedHost(t *testing.T) {
	cases := []struct {
		args, env string
		port      int32
		want      string
	}{
		{`["--port","0","--hostname","127.0.0.1"]`, "", 41001, "127.0.0.1:41001"},
		{`["--hostname","relay.localhost","--port","0"]`, `{"PORT":"0"}`, 41002, "relay.localhost:41002"},
		{`["--port","0","--hostname","0.0.0.0"]`, "", 41003, "127.0.0.1:41003"},
		{"", "", 41004, "127.0.0.1:41004"},
		{`["--port","0"]`, "", 0, ""},
	}
	for _, tc := range cases {
		if got := BoundAddress(tc.args, tc.env, tc.port); got != tc.want {
			t.Fatalf("BoundAddress(%q, %q, %d) = %q, want %q", tc.args, tc.env, tc.port, got, tc.want)
		}
	}
}

func TestTablePrefersTheAddressThePodReported(t *testing.T) {
	source := SourceFunc(func() []*unstructured.Unstructured {
		return []*unstructured.Unstructured{
			withStatusAddress(pod("pds", "default", "root:alice", map[string]string{ArgsKey: `["--port","0"]`}), "127.0.0.1:41010"),
			withStatusAddress(pod("tls", "default", "root:relay", map[string]string{}), "127.0.0.1:41011"),
			pod("plc", "default", "root:global", map[string]string{ArgsKey: `["--port","0"]`}),
			withStatusAddress(pod("old", "default", "root:global", map[string]string{ArgsKey: `["--port","2587"]`}), "127.0.0.1:41012"),
		}
	})
	table, _ := New(Options{Source: source, Domain: "kcp.local"}).Table(context.Background())
	want := map[string]string{
		"pds.default.alice.svc.kcp.local":  "127.0.0.1:41010",
		"tls.default.relay.svc.kcp.local":  "127.0.0.1:41011",
		"old.default.global.svc.kcp.local": "127.0.0.1:41012",
	}
	if len(table) != len(want) {
		t.Fatalf("table = %v, want %v (a port 0 pod that has not reported is absent)", table, want)
	}
	for name, address := range want {
		if table[name] != address {
			t.Fatalf("table[%s] = %q, want %q", name, table[name], address)
		}
	}
}

func TestAnObservedAddressWinsOverTheCache(t *testing.T) {
	source := SourceFunc(func() []*unstructured.Unstructured {
		return []*unstructured.Unstructured{
			withStatusAddress(pod("pds", "default", "root:alice", map[string]string{ArgsKey: `["--port","0"]`}), "127.0.0.1:41020"),
		}
	})
	resolver := New(Options{Source: source, Domain: "kcp.local"})
	target := ref.New("root:alice", "default", "pds")
	resolver.Observe(context.Background(), target, "127.0.0.1:41021")
	table, _ := resolver.Table(context.Background())
	if got := table["pds.default.alice.svc.kcp.local"]; got != "127.0.0.1:41021" {
		t.Fatalf("table = %v, the provider's own observation is newer than its informer cache", table)
	}
	resolver.Observe(context.Background(), target, "")
	table, _ = resolver.Table(context.Background())
	if got := table["pds.default.alice.svc.kcp.local"]; got != "127.0.0.1:41020" {
		t.Fatalf("table = %v, clearing an observation falls back to the cache", table)
	}
}

func TestEnvNamesTheTableFileAndItsOwnWorkspace(t *testing.T) {
	minter := &fakeMinter{}
	resolver := New(Options{
		Source:    SourceFunc(func() []*unstructured.Unstructured { return nil }),
		Minter:    minter,
		Domain:    "kcp.local",
		TableFile: "/runs/.kcpdns/table.json",
	})
	target := ref.New("root:bob", "default", "bidder")
	env := resolver.Env(context.Background(), target, `["--serve-port","0"]`, `{"PORT":"0"}`, nil)
	if env[TableFileKey] != "/runs/.kcpdns/table.json" {
		t.Fatalf("%s = %q, a workload started before its peers bound must be able to re-read the table", TableFileKey, env[TableFileKey])
	}
	var table map[string]string
	_ = json.Unmarshal([]byte(env[TableKey]), &table)
	if _, present := table["bidder.default.bob.svc.kcp.local"]; present {
		t.Fatalf("table = %v, a port 0 workload has no address before it binds", table)
	}
}

func TestWriteTableFileRewritesOnlyOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "table.json")
	address := "127.0.0.1:41030"
	source := SourceFunc(func() []*unstructured.Unstructured {
		return []*unstructured.Unstructured{withStatusAddress(pod("pds", "default", "root:alice", nil), address)}
	})
	resolver := New(Options{Source: source, Domain: "kcp.local", TableFile: path})
	changed, err := resolver.WriteTableFile(context.Background())
	if err != nil || !changed {
		t.Fatalf("first write: changed=%v err=%v", changed, err)
	}
	read := func() map[string]string {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var table map[string]string
		if err := json.Unmarshal(body, &table); err != nil {
			t.Fatal(err)
		}
		return table
	}
	if read()["pds.default.alice.svc.kcp.local"] != address {
		t.Fatalf("table file = %v", read())
	}
	changed, err = resolver.WriteTableFile(context.Background())
	if err != nil || changed {
		t.Fatalf("unchanged write: changed=%v err=%v", changed, err)
	}
	address = "127.0.0.1:41031"
	changed, err = resolver.WriteTableFile(context.Background())
	if err != nil || !changed {
		t.Fatalf("second write: changed=%v err=%v", changed, err)
	}
	if read()["pds.default.alice.svc.kcp.local"] != "127.0.0.1:41031" {
		t.Fatalf("table file = %v, a restarted pod's new port must reach every reader", read())
	}
}

func TestWriteTableFileWithoutAPathDoesNothing(t *testing.T) {
	resolver := New(Options{Source: SourceFunc(func() []*unstructured.Unstructured { return nil })})
	changed, err := resolver.WriteTableFile(context.Background())
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
}
