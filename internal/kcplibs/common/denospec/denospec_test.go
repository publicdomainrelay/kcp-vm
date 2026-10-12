package denospec

import (
	"reflect"
	"strings"
	"testing"
)

func TestArgsAll(t *testing.T) {
	args, err := Args(&Permissions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, []string{"-A"}) {
		t.Fatalf("args = %v", args)
	}
}

func TestArgsCapabilities(t *testing.T) {
	args, err := Args(&Permissions{
		Read:         &Permission{Allow: true},
		Net:          &Permission{AllowList: []string{"example.com", "other.example"}},
		Env:          &Permission{DenyList: []string{"SECRET"}},
		Sys:          &Permission{Deny: true},
		HRTime:       true,
		IgnoreEnv:    &Permission{AllowList: []string{"PUBLIC"}},
		AllowScripts: []string{"npm"},
		NoPrompt:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--allow-hrtime",
		"--allow-read",
		"--allow-net=example.com,other.example",
		"--deny-env=SECRET",
		"--deny-sys",
		"--ignore-env=PUBLIC",
		"--allow-scripts=npm",
		"--no-prompt",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args %q must contain %q", joined, want)
		}
	}
}

func TestArgsModelsEveryFlagForm(t *testing.T) {
	args, err := Args(&Permissions{
		Read:     &Permission{Allow: true},
		Write:    &Permission{AllowList: []string{"./", "./tmp"}},
		Net:      &Permission{AllowList: []string{"example.com:443"}, DenyList: []string{"evil.com"}},
		Env:      &Permission{Allow: true, DenyList: []string{"AWS_SECRET_ACCESS_KEY"}},
		Run:      &Permission{AllowList: []string{"curl", "whoami"}},
		FFI:      &Permission{Deny: true},
		Sys:      &Permission{AllowList: []string{"systemMemoryInfo", "osRelease"}},
		Import:   &Permission{DenyList: []string{"esm.sh"}},
		NoPrompt: true,
		HRTime:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--allow-hrtime",
		"--allow-read",
		"--allow-write=./,./tmp",
		"--allow-net=example.com:443",
		"--deny-net=evil.com",
		"--allow-env",
		"--deny-env=AWS_SECRET_ACCESS_KEY",
		"--allow-run=curl,whoami",
		"--deny-ffi",
		"--allow-sys=systemMemoryInfo,osRelease",
		"--deny-import=esm.sh",
		"--no-prompt",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args =\n%v\nwant\n%v", args, want)
	}
}

func TestArgsDenyWithoutAListIsBare(t *testing.T) {
	args, err := Args(&Permissions{Read: &Permission{Deny: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, []string{"--deny-read"}) {
		t.Fatalf("args = %v, want [--deny-read]", args)
	}
}

func TestArgsIgnoreEnvAndAllowScripts(t *testing.T) {
	args, err := Args(&Permissions{
		IgnoreEnv:    &Permission{AllowList: []string{"PORT", "HOME"}},
		AllowScripts: []string{"esbuild"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--ignore-env=PORT,HOME", "--allow-scripts=esbuild"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestArgsNilIsNoFlags(t *testing.T) {
	args, err := Args(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 0 {
		t.Fatalf("args = %v, want none", args)
	}
}

func TestValidateRejectsCommasAndEmptyValues(t *testing.T) {
	if err := Validate(&Permissions{Net: &Permission{AllowList: []string{"a,b"}}}); err == nil {
		t.Fatal("a comma in a value must be refused")
	}
	if err := Validate(&Permissions{Net: &Permission{AllowList: []string{""}}}); err == nil {
		t.Fatal("an empty value must be refused")
	}
	if err := Validate(&Permissions{IgnoreEnv: &Permission{Allow: true}}); err == nil {
		t.Fatal("ignoreEnv must take only an allowList")
	}
	if err := Validate(nil); err != nil {
		t.Fatal(err)
	}
}

func TestEffectiveRestartPolicy(t *testing.T) {
	if EffectiveRestartPolicy("") != RestartAlways {
		t.Fatal("an unset restart policy is Always")
	}
	if EffectiveRestartPolicy(RestartNever) != RestartNever {
		t.Fatal("an explicit policy is preserved")
	}
}

func TestProbeCommandCarriesTheShimsLease(t *testing.T) {
	command := ProbeCommand("deno", "/runs/.kcpdns/shim.ts", "/runs/.kcpdns/probe.ts", "pds.default.alice.svc.kcp.local", "/health",
		[]string{"KCP_SERVICE_DOMAIN", "KCP_DNS_TABLE"})
	joined := strings.Join(command, " ")
	if command[0] != "deno" {
		t.Fatalf("the argv must name the runtime it runs: %v", command)
	}
	for _, want := range []string{
		"run",
		"--allow-env=KCP_SERVICE_DOMAIN,KCP_DNS_TABLE",
		"--allow-net",
		"--preload /runs/.kcpdns/shim.ts",
		"/runs/.kcpdns/probe.ts",
		"pds.default.alice.svc.kcp.local",
		"/health",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("probe command %q must contain %q", joined, want)
		}
	}
}

func TestProbeCommandDefaultsThePath(t *testing.T) {
	command := ProbeCommand("", "shim", "probe", "name", "", nil)
	if command[0] != "deno" {
		t.Fatalf("the runtime defaults to deno: %v", command)
	}
	if command[len(command)-1] != "/" {
		t.Fatalf("path = %q, want /", command[len(command)-1])
	}
}

func TestProbeCommandReadingGrantsOnlyTheNamedFiles(t *testing.T) {
	command := ProbeCommandReading("deno", "/runs/.kcpdns/shim.ts", "/runs/.kcpdns/probe.ts", "pds.default.alice.svc.kcp.local", "/health",
		[]string{"KCP_DNS_TABLE_FILE"}, []string{"/runs/.kcpdns/table.json"})
	joined := strings.Join(command, " ")
	if !strings.Contains(joined, "--allow-read=/runs/.kcpdns/table.json") {
		t.Fatalf("the probe re-reads the table file, so it must be allowed to read exactly that file: %v", command)
	}
	if strings.Contains(joined, "--allow-read ") || strings.Contains(joined, "--allow-all") {
		t.Fatalf("the probe must not be granted more than the table file: %v", command)
	}
	if without := strings.Join(ProbeCommand("deno", "s", "p", "n", "/", nil), " "); strings.Contains(without, "--allow-read") {
		t.Fatalf("no table file means no read grant: %s", without)
	}
}
