package assets

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/denospec"
)

func TestDNSSetWritesTheShimAndTheProbe(t *testing.T) {
	dir := t.TempDir()
	paths, err := DNSSet(dir).Materialise()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(paths[ShimName]) != filepath.Join(dir, DNSDirName) {
		t.Fatalf("the shim landed in %s, not beside the runs", filepath.Dir(paths[ShimName]))
	}
	for name, want := range map[string]string{ShimName: Shim, ProbeName: Probe} {
		body, err := os.ReadFile(paths[name])
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != want {
			t.Fatalf("%s on disk does not match the embedded copy", name)
		}
	}
}

func TestTheShimNamesEveryVariableTheProbeIsGranted(t *testing.T) {
	for _, name := range DNSProbeEnv {
		if !strings.Contains(Shim, name) {
			t.Fatalf("the probe is granted %s but the shim never reads it, so the grant is a lie", name)
		}
	}
}

func TestTheProbeAllowsEveryVariableTheShimReads(t *testing.T) {
	reads := shimEnvReads(Shim)
	if len(reads) == 0 {
		t.Fatal("the shim reads no environment, so this test would pass without checking anything")
	}
	command := strings.Join(denospec.ProbeCommand("deno", "/runs/.kcpdns/shim.ts", "/runs/.kcpdns/probe.ts", "pds.default.alice.svc.kcp.local", "/health", DNSProbeEnv), " ")
	for _, name := range reads {
		if !knownEnv(DNSProbeEnv, name) {
			t.Fatalf("the shim reads %s and the probe command does not allow it: %s", name, command)
		}
	}
	if !strings.Contains(command, "--allow-net") {
		t.Fatalf("the probe resolves a name to an address and looks one up at the kcp API, so it needs the network: %s", command)
	}
}

func TestTheShimPatchesEveryEntryPointDenoUses(t *testing.T) {
	for _, want := range []string{"globalThis.fetch", "globalThis.WebSocket", `["connect", "connectTls"]`, "Deno.resolveDns"} {
		if !strings.Contains(Shim, want) {
			t.Fatalf("the shim does not patch %s", want)
		}
	}
}

func shimEnvReads(source string) []string {
	seen := map[string]bool{}
	var out []string
	for _, match := range regexp.MustCompile(`Deno\.env\.get\("([A-Z0-9_]+)"\)`).FindAllStringSubmatch(source, -1) {
		if seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		out = append(out, match[1])
	}
	return out
}

func knownEnv(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

func TestTheShimResolvesANameFromTheTable(t *testing.T) {
	deno, err := exec.LookPath("deno")
	if err != nil {
		t.Skip("deno is not on PATH, so the shim cannot be run")
	}
	dir := t.TempDir()
	paths, err := DNSSet(dir).Materialise()
	if err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(dir, "driver.ts")
	body := `const [addr] = await Deno.resolveDns("pds.default.svc.kcp.local", "A");` + "\n" + `console.log(addr);` + "\n"
	if err := os.WriteFile(driver, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, deno, "run",
		"--allow-env="+strings.Join(DNSProbeEnv, ","),
		"--allow-net",
		"--preload", paths[ShimName],
		driver,
	)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"KCP_SERVICE_DOMAIN=kcp.local",
		`KCP_DNS_TABLE={"pds.default.svc.kcp.local":"127.0.0.1:8080"}`,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("deno run: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "127.0.0.1" {
		t.Fatalf("the shim resolved the name to %q, the table says 127.0.0.1:8080", got)
	}
}

func runShimDriver(t *testing.T, dir, driver string, allowRead []string, env ...string) string {
	t.Helper()
	deno, err := exec.LookPath("deno")
	if err != nil {
		t.Skip("deno is not on PATH, so the shim cannot be run")
	}
	paths, err := DNSSet(dir).Materialise()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "driver.ts")
	if err := os.WriteFile(script, []byte(driver), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := []string{"run", "--allow-env=" + strings.Join(DNSProbeEnv, ","), "--allow-net"}
	if len(allowRead) > 0 {
		args = append(args, "--allow-read="+strings.Join(allowRead, ","), "--allow-write="+strings.Join(allowRead, ","))
	}
	args = append(args, "--preload", paths[ShimName], script)
	cmd := exec.CommandContext(ctx, deno, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("deno run: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func namedServer(t *testing.T, name string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(name))
	}))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://")
}

func TestTheShimRereadsTheTableFileWhenItChanges(t *testing.T) {
	dir := t.TempDir()
	first := namedServer(t, "first")
	second := namedServer(t, "second")
	table := filepath.Join(dir, TableName)
	if err := os.WriteFile(table, []byte(`{"pds.default.alice.svc.kcp.local":"`+first+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	driver := `const get = async () => await (await fetch("http://pds.default.alice.svc.kcp.local/")).text();
const a = await get();
await new Promise((r) => setTimeout(r, 20));
await Deno.writeTextFile(Deno.args[0] ?? "` + table + `", JSON.stringify({ "pds.default.alice.svc.kcp.local": "` + second + `" }));
const b = await get();
console.log(a + " " + b);
`
	got := runShimDriver(t, dir, driver, []string{table}, "KCP_SERVICE_DOMAIN=kcp.local", "KCP_DNS_TABLE={}", TableFileEnv+"="+table)
	if got != "first second" {
		t.Fatalf("the shim answered %q, want the address from the file and then the one that replaced it", got)
	}
}

func TestTheShimKeepsItsEnvTableWhenTheFileCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	only := namedServer(t, "env")
	driver := `console.log(await (await fetch("http://pds.default.alice.svc.kcp.local/")).text());`
	got := runShimDriver(t, dir, driver, nil, "KCP_SERVICE_DOMAIN=kcp.local",
		`KCP_DNS_TABLE={"pds.default.alice.svc.kcp.local":"`+only+`"}`, TableFileEnv+"="+filepath.Join(dir, "absent.json"))
	if got != "env" {
		t.Fatalf("the shim answered %q, an unreadable table file must not hide the environment's table", got)
	}
}

func TestTheShimDiscoversTheAddressAPodReported(t *testing.T) {
	dir := t.TempDir()
	service := namedServer(t, "bound")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/clusters/root:alice/apis/deno.computer/v1alpha1/namespaces/default/denopods/pds" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"spec":{"env":{"SERVICE_ARGS":"[\"--port\",\"0\"]"}},"status":{"address":"` + service + `"}}`))
	}))
	t.Cleanup(api.Close)
	driver := `console.log(await (await fetch("http://pds.default.alice.svc.kcp.local/")).text());`
	got := runShimDriver(t, dir, driver, nil, "KCP_SERVICE_DOMAIN=kcp.local", "KCP_DNS_TABLE={}",
		"KCP_SERVER="+api.URL+"/clusters/root:bob", `KCP_TOKENS={"root:alice":"t"}`)
	if got != "bound" {
		t.Fatalf("the shim answered %q, a port 0 pod is found by the address it reported, not by its manifest", got)
	}
}

func TestTheShimKeepsTheMethodAndBodyOfARequestObject(t *testing.T) {
	dir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write([]byte(r.Method + " " + string(body)))
	}))
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")
	driver := `const req = new Request("http://pds.default.alice.svc.kcp.local/op", { method: "POST", body: "signed-op", headers: { "content-type": "application/json" } });
console.log(await (await fetch(req)).text());
`
	got := runShimDriver(t, dir, driver, nil, "KCP_SERVICE_DOMAIN=kcp.local", `KCP_DNS_TABLE={"pds.default.alice.svc.kcp.local":"`+address+`"}`)
	if got != "POST signed-op" {
		t.Fatalf("the service saw %q; a Request object rewritten to its address must keep its method and body", got)
	}
}
