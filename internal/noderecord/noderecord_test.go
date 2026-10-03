//go:build linux

package noderecord

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"
)

const helperEnv = "NODERECORD_TEST_HELPER"

const testNodeName = "liveweb-node1"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "" {
		os.Exit(m.Run())
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	os.Exit(0)
}

func testIdentity() []string { return []string{"agent", testNodeName} }

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func spawnWithArgv(t *testing.T, argv []string) int {
	t.Helper()
	if len(argv) == 0 {
		t.Fatal("a stand-in for a node's process needs an argv")
	}
	cmd := exec.Command(os.Args[0])
	cmd.Args = argv
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a stand-in process with argv %v: %v", argv, err)
	}
	go func() { _ = cmd.Wait() }()
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return pid
}

func spawnAgentOf(t *testing.T, nodeName string) int {
	t.Helper()
	return spawnWithArgv(t, []string{os.Args[0], "agent", "--node-name", nodeName})
}

func spawnVMM(t *testing.T, sock string) int {
	t.Helper()
	return spawnWithArgv(t, []string{"firecracker", "--api-sock", sock, "--config-file", "/z.json"})
}

func recordFor(t *testing.T, pid int, identity []string) Record {
	t.Helper()
	start, err := ProcStart(pid)
	if err != nil {
		t.Fatalf("ProcStat(%d): %v", pid, err)
	}
	return Record{PID: pid, StartTime: start, Identity: identity}
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pid %d is still there: a process left running holds whatever the node held and "+
		"keeps naming its node at the cluster's apiserver", pid)
}

func TestTheNodeRecordRoundTrips(t *testing.T) {
	dir := t.TempDir()
	pid := os.Getpid()
	start, err := ProcStart(pid)
	if err != nil {
		t.Fatalf("ProcStat: %v", err)
	}
	want := Record{PID: pid, StartTime: start, Identity: testIdentity()}
	if err := Write(dir, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("record = %+v, want %+v", got, want)
	}
	fi, err := os.Stat(RecordPath(dir))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("the record is mode %o, want 600: it names a pid this gateway will signal, and "+
			"a record another user can write is a record that points our reap at a pid of their "+
			"choosing", perm)
	}
	if start2, err := ProcStart(pid); err != nil || start2 != start {
		t.Fatalf("the test process start time moved from %d to %d: the identity this record is "+
			"built on is not stable", start, start2)
	}
}

func TestARecordWhoseStartTimeDoesNotMatchIsNotOursAndIsNotKilled(t *testing.T) {
	dir := t.TempDir()
	pid := spawnAgentOf(t, testNodeName)
	rec := recordFor(t, pid, testIdentity())
	rec.StartTime++
	if err := Write(dir, rec); err != nil {
		t.Fatalf("Write: %v", err)
	}

	live, err := LivenessOf(dir, testIdentity())
	if err != nil {
		t.Fatalf("LivenessOf: %v", err)
	}
	if live != LivenessNotOurs {
		t.Fatalf("LivenessOf = %v, want not-ours: the pid was reused by a process that is not "+
			"this node's agent, and the start time is the only thing that says so", live)
	}
	if err := Reap(context.Background(), ReapOptions{
		Cluster: "liveweb", NodeName: testNodeName, DataDir: dir, Identity: testIdentity(),
		Grace: 200 * time.Millisecond, Log: testLog(),
	}); err != nil {
		t.Fatalf("Reap over a record that is not ours: %v", err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the process at pid %d was signalled on the strength of a record whose start time "+
			"does not match it: that pid belongs to somebody else, and killing it kills a stranger "+
			"(%v)", pid, err)
	}
	if _, err := os.Stat(RecordPath(dir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("the record that is not ours was left behind: it names one process and is verified " +
			"against another, so a later reap reads the same mismatch for ever")
	}
}

func TestReapStopsARecordedProcessAndClearsTheRecord(t *testing.T) {
	dir := t.TempDir()
	pid := spawnAgentOf(t, testNodeName)
	if err := Write(dir, recordFor(t, pid, testIdentity())); err != nil {
		t.Fatalf("Write: %v", err)
	}

	live, err := LivenessOf(dir, testIdentity())
	if err != nil {
		t.Fatalf("LivenessOf: %v", err)
	}
	if live != LivenessOurs {
		t.Fatalf("LivenessOf = %v, want ours: this is the process this gateway started, so it is "+
			"the one a restarted gateway may reap", live)
	}
	if err := Reap(context.Background(), ReapOptions{
		Cluster: "liveweb", NodeName: testNodeName, DataDir: dir, Identity: testIdentity(),
		Grace: time.Second, Log: testLog(),
	}); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	waitGone(t, pid)
	if _, err := os.Stat(RecordPath(dir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("the record survived a successful reap, so the next gateway reads a pid that is " +
			"not the process that was stopped")
	}
}

func TestReapKillsARecordedNamespaceChildOnlyUnderItsOwnIdentity(t *testing.T) {
	dir := t.TempDir()
	holder := []string{"namespace", filepath.Join(dir, "namespace.json")}
	pid := spawnWithArgv(t, append([]string{os.Args[0]}, holder...))
	if err := Write(dir, recordFor(t, pid, holder)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	live, err := LivenessOf(dir, testIdentity())
	if err != nil {
		t.Fatalf("LivenessOf: %v", err)
	}
	if live != LivenessNotOurs {
		t.Fatalf("LivenessOf = %v, want not-ours: a record that names the namespace child is not "+
			"the VMM, and reading it as ours under the VMM's identity would signal a process this "+
			"gateway never verified", live)
	}
	live, err = LivenessOf(dir, testIdentity(), holder)
	if err != nil {
		t.Fatalf("LivenessOf with both identities: %v", err)
	}
	if live != LivenessOurs {
		t.Fatalf("LivenessOf = %v, want ours: the record is written before the VMM exists, so the "+
			"namespace child's own identity is the only thing that can match it, and a record no "+
			"identity matches is a process nothing ever reaps", live)
	}
	if err := Reap(context.Background(), ReapOptions{
		Cluster: "liveweb", NodeName: testNodeName, DataDir: dir,
		Identity: testIdentity(), HolderIdentity: holder,
		Grace: time.Second, Log: testLog(),
	}); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	waitGone(t, pid)
	if _, err := os.Stat(RecordPath(dir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("the record survived the reap of the namespace child it names")
	}
}

func TestAVMMRecordMatchesItsOwnSocketPath(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "y.sock")
	dir := t.TempDir()
	pid := spawnVMM(t, sock)
	identity := []string{"firecracker", "--api-sock", sock}
	if err := Write(dir, recordFor(t, pid, identity)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	live, err := LivenessOf(dir, identity)
	if err != nil {
		t.Fatalf("LivenessOf: %v", err)
	}
	if live != LivenessOurs {
		t.Fatalf("LivenessOf = %v, want ours: a firecracker VMM's command line carries neither the "+
			"word agent nor a node name, so an identity that is a flag and its per-node value is "+
			"the only thing that can match it -- and a record this gateway wrote and cannot match "+
			"is a node nothing will ever reap", live)
	}
}

func TestAVMMRecordForAnotherSocketIsNotOursAndIsNotReaped(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "y.sock")
	other := filepath.Join(t.TempDir(), "other.sock")
	dir := t.TempDir()
	pid := spawnVMM(t, sock)
	identity := []string{"firecracker", "--api-sock", other}
	if err := Write(dir, recordFor(t, pid, identity)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	live, err := LivenessOf(dir, identity)
	if err != nil {
		t.Fatalf("LivenessOf: %v", err)
	}
	if live != LivenessNotOurs {
		t.Fatalf("LivenessOf = %v, want not-ours: the socket path is what makes the identity an "+
			"identity rather than a bare flag, and this process's socket is %s while the record "+
			"names %s", live, sock, other)
	}
	err = Reap(context.Background(), ReapOptions{
		Cluster: "liveweb", NodeName: testNodeName, DataDir: dir, Identity: identity,
		Grace: 200 * time.Millisecond, Log: testLog(),
	})
	if !errors.Is(err, ErrNotOurs) {
		t.Fatalf("Reap over a record that names another node's socket = %v, want ErrNotOurs. The "+
			"process this record names is RUNNING, so the reap stops here and reports it rather than "+
			"finishing: a reap that answers success while a process it could not verify is still up "+
			"is a reap whose caller goes on to delete the files underneath that process", err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the VMM at pid %d was signalled on the strength of a record that names another "+
			"node's socket: that VMM belongs to another node, and killing it kills another node's "+
			"guest (%v)", pid, err)
	}
	if _, err := os.Stat(RecordPath(dir)); err != nil {
		t.Fatalf("the record was removed even though the process it names is still running: the "+
			"mismatch is what a later pass needs to see, and a cleared record would read as \"nothing "+
			"is running\" (%v)", err)
	}
}

func TestTheLocalIdentityNeedsEveryToken(t *testing.T) {
	dir := t.TempDir()
	pid := spawnAgentOf(t, "otherweb-node9")
	if err := Write(dir, recordFor(t, pid, testIdentity())); err != nil {
		t.Fatalf("Write: %v", err)
	}

	live, err := LivenessOf(dir, testIdentity())
	if err != nil {
		t.Fatalf("LivenessOf: %v", err)
	}
	if live != LivenessNotOurs {
		t.Fatalf("LivenessOf = %v, want not-ours: this process carries the token agent and not the "+
			"token %s, and an identity that matches on the first token alone matches every k3s "+
			"agent on this host", live, testNodeName)
	}
	if Matches([]string{"agent", "--node-name", "otherweb-node9"}, testIdentity()) {
		t.Fatal("Matches accepted a command line that carries agent but names another node")
	}
}

func TestWriteRefusesARecordWithNoIdentityAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		rec  Record
	}{
		{"a nil identity", Record{PID: os.Getpid(), StartTime: 1}},
		{"an empty identity", Record{PID: os.Getpid(), StartTime: 1, Identity: []string{}}},
	} {
		if err := Write(dir, tc.rec); !errors.Is(err, ErrNoIdentity) {
			t.Fatalf("%s: Write = %v, want ErrNoIdentity -- a record with no identity can never be "+
				"matched to a process, so it is a record that can never be reaped and the process "+
				"it names keeps running for ever", tc.name, err)
		}
		if _, err := os.Stat(RecordPath(dir)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s: the refused record was written to disk anyway", tc.name)
		}
		if _, err := os.Stat(RecordPath(dir) + ".tmp"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s: the refused record was left behind as %s.tmp", tc.name, RecordPath(dir))
		}
	}
}

func TestARecordPredatingTheIdentityFieldIsNotOursAndTheRecordStays(t *testing.T) {
	dir := t.TempDir()
	pid := spawnAgentOf(t, testNodeName)
	start, err := ProcStart(pid)
	if err != nil {
		t.Fatalf("ProcStat: %v", err)
	}
	legacy := fmt.Sprintf("{\"pid\":%d,\"startTime\":%d,\"nodeName\":%q}\n", pid, start, testNodeName)
	if err := os.WriteFile(RecordPath(dir), []byte(legacy), 0o600); err != nil {
		t.Fatalf("writing a record in the shape that predates the identity field: %v", err)
	}
	if rec, err := Read(dir); err != nil {
		t.Fatalf("Read: %v", err)
	} else if len(rec.Identity) != 0 {
		t.Fatalf("a record with no identity field read back with identity %v", rec.Identity)
	}

	live, err := LivenessOf(dir, testIdentity())
	if err != nil {
		t.Fatalf("LivenessOf: %v", err)
	}
	if live != LivenessNotOurs {
		t.Fatalf("LivenessOf = %v, want not-ours: the process is live and its start time matches, "+
			"but the record carries no identity, and an absent identity is the absence of the only "+
			"thing that says this process is ours", live)
	}
	err = Reap(context.Background(), ReapOptions{
		Cluster: "liveweb", NodeName: testNodeName, DataDir: dir, Identity: testIdentity(),
		Grace: 200 * time.Millisecond, Log: testLog(),
	})
	if !errors.Is(err, ErrNotOurs) {
		t.Fatalf("Reap over a record whose process it cannot verify = %v, want ErrNotOurs. An "+
			"unverifiable record names a LIVE process, and this one names a process that is still "+
			"running: clearing the record would take away the only thing that names it, so the next "+
			"pass would read an absent record, answer that nothing is running, and start a second "+
			"process under the same node name while the first still holds the guest and the "+
			"Node object", err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the process at pid %d was signalled on the strength of a record that says "+
			"nothing about which process it names (%v)", pid, err)
	}
	if _, err := os.Stat(RecordPath(dir)); err != nil {
		t.Fatalf("the unverifiable record was removed: the process it names is alive at pid %d, and "+
			"the record is the only thing that says so. Leaving it in place is what makes the "+
			"mismatch visible to every later pass; clearing it makes the process an orphan nobody "+
			"can name again (%v)", pid, err)
	}
}

func TestReapWithoutAnIdentityIsRefused(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		opts ReapOptions
	}{
		{"no node name", ReapOptions{DataDir: dir, Identity: testIdentity()}},
		{"no data dir", ReapOptions{NodeName: testNodeName, Identity: testIdentity()}},
		{"no identity", ReapOptions{NodeName: testNodeName, DataDir: dir}},
	} {
		if err := Reap(context.Background(), tc.opts); !errors.Is(err, ErrIncomplete) {
			t.Fatalf("%s: Reap = %v, want ErrIncomplete", tc.name, err)
		}
	}
}
