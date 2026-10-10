package test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	bidderTemplateName = "socialweb-computer-bidder@.service"
	bidderLauncherName = "socialweb-computer-bidder-run"
	bidderInstance     = "bobvmbuilder.bsky.social"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	return filepath.Dir(filepath.Dir(file))
}

func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(append([]string{repoRoot(t)}, parts...)...))
	if err != nil {
		t.Fatalf("reading %s: %v", filepath.Join(parts...), err)
	}
	return string(raw)
}

func unitDirectives(unit string) map[string][]string {
	out := map[string][]string{}
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[key] = append(out[key], value)
	}
	return out
}

func TestTheBidderUnitIsATemplateKeyedByTheAccountHandle(t *testing.T) {
	unit := readRepoFile(t, "deploy", "bidder", bidderTemplateName)
	got := unitDirectives(unit)

	want := map[string]string{
		"StateDirectory":   "socialweb-computer-bidder/%i",
		"RuntimeDirectory": "socialweb-computer-bidder/%i",
		"ExecStart":        "/usr/local/libexec/socialweb-computer-bidder-run %i",
		"Restart":          "always",
		"Requires":         "docker.service",
		"NoNewPrivileges":  "yes",
		"ProtectSystem":    "strict",
		"ProtectHome":      "yes",
		"PrivateTmp":       "yes",
		"WantedBy":         "multi-user.target",
	}
	for key, value := range want {
		values := got[key]
		if len(values) != 1 || values[0] != value {
			t.Errorf("%s is %q, want exactly %q", key, values, value)
		}
	}

	envFiles := strings.Join(got["EnvironmentFile"], "\n")
	if envFiles != "/etc/socialweb-computer-bidder/common.env\n-/etc/socialweb-computer-bidder/%i.env" {
		t.Errorf("EnvironmentFile is %q: the host's shared settings must come first and the "+
			"instance's optional file second, so an instance overrides the host and a missing "+
			"instance file is not an error", got["EnvironmentFile"])
	}

	if strings.Contains(unit, "%I") {
		t.Error("the unit uses %I, which unescapes the instance: systemd reads '-' in an " +
			"instance name as an escaped '/', so a handle with a hyphen would name a different " +
			"directory than the one the operator typed. Every use must be %i")
	}

	users := got["User"]
	if len(users) != 1 || users[0] == "" || users[0] == "root" {
		t.Errorf("User is %q: the bidder must run as its own unprivileged account", users)
	}
	if !strings.Contains(strings.Join(got["SupplementaryGroups"], " "), "docker") {
		t.Error("SupplementaryGroups does not name docker, and docker is how the provider starts " +
			"every guest's container")
	}
	if len(got["RestartSec"]) != 1 {
		t.Error("RestartSec is not set: an expired session exits 1 and expects to be restarted " +
			"into a fresh QR sign-in, and an unset delay restarts into the start limit")
	}
	if len(got["ReadWritePaths"]) == 0 {
		t.Error("ReadWritePaths is not set: under ProtectSystem=strict the shared module cache " +
			"and image store are read-only and the first start fails")
	}
}

func TestNoAccountSecretReachesTheBidder(t *testing.T) {
	files := map[string]string{
		"the unit":          readRepoFile(t, "deploy", "bidder", bidderTemplateName),
		"the launcher":      readRepoFile(t, "deploy", "bidder", bidderLauncherName),
		"the deploy script": readRepoFile(t, "scripts", "deploy-firecracker-bidder.sh"),
	}
	for name, body := range files {
		for offset, b := range []byte(body) {
			if b > 0x7F {
				t.Errorf("%s: non-ASCII byte 0x%02X at offset %d", name, b, offset)
				break
			}
		}
		lower := strings.ToLower(body)
		lower = strings.ReplaceAll(lower, "id_ed25519_no_password", "")
		if strings.Contains(lower, "password") {
			t.Errorf("%s mentions a password: the account signs in by QR and its session lives "+
				"in the instance's own state directory, so no secret of the account is ever "+
				"written by a deploy", name)
		}
		if strings.Contains(lower, "atproto_handle") || strings.Contains(lower, "--atproto-handle") {
			t.Errorf("%s names a fixed account handle: the account is the unit's instance name", name)
		}
	}
}

func TestTheBidderDeployInstallsTheTemplate(t *testing.T) {
	script := readRepoFile(t, "scripts", "deploy-firecracker-bidder.sh")
	for _, needle := range []string{
		"deploy/bidder/" + bidderTemplateName,
		"deploy/bidder/" + bidderLauncherName,
		"/usr/local/libexec/" + bidderLauncherName,
		"/etc/socialweb-computer-bidder/common.env",
		"--dry-run",
		"--local",
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("the deploy script does not mention %q", needle)
		}
	}
	if strings.Contains(script, "bidder.env") {
		t.Error("the deploy script still writes the fixed account's bidder.env")
	}
	for _, path := range []string{
		filepath.Join(repoRoot(t), "scripts", "deploy-firecracker-bidder.sh"),
		filepath.Join(repoRoot(t), "deploy", "bidder", bidderLauncherName),
	} {
		if out, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
			t.Errorf("bash -n %s: %v\n%s", path, err, out)
		}
	}
}

type launcherRun struct {
	state    string
	argvFile string
	session  string
	output   string
	code     int
	elapsed  time.Duration
}

func runLauncher(t *testing.T, instance string, lines string, linger string) launcherRun {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(filepath.Join(state, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(state, "secrets", "oauth-session.json")
	if err := os.WriteFile(session, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	argvFile := filepath.Join(dir, "argv")
	linesFile := filepath.Join(dir, "lines")
	if err := os.WriteFile(linesFile, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "fake-deno")
	body := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > " + argvFile +
		"\ncat " + linesFile + "\nexec sleep " + linger + "\n"
	if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(repoRoot(t), "deploy", "bidder", bidderLauncherName), instance)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"STATE_DIRECTORY=" + state,
		"BIDDER_DENO=" + fake,
		"BIDDER_CODE_DIR=/opt/example/org-root",
		"BIDDER_HOST_DIR=/var/lib/example/host",
		"BIDDER_SERVE_ADDR=127.0.0.1",
		"BIDDER_SERVE_PORT=0",
		"BIDDER_INGRESS_PROXY_HOST=xrpc.example",
		"BIDDER_PLC_DIRECTORY_URL=https://plc.example",
		"BIDDER_POLICY=open",
		`BIDDER_POLICY_ARGS={"bidWindowSec":8}`,
		"BIDDER_FC_NODEIMAGE=/var/lib/example/host/bin/socialweb-nodeimage",
		"BIDDER_FC_NODEBOOT=/usr/local/bin/socialweb-nodeboot",
		"BIDDER_FC_CONFIG=/var/lib/example/host/nodeimage-firecracker.json",
		"BIDDER_FC_REPO_DIR=/var/lib/example/host",
		"BIDDER_FC_PREINSTALL=/var/lib/example/host/image-inputs/preinstall.json",
		"BIDDER_FC_VMM=/usr/local/bin/firecracker",
		"BIDDER_FC_RUNNER_IMAGE=localhost/runner:local",
		"BIDDER_FC_RANGE_BASE=172.30.0.0",
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	start := time.Now()
	err := cmd.Run()
	run := launcherRun{state: state, argvFile: argvFile, session: session, output: out.String(), elapsed: time.Since(start)}
	if ctx.Err() != nil {
		t.Fatalf("the launcher did not return within 20 s; output:\n%s", run.output)
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		run.code = exitErr.ExitCode()
	default:
		t.Fatalf("running the launcher: %v", err)
	}
	return run
}

func sessionLine(event, handle string) string {
	line := `{"ts":"2026-10-07T00:00:00Z","level":"info","message":"` + event + `","prefix":"bidder","userDid":"did:plc:example"`
	if handle != "" {
		line += `,"handle":"` + handle + `"`
	}
	return line + "}\n"
}

const readyLine = `{"event":"bidder_ready","did":"did:plc:bidder","servePort":1}` + "\n"

func TestTheLauncherSignsInByQRIntoItsOwnStateDirectory(t *testing.T) {
	run := runLauncher(t, bidderInstance, sessionLine("oauth_qr_session_restored", bidderInstance)+readyLine, "0")
	if run.code != 0 {
		t.Fatalf("the launcher exited %d for the instance's own account; output:\n%s", run.code, run.output)
	}
	if !strings.Contains(run.output, `"event":"bidder_ready"`) {
		t.Errorf("the bidder's output did not reach the journal through the launcher:\n%s", run.output)
	}
	raw, err := os.ReadFile(run.argvFile)
	if err != nil {
		t.Fatalf("the fake bidder never ran: %v", err)
	}
	argv := strings.Split(strings.TrimSpace(string(raw)), "\n")
	flag := func(name string) (string, bool) {
		for i, a := range argv {
			if a == name {
				if i+1 < len(argv) {
					return argv[i+1], true
				}
				return "", true
			}
		}
		return "", false
	}
	for name, want := range map[string]string{
		"--oauth-session-file":                     filepath.Join(run.state, "secrets", "oauth-session.json"),
		"--private-key-hex-path":                   filepath.Join(run.state, "secrets", "bidder-key.hex"),
		"--pds-state-path":                         filepath.Join(run.state, "pds"),
		"--compute-provider-firecracker-work-root": filepath.Join(run.state, "guests"),
		"--policy-args":                            `{"bidWindowSec":8}`,
	} {
		if got, ok := flag(name); !ok || got != want {
			t.Errorf("%s is %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"--atproto-oauth-qr", "--compute-provider-firecracker", "--compute-provider-firecracker-reuse-image"} {
		if _, ok := flag(name); !ok {
			t.Errorf("the bidder was not given %s", name)
		}
	}
	for _, a := range argv {
		if a == "--atproto-handle" || strings.Contains(strings.ToLower(a), "password") {
			t.Errorf("the bidder was given %q", a)
		}
	}
	if _, err := os.Stat(run.session); err != nil {
		t.Errorf("the session of the instance's own account was removed: %v", err)
	}
	info, err := os.Stat(filepath.Join(run.state, "secrets"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the secrets directory is %v (%v), want 0700: the session file is written with "+
			"the process umask and the state directory must be traversable by the guest's container", info, err)
	}
}

func TestTheLauncherRefusesAnAccountOtherThanItsInstance(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines string
	}{
		{"restored", sessionLine("oauth_qr_session_restored", "someone-else.bsky.social") + readyLine},
		{"scanned", sessionLine("oauth_qr_session_ready", "someone-else.bsky.social") + readyLine},
		{"unnamed", sessionLine("oauth_qr_session_restored", "") + readyLine},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := runLauncher(t, bidderInstance, tc.lines, "30")
			if run.code == 0 {
				t.Fatalf("the launcher accepted a sign-in that is not %s; output:\n%s", bidderInstance, run.output)
			}
			if run.elapsed > 10*time.Second {
				t.Errorf("the launcher took %v to refuse, so it waited for the bidder instead of stopping it", run.elapsed)
			}
			if _, err := os.Stat(run.session); !os.IsNotExist(err) {
				t.Errorf("the refused account's session is still on disk (%v), so the restart would "+
					"restore the same account instead of showing a fresh QR", err)
			}
		})
	}
}

func TestTheLauncherAcceptsTheHandleInAnyCase(t *testing.T) {
	run := runLauncher(t, bidderInstance, sessionLine("oauth_qr_session_ready", "BobVMBuilder.bsky.social")+readyLine, "0")
	if run.code != 0 {
		t.Fatalf("a handle differing only in case was refused (exit %d); handles are case-insensitive:\n%s", run.code, run.output)
	}
}

func TestTheLauncherRefusesAnInstanceThatIsNotAHandle(t *testing.T) {
	for _, instance := range []string{"", "host", "Bob.bsky.social", "../etc", "bob.bsky.social/x"} {
		run := runLauncher(t, instance, readyLine, "0")
		if run.code != 78 {
			t.Errorf("instance %q: exit %d, want 78 (a configuration error systemd does not restart); output:\n%s",
				instance, run.code, run.output)
		}
		if _, err := os.Stat(run.argvFile); err == nil {
			t.Errorf("instance %q: the bidder was started", instance)
		}
	}
}

func TestTheBidderUnitVerifies(t *testing.T) {
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze is not installed here")
	}
	dir := t.TempDir()
	root := repoRoot(t)
	unit := readRepoFile(t, "deploy", "bidder", bidderTemplateName)
	launcher := filepath.Join(root, "deploy", "bidder", bidderLauncherName)
	unit = strings.ReplaceAll(unit, "/usr/local/libexec/"+bidderLauncherName, launcher)
	if err := os.WriteFile(filepath.Join(dir, bidderTemplateName), []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	instance := filepath.Join(dir, "socialweb-computer-bidder@"+bidderInstance+".service")
	if err := os.Symlink(bidderTemplateName, instance); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(analyze, "verify", "--man=no", instance).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("systemd-analyze verify %s: %v; it reports an unparsable or unknown directive as "+
			"a warning and ignores it, so any output at all is a directive the unit does not have:\n%s",
			instance, err, out)
	}
}
