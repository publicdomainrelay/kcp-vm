package denoruntime

import (
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/manifests"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tsbundle"
)

func fixture() Config {
	return Config{StateDir: "/s/inst", Deno: "/a/deno", Kubeconfig: "/s/inst/kcp/admin.kubeconfig", Unit: "socialweb-core-x-dev.service",
		Closure: tsbundle.Prepared{Root: "/ts/root", Key: strings.Repeat("a", 64), DenoDirs: "/s/inst/deno"}}
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
