package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostcontract"
)

func TestEveryInstalledUnitFileVerifies(t *testing.T) {
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze not on PATH: the rendered unit files are not verified on this host")
	}
	for _, role := range []hostcontract.Role{hostcontract.RoleGateway, hostcontract.RoleBidder} {
		state := t.TempDir()
		bin := filepath.Join(state, "bin", "current")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, CoreName), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		units, err := RenderUnits(role, state)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		var files []string
		for name, b := range units {
			if strings.HasSuffix(name, "@.service") {
				name = strings.TrimSuffix(name, "@.service") + "@alice.example.com.service"
			}
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
			files = append(files, p)
		}
		args := append([]string{"--user", "verify"}, files...)
		if out, err := exec.Command(analyze, args...).CombinedOutput(); err != nil || len(strings.TrimSpace(string(out))) > 0 {
			t.Errorf("%s: systemd-analyze --user verify: %v\n%s", role, err, out)
		}
	}
	drop, err := DropIn()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "socialweb-dropin-check.service")
	content := "[Unit]\nDescription=the delegation drop-in's directives\n\n" + string(drop) + "ExecStart=/bin/true\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(analyze, "verify", p).CombinedOutput(); err != nil || len(strings.TrimSpace(string(out))) > 0 {
		t.Errorf("the delegation drop-in: systemd-analyze verify: %v\n%s", err, out)
	}
}
