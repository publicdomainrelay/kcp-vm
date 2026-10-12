//go:build linux

package livekcp

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/kcp"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/ref"
)

func init() {
	os.Setenv("KUBE_FEATURE_WatchListClient", "false")
}

const (
	EnvRequire = "KCP_LIBS_REQUIRE_LIVE"

	EnvKubeconfig = "KCP_LIBS_KUBECONFIG"

	EnvServer = "KCP_LIBS_SERVER"

	EnvEtcdServers = "KCP_LIBS_ETCD_SERVERS"

	ProviderWorkspace = "provider"

	ConsumerWorkspace = "consumer"

	secondWorkspace = "consumer-two"

	Export = "widgets"

	SecondExport = "gadgets"

	Group = "example.computer"

	Version = "v1alpha1"

	Resource = "widgets"

	SecondResource = "gadgets"

	SecondKind = "Gadget"

	Kind = "Widget"

	APIVersion = Group + "/" + Version
)

func Resolve(name string) string {
	switch name {
	case "kcp":
		if value := os.Getenv("KCP_BIN"); value != "" {
			return value
		}
	case "kubectl":
		if value := os.Getenv("KUBECTL"); value != "" {
			return value
		}
	}
	return name
}

func Missing() []string {
	var missing []string
	for _, name := range []string{"kcp", "kubectl"} {
		if _, err := exec.LookPath(Resolve(name)); err != nil {
			missing = append(missing, name)
		}
	}
	return missing
}

type Phase struct {
	Name string

	Duration time.Duration
}

func (c *Cluster) Phases() []Phase {
	return append([]Phase(nil), c.phases...)
}

func (c *Cluster) Trace() string {
	var builder strings.Builder
	var total time.Duration
	for _, phase := range c.phases {
		total += phase.Duration
		fmt.Fprintf(&builder, "%8.2fs  %s\n", phase.Duration.Seconds(), phase.Name)
	}
	fmt.Fprintf(&builder, "%8.2fs  total\n", total.Seconds())
	return builder.String()
}

func (c *Cluster) mark(name string, start time.Time) time.Time {
	now := time.Now()
	c.phases = append(c.phases, Phase{Name: name, Duration: now.Sub(start)})
	return now
}

type Cluster struct {
	Root string

	Kubeconfig string

	Server string

	Config *rest.Config

	ProviderCluster string

	ConsumerCluster string

	SecondConsumerCluster string

	processes []*exec.Cmd

	logFiles []*os.File

	logs []string

	borrowed bool

	phases []Phase
}

func (c *Cluster) Stop() {
	if c.borrowed {
		return
	}
	for _, process := range c.processes {
		if process.Process == nil {
			continue
		}
		_ = syscall.Kill(-process.Process.Pid, syscall.SIGKILL)
		_ = process.Process.Kill()
	}
	for _, process := range c.processes {
		_, _ = process.Process.Wait()
	}
	for _, log := range c.logFiles {
		_ = log.Close()
	}
	if c.Root != "" {
		_ = os.RemoveAll(c.Root)
	}
}

func (c *Cluster) Logs() string {
	var builder strings.Builder
	for _, path := range c.logs {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		builder.WriteString("== " + path + "\n")
		lines := strings.Split(strings.TrimSpace(string(body)), "\n")
		if len(lines) > 40 {
			lines = lines[len(lines)-40:]
		}
		builder.WriteString(strings.Join(lines, "\n"))
		builder.WriteString("\n")
	}
	return builder.String()
}

func Start(ctx context.Context) (*Cluster, error) {
	var lastErr error
	for attempt := range 3 {
		cluster, err := start(ctx)
		if err == nil {
			return cluster, nil
		}
		lastErr = fmt.Errorf("attempt %d: %w", attempt+1, err)
	}
	return nil, lastErr
}

func start(ctx context.Context) (*Cluster, error) {
	if kubeconfig := os.Getenv(EnvKubeconfig); kubeconfig != "" {
		return borrow(ctx, kubeconfig)
	}
	root, err := os.MkdirTemp("", "kcp-libs-live-")
	if err != nil {
		return nil, err
	}
	kcpPort, err := freePort()
	if err != nil {
		return nil, err
	}
	cluster := &Cluster{
		Root:                  root,
		Kubeconfig:            filepath.Join(root, "admin.kubeconfig"),
		Server:                fmt.Sprintf("https://127.0.0.1:%d", kcpPort),
		ProviderCluster:       kcp.RootWorkspace + ":" + ProviderWorkspace,
		ConsumerCluster:       kcp.RootWorkspace + ":" + ConsumerWorkspace,
		SecondConsumerCluster: kcp.RootWorkspace + ":" + secondWorkspace,
	}
	ok := false
	defer func() {
		if !ok {
			cluster.Stop()
		}
	}()

	phase := time.Now()
	kcpLog := filepath.Join(root, "kcp.log")
	cluster.logs = append(cluster.logs, kcpLog)
	args := []string{"start",
		"--root-directory=" + root,
		"--bind-address=127.0.0.1",
		"--secure-port=" + fmt.Sprint(kcpPort),
		"--feature-gates=WorkspaceMounts=true",
	}
	if servers := os.Getenv(EnvEtcdServers); servers != "" {
		args = append(args, "--etcd-servers="+servers)
	}
	kcp, log, err := cluster.spawn(kcpLog, Resolve("kcp"), args...)
	if err != nil {
		return nil, err
	}
	_ = log
	cluster.processes = append(cluster.processes, kcp)
	if err := waitReady(ctx, cluster.Server, 180*time.Second); err != nil {
		return nil, fmt.Errorf("kcp did not become ready: %w\n%s", err, cluster.Logs())
	}
	phase = cluster.mark("kcp /readyz", phase)
	if err := waitFile(ctx, cluster.Kubeconfig, 180*time.Second); err != nil {
		return nil, fmt.Errorf("%w\n%s", err, cluster.Logs())
	}
	phase = cluster.mark("admin kubeconfig written", phase)

	config, err := clientcmd.BuildConfigFromFlags("", cluster.Kubeconfig)
	if err != nil {
		return nil, err
	}
	cluster.Config = config

	if err := cluster.awaitAdmin(ctx); err != nil {
		return nil, fmt.Errorf("%w\n%s", err, cluster.Logs())
	}
	phase = cluster.mark("admin API accepting", phase)
	if err := cluster.provision(ctx, &phase); err != nil {
		return nil, fmt.Errorf("%w\n%s", err, cluster.Logs())
	}
	cluster.mark("provisioned", phase)
	ok = true
	return cluster, nil
}

func (c *Cluster) awaitAdmin(ctx context.Context) error {
	return waitFor(ctx, 120*time.Second, func() bool {
		_, err := c.Get(ctx, kcp.RootWorkspace, "workspaces")
		return err == nil
	}, "the admin API to accept requests")
}

func borrow(ctx context.Context, kubeconfig string) (*Cluster, error) {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}
	server := os.Getenv(EnvServer)
	if server == "" {
		server = ref.BaseHost(config.Host)
	}
	cluster := &Cluster{
		Root:                  filepath.Dir(kubeconfig),
		Kubeconfig:            kubeconfig,
		Server:                server,
		Config:                config,
		ProviderCluster:       kcp.RootWorkspace + ":" + ProviderWorkspace,
		ConsumerCluster:       kcp.RootWorkspace + ":" + ConsumerWorkspace,
		SecondConsumerCluster: kcp.RootWorkspace + ":" + secondWorkspace,
		borrowed:              true,
	}
	if err := cluster.awaitAdmin(ctx); err != nil {
		return nil, err
	}
	phase := cluster.mark("admin API accepting", time.Now())
	if err := cluster.provision(ctx, &phase); err != nil {
		return nil, err
	}
	cluster.mark("provisioned", phase)
	return cluster, nil
}

func (c *Cluster) provision(ctx context.Context, phase *time.Time) error {
	workspaces := []string{ProviderWorkspace, ConsumerWorkspace, secondWorkspace}
	for _, workspace := range workspaces {
		if _, err := c.Kubectl(ctx, "", workspaceYAML(workspace)); err != nil {
			return err
		}
	}
	if err := c.waitAll(ctx, workspaces, c.waitWorkspace, "workspaces to reach Ready"); err != nil {
		return err
	}
	*phase = c.mark("workspaces ready", *phase)

	for _, manifest := range []string{SchemaYAML, ExportYAML, GadgetSchemaYAML, GadgetExportYAML} {
		if _, err := c.Kubectl(ctx, c.ProviderCluster, manifest); err != nil {
			return err
		}
	}
	if err := c.waitExport(ctx); err != nil {
		return err
	}
	*phase = c.mark("export identity valid", *phase)

	consumers := []string{c.ConsumerCluster, c.SecondConsumerCluster}
	for _, consumer := range consumers {
		for _, binding := range []string{BindingYAML, GadgetBindingYAML} {
			if _, err := c.Kubectl(ctx, consumer, binding); err != nil {
				return err
			}
		}
	}
	if err := c.waitAll(ctx, consumers, c.waitBindingIn, "the api bindings to bind"); err != nil {
		return err
	}
	*phase = c.mark("bindings bound", *phase)
	return nil
}

func (c *Cluster) waitAll(ctx context.Context, subjects []string, wait func(context.Context, string) error, what string) error {
	group, cancel := context.WithCancel(ctx)
	defer cancel()
	failures := make(chan error, len(subjects))
	for _, subject := range subjects {
		go func(subject string) {
			failures <- wait(group, subject)
		}(subject)
	}
	var first error
	for range subjects {
		if err := <-failures; err != nil && first == nil {
			first = err
			cancel()
		}
	}
	if first != nil {
		return fmt.Errorf("livekcp: waiting for %s: %w", what, first)
	}
	return nil
}

func (c *Cluster) Kubectl(ctx context.Context, logicalCluster, manifest string) (string, error) {
	args := []string{"--kubeconfig=" + c.Kubeconfig}
	if logicalCluster != "" {
		args = append(args, "--server="+ref.ClusterURL(c.Server, logicalCluster))
	}
	args = append(args, "apply", "--validate=false", "-f", "-")
	return run(ctx, manifest, Resolve("kubectl"), args...)
}

func (c *Cluster) Get(ctx context.Context, logicalCluster string, args ...string) (string, error) {
	all := append([]string{"--kubeconfig=" + c.Kubeconfig}, "--server="+ref.ClusterURL(c.Server, logicalCluster))
	all = append(all, "get")
	all = append(all, args...)
	return run(ctx, "", Resolve("kubectl"), all...)
}

func (c *Cluster) waitWorkspace(ctx context.Context, name string) error {
	return waitFor(ctx, 120*time.Second, func() bool {
		out, err := c.Get(ctx, kcp.RootWorkspace, "workspace", name, "-o", "jsonpath={.status.phase}")
		return err == nil && strings.TrimSpace(out) == "Ready"
	}, "workspace "+name+" to reach Ready")
}

func (c *Cluster) waitExport(ctx context.Context) error {
	for _, name := range []string{Export, SecondExport} {
		err := waitFor(ctx, 120*time.Second, func() bool {
			out, err := c.Get(ctx, c.ProviderCluster, "apiexport", name, "-o",
				`jsonpath={.status.conditions[?(@.type=="IdentityValid")].status}`)
			return err == nil && strings.TrimSpace(out) == "True"
		}, "apiexport "+name+" to report IdentityValid")
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Cluster) waitBindingIn(ctx context.Context, consumer string) error {
	for _, name := range []string{Export, SecondExport} {
		err := waitFor(ctx, 120*time.Second, func() bool {
			out, err := c.Get(ctx, consumer, "apibinding", name, "-o", "jsonpath={.status.phase}")
			return err == nil && strings.TrimSpace(out) == "Bound"
		}, "the "+name+" apibinding in "+consumer+" to bind")
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Cluster) spawn(logPath string, name string, args ...string) (*exec.Cmd, *os.File, error) {
	log, err := os.Create(logPath)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(name, args...)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		return nil, nil, fmt.Errorf("livekcp: start %s: %w", name, err)
	}
	c.logFiles = append(c.logFiles, log)
	return cmd, log, nil
}

func run(ctx context.Context, stdin string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("livekcp: %s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(errOut.String()))
	}
	return out.String(), nil
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func waitReady(ctx context.Context, server string, timeout time.Duration) error {
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	return waitFor(ctx, timeout, func() bool {
		resp, err := client.Get(server + "/readyz")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, "kcp to answer /readyz")
}

func waitFile(ctx context.Context, path string, timeout time.Duration) error {
	return waitFor(ctx, timeout, func() bool {
		_, err := os.Stat(path)
		return err == nil
	}, path+" to be written")
}

func waitFor(ctx context.Context, timeout time.Duration, condition func() bool, what string) error {
	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("livekcp: timed out waiting for %s", what)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
