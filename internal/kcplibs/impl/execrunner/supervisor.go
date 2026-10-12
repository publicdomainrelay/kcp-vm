package execrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type runState struct {
	PID int `json:"pid"`

	Started string `json:"started"`

	Ticks uint64 `json:"ticks"`

	Stopped bool `json:"stopped"`
}

type process struct {
	dir string

	cmd *exec.Cmd

	done chan struct{}

	waitErr error

	pid int

	ticks uint64

	started time.Time

	stopped atomic.Bool

	exitCode int32

	env map[string]string
}

type processSpec struct {
	binary string

	args []string

	dir string

	files map[string][]byte

	env []string

	envMap map[string]string

	envForDir func(dir string, caller []string) []string
}

const retainedRunsLimit = 128

type supervisor struct {
	prefix string

	runsDir string

	doneFile string

	mu sync.Mutex

	runs map[string]*process

	envs map[string]map[string]string

	completed []string

	retainLimit int

	seq atomic.Int64
}

func newSupervisor(prefix, runsDir, doneFile string) *supervisor {
	return &supervisor{prefix: prefix, runsDir: runsDir, doneFile: doneFile, runs: map[string]*process{}, envs: map[string]map[string]string{}, retainLimit: retainedRunsLimit}
}

func (s *supervisor) nextID() string {
	return fmt.Sprintf("%s-%s-%d", s.prefix, time.Now().UTC().Format("20060102T150405.000000000"), s.seq.Add(1))
}

func (s *supervisor) start(entry processSpec) (string, error) {
	id := s.nextID()
	dir := filepath.Join(s.runsDir, id)
	if err := os.MkdirAll(s.runsDir, 0o755); err != nil {
		return "", fmt.Errorf("execrunner: create the runs directory: %w", err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", fmt.Errorf("execrunner: create the %s directory: %w", s.prefix, err)
	}
	abandon := func(err error) (string, error) {
		_ = os.RemoveAll(dir)
		return "", err
	}

	for name, body := range entry.files {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			return abandon(fmt.Errorf("execrunner: write %s: %w", name, err))
		}
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.txt"))
	if err != nil {
		return abandon(fmt.Errorf("execrunner: create stdout file: %w", err))
	}
	defer stdout.Close()
	stderr, err := os.Create(filepath.Join(dir, "stderr.txt"))
	if err != nil {
		return abandon(fmt.Errorf("execrunner: create stderr file: %w", err))
	}
	defer stderr.Close()

	env := append([]string{}, entry.env...)
	if entry.envForDir != nil {
		env = append(env, entry.envForDir(dir, entry.env)...)
	}

	cmd := exec.Command(entry.binary, entry.args...)
	cmd.Dir = entry.dir
	if entry.dir == "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return abandon(fmt.Errorf("execrunner: start %s process: %w", s.prefix, err))
	}

	run := &process{
		dir:     dir,
		cmd:     cmd,
		done:    make(chan struct{}),
		pid:     cmd.Process.Pid,
		ticks:   startTicks(cmd.Process.Pid),
		started: time.Now(),
		env:     entry.envMap,
	}
	if err := writeState(run); err != nil {
		_ = syscall.Kill(-run.pid, syscall.SIGKILL)
		return abandon(err)
	}
	s.mu.Lock()
	s.runs[id] = run
	s.envs[id] = run.env
	s.mu.Unlock()

	go func() {
		run.waitErr = cmd.Wait()
		if cmd.ProcessState != nil {
			run.exitCode = int32(cmd.ProcessState.ExitCode())
		}
		writeDone(run, s.doneFile)
		close(run.done)
		if s.doneFile != "" {
			s.forget(id)
		}
		s.retain(id)
	}()
	return id, nil
}

func (s *supervisor) held() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}

func (s *supervisor) forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.runs, id)
}

func (s *supervisor) retain(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = append(s.completed, id)
	for len(s.completed) > s.retainLimit {
		oldest := s.completed[0]
		s.completed = s.completed[1:]
		delete(s.runs, oldest)
		delete(s.envs, oldest)
	}
}

func (s *supervisor) retained() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.envs)
}

func writeDone(run *process, doneFile string) {
	if doneFile == "" {
		return
	}
	body, err := json.Marshal(podDone{ExitCode: run.exitCode})
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(run.dir, doneFile), body, 0o644)
}

func writeState(run *process) error {
	body, err := json.Marshal(runState{PID: run.pid, Started: run.started.Format(time.RFC3339Nano), Ticks: run.ticks, Stopped: run.stopped.Load()})
	if err != nil {
		return fmt.Errorf("execrunner: encode the run state: %w", err)
	}
	if err := os.WriteFile(filepath.Join(run.dir, "state.json"), body, 0o644); err != nil {
		return fmt.Errorf("execrunner: write the run state: %w", err)
	}
	return nil
}

func (s *supervisor) lookup(id string) (*process, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	return run, ok
}

func (s *supervisor) dir(id string) (string, error) {
	if run, ok := s.lookup(id); ok {
		return run.dir, nil
	}
	recovered, err := s.recover(id)
	if err != nil {
		return "", err
	}
	return recovered.dir, nil
}

func (s *supervisor) recover(id string) (*process, error) {
	dir := filepath.Join(s.runsDir, id)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("execrunner: no %s run directory for %s", s.prefix, id)
	}
	var state runState
	if body, err := os.ReadFile(filepath.Join(dir, "state.json")); err == nil {
		_ = json.Unmarshal(body, &state)
	}
	started := time.Now()
	if state.Started != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, state.Started); err == nil {
			started = parsed
		}
	}
	recovered := &process{dir: dir, pid: state.PID, ticks: state.Ticks, started: started}
	recovered.stopped.Store(state.Stopped)
	return recovered, nil
}

func (s *supervisor) envOf(id string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run, ok := s.runs[id]; ok {
		return run.env
	}
	return s.envs[id]
}

func (s *supervisor) resolve(id string) (*process, error) {
	if run, ok := s.lookup(id); ok {
		return run, nil
	}
	return s.recover(id)
}

func (s *supervisor) finished(run *process) bool {
	if run.cmd != nil {
		select {
		case <-run.done:
			return true
		default:
			return false
		}
	}
	if run.stopped.Load() {
		return true
	}
	if s.doneFile != "" {
		if _, err := os.Stat(filepath.Join(run.dir, s.doneFile)); err == nil {
			return true
		}
	}
	return !processAlive(run.pid)
}

const stopGrace = 2 * time.Second

func (s *supervisor) stop(id string) error {
	run, err := s.resolve(id)
	if err != nil {
		return nil
	}
	if s.finished(run) {
		return nil
	}
	if run.cmd == nil && run.ticks != 0 && !ownedByUs(run.pid, run.ticks) {
		return nil
	}
	run.stopped.Store(true)
	_ = writeState(run)
	if run.pid > 0 {
		_ = syscall.Kill(-run.pid, syscall.SIGKILL)
	}
	if run.cmd != nil {
		select {
		case <-run.done:
		case <-time.After(stopGrace):
		}
	}
	return nil
}

func (s *supervisor) probe(ctx context.Context, id string, command []string, timeout time.Duration, withEnv bool) (bool, error) {
	if len(command) == 0 {
		return true, nil
	}
	dir, err := s.dir(id)
	if err != nil {
		return false, err
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, command[0], command[1:]...)
	cmd.Dir = dir
	if withEnv {
		cmd.Env = append(os.Environ(), envPairs(s.envOf(id))...)
	}
	if err := cmd.Run(); err != nil {
		return false, nil
	}
	return true, nil
}

func env(req map[string]string, extra []string) []string {
	out := append([]string{}, extra...)
	keys := make([]string, 0, len(req))
	for key := range req {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+"="+req[key])
	}
	return out
}

func envPairs(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	return out
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func startTicks(pid int) uint64 {
	_, ticks, err := readProcessStat(pid)
	if err != nil {
		return 0
	}
	return ticks
}

func ownedByUs(pid int, ticks uint64) bool {
	if pid <= 0 || ticks == 0 {
		return false
	}
	_, current, err := readProcessStat(pid)
	if err != nil {
		return false
	}
	return current == ticks
}

func readProcessStat(pid int) (string, uint64, error) {
	body, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", 0, err
	}
	line := string(body)
	end := strings.LastIndex(line, ")")
	if end < 0 || end+2 > len(line) {
		return "", 0, errors.New("execrunner: unreadable process stat")
	}
	comm := line[strings.Index(line, "(")+1 : end]
	fields := strings.Fields(line[end+2:])
	if len(fields) < 20 {
		return "", 0, errors.New("execrunner: short process stat")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return "", 0, err
	}
	return comm, ticks, nil
}
