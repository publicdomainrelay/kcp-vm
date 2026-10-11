package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ubuntuShapedStage(t *testing.T) string {
	t.Helper()
	stage := t.TempDir()
	for _, dir := range []string{"usr/bin", "usr/sbin", "usr/lib", "etc"} {
		if err := os.MkdirAll(filepath.Join(stage, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		"sbin":               "usr/sbin",
		"bin":                "usr/bin",
		"lib":                "usr/lib",
		"usr/sbin/modprobe":  "../bin/kmod",
		"usr/sbin/iptables":  "/etc/alternatives/iptables",
		"etc/alternatives":   "/usr/lib",
		"usr/sbin/escape-me": "/../../../../../../usr/bin/kmod",
	} {
		if err := os.Symlink(target, filepath.Join(stage, link)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stage, "usr/bin/kmod"), []byte("kmod"), 0o755); err != nil {
		t.Fatal(err)
	}
	return stage
}

func kernelConfigNaming(t *testing.T, line string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	body := "CONFIG_MODULES=y\nCONFIG_MODULE_COMPRESS_XZ=y\n" + line + "CONFIG_NF_TABLES=m\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTheModprobeTheKernelExecsIsReadFromItsConfig(t *testing.T) {
	for _, c := range []struct{ config, want string }{
		{`CONFIG_MODPROBE_PATH="/usr/bin/modprobe"` + "\n", "/usr/bin/modprobe"},
		{`CONFIG_MODPROBE_PATH="/sbin/modprobe"` + "\n", "/sbin/modprobe"},
		{"", "/sbin/modprobe"},
	} {
		got, err := kernelModprobePath([]byte("CONFIG_MODULES=y\n" + c.config))
		if err != nil {
			t.Fatalf("%q: %v", c.config, err)
		}
		if got != c.want {
			t.Fatalf("a kernel config with %q names %q as the modprobe it execs; want %q", c.config, got, c.want)
		}
	}
}

func TestAModprobePathThatIsNotAbsoluteOrIsEmptyIsRefused(t *testing.T) {
	for _, line := range []string{`CONFIG_MODPROBE_PATH=""`, `CONFIG_MODPROBE_PATH="modprobe"`} {
		if got, err := kernelModprobePath([]byte(line + "\n")); err == nil {
			t.Fatalf("%s was read as %q; a kernel that execs a relative or empty path loads nothing on demand", line, got)
		}
	}
}

func TestAKernelThatExecsAModprobeTheRootfsDoesNotHoldGetsALinkToTheRootfsOwn(t *testing.T) {
	stage := ubuntuShapedStage(t)
	cfg := Config{StageDir: stage, KernelConfig: kernelConfigNaming(t, `CONFIG_MODPROBE_PATH="/usr/bin/modprobe"`+"\n")}
	if _, ok := resolveInStage(stage, "/usr/bin/modprobe"); ok {
		t.Fatal("the fixture already holds /usr/bin/modprobe")
	}
	if err := linkKernelModprobe(cfg); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(stage, "usr/bin/modprobe"))
	if err != nil {
		t.Fatalf("the kernel execs /usr/bin/modprobe for every module it loads on demand and the stage holds "+
			"no link there: %v", err)
	}
	if target != "/usr/sbin/modprobe" {
		t.Fatalf("/usr/bin/modprobe points at %q; want the rootfs's own /usr/sbin/modprobe", target)
	}
	resolved, ok := resolveInStage(stage, "/usr/bin/modprobe")
	if !ok || resolved != filepath.Join(stage, "usr/bin/kmod") {
		t.Fatalf("/usr/bin/modprobe resolves to %q (%v) inside the stage; want the stage's kmod", resolved, ok)
	}
}

func TestAKernelWhoseModprobeTheRootfsAlreadyHoldsIsLeftAlone(t *testing.T) {
	stage := ubuntuShapedStage(t)
	cfg := Config{StageDir: stage, KernelConfig: kernelConfigNaming(t, `CONFIG_MODPROBE_PATH="/sbin/modprobe"`+"\n")}
	if err := linkKernelModprobe(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(stage, "usr/bin/modprobe")); err == nil {
		t.Fatal("a link was written although /sbin/modprobe already resolves inside the stage")
	}
	target, err := os.Readlink(filepath.Join(stage, "usr/sbin/modprobe"))
	if err != nil || target != "../bin/kmod" {
		t.Fatalf("the rootfs's own modprobe was rewritten: %q %v", target, err)
	}
}

func TestARootfsWithNoModprobeAtAllIsRefused(t *testing.T) {
	stage := ubuntuShapedStage(t)
	if err := os.Remove(filepath.Join(stage, "usr/sbin/modprobe")); err != nil {
		t.Fatal(err)
	}
	cfg := Config{StageDir: stage, KernelConfig: kernelConfigNaming(t, `CONFIG_MODPROBE_PATH="/usr/bin/modprobe"`+"\n")}
	err := linkKernelModprobe(cfg)
	if err == nil {
		t.Fatal("a rootfs with no modprobe anywhere was accepted for a kernel that loads modules on demand")
	}
	if !strings.Contains(err.Error(), "/usr/bin/modprobe") {
		t.Fatalf("the refusal does not name the path the kernel execs: %v", err)
	}
}

func TestResolvingInsideTheStageNeverLeavesIt(t *testing.T) {
	stage := ubuntuShapedStage(t)
	for _, c := range []struct {
		path string
		want string
		ok   bool
	}{
		{"/sbin/modprobe", "usr/bin/kmod", true},
		{"/usr/sbin/escape-me", "usr/bin/kmod", true},
		{"/usr/bin/modprobe", "", false},
		{"/usr/sbin/iptables", "", false},
	} {
		got, ok := resolveInStage(stage, c.path)
		if ok != c.ok || (ok && got != filepath.Join(stage, c.want)) {
			t.Fatalf("%s resolved to %q (%v); want %q (%v)", c.path, got, ok, c.want, c.ok)
		}
	}
	loop := filepath.Join(stage, "usr/bin/loop")
	if err := os.Symlink("/usr/bin/loop", loop); err != nil {
		t.Fatal(err)
	}
	if got, ok := resolveInStage(stage, "/usr/bin/loop"); ok {
		t.Fatalf("a link to itself resolved to %q", got)
	}
}
