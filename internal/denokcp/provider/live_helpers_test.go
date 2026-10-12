//go:build linux

package provider_test

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

// installProviderAPIs applies the schemas, the exports and the workspace types
// every live harness in this package needs.
//
// ponytail: the schemas are globbed rather than listed. An APIExport names the
// schema it serves, so a kind added under deploy/ and forgotten in a list here
// binds nothing, and the symptom is every test in this package timing out on
// "tenant API bound" rather than naming the kind nobody installed.
func installProviderAPIs(t *testing.T, kubeconfig, providerCluster, rootCluster, deployDir string) {
	t.Helper()
	schemas, err := filepath.Glob(filepath.Join(deployDir, "*-apiresourceschema.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(schemas) == 0 {
		t.Fatalf("no APIResourceSchemas under %s", deployDir)
	}
	sort.Strings(schemas)
	for _, path := range schemas {
		kubectlApply(t, kubeconfig, providerCluster, readFile(t, path))
	}
	for _, name := range []string{
		"policyworkflowrun-apiexport.yaml",
		"denoruntime-apiexport.yaml",
	} {
		kubectlApply(t, kubeconfig, providerCluster, readFile(t, filepath.Join(deployDir, name)))
	}
	for _, name := range []string{
		"workspacetype-workflow.yaml",
		"workspacetype-denoruntime.yaml",
	} {
		kubectlApply(t, kubeconfig, rootCluster, readFile(t, filepath.Join(deployDir, name)))
	}
}

func requireBinary(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		livegate.Require(t, "required binary %q not found: %v", name, err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func startProcess(t *testing.T, logPath, bin string, args ...string) {
	t.Helper()
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", bin, err)
	}
	trackLiveProcess(t, &liveProcess{pid: cmd.Process.Pid, cmd: cmd, log: log})
}

func waitFor(t *testing.T, what string, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitForWith(t *testing.T, what string, timeout, every time.Duration, ok func() bool, dump func()) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := time.Now()
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		if dump != nil && time.Since(last) >= every {
			dump()
			last = time.Now()
		}
		time.Sleep(300 * time.Millisecond)
	}
	if dump != nil {
		dump()
	}
	t.Fatalf("timed out waiting for %s after %s", what, timeout)
}

func liveWait(key string, fallback time.Duration) time.Duration {
	return liveEnvDuration(key, fallback)
}

func tcpOpen(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func httpOK(url string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 400
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func kubectl(t *testing.T, kubeconfig, server string, args ...string) string {
	t.Helper()
	out, err := kubectlTry(t, kubeconfig, server, args...)
	if err != nil {
		t.Fatalf("kubectl %v: %v\n%s", args, err, out)
	}
	return out
}

func kubectlTry(t *testing.T, kubeconfig, server string, args ...string) (string, error) {
	t.Helper()
	full := []string{"--kubeconfig", kubeconfig, "--server", server}
	full = append(full, args...)
	out, err := exec.Command("kubectl", full...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func kubectlApply(t *testing.T, kubeconfig, server, manifest string) {
	t.Helper()
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "--server", server, "apply", "--validate=false", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kubectl apply: %v\n%s", err, out)
	}
}

func caData(t *testing.T, data []byte, file string) []byte {
	t.Helper()
	if len(data) > 0 {
		return data
	}
	if file == "" {
		return nil
	}
	body, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		t.Fatal(err)
	}
	return body
}
