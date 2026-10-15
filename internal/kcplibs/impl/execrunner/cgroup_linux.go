//go:build linux

package execrunner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	cgroupMount = "/sys/fs/cgroup"

	supervisorLeaf = "supervisor"

	podCgroupPrefix = "pod-"

	subtreeAttempts = 20

	subtreeRetryDelay = 50 * time.Millisecond
)

type cgroupBase struct {
	mu   sync.Mutex
	path string
}

var podBase cgroupBase

type limiter struct {
	dir string
	fd  *os.File
}

func ownCgroupDir() (string, error) {
	body, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			rest = strings.TrimSuffix(strings.TrimSpace(rest), " (deleted)")
			return filepath.Join(cgroupMount, rest), nil
		}
	}
	return "", errors.New("the process is in no cgroup v2 hierarchy")
}

func podCgroupBase() (string, error) {
	podBase.mu.Lock()
	defer podBase.mu.Unlock()
	if podBase.path != "" {
		return podBase.path, nil
	}
	path, err := prepareBase()
	if err != nil {
		return "", fmt.Errorf("execrunner: a pod memory limit needs a cgroup this process may subdivide (run the core under a unit with Delegate=yes): %w", err)
	}
	podBase.path = path
	return path, nil
}

func movePids(from, to string) error {
	body, err := os.ReadFile(filepath.Join(from, "cgroup.procs"))
	if err != nil {
		return err
	}
	for _, field := range strings.Fields(string(body)) {
		if err := os.WriteFile(filepath.Join(to, "cgroup.procs"), []byte(field), 0o644); err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func prepareBase() (string, error) {
	dir, err := ownCgroupDir()
	if err != nil {
		return "", err
	}
	leaf := filepath.Join(dir, supervisorLeaf)
	if err := os.Mkdir(leaf, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(leaf, "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return "", err
	}
	for attempt := 0; ; attempt++ {
		if err := movePids(dir, leaf); err != nil {
			return "", err
		}
		err := os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte("+memory"), 0o644)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EBUSY) || attempt >= subtreeAttempts {
			return "", err
		}
		time.Sleep(subtreeRetryDelay)
	}
	removeStalePodCgroups(dir)
	return dir, nil
}

func removeStalePodCgroups(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), podCgroupPrefix) {
			dir := filepath.Join(base, e.Name())
			_ = os.WriteFile(filepath.Join(dir, "cgroup.kill"), []byte("1"), 0o644)
			_ = os.Remove(dir)
		}
	}
}

func newLimiter(cmd *exec.Cmd, id string, memoryMax uint64) (*limiter, error) {
	base, err := podCgroupBase()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, podCgroupPrefix+id)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, fmt.Errorf("execrunner: create the pod cgroup: %w", err)
	}
	l := &limiter{dir: dir}
	for name, value := range map[string]string{
		"memory.max":       strconv.FormatUint(memoryMax, 10),
		"memory.swap.max":  "0",
		"memory.oom.group": "1",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o644); err != nil {
			l.release()
			return nil, fmt.Errorf("execrunner: set %s on the pod cgroup: %w", name, err)
		}
	}
	fd, err := os.Open(dir)
	if err != nil {
		l.release()
		return nil, fmt.Errorf("execrunner: open the pod cgroup: %w", err)
	}
	l.fd = fd
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = int(fd.Fd())
	return l, nil
}

func (l *limiter) started() {
	if l != nil && l.fd != nil {
		_ = l.fd.Close()
		l.fd = nil
	}
}

func (l *limiter) oomKilled() bool {
	if l == nil {
		return false
	}
	body, err := os.ReadFile(filepath.Join(l.dir, "memory.events"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(body), "\n") {
		if rest, ok := strings.CutPrefix(line, "oom_kill "); ok {
			n, _ := strconv.ParseUint(strings.TrimSpace(rest), 10, 64)
			return n > 0
		}
	}
	return false
}

func (l *limiter) release() {
	if l == nil {
		return
	}
	l.started()
	_ = os.WriteFile(filepath.Join(l.dir, "cgroup.kill"), []byte("1"), 0o644)
	_ = os.Remove(l.dir)
}
