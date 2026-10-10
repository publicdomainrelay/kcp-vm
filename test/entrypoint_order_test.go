package test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTheLegacyKeyMigrationRunsBeforeAnythingCanReadAMintCredential(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	root := filepath.Dir(filepath.Dir(file))
	raw, err := os.ReadFile(filepath.Join(root, "deploy", "container-entrypoint.sh"))
	if err != nil {
		t.Fatalf("reading the container entrypoint: %v", err)
	}
	script := string(raw)

	migrate := strings.Index(script, "/usr/local/bin/socialweb-clusterprovider migrate")
	if migrate < 0 {
		t.Fatal("the container entrypoint never runs the legacy-key migration as its own step, so " +
			"the rename happens inside the clusterprovider's startup instead -- after the gateway is " +
			"already serving. A mint request in that window names the legacy credential file and is " +
			"refused, which fails closed but reads to an operator as a broken deployment")
	}
	gateway := strings.Index(script, "/usr/local/bin/socialweb-gateway &")
	if gateway < 0 {
		t.Fatal("the container entrypoint does not start the gateway where this test expects it")
	}
	if migrate > gateway {
		t.Fatalf("the entrypoint starts the gateway at byte %d and runs the migration at byte %d. "+
			"The migration is what renames a legacy mint credential to the cluster's key, and the "+
			"gateway is what serves a mint request: run in this order, every mint in between is "+
			"refused for naming a file that is about to be renamed", gateway, migrate)
	}
}
