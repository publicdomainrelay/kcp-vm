package denoruntime

import (
	"slices"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/manifests"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tsbundle"
)

func fixture() Config {
	return Config{StateDir: "/s/inst", Deno: "/a/deno", Kubeconfig: "/s/inst/kcp/admin.kubeconfig", Unit: "socialweb-core-x-dev.service",
		Closure: tsbundle.Prepared{Root: "/ts/root", Key: strings.Repeat("a", 64), DenoDirs: "/s/inst/deno"}}
}

func TestThePodRunnerRunsTheExtractedDenoUnderTheStateDirWithNoTimeout(t *testing.T) {
	o := fixture().PodOptions(nil)
	if o.DenoBin != "/a/deno" || o.RunsDir != "/s/inst/denokcp/runs" || o.Timeout >= 0 || o.Unit != "socialweb-core-x-dev.service" {
		t.Fatalf("pod options %+v: want the extracted deno, <state>/denokcp/runs, a negative timeout and the core unit", o)
	}
	if o.NamespaceDir != "/s/inst/deno/"+strings.Repeat("a", 64) {
		t.Fatalf("DENO_DIR %q: want the closure-keyed module cache", o.NamespaceDir)
	}
	for _, kv := range []string{TSRootEnv + "=/ts/root", TSKeyEnv + "=" + strings.Repeat("a", 64)} {
		if !slices.Contains(o.ExtraEnv, kv) {
			t.Fatalf("env %v lacks %s: every DenoPod names the closure it runs", o.ExtraEnv, kv)
		}
	}
	e := fixture().EngineOptions()
	if e.Timeout >= 0 || e.RunsDir != o.RunsDir || e.DenoBin != o.DenoBin {
		t.Fatalf("engine options %+v", e)
	}
}

func TestARelativePathOrAnUnpreparedClosureIsRefused(t *testing.T) {
	if err := fixture().Check(); err != nil {
		t.Fatal(err)
	}
	c := fixture()
	c.Deno = "deno"
	if c.Check() == nil {
		t.Fatal("deno from PATH must be refused")
	}
	c = fixture()
	c.Closure.Key = ""
	if c.Check() == nil {
		t.Fatal("an unprepared closure must be refused")
	}
}

func TestTheRuntimeSetIsEmbedded(t *testing.T) {
	ix, err := manifests.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ix.Set(ManifestSet); !ok {
		t.Fatalf("deploy/sets.yaml names no %s set; the core applies it before the provider watches", ManifestSet)
	}
}
