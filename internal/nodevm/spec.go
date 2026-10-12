package nodevm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	NodePasswordName = "node-password"

	DefaultVcpu      = 2
	DefaultMemMiB    = 2048
	DefaultInterface = "eth0"
	DefaultPasta     = "pasta"

	VMMFirecracker = "firecracker"
	VMMQEMU        = "qemu"

	InitVerb = "init"
)

func NormalizeVMM(name string) (string, error) {
	switch name {
	case "", VMMFirecracker:
		return VMMFirecracker, nil
	case VMMQEMU:
		return VMMQEMU, nil
	}
	return "", fmt.Errorf("nodevm: %q is not one of %s, %s. The name decides which VMM a node "+
		"is booted by, and it is written into the image a node boots from and read back before the "+
		"boot: a value nothing here understands would be carried as an image's provenance and "+
		"compared against a substrate that never produced it", name, VMMFirecracker, VMMQEMU)
}

var ErrIncomplete = errors.New(
	"nodevm: a node needs a cluster, the server it dials, its own name, the token file whose " +
		"CONTENT is written onto a per-node read-only disk, a data dir, the agent binary that becomes " +
		"the guest's PID 1 parent process, the VMM binary, and the three files the guest boots from; " +
		"the token VALUE is never one of them because it must not ride argv or a kernel command line, " +
		"and no port is one of them either because every port this node binds, its kubelet's " +
		"included, is bound inside the guest's own network namespace")

type Image struct {
	Kernel string

	Initramfs string

	Rootfs string
}

func (i Image) Validate() error {
	var problems []error
	for _, f := range []struct{ name, path string }{
		{"kernel", i.Kernel},
		{"initramfs", i.Initramfs},
		{"rootfs", i.Rootfs},
	} {
		if f.path == "" {
			problems = append(problems, fmt.Errorf("%w: the image names no %s", ErrIncomplete, f.name))
			continue
		}
		st, err := os.Stat(f.path)
		if err != nil {
			problems = append(problems, fmt.Errorf("the image's %s at %s: %w", f.name, f.path, err))
			continue
		}
		if st.IsDir() {
			problems = append(problems, fmt.Errorf("the image's %s at %s is a directory, so it "+
				"cannot be handed to the VMM as a file", f.name, f.path))
		}
	}
	if err := errors.Join(problems...); err != nil {
		return fmt.Errorf("nodevm: %w -- every one of the three is booted from, so a guest "+
			"missing any of them starts a VMM that exits before it can boot, which reads as a node "+
			"that never joined rather than as an image that was never built", err)
	}
	return nil
}

type Guest struct {
	Vcpu int

	MemMiB int

	SMP bool

	BootArgs []string
}

func (g Guest) WithDefaults() Guest {
	if g.Vcpu == 0 {
		g.Vcpu = DefaultVcpu
	}
	if g.MemMiB == 0 {
		g.MemMiB = DefaultMemMiB
	}
	return g
}

func (g Guest) CommandLine() string {
	return strings.Join(g.BootArgs, " ")
}

func NodePasswordPath(dataDir string) string { return filepath.Join(dataDir, NodePasswordName) }
