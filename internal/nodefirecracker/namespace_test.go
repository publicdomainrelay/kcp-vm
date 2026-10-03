//go:build linux

package nodefirecracker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ipPrintsOnlyLoopback = "case \"$1\" in\n" +
	"-o)\n" +
	"  printf '1: lo    inet 127.0.0.1/8 scope host lo\\n'\n" +
	"  exit 0\n" +
	"  ;;\n" +
	"esac\n" +
	"exit 1\n"

const ipFailsSilently = "exit 1\n"

func fakeIP(t *testing.T, behaviour string) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + calls + "\n" + behaviour
	if err := os.WriteFile(filepath.Join(dir, "ip"), []byte(script), 0o755); err != nil {
		t.Fatalf("staging a stand-in for ip: %v", err)
	}
	t.Setenv("PATH", dir)
	return calls
}

func TestAVMMIsNotConfiguredWhenTheNamespaceHasNoPastaLink(t *testing.T) {
	calls := fakeIP(t, ipPrintsOnlyLoopback)
	err := wireNamespace(context.Background(), namespaceParams{Tap: TapName, Bridge: BridgeName})
	if err == nil {
		t.Fatalf("a namespace whose only address is loopback was accepted, and pasta's link is " +
			"the only evidence that this child has the namespace it was promised: configuring " +
			"the VMM without it boots a guest whose tap reaches nothing and whose agent reports " +
			"a join that never happens, which is what a guest that never booted also looks like")
	}
	if !strings.Contains(err.Error(), "no interface carrying an IPv4") {
		t.Fatalf("a namespace with no pasta link failed with %q, and the failure has to say that "+
			"the link was never found rather than report whatever broke next", err)
	}
	logged, readErr := os.ReadFile(calls)
	if readErr != nil {
		t.Fatalf("reading what the namespace child ran: %v", readErr)
	}
	if got := strings.TrimSpace(string(logged)); got != "-o -4 addr show" {
		t.Fatalf("the child ran %q, and a namespace that has not been observed to carry pasta's "+
			"link must not be touched at all: this probe is the gate rather than a step that runs "+
			"ahead of the work, and it is the gate because it tests a property of the namespace "+
			"instead of the SELinux domain the gateway happens to be launched in", got)
	}
}

func TestAVMMIsNotConfiguredWhenTheAddressListingFailsSilently(t *testing.T) {
	calls := fakeIP(t, ipFailsSilently)
	err := wireNamespace(context.Background(), namespaceParams{Tap: TapName, Bridge: BridgeName})
	if err == nil {
		t.Fatalf("a namespace whose addresses could not be listed was accepted, and a failed " +
			"probe is not evidence of a link: the empty case is the one that cost the most " +
			"before, because a denial that also denies the report of it arrives as no message")
	}
	if !strings.Contains(err.Error(), "wrote nothing at all") {
		t.Fatalf("an address listing that failed with nothing to say reported %q, and it has to "+
			"name the emptiness: an error with no message is what this whole failure looked like", err)
	}
	logged, readErr := os.ReadFile(calls)
	if readErr != nil {
		t.Fatalf("reading what the namespace child ran: %v", readErr)
	}
	if got := strings.TrimSpace(string(logged)); got != "-o -4 addr show" {
		t.Fatalf("the child ran %q after failing to list this namespace's addresses, and a "+
			"listing that failed is not a namespace that was observed to be pasta's", got)
	}
}

func TestAFailedIPCommandThatWroteNothingNamesItself(t *testing.T) {
	fakeIP(t, ipFailsSilently)
	err := ip(context.Background(), "tuntap", "add", "dev", TapName, "mode", "tap")
	if err == nil {
		t.Fatalf("a command that exited non-zero was reported as success, so the guest would be " +
			"booted onto a tap that was never created")
	}
	if !strings.Contains(err.Error(), "wrote nothing at all") {
		t.Fatalf("a failure with no output reported %q, and it has to say that nothing was "+
			"written: a child of pasta that is denied an operation is often denied the write "+
			"that would describe it, so empty output is a failure to be named rather than an "+
			"error with nothing to say", err)
	}
	if !strings.Contains(err.Error(), "tuntap add dev "+TapName+" mode tap") {
		t.Fatalf("the failure reported %q and does not name the command that failed, so the "+
			"operator cannot tell which of the six steps in this namespace was refused", err)
	}
}
