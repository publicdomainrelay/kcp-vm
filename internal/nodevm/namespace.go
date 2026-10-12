package nodevm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/noderecord"
)

type Runner func(ctx context.Context, params NamespaceParams) error

func RunNamespace(ctx context.Context, paramsPath string, runners map[string]Runner) error {
	if err := WriteNamespaceRecord(paramsPath); err != nil {
		return err
	}
	params, err := readNamespaceParams(paramsPath)
	if err != nil {
		return err
	}
	run, ok := runners[params.Kind]
	if !ok || run == nil {
		return fmt.Errorf("nodevm: the namespace parameters at %s name substrate %q, and no VMM "+
			"runner for that name was supplied. The parameters are written by the host that "+
			"started pasta, so a kind nothing here can run is a mismatch between the gateway's "+
			"configuration and the agent binary inside the namespace: starting the agent anyway "+
			"would leave a namespace wired for a guest that is never booted, which reads as a node "+
			"that never joined", paramsPath, params.Kind)
	}
	if err := WireNamespace(ctx, params); err != nil {
		return err
	}
	return run(ctx, params)
}

func WriteNamespaceRecord(paramsPath string) error {
	dataDir := filepath.Dir(paramsPath)
	params, err := readNamespaceParams(paramsPath)
	if err != nil {
		return fmt.Errorf("nodevm: the substrate this namespace child runs for, which its own "+
			"record has to name: %w -- the record is what a later sweep matches a process "+
			"against, and only the substrate that wrote a record knows how to read the argv "+
			"inside it", err)
	}
	pid := os.Getpid()
	start, err := noderecord.ProcStart(pid)
	if err != nil {
		return fmt.Errorf("nodevm: reading the start time this namespace child is recorded "+
			"by, before it starts a VMM: %w -- the record names a pid, and without the start time "+
			"from the same read, a later gateway could not tell this process from whatever reuses "+
			"its pid number", err)
	}
	if err := noderecord.Write(dataDir, noderecord.Record{
		PID: pid, StartTime: start, Identity: paramsNamespaceIdentity(dataDir),
		Substrate: params.Kind,
	}); err != nil {
		return fmt.Errorf("nodevm: recording the namespace child at pid %d before it "+
			"starts a VMM: %w -- this runs FIRST, so a gateway killed between starting pasta and "+
			"writing the VMM's own record still leaves a record naming a process whose exit tears "+
			"the chain down, and a sweep that reads no record reaps nothing while the guest stays up",
			pid, err)
	}
	return nil
}

func paramsNamespaceIdentity(dataDir string) []string {
	return []string{NamespaceVerb, NamespacePath(dataDir)}
}

func readNamespaceParams(path string) (NamespaceParams, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return NamespaceParams{}, fmt.Errorf("nodevm: reading the namespace parameters at "+
			"%s: %w -- this process is the child of pasta, so it only exists because a parent wrote "+
			"that file first; a failure here is a wiring mistake, not a guest that failed to boot",
			path, err)
	}
	var params NamespaceParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return NamespaceParams{}, fmt.Errorf("nodevm: the namespace parameters at %s: %w",
			path, err)
	}
	if params.Kind == "" || params.VMM == "" || params.Rootfs == "" ||
		params.Instance == "" || params.Console == "" {
		return NamespaceParams{}, fmt.Errorf("nodevm: the namespace parameters at %s name kind %q, "+
			"VMM %q, rootfs %q, instance %q and console %q, and a parameter set missing any of "+
			"those would start a VMM with no kernel to run, no filesystem to run it from, or no "+
			"path for its API and its console. The node disk is not in this list on purpose: it is "+
			"the disk a k3s node's identity is written on, and a guest booted from a cloud-init "+
			"document carries its identity in that document instead, so requiring the disk here "+
			"would be this layer encoding one substrate's shape into a helper every substrate uses",
			path, params.Kind, params.VMM, params.Rootfs, params.Instance, params.Console)
	}
	return params, nil
}

func WireNamespace(ctx context.Context, params NamespaceParams) error {
	link, err := pastaLink(ctx)
	if err != nil {
		return err
	}
	for _, c := range [][]string{
		{"tuntap", "add", "dev", params.Tap, "mode", "tap"},
		{"link", "set", params.Tap, "up"},
		{"link", "add", params.Bridge, "type", "bridge"},
		{"link", "set", params.Bridge, "up"},
		{"link", "set", link, "master", params.Bridge},
		{"link", "set", params.Tap, "master", params.Bridge},
	} {
		if err := IP(ctx, c...); err != nil {
			return fmt.Errorf("nodevm: bridging pasta's %s to the tap %s for the guest: %w "+
				"-- the guest's only route to this host's apiserver is pasta's namespace, so a "+
				"tap that is not bridged to pasta's own link is a guest that boots, joins nothing "+
				"and reports nothing, which is indistinguishable from an agent that never started",
				link, params.Tap, err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	return nil
}

func pastaLink(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "ip", "-o", "-4", "addr", "show").CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			return "", fmt.Errorf("nodevm: listing this namespace's addresses failed (%v) "+
				"and wrote nothing at all, on neither stream, so nothing here says whether ip was "+
				"denied or only the write that would have described the denial was. This listing "+
				"is the gate that decides whether this namespace is pasta's, so an unexplained "+
				"failure to read it is a VMM that must not be configured, and it is named as a "+
				"failure rather than passed on as an error with no message", err)
		}
		return "", fmt.Errorf("nodevm: listing this namespace's addresses: %v: %s -- pasta "+
			"gives its child a namespace whose upstream link carries one of the host's own "+
			"addresses, and without that listing there is nothing to bridge the guest's tap onto",
			err, detail)
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[2] != "inet" {
			continue
		}
		name := strings.TrimSuffix(fields[1], ":")
		if name == "lo" || name == "" {
			continue
		}
		return name, nil
	}
	return "", fmt.Errorf("nodevm: this namespace has no interface carrying an IPv4 " +
		"address besides loopback, so pasta did not configure it, and on this evidence the VMM is " +
		"not configured either: this listing is the gate, not a step that runs before one. That is " +
		"what a child that is not actually pasta's finds: it runs on the host's own network, sees " +
		"only loopback, and would then bridge the guest onto nothing -- the guest would boot with a " +
		"tap that reaches nowhere and its agent would report a join that never happens. The gate " +
		"tests the property this package needs, a namespace pasta has actually configured, rather " +
		"than the SELinux label of the program pasta execs, which is why it survives the domain " +
		"the gateway is launched in changing under it")
}

func IP(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "ip", args...).CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			return fmt.Errorf("ip %s: %v: and it wrote nothing at all, on neither stream, so this "+
				"says nothing about whether ip itself failed or only the report of the failure was "+
				"denied. That is the shape a child of pasta produces in a domain that cannot write "+
				"to the pipe pasta handed it -- the denial is real and the account of it is denied "+
				"too -- and it is the shape that made one cause look like two plumbing failures, so "+
				"an empty failure is named here rather than reported as an error with no message",
				strings.Join(args, " "), err)
		}
		return fmt.Errorf("ip %s: %v: %s", strings.Join(args, " "), err, detail)
	}
	return nil
}

func PastaArgv(pasta, shell, agent string, net Network, paramsPath string) []string {
	return []string{
		pasta, "--config-net", "-q", "-f",
		"-a", net.PastaIP,
		"-n", Netmask(net.Prefix),
		"-g", net.Gateway,
		"--dns-forward", net.Gateway,
		shell, "-c", `exec "$0" ` + NamespaceVerb + ` "$1"`,
		agent, paramsPath,
	}
}

func Netmask(prefix int) string {
	var mask uint32
	if prefix > 0 && prefix < 32 {
		mask = ^uint32(0) << uint(32-prefix)
	}
	return fmt.Sprintf("%d.%d.%d.%d", byte(mask>>24), byte(mask>>16), byte(mask>>8), byte(mask))
}

func WriteInstance(path string, pid int, startNanos int64) error {
	raw, err := json.Marshal(InstanceRecord{PID: pid, StartNanos: startNanos})
	if err != nil {
		return fmt.Errorf("nodevm: encoding the VMM's pid: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("nodevm: writing the VMM's pid to %s: %w -- the parent waits for "+
			"this file to know which process to record, and without it the VMM runs with no record "+
			"and no later gateway can name, check or reap it", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("nodevm: publishing the VMM's pid at %s: %w", path, err)
	}
	return nil
}

func FindVMM(ctx context.Context, identity []string) (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, fmt.Errorf("nodevm: reading /proc: %w", err)
	}
	found := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		if noderecord.Matches(ParseCmdline(raw), identity) {
			if found != 0 && found != pid {
				return 0, fmt.Errorf("nodevm: two processes match %v (%d and %d), and the "+
					"identity that is supposed to name one VMM names both: a record written for "+
					"either of them would reap whichever it happened to name while the other ran on "+
					"holding the node", identity, found, pid)
			}
			found = pid
		}
	}
	if found == 0 {
		return 0, fmt.Errorf("nodevm: no process on this host matches %v, so the VMM the "+
			"guest side reported has already exited", identity)
	}
	return found, nil
}

func ParseCmdline(raw []byte) []string {
	fields := strings.Split(string(raw), "\x00")
	if n := len(fields); n > 0 && fields[n-1] == "" {
		return fields[:n-1]
	}
	return fields
}
