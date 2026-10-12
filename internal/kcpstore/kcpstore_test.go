package kcpstore

import (
	"slices"
	"strings"
	"testing"
)

func TestTheBidderKcpStoreIsOneKineOnSqliteAndNoPostgres(t *testing.T) {
	spec, err := KineSqliteSpec(KineConfig{Unit: "socialweb-kcp-abcd1234-kine-a.service", Slice: "socialweb-kcp-abcd1234.slice",
		Exec: "/opt/kine", Dir: "/s/kcp/kine-a"})
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(spec.Exec, " ")
	if !strings.Contains(argv, "--endpoint sqlite:///s/kcp/kine-a/kine.db") || strings.Contains(argv, "postgres") {
		t.Fatalf("argv %s", argv)
	}
	if !slices.Contains(spec.Exec, "127.0.0.1:0") || !slices.Contains(spec.Exec, "--metrics-bind-address=0") {
		t.Fatalf("kine must bind port 0 and serve no metrics port: %s", argv)
	}
}

func TestKineRefusesARelativeExecutable(t *testing.T) {
	if _, err := KineSqliteArgv(KineConfig{Exec: "kine", Dir: "/s"}); err == nil {
		t.Fatal("a PATH lookup name was accepted")
	}
}
