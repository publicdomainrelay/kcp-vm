//go:build linux

package nodefirecracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/noderecord"
)

const standInPastaEnv = "NODEFIRECRACKER_TEST_STAND_IN_PASTA"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == NamespaceVerb {
		if len(os.Args) > 2 {
			if err := writeNamespaceRecord(os.Args[2]); err != nil {
				fmt.Fprintf(os.Stderr, "stand-in namespace child: %v\n", err)
				os.Exit(3)
			}
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	if os.Getenv(standInPastaEnv) != "" {
		runStandInPasta()
	}
	os.Exit(m.Run())
}

func runStandInPasta() {
	if len(os.Args) < 5 {
		os.Exit(2)
	}
	tail := os.Args[len(os.Args)-5:]
	cmd := exec.Command(tail[0], tail[1:]...)
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func validOptions(t *testing.T) Options {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{VMLinuxName, InitramfsName, RootfsImageName} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("staging %s: %v", name, err)
		}
	}
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("K10token"), 0o600); err != nil {
		t.Fatalf("staging the token: %v", err)
	}
	return Options{
		Cluster:     "liveweb",
		Server:      "https://10.100.0.1:24000",
		NodeName:    "liveweb-node1",
		TokenFile:   token,
		DataDir:     filepath.Join(dir, "nodes", "liveweb-node1"),
		Record:      func(context.Context, string, v1alpha1.NodePhase) error { return nil },
		Agent:       filepath.Join(dir, "kcpagent"),
		Firecracker: filepath.Join(dir, "firecracker"),
		Image: Image{
			Kernel:    filepath.Join(dir, VMLinuxName),
			Initramfs: filepath.Join(dir, InitramfsName),
			Rootfs:    filepath.Join(dir, RootfsImageName),
		},
		Network: Network{
			Gateway: "10.100.0.1",
			GuestIP: "10.100.0.2",
			Prefix:  24,
			PastaIP: "10.100.0.0",
		},
	}
}

func TestAnIncompleteNodeIsRefusedByName(t *testing.T) {
	for _, tc := range []struct {
		name string
		drop func(*Options)
		want string
	}{
		{"no cluster", func(o *Options) { o.Cluster = "" }, "no cluster"},
		{"no server", func(o *Options) { o.Server = "" }, "no server"},
		{"no node name", func(o *Options) { o.NodeName = "" }, "no node name"},
		{"no token file", func(o *Options) { o.TokenFile = "" }, "no token file"},
		{"no data dir", func(o *Options) { o.DataDir = "" }, "no data dir"},
		{"no recorder", func(o *Options) { o.Record = nil }, "no Node-CR recorder"},
		{"no agent", func(o *Options) { o.Agent = "" }, "no agent binary"},
		{"no VMM", func(o *Options) { o.Firecracker = "" }, "no firecracker binary"},
		{"no kernel", func(o *Options) { o.Image.Kernel = "" }, "kernel"},
		{"no gateway", func(o *Options) { o.Network.Gateway = "" }, "gateway"},
		{"no address", func(o *Options) { o.Network.GuestIP = "" }, "address"},
		{"no prefix", func(o *Options) { o.Network.Prefix = 0 }, "prefix"},
		{"no address for pasta's own end", func(o *Options) { o.Network.PastaIP = "" }, "pasta's own address"},
	} {
		opts := validOptions(t)
		tc.drop(&opts)
		_, err := New(opts)
		if err == nil {
			t.Errorf("%s was accepted, and a node missing it is one the substrate would start "+
				"anyway and then fail to account for", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: the refusal does not name %q, so it reads as a value that was not "+
				"understood rather than one that was understood and refused: %v",
				tc.name, tc.want, err)
		}
	}
}

func TestANodeAddressedOutsideItsClustersRangeIsRefused(t *testing.T) {
	leased := Network{Gateway: "10.100.0.1", GuestIP: "10.100.0.2", Prefix: 24, PastaIP: "10.100.0.0"}
	for _, tc := range []struct {
		name string
		net  Network
		want string
	}{
		{"the address this host carries, under a gateway from a leased range",
			Network{Gateway: "10.100.0.1", GuestIP: "192.168.0.20", Prefix: 24, PastaIP: "10.100.0.0"},
			"192.168.0.20"},
		{"a node sharing the gateway's address",
			Network{Gateway: "10.100.0.1", GuestIP: "10.100.0.1", Prefix: 24, PastaIP: "10.100.0.0"},
			"IS the gateway"},
		{"pasta carrying the gateway's own address, which is what was measured not to work",
			Network{Gateway: "10.100.0.1", GuestIP: "10.100.0.2", Prefix: 24, PastaIP: "10.100.0.1"},
			"IS the gateway"},
		{"an address from another cluster's range",
			Network{Gateway: "10.100.0.1", GuestIP: "10.100.1.2", Prefix: 24, PastaIP: "10.100.0.0"},
			"10.100.1.2"},
		{"pasta's own end left in another cluster's range",
			Network{Gateway: "10.100.0.1", GuestIP: "10.100.0.2", Prefix: 24, PastaIP: "10.100.1.0"},
			"10.100.1.0"},
		{"a prefix wider than the pool is ever cut into",
			Network{Gateway: "10.100.0.1", GuestIP: "10.100.0.2", Prefix: 31, PastaIP: "10.100.0.0"},
			"/31"},
	} {
		opts := validOptions(t)
		opts.Network = tc.net
		_, err := New(opts)
		if err == nil {
			t.Errorf("%s was accepted, and this is the measured failure the lease exists to "+
				"remove: a node addressed outside the range its cluster holds shares an address "+
				"with a node of another cluster or with this host, and every address involved is a "+
				"legal address, so nothing in either cluster reports it", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: the refusal does not name %q, so an operator cannot see which of the "+
				"two values is wrong: %v", tc.name, tc.want, err)
		}
	}
	if err := leased.Validate(); err != nil {
		t.Fatalf("a node in a leased range was refused: %v", err)
	}
}

func TestTheIdentityIsTheVMMsCommandLine(t *testing.T) {
	got := Identity("/state/liveweb/nodes/liveweb-node1")
	want := []string{"firecracker", "--api-sock", "/state/liveweb/nodes/liveweb-node1/fc.sock"}
	if len(got) != len(want) {
		t.Fatalf("Identity = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Identity = %v, want %v: the socket path is per node and per run, which is "+
				"what separates this node's VMM from every other one on the host", got, want)
		}
	}
}

func TestTheNodeDiskCarriesItsIdentityAndNothingElseReadsItBack(t *testing.T) {
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		livegate.RequireLocal(t, "mkfs.ext4 is not on this host: %v", err)
	}
	if _, err := exec.LookPath("debugfs"); err != nil {
		livegate.RequireLocal(t, "debugfs is not on this host, so the disk cannot be read back: %v", err)
	}
	dir := t.TempDir()
	disk := filepath.Join(dir, NodeDiskName)
	doc := NodeDocument{
		NodeName:     "liveweb-node1",
		Server:       "https://192.168.0.1:24000",
		Token:        "K10secret",
		NodePassword: "np-node1-secret",
		Address:      "192.168.0.20",
		Prefix:       24,
		Gateway:      "192.168.0.1",
		Interface:    "eth0",
	}
	if err := BuildNodeDisk(disk, doc, DefaultDiskMiB, nil); err != nil {
		t.Fatalf("BuildNodeDisk: %v", err)
	}
	st, err := os.Stat(disk)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("the disk is %v, want 0600: the join token is inside it, and a world-readable "+
			"token is a leaked cluster", st.Mode().Perm())
	}
	out, err := exec.Command("debugfs", "-R", "cat /"+NodeFileName, disk).Output()
	if err != nil {
		t.Fatalf("reading the disk back: %v", err)
	}
	var back NodeDocument
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("the disk's %s is not a node document: %v: %q", NodeFileName, err, string(out))
	}
	if back != doc {
		t.Fatalf("the disk carries %+v, want %+v", back, doc)
	}
}

func TestADiskWithoutAnIdentityIsRefused(t *testing.T) {
	dir := t.TempDir()
	err := BuildNodeDisk(filepath.Join(dir, NodeDiskName), NodeDocument{
		NodeName: "liveweb-node1", Server: "https://192.168.0.1:24000",
	}, DefaultDiskMiB, nil)
	if err == nil {
		t.Fatal("a disk with no token was built: the guest would boot, read it, and fail at the " +
			"join with nothing saying which of the three parts was missing")
	}
	err = BuildNodeDisk(filepath.Join(dir, NodeDiskName), NodeDocument{
		NodeName: "liveweb-node1", Server: "https://192.168.0.1:24000", Token: "K10secret",
	}, DefaultDiskMiB, nil)
	if err == nil {
		t.Fatal("a disk carrying the cluster token and no per-node password was built: the " +
			"guest would write an empty password over k3s's own, and k3s would mint a credential " +
			"the server cannot have issued")
	}
}

func TestANodesOwnPasswordIsMintedOnceAndReused(t *testing.T) {
	dir := t.TempDir()
	path := NodePasswordPath(dir)
	first, err := mintOrReadNodePassword(path)
	if err != nil {
		t.Fatalf("mintOrReadNodePassword: %v", err)
	}
	if first == "" {
		t.Fatal("a node was minted an empty password")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the node password was not written: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("the node password at %s is %04o, want 0600", path, info.Mode().Perm())
	}
	second, err := mintOrReadNodePassword(path)
	if err != nil {
		t.Fatalf("second mintOrReadNodePassword: %v", err)
	}
	if first != second {
		t.Fatal("a second staging minted a NEW node password under the same node name, so a " +
			"re-provisioned node presents a password the server stored immutably for that name " +
			"and is refused with 'Node password rejected, duplicate hostname'")
	}
}

func TestAManifestCarriesTheBootLineWithTheKernel(t *testing.T) {
	dir := t.TempDir()
	raw, err := json.Marshal(Manifest{
		BootArgs:       []string{"selinux=0", "console=ttyS0", "root=/dev/vda"},
		K3sBinary:      GuestK3sBinary,
		K3sDataDir:     GuestK3sDataDir,
		Modules:        []string{"nf_conntrack"},
		GuestInterface: DefaultInterface,
		Inputs: &BuildInputs{
			ConfigPath:         "/etc/kcp/nodeimage.json",
			InitramfsModules:   []string{"kernel/drivers/virtio/virtio_mmio"},
			KernelSource:       "/boot/vmlinuz",
			KernelSourceDigest: digestPrefix + "00",
			KernelConfig:       "/boot/config",
			KernelConfigDigest: digestPrefix + "11",
			BaseImage:          "/img/base.squashfs",
			BaseImageDigest:    digestPrefix + "22",
			K3sBinaryDigest:    digestPrefix + "33",
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	manifest, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	guest := manifest.Guest()
	if guest.Vcpu != DefaultVcpu || guest.MemMiB != DefaultMemMiB {
		t.Fatalf("the manifest's guest is %+v, and an unset machine size must resolve to the "+
			"library's default rather than to zero", guest)
	}
	if guest.CommandLine() != "selinux=0 console=ttyS0 root=/dev/vda" {
		t.Fatalf("the command line is %q", guest.CommandLine())
	}
}

func TestAFatalGuestIsReportedFromTheConsole(t *testing.T) {
	dir := t.TempDir()
	if got := Fatal(dir); got != "" {
		t.Fatalf("an absent console reported %q: a node with no console yet must not read as dead",
			got)
	}
	body := "[    1.277227] kcpagent[334]: KCP_AGENT fatal: fork/exec /usr/local/bin/k3s: no such " +
		"file or directory\n"
	if err := os.WriteFile(ConsolePath(dir), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := Fatal(dir)
	if !strings.Contains(got, "KCP_AGENT fatal") || !strings.Contains(got, "no such file") {
		t.Fatalf("Fatal = %q, and it must carry the guest's own words: a watchdog that reports only "+
			"that something died costs the same as the timeout it replaces", got)
	}
}

func TestPastasChildIsAShellThatExecsTheAgent(t *testing.T) {
	opts := validOptions(t).withDefaults()
	argv := pastaArgv(opts, "/state/namespace.json")
	if len(argv) == 0 || argv[0] != DefaultPasta {
		t.Fatalf("pastaArgv = %v, want it to start pasta", argv)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, NamespaceVerb) {
		t.Fatalf("pastaArgv = %v, and pasta's child must run the namespace verb: that child is "+
			"what bridges the guest's tap onto pasta's own link", argv)
	}
	shell := indexOf(argv, DefaultShell)
	if shell < 0 {
		t.Fatalf("pastaArgv = %v, and pasta's child must be a shell, because a child of pasta "+
			"inherits pasta's own SELinux domain, and in any domain other than unconfined_t that "+
			"child is denied the net_admin it needs to add the guest's tap and denied the write "+
			"that would have described the denial, so the failure arrives as an EMPTY error. What "+
			"makes %s work here is measured and is not that it carries a privileged label: on this "+
			"host it is system_u:object_r:bin_t:s0, the shell_exec_t one is /usr/bin/bash, and "+
			"pasta itself is pasta_exec_t. The shell is enough only because the gateway is itself "+
			"launched in unconfined_t, so exec'ing a bin_t file does not transition at all, which "+
			"is incidental to how the gateway happens to be started. The property this package "+
			"actually relies on is the positive probe in namespace.go, which refuses to configure "+
			"a VMM unless the namespace has been observed to carry pasta's own link", argv,
			DefaultShell)
	}
	if !strings.Contains(joined, `exec "$0"`) {
		t.Fatalf("pastaArgv = %v, and the shell must exec rather than fork, so closing the process "+
			"we started is still the teardown of the whole chain", argv)
	}
}

func TestAShellThisHostOwnsIsRefused(t *testing.T) {
	dir := t.TempDir()
	owned := filepath.Join(dir, "kcpagent")
	if err := os.WriteFile(owned, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("staging a binary this host owns: %v", err)
	}
	for _, tc := range []struct {
		name  string
		shell string
		want  string
	}{
		{"this node's own agent binary", owned, "agent binary"},
		{"a name pasta would resolve itself", "sh", "not an absolute path"},
		{"a path that cannot be examined", filepath.Join(dir, "absent"), "cannot be examined"},
		{"a directory", dir, "executable regular file"},
	} {
		opts := validOptions(t)
		opts.Agent = owned
		opts.Shell = tc.shell
		_, err := New(opts)
		if err == nil {
			t.Errorf("a shell that is %s was accepted, and pasta's child inherits pasta's own "+
				"SELinux domain: outside unconfined_t that child is denied CAP_NET_ADMIN and "+
				"denied the write that would have reported the denial, so the guest never boots "+
				"and the failure arrives empty", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("a shell that is %s was refused with %q, and the refusal has to name %q: a "+
				"refusal that does not say what is wrong with the shell sends the operator back "+
				"to the plumbing, which is what this failure cost the first time",
				tc.name, err, tc.want)
		}
	}
}

func TestAShellInsideThisNodesOwnDataDirIsRefused(t *testing.T) {
	opts := validOptions(t)
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		t.Fatalf("creating the node's data dir: %v", err)
	}
	inside := filepath.Join(opts.DataDir, "sh")
	if err := os.WriteFile(inside, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("staging a shell this host wrote: %v", err)
	}
	opts.Shell = inside
	if _, err := New(opts); err == nil ||
		!strings.Contains(err.Error(), "own data directory") {
		t.Fatalf("a shell at %s was accepted or refused for the wrong reason (%v): a tree this "+
			"host creates and writes is the tree the failing arrangement came from, and the check "+
			"has to be the ownership of the file rather than its name", inside, err)
	}
}

func TestPastaIsHandedThisClustersAddressingAndNotTheHosts(t *testing.T) {
	opts := validOptions(t).withDefaults()
	argv := pastaArgv(opts, "/state/namespace.json")
	shell := indexOf(argv, DefaultShell)
	if shell < 0 {
		t.Fatalf("pastaArgv = %v names no shell, so the child's argc cannot be read", argv)
	}
	for _, want := range [][]string{
		{"-a", opts.Network.PastaIP},
		{"-n", "255.255.255.0"},
		{"-g", opts.Network.Gateway},
		{"--dns-forward", opts.Network.Gateway},
	} {
		if i := indexOf(argv, want[0]); i < 0 || i+1 >= shell || argv[i+1] != want[1] {
			t.Fatalf("pastaArgv = %v, want %s %s before the child. The three values are the "+
				"namespace's own address, its netmask and the gateway its default route names, and "+
				"they are what stops pasta from copying this host's network into the namespace: "+
				"pasta reads its own defaults off the interface carrying this host's default route, "+
				"so a namespace configured that way carries this host's LAN address, its netmask "+
				"and its router -- which is how a node's InternalIP was the address of the machine "+
				"running the gateway, and how a guest's default route covered this host's real LAN",
				argv, want[0], want[1])
		}
	}
	if strings.Contains(strings.Join(argv[:shell], " "), "-n 0.0.0.0") {
		t.Fatalf("pastaArgv = %v hands pasta a netmask of 0.0.0.0, which reads as every address "+
			"being on-link", argv)
	}
}

func TestAProviderKilledBeforeTheVMMRecordStillLeavesAReapableRecord(t *testing.T) {
	opts := validOptions(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary to stand in for pasta and its namespace child: %v", err)
	}
	opts.Pasta = exe
	opts.Agent = exe
	opts.InstanceTimeout = 500 * time.Millisecond
	t.Setenv(standInPastaEnv, "1")

	h, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := h.Provision(context.Background()); err == nil {
		t.Fatal("a node whose namespace child never started a VMM was reported as started, so this " +
			"test never reached the state it is about")
	}

	rec, err := noderecord.Read(opts.DataDir)
	if err != nil {
		t.Fatalf("pasta started and its namespace child is running, but no record names that child: "+
			"a gateway killed in this window leaves the guest and its netns with nothing for a later "+
			"sweep to find, and the window is exactly the one between pasta starting and the VMM's "+
			"own record being written: %v", err)
	}
	namespace := []string{NamespaceVerb, NamespacePath(opts.DataDir)}
	live, err := noderecord.LivenessOf(opts.DataDir, Identity(opts.DataDir), namespace)
	if err != nil {
		t.Fatalf("LivenessOf: %v", err)
	}
	if live != noderecord.LivenessOurs {
		t.Fatalf("the record at pid %d verified as %v under the VMM's and the namespace child's "+
			"identities, and it has to verify as the child's: the child is recorded before the VMM "+
			"exists, and the child's exit is what tears the VMM and pasta down", rec.PID, live)
	}
	t.Cleanup(func() { _ = syscall.Kill(rec.PID, syscall.SIGKILL) })

	if err := noderecord.Reap(context.Background(), noderecord.ReapOptions{
		Cluster:        opts.Cluster,
		NodeName:       opts.NodeName,
		DataDir:        opts.DataDir,
		Identity:       Identity(opts.DataDir),
		HolderIdentity: namespace,
		Grace:          time.Second,
	}); err != nil {
		t.Fatalf("reaping the record the window depends on: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(rec.PID, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the recorded namespace child at pid %d survived the reap, so a provider killed in "+
		"the window still leaves it and the guest behind it running", rec.PID)
}

func indexOf(argv []string, want string) int {
	for i, arg := range argv {
		if arg == want {
			return i
		}
	}
	return -1
}
