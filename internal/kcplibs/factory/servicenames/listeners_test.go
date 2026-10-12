package servicenames

import (
	"context"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/listeners"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/ref"
)

func withStatusListeners(p *unstructured.Unstructured, address string, ls ...listeners.Listener) *unstructured.Unstructured {
	items := []any{}
	for _, l := range ls {
		items = append(items, map[string]any{"name": l.Name, "kind": l.Kind, "address": l.Address})
	}
	p.Object["status"] = map[string]any{"address": address, "listeners": items}
	return p
}

var threeListeners = []listeners.Listener{
	{Name: "http", Kind: listeners.KindTCP, Address: "127.0.0.1:41101"},
	{Name: "admin", Kind: listeners.KindTCP, Address: "127.0.0.1:41102"},
	{Name: "control", Kind: listeners.KindUnix, Address: "/runs/pds/control.sock"},
}

func TestTheTableNamesEveryListenerAPodReported(t *testing.T) {
	source := SourceFunc(func() []*unstructured.Unstructured {
		return []*unstructured.Unstructured{
			withStatusListeners(pod("pds", "default", "root:alice", map[string]string{ArgsKey: `["--port","0"]`}), "127.0.0.1:41101", threeListeners...),
		}
	})
	table, _ := New(Options{Source: source, Domain: "kcp.local"}).Table(context.Background())
	want := map[string]string{
		"pds.default.alice.svc.kcp.local":          "127.0.0.1:41101",
		"_http.pds.default.alice.svc.kcp.local":    "127.0.0.1:41101",
		"_admin.pds.default.alice.svc.kcp.local":   "127.0.0.1:41102",
		"_control.pds.default.alice.svc.kcp.local": "unix:/runs/pds/control.sock",
	}
	if !reflect.DeepEqual(table, want) {
		t.Fatalf("table = %v, want %v", table, want)
	}
}

func TestObservedListenersReplaceAndClearAPodsEntries(t *testing.T) {
	resolver := New(Options{Source: SourceFunc(func() []*unstructured.Unstructured { return nil }), Domain: "kcp.local"})
	target := ref.New("root:alice", "default", "pds")
	ctx := context.Background()
	resolver.ObserveListeners(ctx, target, "127.0.0.1:41101", threeListeners)
	table, _ := resolver.Table(ctx)
	if table["_admin.pds.default.alice.svc.kcp.local"] != "127.0.0.1:41102" || table["pds.default.alice.svc.kcp.local"] != "127.0.0.1:41101" {
		t.Fatalf("table = %v, an observed listener is resolvable by service and listener name", table)
	}
	resolver.ObserveListeners(ctx, target, "127.0.0.1:41201", threeListeners[:1])
	table, _ = resolver.Table(ctx)
	if _, stale := table["_admin.pds.default.alice.svc.kcp.local"]; stale {
		t.Fatalf("table = %v, a listener the pod no longer reports is gone", table)
	}
	resolver.ObserveListeners(ctx, target, "", nil)
	table, _ = resolver.Table(ctx)
	if len(table) != 0 {
		t.Fatalf("table = %v, clearing the observation clears every entry of the pod", table)
	}
}

func TestBoundListenersPreferTheReportAndFallBackToThePortFile(t *testing.T) {
	got := BoundListeners(`["--port","0","--hostname","127.0.0.1"]`, "", 41300, threeListeners)
	if !reflect.DeepEqual(got, threeListeners) {
		t.Fatalf("BoundListeners = %+v, the listeners report wins over the single port file", got)
	}
	got = BoundListeners(`["--port","0","--hostname","0.0.0.0"]`, "", 41301, nil)
	want := []listeners.Listener{{Name: listeners.DefaultTCPName, Kind: listeners.KindTCP, Address: "127.0.0.1:41301"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BoundListeners = %+v, want %+v from the port file", got, want)
	}
	if got := BoundListeners("", "", 0, nil); got != nil {
		t.Fatalf("BoundListeners = %+v before the process bound anything", got)
	}
}

func TestALoopbackListenerIsAdvertisedUnderTheServicesLocalhostName(t *testing.T) {
	reported := []listeners.Listener{
		{Name: "tcp", Kind: listeners.KindTCP, Address: "127.0.0.1:35051"},
		{Name: "relay", Kind: listeners.KindRelay, Address: "https://did-key-x.relay.localhost"},
	}
	got := BoundListeners(`["--hostname","relay.localhost","--port","0"]`, "", 35051, reported)
	want := []listeners.Listener{
		{Name: "tcp", Kind: listeners.KindTCP, Address: "relay.localhost:35051"},
		{Name: "relay", Kind: listeners.KindRelay, Address: "https://did-key-x.relay.localhost"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BoundListeners = %+v, want %+v: a service that checks Host is dialled by its own .localhost name", got, want)
	}
	if !reflect.DeepEqual(reported[0].Address, "127.0.0.1:35051") {
		t.Fatalf("BoundListeners rewrote the caller's report in place: %+v", reported)
	}
	got = BoundListeners("", `{"HOSTNAME":"relay.default.relay.svc.kcp.local"}`, 42517, reported[:1])
	if !reflect.DeepEqual(got, reported[:1]) {
		t.Fatalf("BoundListeners = %+v, a name that is not a .localhost name does not resolve to loopback and is not advertised", got)
	}
}

func TestFixedPortsNamesEveryPortAPodWouldBind(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want []string
	}{
		{map[string]string{ArgsKey: `["--port","0"]`, EnvKey: `{"PORT":"0","SERVE_PORT":"0"}`}, nil},
		{map[string]string{ArgsKey: `["--port","2583"]`}, []string{"SERVICE_ARGS --port 2583"}},
		{map[string]string{ArgsKey: `["--serve-port","2586","--guest-tls-port","41000"]`}, []string{"SERVICE_ARGS --serve-port 2586"}},
		{map[string]string{EnvKey: `{"PORT":"2587","DISPATCHER_PORT":"41000"}`}, []string{"SERVICE_ENV PORT 2587"}},
		{map[string]string{"TLS_PORT": "2589", "DISPATCHER_PORT": "41000"}, []string{"env TLS_PORT 2589"}},
		{map[string]string{"PORT": "__PORT__", ArgsKey: `["--port","__PORT__"]`}, nil},
	}
	for _, tc := range cases {
		if got := FixedPorts(tc.env); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("FixedPorts(%v) = %q, want %q", tc.env, got, tc.want)
		}
	}
}
