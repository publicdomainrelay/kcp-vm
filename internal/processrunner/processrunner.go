package processrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
)

const OutputTailBytes = 256 << 10

// ponytail: 5s bounds how long Wait waits for a caller's log writer after the
// direct child exits; raise it if a caller's writer can legitimately stall longer.
const waitDelay = 5 * time.Second

type Spec struct {
	Argv   []string
	Dir    string
	Env    []string
	Prefix []string
	Output io.Writer

	DieWithParent bool
}

type outputTail struct {
	mu   sync.Mutex
	ring []byte
	next int
	size int
}

func newOutputTail(n int) *outputTail {
	if n < 1 {
		n = 1
	}
	return &outputTail{ring: make([]byte, n)}
}

func (t *outputTail) write(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for len(p) > 0 {
		n := copy(t.ring[t.next:], p)
		t.next = (t.next + n) % len(t.ring)
		t.size += n
		if t.size > len(t.ring) {
			t.size = len(t.ring)
		}
		p = p[n:]
	}
}

func (t *outputTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.size < len(t.ring) {
		return string(t.ring[:t.size])
	}
	out := make([]byte, 0, len(t.ring))
	out = append(out, t.ring[t.next:]...)
	out = append(out, t.ring[:t.next]...)
	return string(out)
}

type outputSink struct {
	tail *outputTail
	also io.Writer
	mu   sync.Mutex
}

func (s *outputSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tail.write(p)
	if s.also != nil {
		_, _ = s.also.Write(p)
	}
	return len(p), nil
}

type Process struct {
	PID int

	out *outputTail

	cmd    *exec.Cmd
	once   sync.Once
	waited chan struct{}
	err    error
}

func Start(ctx context.Context, spec Spec) (*Process, error) {
	if len(spec.Argv) == 0 {
		return nil, errors.New("processrunner: empty argv, so there is nothing to spawn. The " +
			"embedded-postgres component is a Go library that runs the server inside whoever " +
			"links it, so it has no argv at all; a step built for it would start nothing and " +
			"then gate every later step on a process that does not exist")
	}
	argv := append(append([]string{}, spec.Prefix...), spec.Argv...)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.WaitDelay = waitDelay
	cmd.Dir = spec.Dir
	if spec.Env != nil {
		cmd.Env = spec.Env
	}
	tail := newOutputTail(OutputTailBytes)
	sink := &outputSink{tail: tail, also: spec.Output}
	cmd.Stdout = sink
	cmd.Stderr = sink
	p := &Process{out: tail, cmd: cmd, waited: make(chan struct{})}
	var err error
	if spec.DieWithParent {
		cmd.SysProcAttr = hostos.DieWithParent()
		started := make(chan error, 1)
		go p.holdSpawningThread(started)
		err = <-started
	} else {
		err = cmd.Start()
	}
	if err != nil {
		return nil, fmt.Errorf("processrunner: start %q: %w. Nothing was spawned, so there "+
			"is no PID to signal and no child to reap: a caller that kept a handle here "+
			"would gate the rest of the process group on a component that never ran",
			argv[0], err)
	}
	p.PID = cmd.Process.Pid
	return p, nil
}

func (p *Process) holdSpawningThread(started chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := p.cmd.Start(); err != nil {
		started <- err
		return
	}
	started <- nil
	_ = p.Wait()
}

func (p *Process) Output() string { return p.out.String() }

func (p *Process) Wait() error {
	p.once.Do(func() {
		p.err = p.cmd.Wait()
		if errors.Is(p.err, exec.ErrWaitDelay) {
			p.err = nil
		}
		close(p.waited)
	})
	<-p.waited
	return p.err
}

func (p *Process) Signal(sig os.Signal) error {
	if p.cmd.Process == nil {
		return errors.New("processrunner: no process")
	}
	return p.cmd.Process.Signal(sig)
}

func (p *Process) Stop(sig os.Signal) error {
	if err := p.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	err := p.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return nil
	}
	return err
}
