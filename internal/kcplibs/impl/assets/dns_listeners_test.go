package assets

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func echoHostServer(t *testing.T, name string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(name + " " + r.Host + " " + r.URL.Path))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestTheShimResolvesAServiceListenerByName(t *testing.T) {
	dir := t.TempDir()
	http := namedServer(t, "http")
	admin := namedServer(t, "admin")
	driver := `const get = async (u: string) => await (await fetch(u)).text();
console.log(await get("http://pds.default.alice.svc.kcp.local/") + " " + await get("http://_admin.pds.default.alice.svc.kcp.local/"));
`
	got := runShimDriver(t, dir, driver, nil, "KCP_SERVICE_DOMAIN=kcp.local",
		`KCP_DNS_TABLE={"pds.default.alice.svc.kcp.local":"`+http+`","_http.pds.default.alice.svc.kcp.local":"`+http+`","_admin.pds.default.alice.svc.kcp.local":"`+admin+`"}`)
	if got != "http admin" {
		t.Fatalf("the shim answered %q, a service's listener is resolved by service name plus listener name", got)
	}
}

func TestTheShimConnectsToAServiceListenerByName(t *testing.T) {
	dir := t.TempDir()
	admin := namedServer(t, "admin")
	driver := `const conn = await Deno.connect({ hostname: "_admin.pds.default.alice.svc.kcp.local", port: 1 });
await conn.write(new TextEncoder().encode("GET / HTTP/1.0\r\nHost: x\r\n\r\n"));
const buf = new Uint8Array(4096);
let text = "";
for (;;) { const n = await conn.read(buf); if (n === null) break; text += new TextDecoder().decode(buf.subarray(0, n)); }
console.log(text.split("\r\n\r\n")[1]);
`
	got := runShimDriver(t, dir, driver, nil, "KCP_SERVICE_DOMAIN=kcp.local",
		`KCP_DNS_TABLE={"_admin.pds.default.alice.svc.kcp.local":"`+admin+`"}`)
	if got != "admin" {
		t.Fatalf("Deno.connect reached %q, want the admin listener", got)
	}
}

func TestTheShimReachesAUnixListener(t *testing.T) {
	sockDir, err := os.MkdirTemp("", "kcpdns")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	socket := filepath.Join(sockDir, "control.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("unix " + r.Host + " " + r.URL.Path))
	})}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close() })

	dir := t.TempDir()
	driver := `console.log(await (await fetch("http://_control.pds.default.alice.svc.kcp.local/state")).text());`
	got := runShimDriver(t, dir, driver, []string{sockDir}, "KCP_SERVICE_DOMAIN=kcp.local",
		`KCP_DNS_TABLE={"_control.pds.default.alice.svc.kcp.local":"unix:`+socket+`"}`)
	if got != "unix localhost /state" {
		t.Fatalf("the unix listener saw %q, want the request on its socket", got)
	}
}

func TestTheShimReachesARelayListenerByItsIngressURL(t *testing.T) {
	dir := t.TempDir()
	relay := echoHostServer(t, "relay")
	driver := `console.log(await (await fetch("https://_relay.pds.default.alice.svc.kcp.local/xrpc/x?y=1")).text());`
	got := runShimDriver(t, dir, driver, nil, "KCP_SERVICE_DOMAIN=kcp.local",
		`KCP_DNS_TABLE={"_relay.pds.default.alice.svc.kcp.local":"`+relay.URL+`"}`)
	want := "relay " + strings.TrimPrefix(relay.URL, "http://") + " /xrpc/x"
	if got != want {
		t.Fatalf("the relay saw %q, want %q: the ingress routes by its own host", got, want)
	}
}

func TestTheShimDiscoversAListenerAPodReported(t *testing.T) {
	dir := t.TempDir()
	admin := namedServer(t, "admin")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/clusters/root:alice/apis/deno.computer/v1alpha1/namespaces/default/denopods/pds" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"spec":{"env":{}},"status":{"address":"127.0.0.1:1","listeners":[{"name":"http","kind":"tcp","address":"127.0.0.1:1"},{"name":"admin","kind":"tcp","address":"` + admin + `"}]}}`))
	}))
	t.Cleanup(api.Close)
	driver := `console.log(await (await fetch("http://_admin.pds.default.alice.svc.kcp.local/")).text());`
	got := runShimDriver(t, dir, driver, nil, "KCP_SERVICE_DOMAIN=kcp.local", "KCP_DNS_TABLE={}",
		"KCP_SERVER="+api.URL+"/clusters/root:bob", `KCP_TOKENS={"root:alice":"t"}`)
	if got != "admin" {
		t.Fatalf("the shim answered %q, a listener is discovered from the pod's status.listeners", got)
	}
}
