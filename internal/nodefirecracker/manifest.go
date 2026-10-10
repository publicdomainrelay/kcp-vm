package nodefirecracker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
)

const ManifestName = "image.json"

type Manifest struct {
	VMM string `json:"vmm"`

	BootArgs []string `json:"bootArgs"`

	Vcpu int `json:"vcpu"`

	MemMiB int `json:"memMiB"`

	SMP bool `json:"smp"`

	GuestInterface string `json:"guestInterface"`

	K3sBinary string `json:"k3sBinary"`

	K3sDataDir string `json:"k3sDataDir"`

	Modules []string `json:"modules"`

	Images []SeededImage `json:"images"`

	Inputs *BuildInputs `json:"buildInputs,omitempty"`

	Fingerprint string `json:"fingerprint,omitempty"`
}

func (m Manifest) RecordedFingerprint() (string, error) {
	if m.Fingerprint == "" {
		return "", fmt.Errorf("the manifest in this image records no fingerprint, so the name of " +
			"its directory cannot be checked against it. Every image built since the name was " +
			"recorded in the manifest carries it; one that does not was built before that, and " +
			"its record is also a different shape. It is treated as stale rather than as whole: " +
			"rebuilding it under the current name is the only way to know what it contains")
	}
	if !IsFingerprint(m.Fingerprint) {
		return "", fmt.Errorf("the manifest in this image records the fingerprint %q, which is not "+
			"a name this store issues", m.Fingerprint)
	}
	return m.Fingerprint, nil
}

type SeededImage struct {
	Reference string `json:"reference"`

	Repository string `json:"repository"`

	Digest string `json:"digest"`

	Platform string `json:"platform"`

	TarBytes int64 `json:"tarBytes"`
}

func (m Manifest) Guest() Guest {
	return Guest{Vcpu: m.Vcpu, MemMiB: m.MemMiB, SMP: m.SMP, BootArgs: m.BootArgs}.WithDefaults()
}

func LoadManifest(dir string) (Manifest, error) {
	path := filepath.Join(dir, ManifestName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("nodefirecracker: reading %s: %w -- the manifest is what says "+
			"how the kernel in this image is told to boot, and it is written by the same builder "+
			"that produced the kernel, because the command line and the image are one artifact: a "+
			"boot line kept anywhere else drifts from the image it is booting", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("nodefirecracker: %s: %w", path, err)
	}
	if len(m.BootArgs) == 0 {
		return Manifest{}, fmt.Errorf("nodefirecracker: %s carries no boot arguments, so the guest "+
			"would be started with firecracker's own default command line -- which passes nomodule, "+
			"leaving a distro kernel unable to load the transport its root device arrives on",
			path)
	}
	return m, nil
}

func (m Manifest) Validate() error {
	if len(m.BootArgs) == 0 {
		return fmt.Errorf("nodefirecracker: the image manifest carries no boot arguments")
	}
	for _, req := range []struct{ name, value string }{
		{"guestInterface", m.GuestInterface},
		{"k3sBinary", m.K3sBinary},
		{"k3sDataDir", m.K3sDataDir},
	} {
		if req.value == "" {
			return fmt.Errorf("nodefirecracker: the image manifest carries no %s", req.name)
		}
	}
	if len(m.Modules) == 0 {
		return fmt.Errorf("nodefirecracker: the image manifest names no kernel modules, so the guest " +
			"would boot a kernel whose netfilter support k3s's own check-config requires and nothing " +
			"would load it")
	}
	if m.Inputs == nil {
		return ErrNoBuildInputs
	}
	return nil
}

func (m Manifest) CheckVMM(want string) error {
	recorded, err := nodevm.NormalizeVMM(m.VMM)
	if err != nil {
		return fmt.Errorf("nodefirecracker: the image manifest records vmm %q: %w", m.VMM, err)
	}
	selected, err := nodevm.NormalizeVMM(want)
	if err != nil {
		return err
	}
	if recorded != selected {
		return fmt.Errorf("nodefirecracker: the image in this directory was built for the %s VMM "+
			"and this gateway places nodes with %s. A guest booted across the two is not caught by "+
			"anything downstream: both VMMs boot the same kernel and hand the guest the same image "+
			"files, so the guest would come up, join and reach Running, and this field is the only "+
			"place the mismatch is written down. The image is refused rather than booted: boot it "+
			"with the substrate it was built for, or rebuild it for %s with "+
			"`socialweb-nodeimage -config <the configuration naming %s>`",
			recorded, selected, selected, selected)
	}
	return nil
}
