package test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
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

func envWith(overrides map[string]string) []string {
	current := map[string]string{}
	for _, entry := range os.Environ() {
		if key, value, ok := strings.Cut(entry, "="); ok {
			current[key] = value
		}
	}
	for key, value := range overrides {
		current[key] = value
	}
	out := make([]string, 0, len(current))
	for key, value := range current {
		out = append(out, key+"="+value)
	}
	sort.Strings(out)
	return out
}

func writeFileIn(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func runBidderDeployDryRun(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	home := t.TempDir()
	deno := writeFileIn(t, home, "deno", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(deno, 0o755); err != nil {
		t.Fatal(err)
	}
	inputs := filepath.Join(home, "inputs")
	if err := os.MkdirAll(filepath.Join(inputs, "modules", "kernel/drivers/virtio"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileIn(t, filepath.Join(inputs, "modules"), "modules.dep", "kernel/drivers/virtio/virtio_mmio.ko:\n")
	writeFileIn(t, filepath.Join(inputs, "modules"), "modules.builtin", "")
	writeFileIn(t, filepath.Join(inputs, "modules"), "modules.alias", "")
	writeFileIn(t, filepath.Join(inputs, "modules", "kernel/drivers/virtio"), "virtio_mmio.ko", "the firecracker transport\n")
	kernel := writeFileIn(t, inputs, "vmlinuz", "this host's kernel\n")
	kernelConfig := writeFileIn(t, inputs, "config", "CONFIG_NETFILTER_XT_TARGET_NFLOG=m\n")
	base := writeFileIn(t, inputs, "base.squashfs", "the base userspace\n")
	k3s := writeFileIn(t, inputs, "k3s", "k3s version v1.31.1+k3s1\n")
	initBinary := writeFileIn(t, inputs, "kcpinit", "#!/bin/sh\nexec /bin/sh\n")
	preinstall := writeFileIn(t, inputs, "preinstall.json", `{"packages":["cloud-init"]}`+"\n")

	config := filepath.Join(home, "nodeimage-market.json")
	body, err := json.Marshal(map[string]any{
		"firecracker":       "/usr/bin/firecracker",
		"kernel_source":     kernel,
		"kernel_config":     kernelConfig,
		"module_source":     filepath.Join(inputs, "modules"),
		"base_image":        base,
		"stage_dir":         filepath.Join(home, "stage"),
		"out_dir":           filepath.Join(home, "store"),
		"init_package":      filepath.Join(root, "cmd", "socialweb-nodeinit"),
		"init_binary":       initBinary,
		"k3s_binary":        k3s,
		"k3s_data_dir":      "/var/lib/rancher/k3s",
		"image_cache_dir":   filepath.Join(home, "cache"),
		"boot_args":         []string{"console=ttyS0", "root=/dev/vda"},
		"modules":           []string{"nf_conntrack"},
		"initramfs_modules": []string{"kernel/drivers/virtio/virtio_mmio"},
		"images":            []string{"rancher/mirrored-pause:3.10.2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash",
		filepath.Join(root, "scripts", "deploy-firecracker-bidder.sh"), "--dry-run")
	cmd.Dir = filepath.Join(home, "run")
	if err := os.MkdirAll(cmd.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd.Env = envWith(map[string]string{
		"LOCAL_DENO":          deno,
		"LOCAL_CONFIG":        config,
		"LOCAL_PREINSTALL":    preinstall,
		"LOCAL_INIT_BINARY":   initBinary,
		"LOCAL_INIT_PACKAGE":  filepath.Join(root, "cmd", "socialweb-nodeinit"),
		"LOCAL_NODEIMAGE_BIN": filepath.Join(home, "socialweb-nodeimage"),
	})
	out := &bytes.Buffer{}
	cmd.Stdout = out
	cmd.Stderr = out
	runErr := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("the deploy script did not return within 5 minutes; output:\n%s", out.String())
	}
	text := out.String()
	if runErr != nil {
		t.Fatalf("deploy-firecracker-bidder.sh --dry-run exited with %v. It previews the steps it "+
			"would take without taking them, so a preview that fails is a preview of nothing:\n%s",
			runErr, text)
	}
	if !strings.Contains(text, "checking the build host") {
		t.Fatalf("the deploy script printed nothing that says it reached its own preflight, so this "+
			"test is reading the output of something else:\n%s", text)
	}
	return text
}

type launcherRun struct {
	state      string
	argvFile   string
	session    string
	output     string
	code       int
	elapsed    time.Duration
	keyAtStart string
}

func runLauncher(t *testing.T, instance string, lines string, linger string) launcherRun {
	t.Helper()
	state := filepath.Join(t.TempDir(), "state")
	return runLauncherIn(t, state, instance, lines, linger)
}

func runLauncherIn(t *testing.T, state, instance string, lines string, linger string) launcherRun {
	t.Helper()
	dir := t.TempDir()
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
	keyAtStart := filepath.Join(dir, "key-at-start")
	body := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > " + argvFile +
		"\ncat \"$STATE_DIRECTORY/secrets/bidder-key.hex\" > " + keyAtStart + " 2>/dev/null" +
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
		"BIDDER_CODE_DIR=/opt/example/socialweb-computer-kcp",
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
	run := launcherRun{state: state, argvFile: argvFile, session: session, output: out.String(), elapsed: time.Since(start), keyAtStart: keyAtStart}
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

func TestTheBidderDeployShipsTheImageTheInputsNameAndBuildsItUnderSudo(t *testing.T) {
	out := runBidderDeployDryRun(t)

	var asked, built, handed []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "--- [dry-run] on the build host") {
			continue
		}
		if strings.Contains(line, "-reuse-stale") {
			t.Errorf("the build host's own steps still carry -reuse-stale: %s\n\nThat switch answers "+
				"with the newest whole image in the store whatever its inputs, which is how an image "+
				"built with no preinstall (its image.json carries \"preinstall\": \"\", its rootfs "+
				"has no cloud-init) was shipped while the inputs named a different name -- and every "+
				"market guest then booted with no cloud-init and the contract timed out. -reuse-stale "+
				"stays on the TARGET, where the build host's busybox is an input and the two hosts "+
				"can never agree on a name; the build host has to name the exact image.", line)
		}
		switch {
		case strings.Contains(line, "-present-only"):
			asked = append(asked, line)
		case strings.Contains(line, "GOTOOLCHAIN=local"):
			built = append(built, line)
		case strings.Contains(line, "chown -R"):
			handed = append(handed, line)
		}
	}

	if len(asked) == 0 {
		t.Fatalf("the preview never runs socialweb-nodeimage on the build host, so it does not say "+
			"which image it would ship:\n%s", out)
	}
	for _, line := range asked {
		if !strings.Contains(line, "-present-only") {
			t.Errorf("the build host's ask is not -present-only: %s\n\n-ensure alone answers the "+
				"question by BUILDING, and on a host whose user cannot mount that build gets as far "+
				"as the preinstall chroot -- after the base image and every seeded image have been "+
				"unpacked into the stage -- before it fails, so the ask has to be one that never "+
				"builds", line)
		}
	}

	if len(built) == 0 {
		t.Fatalf("the preview never builds the image through sudo, so an image the store does not "+
			"hold has no way to reach it:\n%s", out)
	}
	for _, line := range built {
		if !strings.Contains(line, "sudo -n env -u TMPDIR") {
			t.Errorf("the build under sudo is not run with TMPDIR unset: %s\n\nThe preinstall chroot "+
				"inherits TMPDIR from this user, and dpkg's ca-certificates configure step then fails "+
				"with \"mktemp: failed to create file via template '<TMPDIR>/ca-certificates.tmp.XXXXXX'\" "+
				"-- a build that dies there leaves no image and no name", line)
		}
		if !strings.Contains(line, "toolchain@v0.0.1-go") || !strings.Contains(line, "GOTOOLCHAIN=local") {
			t.Errorf("the build under sudo does not run on the toolchain the go.mod line names: %s\n\n"+
				"root's own go cannot select the version go.mod asks for, because the downloaded "+
				"toolchain lives in the invoking user's module cache under a different HOME", line)
		}
	}
	if len(handed) == 0 {
		t.Errorf("the preview never hands ownership of the new image and the stage back to this "+
			"user. A build under sudo writes them root-owned and mode 0700, so the next run as this "+
			"user cannot read image.json and a later -gc cannot reap them:\n%s", out)
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

func TestTheBidderStartsWithTheSameKeyEveryTimeBeforeASignIn(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	first := runLauncherIn(t, state, bidderInstance, "", "0")
	keyPath := filepath.Join(state, "secrets", "bidder-key.hex")
	seen, err := os.ReadFile(first.keyAtStart)
	if err != nil || len(strings.TrimSpace(string(seen))) == 0 {
		t.Fatalf("the bidder started with no key at %s (%v): it then mints a new did:plc on every "+
			"restart while the QR waits, and a QR that times out every five minutes registers a new "+
			"identity at the PLC directory every five minutes", keyPath, err)
	}
	key := strings.TrimSpace(string(seen))
	if ok, _ := regexp.MatchString(`^[0-9a-f]{64}$`, key); !ok {
		t.Fatalf("the key the launcher wrote is %q, want 64 lowercase hex characters", key)
	}
	info, err := os.Stat(keyPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the key file is %v (%v), want mode 0600", info, err)
	}
	second := runLauncherIn(t, state, bidderInstance, "", "0")
	again, err := os.ReadFile(second.keyAtStart)
	if err != nil || strings.TrimSpace(string(again)) != key {
		t.Fatalf("the second start saw key %q, want the first start's %q: a restart must keep the "+
			"bidder's identity", strings.TrimSpace(string(again)), key)
	}
}

func TestTheLauncherRunsTheBidderWithoutAV8CodeCache(t *testing.T) {
	run := runLauncher(t, bidderInstance, sessionLine("oauth_qr_session_restored", bidderInstance)+readyLine, "0")
	if run.code != 0 {
		t.Fatalf("the launcher exited %d; output:\n%s", run.code, run.output)
	}
	raw, err := os.ReadFile(run.argvFile)
	if err != nil {
		t.Fatalf("the fake bidder never ran: %v", err)
	}
	argv := strings.Split(strings.TrimSpace(string(raw)), "\n")
	script := -1
	cache := -1
	for i, a := range argv {
		if strings.HasSuffix(a, "/hono-bidder/mod.ts") && script < 0 {
			script = i
		}
		if a == "--no-code-cache" && cache < 0 {
			cache = i
		}
	}
	if cache < 0 || script < 0 || cache > script {
		t.Fatalf("argv %q: --no-code-cache must be a deno flag, before the bidder's module. Measured "+
			"2026-10-07 on hat-8: a bidder restarted onto new code still wrote the old provider's "+
			"bid_config and booted the old image until its DENO_DIR's v8_code_cache_v2 was moved aside", argv)
	}
}
