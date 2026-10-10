package nodefirecracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/noderecord"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/processrunner"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

type Host struct {
	mu     sync.Mutex
	opts   Options
	pasta  *processrunner.Process
	exited chan struct{}
}

const Substrate = "firecracker"

var _ scale.Nodes = (*Host)(nil)

func New(opts Options) (*Host, error) {
	opts = opts.withDefaults()
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	return &Host{opts: opts}, nil
}

func (h *Host) log() *slog.Logger {
	if h.opts.Log == nil {
		return slog.Default()
	}
	return h.opts.Log
}

func (h *Host) Provision(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pasta != nil {
		if !h.exitedLocked() {
			return nil
		}
		h.log().Warn("nodefirecracker: the VMM for this node had already exited, so the node that "+
			"was counted as up is not running",
			"cluster", h.opts.Cluster, "node", h.opts.NodeName, "pid", h.pasta.PID)
		if err := h.forgetLocked(); err != nil {
			return err
		}
	}
	if h.pasta == nil {
		live, err := noderecord.LivenessOf(h.opts.DataDir, Identity(h.opts.DataDir), NamespaceIdentity(h.opts.DataDir))
		if err != nil {
			return fmt.Errorf("nodefirecracker: before provisioning node %s, deciding whether a "+
				"record at %s names a VMM that is still running: %w -- a record that cannot be read "+
				"is not a node that is absent, and provisioning over one is how two VMMs end up "+
				"behind one node name", h.opts.NodeName, noderecord.RecordPath(h.opts.DataDir), err)
		}
		if live == noderecord.LivenessOurs {
			rec, _ := noderecord.Read(h.opts.DataDir)
			return fmt.Errorf("nodefirecracker: node %s (record at %s names pid %d): %w",
				h.opts.NodeName, noderecord.RecordPath(h.opts.DataDir), rec.PID, ErrAlreadyRunning)
		}
	}
	if err := os.MkdirAll(h.opts.DataDir, 0o700); err != nil {
		return fmt.Errorf("nodefirecracker: creating %s: %w", h.opts.DataDir, err)
	}
	if err := h.stageLocked(ctx); err != nil {
		return err
	}
	if err := h.opts.Record(ctx, h.opts.NodeName, v1alpha1.NodePending); err != nil {
		return fmt.Errorf("nodefirecracker: recording node %s as Pending before its VMM is "+
			"started: %w -- the phase is written first because the machine is about to exist, and a "+
			"gateway that died between the two would otherwise leave no record at all of a node it "+
			"booted", h.opts.NodeName, err)
	}
	params, err := h.writeNamespaceParams()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(filepath.Join(h.opts.DataDir, NamespaceLogName),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("nodefirecracker: the log a guest's failure is the only account of: %w", err)
	}
	pasta, err := processrunner.Start(ctx, processrunner.Spec{
		Argv:   pastaArgv(h.opts, params),
		Output: out,
	})
	if err != nil {
		return fmt.Errorf("nodefirecracker: starting pasta for node %s: %w", h.opts.NodeName, err)
	}
	h.pasta = pasta
	h.watchLocked(pasta)
	pid, err := h.awaitInstanceLocked()
	if err != nil {
		_ = h.stopLocked()
		return err
	}
	if err := h.recordLocked(ctx); err != nil {
		_ = h.stopLocked()
		return err
	}
	h.log().Info("nodefirecracker: node started", "cluster", h.opts.Cluster, "node", h.opts.NodeName,
		"server", h.opts.Server, "vmmPID", pid, "pastaPID", pasta.PID,
		"phase", v1alpha1.NodePending,
		"next", "ConfirmJoined, once the tenant cluster reports this node")
	return nil
}

func (h *Host) stageLocked(ctx context.Context) error {
	token, err := os.ReadFile(h.opts.TokenFile)
	if err != nil {
		return fmt.Errorf("nodefirecracker: reading the join token at %s for node %s: %w -- the "+
			"token reaches the guest as the content of a file on its own read-only disk, so it is "+
			"read here and never placed on a command line, in an environment variable or in a "+
			"kernel argument, all three of which are readable by anything on this host that can "+
			"open /proc", h.opts.TokenFile, h.opts.NodeName, err)
	}
	password, err := mintOrReadNodePassword(NodePasswordPath(h.opts.DataDir))
	if err != nil {
		return err
	}
	doc := NodeDocument{
		NodeName:     h.opts.NodeName,
		Server:       h.opts.Server,
		Token:        string(token),
		NodePassword: password,
		Address:      h.opts.Network.GuestIP,
		Prefix:       h.opts.Network.Prefix,
		Gateway:      h.opts.Network.Gateway,
		Interface:    h.opts.Network.Interface,
		AuditKernel:  h.opts.AuditKernel,
	}
	var stage func(string) error
	if h.opts.NodeDiskStage != nil {
		stage = func(dir string) error { return h.opts.NodeDiskStage(ctx, dir, password) }
	}
	if err := BuildNodeDisk(NodeDiskPath(h.opts.DataDir), doc, h.opts.DiskMiB, stage); err != nil {
		return err
	}
	return CloneFile(h.opts.Image.Rootfs, RootfsPath(h.opts.DataDir))
}

func mintOrReadNodePassword(path string) (string, error) {
	return nodevm.MintOrReadNodePassword(path)
}

func (h *Host) writeNamespaceParams() (string, error) {
	params := namespaceParams{
		Kind:      nodevm.KindFirecracker,
		VMM:       h.opts.Firecracker,
		Name:      h.opts.NodeName,
		Kernel:    h.opts.Image.Kernel,
		Initramfs: h.opts.Image.Initramfs,
		Rootfs:    RootfsPath(h.opts.DataDir),
		NodeDisk:  NodeDiskPath(h.opts.DataDir),
		BootArgs:  h.opts.Guest.CommandLine(),
		Socket:    SocketPath(h.opts.DataDir),
		Console:   ConsolePath(h.opts.DataDir),
		Instance:  InstancePath(h.opts.DataDir),
		Vcpu:      h.opts.Guest.Vcpu,
		MemMiB:    h.opts.Guest.MemMiB,
		SMP:       h.opts.Guest.SMP,
		Tap:       h.opts.Network.Tap,
		Bridge:    h.opts.Network.Bridge,
		GuestMAC:  h.opts.Network.GuestMAC,
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("nodefirecracker: encoding the namespace parameters for node %s: %w",
			h.opts.NodeName, err)
	}
	path := NamespacePath(h.opts.DataDir)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", fmt.Errorf("nodefirecracker: writing the namespace parameters to %s: %w", path, err)
	}
	return path, nil
}

func (h *Host) watchLocked(pasta *processrunner.Process) {
	done := make(chan struct{})
	h.exited = done
	go func() {
		_ = pasta.Wait()
		close(done)
	}()
}

func (h *Host) exitedLocked() bool {
	if h.exited == nil {
		return true
	}
	select {
	case <-h.exited:
		return true
	default:
		return false
	}
}

func (h *Host) awaitInstanceLocked() (int, error) {
	deadline := time.Now().Add(h.opts.InstanceTimeout)
	for {
		if pid, err := readInstance(InstancePath(h.opts.DataDir)); err == nil {
			return pid, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		if h.exitedLocked() {
			return 0, fmt.Errorf("nodefirecracker: the process hosting node %s's guest exited "+
				"before it created a VMM, so no guest exists and nothing was recorded. Its output "+
				"follows, and it is the only account of why:\n%s",
				h.opts.NodeName, h.pastaOutput())
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("nodefirecracker: node %s's guest had not started %s after pasta "+
				"was launched, so the parent stops here rather than recording a node that is not "+
				"running. Its output follows:\n%s",
				h.opts.NodeName, h.opts.InstanceTimeout, h.pastaOutput())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readInstance(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var rec instanceRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return 0, fmt.Errorf("nodefirecracker: the VMM pid at %s: %w", path, err)
	}
	if rec.PID <= 0 {
		return 0, fmt.Errorf("nodefirecracker: the VMM pid at %s is %d, and a pid of zero names no "+
			"process to record, check or reap", path, rec.PID)
	}
	return rec.PID, nil
}

func (h *Host) recordLocked(ctx context.Context) error {
	pid, err := FindVMM(ctx, Identity(h.opts.DataDir))
	if err != nil {
		return fmt.Errorf("nodefirecracker: finding node %s's VMM on this host: %w -- the pid the "+
			"guest's side publishes is the pid INSIDE the namespace pasta gave it, and the record "+
			"must name the host's pid or every later check and reap compares the host's process "+
			"table against a number that belongs to a different table", h.opts.NodeName, err)
	}
	start, err := noderecord.ProcStart(pid)
	if err != nil {
		return fmt.Errorf("nodefirecracker: reading pid %d for node %s through the host: %w", pid,
			h.opts.NodeName, err)
	}
	if err := noderecord.Write(h.opts.DataDir, noderecord.Record{
		PID: pid, StartTime: start, Identity: Identity(h.opts.DataDir), Substrate: Substrate,
	}); err != nil {
		return err
	}
	return nil
}

func (h *Host) pastaOutput() string {
	if h.pasta == nil {
		return ""
	}
	return h.pasta.Output()
}

func (h *Host) forgetLocked() error {
	pasta := h.pasta
	h.pasta = nil
	h.exited = nil
	if pasta != nil {
		_ = pasta.Wait()
	}
	return noderecord.Remove(h.opts.DataDir)
}

func (h *Host) stopLocked() error {
	pasta := h.pasta
	h.pasta = nil
	h.exited = nil
	if pasta == nil {
		return nil
	}
	if err := pasta.Stop(os.Interrupt); err != nil {
		return fmt.Errorf("nodefirecracker: stopping the process hosting node %s's guest: %w",
			h.opts.NodeName, err)
	}
	return nil
}

func (h *Host) Alive() (noderecord.Liveness, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pasta != nil {
		if h.exitedLocked() {
			return noderecord.LivenessGone, nil
		}
		return noderecord.LivenessOurs, nil
	}
	return noderecord.LivenessOf(h.opts.DataDir, Identity(h.opts.DataDir), NamespaceIdentity(h.opts.DataDir))
}

func (h *Host) ConfirmJoined(ctx context.Context) error {
	h.mu.Lock()
	dead := h.pasta == nil || h.exitedLocked()
	h.mu.Unlock()
	if dead {
		return fmt.Errorf("nodefirecracker: node %s: %w", h.opts.NodeName, ErrNotRunning)
	}
	if err := h.assertGuestDialledOurs(); err != nil {
		return err
	}
	if err := h.opts.Record(ctx, h.opts.NodeName, v1alpha1.NodeRunning); err != nil {
		return fmt.Errorf("nodefirecracker: recording node %s as Running after k3s reported it: %w",
			h.opts.NodeName, err)
	}
	return nil
}

func (h *Host) assertGuestDialledOurs() error {
	want, err := advertiseEndpoint(h.opts.Server)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(ConsolePath(h.opts.DataDir))
	if err != nil {
		return fmt.Errorf("nodefirecracker: reading node %s's console to check which apiserver it "+
			"dialled: %w -- the tenant cluster reporting this node says a k3s agent registered, and "+
			"a console that cannot be read leaves WHICH apiserver it registered with unverified",
			h.opts.NodeName, err)
	}
	if err := portallocator.AssertAgentDialledOurPort(string(raw), want); err != nil {
		return fmt.Errorf("nodefirecracker: node %s is reported by the tenant cluster, but its own "+
			"account of what it dialled does not stand up: %w. The node joining is not evidence that "+
			"it joined THIS cluster -- two clusters on one host share neither their lease nor their "+
			"CA, and the one that gets the address wrong registers somewhere else and reports an "+
			"x509 error that reads as a certificate fault", h.opts.NodeName, err)
	}
	return nil
}

func advertiseEndpoint(server string) (portallocator.Endpoint, error) {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(server, "https://"), "http://")
	host, portText, ok := strings.Cut(trimmed, ":")
	if !ok {
		return portallocator.Endpoint{}, fmt.Errorf("nodefirecracker: the server this node was "+
			"built with, %q, names no port, so an address that answers cannot be told from one that "+
			"does not and the node's own dial line cannot be checked against anything", server)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return portallocator.Endpoint{}, fmt.Errorf("nodefirecracker: the port in %q: %w", server, err)
	}
	return portallocator.Endpoint{Address: host, Port: port}, nil
}

func (h *Host) Reap(ctx context.Context) error {
	return noderecord.Reap(ctx, noderecord.ReapOptions{
		Cluster:        h.opts.Cluster,
		NodeName:       h.opts.NodeName,
		DataDir:        h.opts.DataDir,
		Identity:       Identity(h.opts.DataDir),
		HolderIdentity: NamespaceIdentity(h.opts.DataDir),
		Log:            h.opts.Log,
	})
}

func (h *Host) artifacts() []string {
	return []string{
		RootfsPath(h.opts.DataDir),
		NodeDiskPath(h.opts.DataDir),
		SocketPath(h.opts.DataDir),
		InstancePath(h.opts.DataDir),
		NamespacePath(h.opts.DataDir),
		ConsolePath(h.opts.DataDir),
		filepath.Join(h.opts.DataDir, NamespaceLogName),
	}
}

func (h *Host) anyArtifacts() bool {
	for _, p := range h.artifacts() {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func (h *Host) Teardown(ctx context.Context) error {
	h.mu.Lock()
	pasta := h.pasta
	h.pasta = nil
	h.exited = nil
	h.mu.Unlock()
	if pasta == nil && !h.anyArtifacts() {
		return nil
	}
	log := h.log()
	log.Info("nodefirecracker: stopping the node", "cluster", h.opts.Cluster,
		"node", h.opts.NodeName)
	var problems []error
	if err := h.opts.Record(ctx, h.opts.NodeName, v1alpha1.NodeDraining); err != nil {
		problems = append(problems, fmt.Errorf("nodefirecracker: recording node %s as Draining: %w",
			h.opts.NodeName, err))
	}
	reapErr := noderecord.Reap(ctx, noderecord.ReapOptions{
		Cluster:        h.opts.Cluster,
		NodeName:       h.opts.NodeName,
		DataDir:        h.opts.DataDir,
		Identity:       Identity(h.opts.DataDir),
		HolderIdentity: NamespaceIdentity(h.opts.DataDir),
		Log:            h.opts.Log,
	})
	if reapErr != nil {
		problems = append(problems, fmt.Errorf("nodefirecracker: reaping node %s's VMM: %w -- the "+
			"guest is still up with its Node object, and this returns an error rather than a success "+
			"because the record and the root filesystem under it are left where they are for the "+
			"process that still holds them",
			h.opts.NodeName, reapErr))
	}
	if pasta != nil {
		if err := pasta.Stop(os.Interrupt); err != nil {
			problems = append(problems, fmt.Errorf("nodefirecracker: stopping the process hosting "+
				"node %s's guest: %w", h.opts.NodeName, err))
		}
	}
	if reapErr == nil {
		for _, p := range h.artifacts() {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				problems = append(problems, fmt.Errorf("nodefirecracker: removing %s after node %s "+
					"stopped: %w -- the root filesystem copy is this node's alone, the disk beside it "+
					"carries the join token and, once the guest has booted, so does the root filesystem "+
					"copy in /kcp/token and in k3s's node-password files; the console and the namespace "+
					"log are the only account of what happened in the guest. The set of files cleared "+
					"here is read off the data directory and NOT off the node's record, because a "+
					"gateway that restarted has no record and would otherwise report a teardown it "+
					"never performed, leaving two gigabytes and a secret behind per node",
					p, h.opts.NodeName, err))
			}
		}
	}
	return errors.Join(problems...)
}
