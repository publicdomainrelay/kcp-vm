package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kernelmodules"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

func hostKernelRelease(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("uname", "-r").Output()
	if err != nil {
		livegate.RequireLocal(t, "uname -r: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func moduleTreeAt(t *testing.T, deps, builtins, aliases string) Config {
	t.Helper()
	source := t.TempDir()
	for name, body := range map[string]string{
		"modules.dep":     deps,
		"modules.builtin": builtins,
		"modules.alias":   aliases,
	} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	cfg := validConfig(t)
	cfg.ModuleSource = source
	return cfg
}

const kcpKernelDeps = `kernel/net/netfilter/nf_conntrack.ko.xz:
kernel/net/netfilter/xt_NFLOG.ko.xz: kernel/net/netfilter/nfnetlink_log.ko.xz
kernel/net/netfilter/xt_physdev.ko.xz:
kernel/net/netfilter/xt_nat.ko.xz:
kernel/net/netfilter/xt_comment.ko.xz:
kernel/net/ipv4/netfilter/ipt_REJECT.ko.xz:
kernel/net/ipv6/netfilter/ip6t_REJECT.ko.xz:
`

const kcpKernelAliases = `alias ipt_NFLOG xt_NFLOG
alias ipt_physdev xt_physdev
alias ipt_DNAT xt_nat
alias ipt_SNAT xt_nat
`

func TestAnExtensionTheClusterUsesWithNothingNamedToLoadItIsRefused(t *testing.T) {
	cfg := moduleTreeAt(t, kcpKernelDeps, "", kcpKernelAliases)
	cfg.Modules = []string{"nf_conntrack", "ipt_REJECT", "ip6t_REJECT", "xt_nat"}
	err := VerifyModules(cfg)
	if err == nil {
		t.Fatal("a config whose guest would reach for NFLOG with no module named for it was accepted")
	}
	for _, want := range []string{"NFLOG", "xt_NFLOG", "physdev", "xt_physdev"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %s: %v", want, err)
		}
	}
}

func TestANameTheKernelDoesNotCarryIsRefused(t *testing.T) {
	cfg := moduleTreeAt(t, kcpKernelDeps, "", kcpKernelAliases)
	cfg.Modules = []string{"nf_conntrack", "xt_REJECT", "ipt_REJECT", "ip6t_REJECT", "xt_nat",
		"xt_NFLOG", "xt_physdev"}
	err := VerifyModules(cfg)
	if err == nil {
		t.Fatal("a config naming the module xt_REJECT, which no kernel carries, was accepted")
	}
	if !strings.Contains(err.Error(), "xt_REJECT") || !strings.Contains(err.Error(), "ipt_REJECT") {
		t.Fatalf("the refusal does not explain that REJECT is ipt_REJECT rather than xt_REJECT: %v", err)
	}
}

func TestEveryProviderNamedIsAccepted(t *testing.T) {
	cfg := moduleTreeAt(t, kcpKernelDeps, "kernel/net/netfilter/xt_tcpudp.ko\n", kcpKernelAliases)
	cfg.Modules = []string{"nf_conntrack", "xt_NFLOG", "xt_physdev", "xt_nat", "xt_comment",
		"ipt_REJECT", "ip6t_REJECT", "xt_tcpudp"}
	if err := VerifyModules(cfg); err != nil {
		t.Fatalf("a config that names a module for every extension the cluster uses was refused: %v", err)
	}
}

func TestTheMeasuredExtensionsResolveAgainstThisHostsOwnModuleTree(t *testing.T) {
	tree, err := kernelmodules.LoadTree("/lib/modules/" + hostKernelRelease(t))
	if err != nil {
		livegate.RequireLocal(t, "this host's module tree is not readable here: %v", err)
	}
	if tree.Files() == 0 {
		livegate.RequireLocal(t, "this host's module index is empty")
	}
	for extension, want := range map[string]string{
		"REJECT":  "ipt_REJECT",
		"DNAT":    "xt_nat",
		"NFLOG":   "xt_NFLOG",
		"physdev": "xt_physdev",
	} {
		providers := tree.Providers(extension)
		named := false
		for _, provider := range providers {
			if provider == want {
				named = true
			}
		}
		if !named {
			t.Fatalf("this kernel's tree offers %v for the extension %s, and the extension the "+
				"guest measured to need a named module is %s", providers, extension, want)
		}
	}
}
