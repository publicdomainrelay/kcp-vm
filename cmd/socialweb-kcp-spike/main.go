package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"k8s.io/client-go/rest"
	cliflag "k8s.io/component-base/cli/flag"
	logsapiv1 "k8s.io/component-base/logs/api/v1"

	"github.com/kcp-dev/embeddedetcd"
	"github.com/kcp-dev/kcp/cmd/kcp/options"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/server"
)

type instance struct {
	name       string
	addr       string
	cancel     context.CancelFunc
	etcdCancel context.CancelFunc
	done       chan error
	started    time.Time
}

func startKcp(parent context.Context, name, rootDir string, args []string) (*instance, error) {
	inst := &instance{name: name, done: make(chan error, 1), started: time.Now()}
	opts := options.NewOptions(rootDir)
	opts.Server.GenericControlPlane.Logs.Verbosity = logsapiv1.VerbosityLevel(2)
	fss := cliflag.NamedFlagSets{}
	opts.AddFlags(&fss)
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	for _, f := range fss.FlagSets {
		fs.AddFlagSet(f)
	}
	if err := fs.Parse(append([]string{"--root-directory=" + rootDir}, args...)); err != nil {
		return nil, fmt.Errorf("flags: %w", err)
	}
	if err := logsapiv1.ValidateAndApply(opts.Server.GenericControlPlane.Logs, kcpfeatures.DefaultFeatureGate); err != nil {
		fmt.Fprintf(os.Stderr, "SPIKE %s logs ValidateAndApply: %v\n", name, err)
	}
	ctx, cancel := context.WithCancel(parent)
	inst.cancel = cancel
	completed, err := opts.Complete(ctx)
	if err != nil {
		return nil, fmt.Errorf("complete: %w", err)
	}
	if errs := completed.Validate(); len(errs) > 0 {
		return nil, fmt.Errorf("validate: %v", errs)
	}
	if l := completed.Server.GenericControlPlane.SecureServing.Listener; l != nil {
		inst.addr = l.Addr().String()
	} else {
		inst.addr = net.JoinHostPort(completed.Server.GenericControlPlane.SecureServing.BindAddress.String(),
			fmt.Sprint(completed.Server.GenericControlPlane.SecureServing.BindPort))
	}
	fmt.Fprintf(os.Stderr, "SPIKE %s listener %s after %s\n", name, inst.addr, time.Since(inst.started))
	serverConfig, err := server.NewConfig(ctx, completed.Server)
	if err != nil {
		return nil, fmt.Errorf("newconfig: %w", err)
	}
	completedConfig, err := serverConfig.Complete()
	if err != nil {
		return nil, fmt.Errorf("config complete: %w", err)
	}
	etcdCtx, etcdCancel := context.WithCancel(context.Background())
	inst.etcdCancel = etcdCancel
	if completedConfig.EmbeddedEtcd.Config != nil {
		if err := embeddedetcd.NewServer(completedConfig.EmbeddedEtcd).Run(etcdCtx); err != nil {
			return nil, fmt.Errorf("embedded etcd: %w", err)
		}
	}
	s, err := server.NewServer(completedConfig)
	if err != nil {
		return nil, fmt.Errorf("newserver: %w", err)
	}
	go func() { inst.done <- s.Run(ctx) }()
	return inst, nil
}

func waitReady(inst *instance, limit time.Duration) (time.Duration, error) {
	c := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		select {
		case err := <-inst.done:
			return 0, fmt.Errorf("run returned before ready: %v", err)
		default:
		}
		resp, err := c.Get("https://" + inst.addr + "/readyz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return time.Since(inst.started), nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0, errors.New("not ready in time")
}

func stop(inst *instance, limit time.Duration) (time.Duration, error) {
	t := time.Now()
	inst.cancel()
	select {
	case err := <-inst.done:
		inst.etcdCancel()
		return time.Since(t), err
	case <-time.After(limit):
		inst.etcdCancel()
		return time.Since(t), errors.New("run did not return within bound")
	}
}

func status(field string) string {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "-"
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, field+":") {
			return strings.TrimSpace(strings.TrimPrefix(l, field+":"))
		}
	}
	return "-"
}

func portFree(addr string) bool {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	l.Close()
	return true
}

func main() {
	root := flag.String("root", "", "root directory; instance i uses <root>/kcp-<i>")
	etcd := flag.String("etcd-servers", "", "comma list, one per instance; empty = embedded etcd")
	n := flag.Int("instances", 1, "kcps to start in this process")
	etcdBase := flag.Int("embedded-etcd-base-port", 0, "embedded etcd: client port = base+2i, peer = base+2i+1; 0 = kcp defaults")
	restart := flag.Bool("restart", false, "after stop, start instance 0 again in the same process")
	settle := flag.Duration("settle", 30*time.Second, "wait after ready before RSS and stop")
	flag.Parse()
	rest.SetDefaultWarningHandler(rest.NoWarnings{})
	ctx := context.Background()
	stores := strings.Split(*etcd, ",")
	argsFor := func(i int) []string {
		a := []string{"--bind-address=127.0.0.1", "--secure-port=0", "--experimental-bind-free-port", "--feature-gates=WorkspaceMounts=true"}
		if *etcd != "" {
			a = append(a, "--etcd-servers="+stores[i%len(stores)])
		} else if *etcdBase != 0 {
			a = append(a, fmt.Sprintf("--embedded-etcd-client-port=%d", *etcdBase+2*i), fmt.Sprintf("--embedded-etcd-peer-port=%d", *etcdBase+2*i+1))
		}
		return a
	}
	var insts []*instance
	for i := 0; i < *n; i++ {
		name := fmt.Sprintf("kcp-%d", i)
		inst, err := startKcp(ctx, name, filepath.Join(*root, name), argsFor(i))
		if err != nil {
			fmt.Printf("RESULT %s start error: %v\n", name, err)
			os.Exit(2)
		}
		d, err := waitReady(inst, 5*time.Minute)
		fmt.Printf("RESULT %s addr=%s ready=%s err=%v rss=%s goroutines=%d\n", name, inst.addr, d.Round(time.Millisecond), err, status("VmRSS"), runtime.NumGoroutine())
		if err != nil {
			os.Exit(3)
		}
		insts = append(insts, inst)
	}
	time.Sleep(*settle)
	fmt.Printf("RESULT settled rss=%s hwm=%s goroutines=%d\n", status("VmRSS"), status("VmHWM"), runtime.NumGoroutine())
	for _, inst := range insts {
		d, err := stop(inst, 60*time.Second)
		time.Sleep(500 * time.Millisecond)
		etcdFree := "n/a"
		if *etcd == "" {
			etcdFree = fmt.Sprint(portFree(fmt.Sprintf("127.0.0.1:%d", 2379)))
			if *etcdBase != 0 {
				etcdFree = fmt.Sprint(portFree(fmt.Sprintf("127.0.0.1:%d", *etcdBase)))
			}
		}
		fmt.Printf("RESULT %s stop=%s err=%v secureFree=%v etcdClientFree=%s goroutines=%d rss=%s\n", inst.name, d.Round(time.Millisecond), err, portFree(inst.addr), etcdFree, runtime.NumGoroutine(), status("VmRSS"))
	}
	if *restart {
		time.Sleep(2 * time.Second)
		inst, err := startKcp(ctx, "kcp-0-again", filepath.Join(*root, "kcp-0"), argsFor(0))
		if err != nil {
			fmt.Printf("RESULT restart start error: %v\n", err)
			os.Exit(4)
		}
		d, err := waitReady(inst, 5*time.Minute)
		fmt.Printf("RESULT restart addr=%s ready=%s err=%v rss=%s goroutines=%d\n", inst.addr, d.Round(time.Millisecond), err, status("VmRSS"), runtime.NumGoroutine())
		sd, serr := stop(inst, 60*time.Second)
		fmt.Printf("RESULT restart stop=%s err=%v\n", sd.Round(time.Millisecond), serr)
	}
}
