package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/noderecord"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/processrunner"
)

const usage = `socialweb-nodeboot - boot one firecracker guest from a cloud-init user_data file

  socialweb-nodeboot -spec guest.json
  socialweb-nodeboot -stop -work-dir <dir>

Where socialweb-nodeimage builds the artifacts a guest boots from, this boots one guest from them
and hands it a cloud-init document instead of a cluster identity. The guest it starts is not a k3s
node: it joins nothing, it runs no agent of ours, and the only thing that makes it a guest is the
user-data the seed disk carries. That is the shape the market needs, where the requester's own
cloud-init is the whole contract.

The user-data reaches the guest on a second read-only disk, an ext4 labelled cidata holding
user-data and meta-data at its root. cloud-init's NoCloud datasource looks for exactly that label on
the block devices it can see, so nothing about how the guest is started has to change to carry it:
no kernel argument, and no metadata service, which firecracker does not have. The same mkfs.ext4 -d
that populates a directory into an ext4 without mounting it is what builds the seed.

Boot arguments and the machine size come from the image's own manifest, because the command line and
the image are one artifact and a caller that kept its own copy would drift from the one the kernel
was built for. -spec may override the size, which is a property of the machine rather than of the
image; it may not override the command line.

The spec is JSON:

  {
    "name": "guest-1",
    "image_dir": "/var/lib/socialweb/images/sha256-...",
    "work_dir": "/var/lib/socialweb/guests/guest-1",
    "firecracker": "/usr/local/bin/firecracker",
    "user_data_file": "/var/lib/socialweb/guests/user-data.yaml",
    "vcpu": 2,
    "mem_mib": 2048,
    "disk_mib": 4096,
    "network": {
      "guest_ip": "172.30.0.2", "prefix": 30, "gateway": "172.30.0.1",
      "pasta_ip": "172.30.0.0", "guest_mac": "06:00:ac:1e:00:02"
    }
  }

The result is one JSON object on standard output:

  {"name":..., "vmm_pid":..., "pasta_pid":..., "guest_ip":..., "socket":..., "console":...}

Every guest needs its own address range on this host: two guests sharing one range are two guests
whose packets arrive at whichever of them the host routes to. The MAC is derived from the address by
the same rule the guest's own network script reads it back with, so the address the guest configures
itself with is the address the host routed to it -- see internal/nodevm and the firecracker base
image's fcnet-setup.sh, which derives the guest's address from its MAC and nothing else.

-stdout is kept for the result alone; the guest's console goes to <work_dir>/guest.console and the
process hosting it logs to <work_dir>/namespace.log, which is where a guest that dies before it can
report anything is explained.

The spec may carry "pasta_args", which are inserted into pasta's own argv ahead of the arguments this
builds. Pasta's argv is a host policy decision and the arguments that are right here are not right
everywhere: on a host where pasta running as root cannot write the process's uid map, "pasta_args":
["--runas", "0:0"] is the difference between a guest and no guest, and on a host where it can, the
same flag is wrong. Nothing here guesses it, and unset is the default.

This must NOT be run as root unless the host has been shown to need it. Pasta drops the process that
starts the VMM to nobody when it is started as root, and a process running as nobody cannot write the
record or open the devices a guest needs. Started as an ordinary user, pasta does its own user
mapping and the process inside holds the same files and devices the caller did, which is the shape
this is built for and the shape the kcp container runs its runtime in.
`

type networkSpec struct {
	GuestIP string `json:"guest_ip"`

	Prefix int `json:"prefix"`

	Gateway string `json:"gateway"`

	PastaIP string `json:"pasta_ip"`

	Interface string `json:"interface"`

	Tap string `json:"tap"`

	Bridge string `json:"bridge"`

	GuestMAC string `json:"guest_mac"`
}

type guestSpec struct {
	Name string `json:"name"`

	ImageDir string `json:"image_dir"`

	WorkDir string `json:"work_dir"`

	Firecracker string `json:"firecracker"`

	Pasta string `json:"pasta"`

	PastaArgs []string `json:"pasta_args"`

	Shell string `json:"shell"`

	UserDataFile string `json:"user_data_file"`

	MetaData string `json:"meta_data"`

	Vcpu int `json:"vcpu"`

	MemMiB int `json:"mem_mib"`

	InstanceTimeoutSec int `json:"instance_timeout_sec"`

	Network networkSpec `json:"network"`
}

type result struct {
	Name string `json:"name"`

	Fingerprint string `json:"fingerprint"`

	VMMPID int `json:"vmm_pid"`

	PastaPID int `json:"pasta_pid"`

	GuestIP string `json:"guest_ip"`

	Socket string `json:"socket"`

	Console string `json:"console"`

	WorkDir string `json:"work_dir"`
}

const seedLabel = "cidata"

const seedFillDir = "seed"

const seedDiskName = "seed.ext4"

func main() {
	specPath := flag.String("spec", "", "path to the guest spec")
	stop := flag.Bool("stop", false, "stop the guest whose work directory is named by -work-dir")
	workDir := flag.String("work-dir", "", "the work directory of the guest to stop")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()
	if len(os.Args) > 1 && os.Args[1] == nodevm.NamespaceVerb {
		if err := namespaceChild(os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "socialweb-nodeboot: %v\n", err)
			os.Exit(1)
		}
		return
	}
	var err error
	switch {
	case *stop:
		err = Stop(*workDir)
	case *specPath == "":
		err = fmt.Errorf("-spec is required, and so is -stop with -work-dir: a boot needs the guest " +
			"to start and a stop needs the one to stop, and neither is a default")
	default:
		err = Boot(*specPath)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "socialweb-nodeboot: %v\n", err)
		os.Exit(1)
	}
}

func namespaceChild(paramsPath string) error {
	if paramsPath == "" {
		return fmt.Errorf("the namespace verb was given no parameters path, and it is the only " +
			"reading of what to boot: this process is exec'd by pasta with the path the parent " +
			"wrote, and one started by hand has nothing to boot")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return nodevm.RunNamespace(ctx, paramsPath, map[string]nodevm.Runner{
		nodevm.KindFirecracker: nodefirecracker.RunVMM,
	})
}

func LoadSpec(path string) (guestSpec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return guestSpec{}, fmt.Errorf("reading the guest spec at %s: %w", path, err)
	}
	var s guestSpec
	if err := json.Unmarshal(raw, &s); err != nil {
		return guestSpec{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := s.validate(); err != nil {
		return guestSpec{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func (s guestSpec) validate() error {
	required := []struct{ name, value string }{
		{"name", s.Name},
		{"image_dir", s.ImageDir},
		{"work_dir", s.WorkDir},
		{"firecracker", s.Firecracker},
		{"user_data_file", s.UserDataFile},
	}
	for _, req := range required {
		if req.value == "" {
			return fmt.Errorf("%s is not set. Nothing here has a default that would be right on "+
				"another host: the image, the VMM and the work directory are all host paths, and a "+
				"guess at one of them boots a guest from something nobody chose", req.name)
		}
	}
	network := nodevm.Network{
		Gateway:   s.Network.Gateway,
		GuestIP:   s.Network.GuestIP,
		Prefix:    s.Network.Prefix,
		PastaIP:   s.Network.PastaIP,
		Tap:       s.Network.Tap,
		Bridge:    s.Network.Bridge,
		GuestMAC:  s.Network.GuestMAC,
		Interface: s.Network.Interface,
	}
	if err := network.Validate(); err != nil {
		return fmt.Errorf("the network this guest is placed on: %w", err)
	}
	return nil
}

func (s guestSpec) network() nodevm.Network {
	network := nodevm.Network{
		Gateway:   s.Network.Gateway,
		GuestIP:   s.Network.GuestIP,
		Prefix:    s.Network.Prefix,
		PastaIP:   s.Network.PastaIP,
		Tap:       s.Network.Tap,
		Bridge:    s.Network.Bridge,
		GuestMAC:  s.Network.GuestMAC,
		Interface: s.Network.Interface,
	}
	return network.WithDefaults()
}

func Boot(specPath string) error {
	s, err := LoadSpec(specPath)
	if err != nil {
		return err
	}
	manifest, err := nodefirecracker.LoadManifest(s.ImageDir)
	if err != nil {
		return err
	}
	if err := manifest.CheckVMM(nodevm.VMMFirecracker); err != nil {
		return err
	}
	guest := manifest.Guest()
	if s.Vcpu > 0 {
		guest.Vcpu = s.Vcpu
	}
	if s.MemMiB > 0 {
		guest.MemMiB = s.MemMiB
	}
	if err := os.MkdirAll(s.WorkDir, 0o700); err != nil {
		return fmt.Errorf("creating the work directory %s: %w", s.WorkDir, err)
	}
	if _, err := os.Stat(filepath.Join(s.WorkDir, nodevm.InstanceName)); err == nil {
		return fmt.Errorf("the work directory %s already holds a guest. A guest is named by the "+
			"directory it works in, and two guests sharing one are two VMMs behind one console and one "+
			"record: stop it first (socialweb-nodeboot -stop -work-dir %s) or give this one its own",
			s.WorkDir, s.WorkDir)
	}
	if err := writeSeed(s); err != nil {
		return err
	}
	if err := nodefirecracker.CloneFile(
		filepath.Join(s.ImageDir, nodefirecracker.RootfsImageName),
		filepath.Join(s.WorkDir, nodevm.RootfsName),
	); err != nil {
		return err
	}
	paramsPath, err := writeNamespaceParams(s, guest)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding this process's own path: %w -- this binary is also the process "+
			"pasta runs inside the guest's namespace, which is what wires the tap and starts the VMM, "+
			"so a boot that cannot name itself cannot start anything", err)
	}
	network := s.network()
	pasta := s.Pasta
	if pasta == "" {
		pasta = nodevm.DefaultPasta
	}
	shell := s.Shell
	if shell == "" {
		shell = nodevm.DefaultShell
	}
	log, err := os.OpenFile(filepath.Join(s.WorkDir, nodevm.NamespaceLogName),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", filepath.Join(s.WorkDir, nodevm.NamespaceLogName), err)
	}
	defer log.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	argv := nodevm.PastaArgv(pasta, shell, self, network, paramsPath)
	if len(s.PastaArgs) > 0 {
		argv = append(append([]string{argv[0]}, s.PastaArgs...), argv[1:]...)
	}
	proc, err := processrunner.Start(ctx, processrunner.Spec{
		Argv:   argv,
		Output: log,
	})
	if err != nil {
		return fmt.Errorf("starting pasta for guest %s: %w", s.Name, err)
	}
	timeout := time.Duration(s.InstanceTimeoutSec) * time.Second
	if timeout == 0 {
		timeout = nodevm.DefaultInstanceTimeout
	}
	exited := make(chan struct{})
	go func() {
		_ = proc.Wait()
		close(exited)
	}()
	pid, err := awaitInstance(s, proc, exited, timeout)
	if err != nil {
		_ = proc.Stop(os.Interrupt)
		return err
	}
	out := result{
		Name:        s.Name,
		Fingerprint: filepath.Base(filepath.Clean(s.ImageDir)),
		VMMPID:      pid,
		PastaPID:    proc.PID,
		GuestIP:     network.GuestIP,
		Socket:      nodefirecracker.SocketPath(s.WorkDir),
		Console:     nodefirecracker.ConsolePath(s.WorkDir),
		WorkDir:     s.WorkDir,
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", raw)
	return nil
}

func awaitInstance(
	s guestSpec,
	proc *processrunner.Process,
	exited <-chan struct{},
	timeout time.Duration,
) (int, error) {
	deadline := time.Now().Add(timeout)
	for {
		if pid, err := readInstance(nodefirecracker.InstancePath(s.WorkDir)); err == nil {
			return pid, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		select {
		case <-exited:
			return 0, fmt.Errorf("the process hosting guest %s exited before it created a VMM, so "+
				"no guest exists. Its output follows, and it is the only account of why:\n%s",
				s.Name, proc.Output())
		default:
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("guest %s had not started %s after pasta was launched, so this "+
				"stops here rather than reporting a guest that is not running. Its output follows:\n%s",
				s.Name, timeout, proc.Output())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readInstance(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var rec nodevm.InstanceRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return 0, fmt.Errorf("the VMM pid at %s: %w", path, err)
	}
	if rec.PID <= 0 {
		return 0, fmt.Errorf("the VMM pid at %s is %d, and a pid of zero names no process", path, rec.PID)
	}
	return rec.PID, nil
}

func writeSeed(s guestSpec) error {
	raw, err := os.ReadFile(s.UserDataFile)
	if err != nil {
		return fmt.Errorf("reading the guest's user-data at %s: %w -- this file is the whole "+
			"contract the requester asked for, so a guest booted without it is one nothing has "+
			"asked anything of and nothing would be installed in", s.UserDataFile, err)
	}
	if len(raw) == 0 {
		return fmt.Errorf("the guest's user-data at %s is empty, and a seed that carries nothing is "+
			"one cloud-init reads and does nothing with", s.UserDataFile)
	}
	meta := s.MetaData
	if meta == "" {
		meta = "instance-id: " + s.Name + "\nlocal-hostname: " + s.Name + "\n"
	}
	fill := filepath.Join(s.WorkDir, seedFillDir)
	if err := os.RemoveAll(fill); err != nil {
		return fmt.Errorf("clearing %s: %w", fill, err)
	}
	if err := os.MkdirAll(fill, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", fill, err)
	}
	defer os.RemoveAll(fill)
	for _, f := range []struct {
		name string
		body string
	}{
		{name: "user-data", body: string(raw)},
		{name: "meta-data", body: meta},
	} {
		if err := os.WriteFile(filepath.Join(fill, f.name), []byte(f.body), 0o600); err != nil {
			return fmt.Errorf("writing %s into the seed: %w", f.name, err)
		}
	}
	disk := filepath.Join(s.WorkDir, seedDiskName)
	if err := os.Remove(disk); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing a previous seed at %s: %w", disk, err)
	}
	f, err := os.OpenFile(disk, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("creating the seed disk at %s: %w", disk, err)
	}
	size := int64(len(raw)) + int64(len(meta)) + 4<<20
	if err := f.Truncate(size); err != nil {
		f.Close()
		return fmt.Errorf("sizing the seed disk at %s to %d bytes: %w", disk, size, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := nodevm.MkfsExt4(disk, fill, seedLabel); err != nil {
		return fmt.Errorf("laying the cloud-init seed down as %s: %w -- the label is what cloud-init "+
			"looks for, so a seed with the wrong one is a disk the guest mounts and ignores", disk, err)
	}
	return nil
}

func writeNamespaceParams(s guestSpec, guest nodevm.Guest) (string, error) {
	network := s.network()
	params := nodevm.NamespaceParams{
		Kind: nodevm.KindFirecracker,

		VMM: s.Firecracker,

		Name: s.Name,

		Kernel: filepath.Join(s.ImageDir, nodefirecracker.VMLinuxName),

		Initramfs: filepath.Join(s.ImageDir, nodefirecracker.InitramfsName),

		Rootfs: filepath.Join(s.WorkDir, nodevm.RootfsName),

		NodeDisk: filepath.Join(s.WorkDir, seedDiskName),

		BootArgs: guest.CommandLine(),

		Socket: nodefirecracker.SocketPath(s.WorkDir),

		Console: nodefirecracker.ConsolePath(s.WorkDir),

		Instance: nodefirecracker.InstancePath(s.WorkDir),

		Vcpu: guest.Vcpu,

		MemMiB: guest.MemMiB,

		SMP: guest.SMP,

		Tap: network.Tap,

		Bridge: network.Bridge,

		GuestMAC: network.GuestMAC,
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("encoding the namespace parameters for guest %s: %w", s.Name, err)
	}
	path := nodefirecracker.NamespacePath(s.WorkDir)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", fmt.Errorf("writing the namespace parameters to %s: %w", path, err)
	}
	return path, nil
}

func Stop(workDir string) error {
	if workDir == "" {
		return fmt.Errorf("-work-dir is required: a guest is named by the directory it works in, " +
			"and there is nothing to stop without one")
	}
	var problems []error
	if rec, err := noderecord.Read(workDir); err == nil && rec.PID > 0 {
		if err := kill(rec.PID); err != nil {
			problems = append(problems, fmt.Errorf("stopping the process hosting the guest at pid %d: %w",
				rec.PID, err))
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		problems = append(problems, fmt.Errorf("reading the record at %s: %w",
			noderecord.RecordPath(workDir), err))
	}
	if pid, err := readInstance(nodefirecracker.InstancePath(workDir)); err == nil {
		if err := kill(pid); err != nil {
			problems = append(problems, fmt.Errorf("stopping the VMM at pid %d: %w", pid, err))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		problems = append(problems, err)
	}
	return errors.Join(problems...)
}

func kill(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
