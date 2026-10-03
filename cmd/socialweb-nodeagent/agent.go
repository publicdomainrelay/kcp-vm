//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultcert"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

const (
	nodeDiskMount = nodefirecracker.GuestNodeDir

	nodeInterfaceDeadline = 60 * time.Second
)

func GuestAgent() error {
	ctx := context.Background()
	out := os.Stdout
	doc, err := loadGuestDocument()
	if err != nil {
		return err
	}
	mounted, err := mountNodeDisk()
	if err != nil {
		return err
	}
	node, err := nodefirecracker.LoadNodeDocument(filepath.Join(mounted, nodefirecracker.NodeFileName))
	if err != nil {
		return err
	}
	if node.AuditKernel {
		if err := KernelAudit(out, doc); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "KCP_AGENT_START server=%s node=%s datadir=%s\n",
		node.Server, node.NodeName, doc.K3s.DataDir)
	if err := configureResolver(node); err != nil {
		return err
	}
	if err := configureInterface(node); err != nil {
		return err
	}
	if err := adoptVaultPassword(ctx, mounted, &node); err != nil {
		return err
	}
	if err := adoptIdentity(node, doc); err != nil {
		return err
	}
	probe(node.Server)
	fmt.Fprintf(out, "KCP_AGENT_STARTING_K3S %s\n", time.Now().UTC().Format(time.RFC3339))
	return runK3s(doc, node)
}

func adoptVaultPassword(ctx context.Context, diskDir string, node *nodefirecracker.NodeDocument) error {
	cert, err := vaultcert.Load(diskDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stdout, "KCP_AGENT_VAULT node=%s state=none (the disk carries no vault "+
				"certificate; this cluster has no vault and the disk password is used)\n", node.NodeName)
			return nil
		}
		return fmt.Errorf("nodeagent: this guest's identity disk carries a vault client certificate "+
			"that cannot be read: %w", err)
	}
	secret, err := cert.FetchOwnSecret(ctx)
	if err != nil {
		return fmt.Errorf("nodeagent: node %s could not read its own secret from the vault at %s "+
			"through CERT auth: %w -- the certificate on its disk is the only vault credential it "+
			"holds, and the per-node password k3s needs is the secret it reads, so a refused read "+
			"is a node that cannot join rather than one that joins with the wrong password",
			node.NodeName, cert.Metadata.VaultAddr, err)
	}
	password, ok := secret["password"].(string)
	if !ok || password == "" {
		return fmt.Errorf("nodeagent: the vault returned %v for node %s, which carries no usable "+
			"per-node password", secret, node.NodeName)
	}
	fmt.Fprintf(os.Stdout, "KCP_AGENT_VAULT node=%s path=%s state=read\n",
		node.NodeName, vaultlayout.NodeSecretPath(node.NodeName))
	node.NodePassword = password
	return nil
}

func runK3s(doc nodefirecracker.GuestDocument, node nodefirecracker.NodeDocument) error {
	sink, closeSink, err := agentSink()
	if err != nil {
		return err
	}
	defer closeSink()
	argv := []string{
		"agent",
		"--server", node.Server,
		"--node-name", node.NodeName,
		"--token-file", nodefirecracker.GuestTokenPath,
		"--data-dir", doc.K3s.DataDir,
		"--node-ip", node.Address,
		"--flannel-iface", node.Interface,
	}
	fmt.Fprintf(sink, "KCP_AGENT_EXEC %s %s\n", doc.K3s.Binary, strings.Join(argv, " "))
	cmd := exec.Command(doc.K3s.Binary, argv...)
	cmd.Stdout, cmd.Stderr = sink, sink
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("nodeagent: starting %s agent for node %s: %w -- the unit that runs "+
			"this exits when this returns, so the guest stops trying to join at the moment it "+
			"first tries", doc.K3s.Binary, node.NodeName, err)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		for sig := range signals {
			_ = syscall.Kill(-cmd.Process.Pid, sig.(syscall.Signal))
		}
	}()
	err = cmd.Wait()
	rc := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		rc = exit.ExitCode()
	} else if err != nil {
		return fmt.Errorf("nodeagent: waiting for the k3s agent of node %s: %w", node.NodeName, err)
	}
	fmt.Fprintf(sink, "KCP_AGENT_EXIT rc=%d\n", rc)
	return nil
}

func agentSink() (io.Writer, func(), error) {
	log, err := os.OpenFile(nodefirecracker.GuestAgentLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("nodeagent: opening %s: %w -- the console is forwarded through "+
			"the guest's journald, and a journald that has crashed or is rate-limiting turns a "+
			"guest's only account of its own failure into silence; the log file is written by this "+
			"process directly, so it survives that", nodefirecracker.GuestAgentLog, err)
	}
	closers := []io.Closer{log}
	writers := []io.Writer{os.Stdout, log}
	console, err := os.OpenFile("/dev/console", os.O_WRONLY, 0)
	if err == nil {
		closers = append(closers, console)
		writers = append(writers, console)
	}
	return io.MultiWriter(writers...), func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}, nil
}

func mountNodeDisk() (string, error) {
	if err := os.MkdirAll(nodeDiskMount, 0o700); err != nil {
		return "", fmt.Errorf("nodeagent: creating %s: %w", nodeDiskMount, err)
	}
	for i := 'b'; i <= 'z'; i++ {
		device := fmt.Sprintf("/dev/vd%c", i)
		if _, err := os.Stat(device); err != nil {
			continue
		}
		if err := syscall.Mount(device, nodeDiskMount, "ext4", syscall.MS_RDONLY, ""); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(nodeDiskMount, nodefirecracker.NodeFileName)); err == nil {
			return nodeDiskMount, nil
		}
		_ = syscall.Unmount(nodeDiskMount, 0)
	}
	return "", fmt.Errorf("nodeagent: no attached disk carries %s, so this guest has no name, no "+
		"server and no join token. The identity disk is attached read-only and is where every "+
		"per-node value comes from, because a kernel command line is visible to every process in "+
		"the guest and to the host's console log, and a token on one is a token published",
		nodefirecracker.NodeFileName)
}

type identityWrite struct {
	path  string
	value string
	mode  os.FileMode
}

func identityWrites(node nodefirecracker.NodeDocument, k3sDataDir string) []identityWrite {
	return []identityWrite{
		{nodefirecracker.GuestTokenPath, node.Token, 0o600},
		{"/etc/rancher/node/password", node.NodePassword, 0o600},
		{filepath.Join(k3sDataDir, "agent", "node-password"), node.NodePassword, 0o600},
	}
}

func adoptIdentity(node nodefirecracker.NodeDocument, doc nodefirecracker.GuestDocument) error {
	for _, d := range []string{"/etc/rancher/node", filepath.Join(doc.K3s.DataDir, "agent")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("nodeagent: creating %s: %w", d, err)
		}
	}
	for _, f := range identityWrites(node, doc.K3s.DataDir) {
		if err := os.WriteFile(f.path, []byte(f.value), f.mode); err != nil {
			return fmt.Errorf("nodeagent: writing a node credential to %s: %w -- k3s takes the "+
				"CLUSTER JOIN TOKEN from the file named by --token-file and its own per-node "+
				"PASSWORD from the node-password file, and the two are different values: a token "+
				"passed as an argument is readable from /proc by every process in this guest, and "+
				"a token written where the per-node password belongs is one credential with two "+
				"roles, shared by every node", f.path, err)
		}
	}
	if err := os.WriteFile("/etc/hostname", []byte(node.NodeName+"\n"), 0o644); err != nil {
		return fmt.Errorf("nodeagent: writing /etc/hostname: %w", err)
	}
	if out, err := exec.Command("hostname", node.NodeName).CombinedOutput(); err != nil {
		return fmt.Errorf("nodeagent: setting this guest's hostname to %s: %v: %s",
			node.NodeName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func configureResolver(node nodefirecracker.NodeDocument) error {
	body := fmt.Sprintf("nameserver %s\n", node.Gateway)
	if err := os.WriteFile("/etc/resolv.conf", []byte(body), 0o644); err != nil {
		return fmt.Errorf("nodeagent: writing /etc/resolv.conf with nameserver %s: %w -- the base "+
			"image ships an EMPTY resolver file, and an empty one is not a resolver that falls back: "+
			"Go reads it, finds no nameserver, and every lookup fails with \"no such host\" or "+
			"\"Try again\". A node whose kubelet never resolves anything still registers and still "+
			"reports Ready, because joining a cluster is a dial to an address the join document "+
			"already carries -- so the missing resolver is invisible until the first image pull, and "+
			"it reads there as a registry that is down rather than as a guest with no DNS",
			node.Gateway, err)
	}
	fmt.Fprintf(os.Stdout, "KCP_AGENT_RESOLV=nameserver %s\n", node.Gateway)
	return nil
}

func configureInterface(node nodefirecracker.NodeDocument) error {
	if err := awaitInterface(node.Interface); err != nil {
		return err
	}
	steps := [][]string{
		{"link", "set", node.Interface, "up"},
		{"addr", "add", fmt.Sprintf("%s/%d", node.Address, node.Prefix), "dev", node.Interface},
		{"route", "add", "default", "via", node.Gateway, "dev", node.Interface},
	}
	for _, s := range steps {
		out, err := exec.Command("ip", s...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("nodeagent: ip %s: %v: %s -- the guest reaches this host's apiserver "+
				"only through the address pasta gave this namespace, so an interface left without "+
				"its address or its default route is a guest whose agent retries a join for ever",
				strings.Join(s, " "), err, strings.TrimSpace(string(out)))
		}
	}
	if raw, err := exec.Command("ip", "-o", "addr", "show", "dev", node.Interface).Output(); err == nil {
		fmt.Fprintf(os.Stdout, "KCP_AGENT_IPADDR=%s", string(raw))
	}
	if raw, err := exec.Command("ip", "route").Output(); err == nil {
		fmt.Fprintf(os.Stdout, "KCP_AGENT_ROUTE=%s", string(raw))
	}
	return nil
}

func awaitInterface(name string) error {
	deadline := time.Now().Add(nodeInterfaceDeadline)
	for {
		if _, err := net.InterfaceByName(name); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("nodeagent: %s is not in this guest %s after this node's agent started "+
				"looking for it. The device appears when the kernel's virtio driver for it is loaded, "+
				"and that load is udev's, in parallel with the units before this one, so it is not "+
				"ordered against this process and this process must not assume it. Measured: this is "+
				"where the first boot without the unit that used to slow the agent down enough to hide "+
				"the wait failed, because the agent reached this point before the device existed",
				name, nodeInterfaceDeadline)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func probe(server string) {
	addr := strings.TrimPrefix(strings.TrimPrefix(server, "https://"), "http://")
	conn, err := net.DialTimeout("tcp", addr, 6*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stdout, "KCP_CONN apiserver %s -> FAIL %v\n", addr, err)
		return
	}
	conn.Close()
	fmt.Fprintf(os.Stdout, "KCP_CONN apiserver %s -> OK\n", addr)
}
