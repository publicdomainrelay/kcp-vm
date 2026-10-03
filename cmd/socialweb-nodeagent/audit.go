package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

func KernelAudit(out io.Writer, doc nodefirecracker.GuestDocument) error {
	release := kernelRelease()
	builtin, err := builtinModules(release)
	if err != nil {
		fmt.Fprintf(out, "KCP_AUDIT fatal: %v\n", err)
		return err
	}
	fmt.Fprintf(out, "KCP_AUDIT_START kernel=%s modules=%d\n", release, len(doc.Modules))
	loaded, inKernel := 0, 0
	var absent []string
	for _, name := range doc.Modules {
		if _, err := os.Stat("/sys/module/" + name); err == nil {
			loaded++
			continue
		}
		if builtin[strings.ReplaceAll(name, "-", "_")] {
			inKernel++
			continue
		}
		absent = append(absent, name)
	}
	for _, name := range absent {
		fmt.Fprintf(out, "KCP_AUDIT_MODULE_ABSENT %s\n", name)
	}
	fmt.Fprintf(out, "KCP_AUDIT_MODULES loaded=%d in_kernel=%d absent=%d\n", loaded, inKernel, len(absent))
	fmt.Fprintf(out, "KCP_CHECKCONFIG_BEGIN\n")
	rc, err := checkConfig(out, doc.K3s.Binary)
	if err != nil {
		fmt.Fprintf(out, "KCP_AUDIT fatal: %v\n", err)
		return err
	}
	fmt.Fprintf(out, "KCP_CHECKCONFIG_RC=%d\n", rc)
	fmt.Fprintf(out, "KCP_CHECKCONFIG_END\n")
	fmt.Fprintf(out, "KCP_AUDIT_STATUS %s\n", verdict(len(absent) == 0 && rc == 0))
	fmt.Fprintf(out, "KCP_AUDIT_END\n")
	return nil
}

func builtinModules(release string) (map[string]bool, error) {
	path := filepath.Join("/lib/modules", release, "modules.builtin")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("nodeagent: reading %s, the list of modules compiled into this guest's own "+
			"kernel: %w -- a module built into the kernel has no file to load and appears nowhere under "+
			"/sys/module, so an audit that looked only at what is loaded would call every built-in module "+
			"this image names one the guest does not have, and a check that fails on a correct image is "+
			"read as noise and then ignored", path, err)
	}
	builtin := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		base := filepath.Base(strings.TrimSpace(line))
		base = strings.TrimSuffix(base, ".ko")
		if base == "" || base == "." || base == "/" {
			continue
		}
		builtin[strings.ReplaceAll(base, "-", "_")] = true
	}
	return builtin, nil
}

func checkConfig(out io.Writer, binary string) (int, error) {
	cmd := exec.Command(binary, "check-config")
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, fmt.Errorf("nodeagent: running %s check-config in this guest: %w -- the audit is here so "+
		"that a kernel and a module set are checked against the image that was actually built, and it "+
		"is asked for by the auditKernel flag on this node's own disk rather than by a unit, because "+
		"the answer changes only when the image does and every node paid seconds of its boot for it. "+
		"An audit that cannot be started at all is the one audit outcome that stops the guest: a check "+
		"nothing ran and a check that passed are the same silence on the console, and only one of them "+
		"is the verification this exists to be", binary, err)
}

func verdict(passed bool) string {
	if passed {
		return "pass"
	}
	return "fail"
}

func kernelRelease() string {
	out, err := exec.Command("uname", "-r").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}
