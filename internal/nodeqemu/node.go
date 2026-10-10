package nodeqemu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/noderecord"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/processrunner"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

type Host struct {
	mu     sync.Mutex
	opts   Options
	pasta  *processrunner.Process
	exited chan struct{}
}

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
		h.log().Warn("nodeqemu: the QEMU process for this node had already exited, so the node "+
			"that was counted as up is not running",
			"cluster", h.opts.Cluster, "node", h.opts.NodeName, "pid", h.pasta.PID)
		if err := h.forgetLocked(); err != nil {
			return err
		}
	}
	if h.pasta == nil {
		live, err := noderecord.LivenessOf(h.opts.DataDir, Identity(h.opts.DataDir), NamespaceIdentity(h.opts.DataDir))
		if err != nil {
			return fmt.Errorf("nodeqemu: before provisioning node %s, deciding whether a "+
				"record at %s names a VMM that is still running: %w -- a record that cannot be read "+
				"is not a node that is absent, and provisioning over one is how two VMMs end up "+
				"behind one node name", h.opts.NodeName, noderecord.RecordPath(h.opts.DataDir), err)
		}
		if live == noderecord.LivenessOurs {
			rec, _ := noderecord.Read(h.opts.DataDir)
			return fmt.Errorf("nodeqemu: node %s (record at %s names pid %d): %w",
				h.opts.NodeName, noderecord.RecordPath(h.opts.DataDir), rec.PID, ErrAlreadyRunning)
		}
	}
	if err := os.MkdirAll(h.opts.DataDir, 0o700); err != nil {
		return fmt.Errorf("nodeqemu: creating %s: %w", h.opts.DataDir, err)
	}
	if err := h.stageLocked(ctx); err != nil {
		return err
	}
	if err := h.opts.Record(ctx, h.opts.NodeName, v1alpha1.NodePending); err != nil {
		return fmt.Errorf("nodeqemu: recording node %s as Pending before its VMM is "+
			"started: %w -- the phase is written first because the machine is about to exist, and a "+
			"gateway that died between the two would otherwise leave no record at all of a node it "+
			"booted", h.opts.NodeName, err)
	}
	params, err := h.writeNamespaceParams()
	if err != nil {
		return err
	}
	if hostos.NetworkMode() == hostos.NetworkUser {
		return h.provisionUserLocked(ctx)
	}
	out, err := os.OpenFile(filepath.Join(h.opts.DataDir, nodevm.NamespaceLogName),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("nodeqemu: the log a guest's failure is the only account of: %w", err)
	}
	pasta, err := processrunner.Start(ctx, processrunner.Spec{
		Argv:   nodevm.PastaArgv(h.opts.Pasta, h.opts.Shell, h.opts.Agent, h.opts.Network, params),
		Output: out,
	})
	if err != nil {
		return fmt.Errorf("nodeqemu: starting pasta for node %s: %w", h.opts.NodeName, err)
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
	h.log().Info("nodeqemu: node started", "cluster", h.opts.Cluster, "node", h.opts.NodeName,
		"server", h.opts.Server, "vmmPID", pid, "pastaPID", pasta.PID,
		"phase", v1alpha1.NodePending,
		"next", "ConfirmJoined, once the tenant cluster reports this node")
	return nil
}

func (h *Host) provisionUserLocked(ctx context.Context) error {
	out, err := os.OpenFile(filepath.Join(h.opts.DataDir, nodevm.NamespaceLogName),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("nodeqemu: the log a guest's failure is the only account of: %w", err)
	}
	argv := append([]string{h.opts.QEMU}, QEMUArgv(h.namespaceParams(), PlatformNow())...)
	vmm, err := processrunner.Start(ctx, processrunner.Spec{Argv: argv, Output: out})
	if err != nil {
		return fmt.Errorf("nodeqemu: starting qemu for node %s: %w", h.opts.NodeName, err)
	}
	h.pasta = vmm
	h.watchLocked(vmm)
	if err := nodevm.WriteInstance(nodevm.InstancePath(h.opts.DataDir), vmm.PID, time.Now().UnixNano()); err != nil {
		_ = h.stopLocked()
		return err
	}
	if err := h.recordPIDLocked(vmm.PID); err != nil {
		_ = h.stopLocked()
		return err
	}
	h.log().Info("nodeqemu: node started", "cluster", h.opts.Cluster, "node", h.opts.NodeName,
		"server", h.opts.Server, "vmmPID", vmm.PID, "network", hostos.NetworkUser,
		"phase", v1alpha1.NodePending,
		"next", "ConfirmJoined, once the tenant cluster reports this node")
	return nil
}

func (h *Host) recordPIDLocked(pid int) error {
	start, err := noderecord.ProcStart(pid)
	if err != nil {
		return fmt.Errorf("nodeqemu: reading pid %d for node %s through the host: %w", pid,
			h.opts.NodeName, err)
	}
	return noderecord.Write(h.opts.DataDir, noderecord.Record{
		PID: pid, StartTime: start, Identity: Identity(h.opts.DataDir),
	})
}

func (h *Host) stageLocked(ctx context.Context) error {
	token, err := os.ReadFile(h.opts.TokenFile)
	if err != nil {
		return fmt.Errorf("nodeqemu: reading the join token at %s for node %s: %w -- the "+
			"token reaches the guest as the content of a file on its own read-only disk, so it is "+
			"read here and never placed on a command line, in an environment variable or in a "+
			"kernel argument, all three of which are readable by anything on this host that can "+
			"open /proc", h.opts.TokenFile, h.opts.NodeName, err)
	}
	password, err := nodevm.MintOrReadNodePassword(nodevm.NodePasswordPath(h.opts.DataDir))
	if err != nil {
		return err
	}
	doc := nodevm.NodeDocument{
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
	if err := nodevm.BuildNodeDisk(nodevm.NodeDiskPath(h.opts.DataDir), doc, h.opts.DiskMiB, stage); err != nil {
		return err
	}
	return nodevm.CloneFile(h.opts.Image.Rootfs, nodevm.RootfsPath(h.opts.DataDir))
}

func (h *Host) namespaceParams() nodevm.NamespaceParams {
	return nodevm.NamespaceParams{
		Kind:      nodevm.KindQEMU,
		VMM:       h.opts.QEMU,
		Name:      h.opts.NodeName,
		Kernel:    h.opts.Image.Kernel,
		Initramfs: h.opts.Image.Initramfs,
		Rootfs:    nodevm.RootfsPath(h.opts.DataDir),
		NodeDisk:  nodevm.NodeDiskPath(h.opts.DataDir),
		BootArgs:  h.opts.Guest.CommandLine(),
		Console:   nodevm.ConsolePath(h.opts.DataDir),
		Instance:  nodevm.InstancePath(h.opts.DataDir),
		Vcpu:      h.opts.Guest.Vcpu,
		MemMiB:    h.opts.Guest.MemMiB,
		SMP:       h.opts.Guest.SMP,
		Tap:       h.opts.Network.Tap,
		Bridge:    h.opts.Network.Bridge,
		GuestMAC:  h.opts.Network.GuestMAC,
	}
}

func (h *Host) writeNamespaceParams() (string, error) {
	params := h.namespaceParams()
	raw, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("nodeqemu: encoding the namespace parameters for node %s: %w",
			h.opts.NodeName, err)
	}
	path := nodevm.NamespacePath(h.opts.DataDir)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", fmt.Errorf("nodeqemu: writing the namespace parameters to %s: %w", path, err)
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
		if pid, err := readInstance(nodevm.InstancePath(h.opts.DataDir)); err == nil {
			return pid, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		if h.exitedLocked() {
			return 0, fmt.Errorf("nodeqemu: the process hosting node %s's guest exited "+
				"before it created a VMM, so no guest exists and nothing was recorded. Its output "+
				"follows, and it is the only account of why:\n%s",
				h.opts.NodeName, h.pastaOutput())
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("nodeqemu: node %s's guest had not started %s after pasta "+
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
	var rec nodevm.InstanceRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return 0, fmt.Errorf("nodeqemu: the VMM pid at %s: %w", path, err)
	}
	if rec.PID <= 0 {
		return 0, fmt.Errorf("nodeqemu: the VMM pid at %s is %d, and a pid of zero names no "+
			"process to record, check or reap", path, rec.PID)
	}
	return rec.PID, nil
}

func (h *Host) recordLocked(ctx context.Context) error {
	pid, err := nodevm.FindVMM(ctx, Identity(h.opts.DataDir))
	if err != nil {
		return fmt.Errorf("nodeqemu: finding node %s's VMM on this host: %w -- the pid the "+
			"guest's side publishes is the pid INSIDE the namespace pasta gave it, and the record "+
			"must name the host's pid or every later check and reap compares the host's process "+
			"table against a number that belongs to a different table", h.opts.NodeName, err)
	}
	start, err := noderecord.ProcStart(pid)
	if err != nil {
		return fmt.Errorf("nodeqemu: reading pid %d for node %s through the host: %w", pid,
			h.opts.NodeName, err)
	}
	if err := noderecord.Write(h.opts.DataDir, noderecord.Record{
		PID: pid, StartTime: start, Identity: Identity(h.opts.DataDir),
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
	return h.stopProcessLocked(pasta)
}

func (h *Host) stopProcessLocked(p *processrunner.Process) error {
	if p == nil {
		return nil
	}
	if hostos.NetworkMode() == hostos.NetworkUser {
		if err := hostos.Signal(p.PID, true); err != nil {
			return fmt.Errorf("nodeqemu: stopping the process hosting node %s's guest: %w",
				h.opts.NodeName, err)
		}
		_ = p.Wait()
		return nil
	}
	if err := p.Stop(os.Interrupt); err != nil {
		return fmt.Errorf("nodeqemu: stopping the process hosting node %s's guest: %w",
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
		return fmt.Errorf("nodeqemu: node %s: %w", h.opts.NodeName, ErrNotRunning)
	}
	if err := nodevm.AssertGuestDialledOurs(h.opts.NodeName, h.opts.Server,
		nodevm.ConsolePath(h.opts.DataDir)); err != nil {
		return err
	}
	if err := h.opts.Record(ctx, h.opts.NodeName, v1alpha1.NodeRunning); err != nil {
		return fmt.Errorf("nodeqemu: recording node %s as Running after k3s reported it: %w",
			h.opts.NodeName, err)
	}
	return nil
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
		nodevm.RootfsPath(h.opts.DataDir),
		nodevm.NodeDiskPath(h.opts.DataDir),
		nodevm.InstancePath(h.opts.DataDir),
		nodevm.NamespacePath(h.opts.DataDir),
		nodevm.ConsolePath(h.opts.DataDir),
		filepath.Join(h.opts.DataDir, nodevm.NamespaceLogName),
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
	log.Info("nodeqemu: stopping the node", "cluster", h.opts.Cluster,
		"node", h.opts.NodeName)
	var problems []error
	if err := h.opts.Record(ctx, h.opts.NodeName, v1alpha1.NodeDraining); err != nil {
		problems = append(problems, fmt.Errorf("nodeqemu: recording node %s as Draining: %w",
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
		problems = append(problems, fmt.Errorf("nodeqemu: reaping node %s's VMM: %w -- the "+
			"guest is still up with its Node object, and this returns an error rather than a success "+
			"because the record and the root filesystem under it are left where they are for the "+
			"process that still holds them",
			h.opts.NodeName, reapErr))
	}
	if pasta != nil {
		if err := h.stopProcessLocked(pasta); err != nil {
			problems = append(problems, err)
		}
	}
	if reapErr == nil {
		for _, p := range h.artifacts() {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				problems = append(problems, fmt.Errorf("nodeqemu: removing %s after node %s "+
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

func (h *Host) Terminal() string { return nodevm.Fatal(h.opts.DataDir) }
