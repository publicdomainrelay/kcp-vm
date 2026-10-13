package apiserverfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const DefaultDialTimeout = 5 * time.Second

var ErrCurrentBackend = errors.New("apiserverfront: refusing to drain the current backend")

type Status struct {
	Address      string         `json:"address"`
	Backend      string         `json:"backend"`
	Generation   int64          `json:"generation"`
	Open         map[string]int `json:"open"`
	Accepted     int64          `json:"accepted"`
	DialFailures int64          `json:"dialFailures"`
	Inherited    bool           `json:"inherited"`
	PID          int            `json:"pid"`
	SwitchedAt   time.Time      `json:"switchedAt,omitzero"`
}

type State struct {
	Address    string    `json:"address,omitempty"`
	Backend    string    `json:"backend"`
	Generation int64     `json:"generation"`
	SwitchedAt time.Time `json:"switchedAt"`
}

type Options struct {
	StatePath   string
	DialTimeout time.Duration
	Inherited   bool
	Log         *slog.Logger
}

type pair struct {
	backend string
	client  net.Conn
	server  net.Conn
}

type Front struct {
	ln       net.Listener
	o        Options
	log      *slog.Logger
	mu       sync.Mutex
	state    State
	conns    map[*pair]struct{}
	accepted atomic.Int64
	failures atomic.Int64
	wg       sync.WaitGroup
}

func ValidBackend(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("apiserverfront: backend %q is no host:port: %w", addr, err)
	}
	if host == "" {
		return fmt.Errorf("apiserverfront: backend %q names no host", addr)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p <= 0 || p > 65535 {
		return fmt.Errorf("apiserverfront: backend %q names no port", addr)
	}
	return nil
}

func New(ln net.Listener, o Options) (*Front, error) {
	if o.DialTimeout <= 0 {
		o.DialTimeout = DefaultDialTimeout
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	f := &Front{ln: ln, o: o, log: log, conns: map[*pair]struct{}{}}
	if o.StatePath != "" {
		b, err := os.ReadFile(o.StatePath)
		switch {
		case err == nil:
			var st State
			if err := json.Unmarshal(b, &st); err != nil {
				return nil, fmt.Errorf("apiserverfront: %s: %w", o.StatePath, err)
			}
			if st.Backend != "" {
				if err := ValidBackend(st.Backend); err != nil {
					return nil, err
				}
			}
			f.state = st
		case !errors.Is(err, os.ErrNotExist):
			return nil, err
		}
		if a := ln.Addr().String(); f.state.Address != a {
			f.state.Address = a
			if err := f.persist(f.state); err != nil {
				return nil, err
			}
		}
	}
	return f, nil
}

func (f *Front) Serve(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { f.ln.Close() })
	defer stop()
	st := f.Status()
	f.log.Info("front serving", "address", st.Address, "backend", st.Backend, "generation", st.Generation,
		"inherited", st.Inherited, "pid", st.PID)
	var err error
	for {
		var c net.Conn
		c, err = f.ln.Accept()
		if err != nil {
			break
		}
		f.accepted.Add(1)
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			f.handle(c)
		}()
	}
	f.closeAll()
	f.wg.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func (f *Front) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for p := range f.conns {
		p.client.Close()
		p.server.Close()
	}
}

func (f *Front) current() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state.Backend
}

func (f *Front) handle(c net.Conn) {
	backend := f.current()
	if backend == "" {
		f.failures.Add(1)
		c.Close()
		return
	}
	s, err := net.DialTimeout("tcp", backend, f.o.DialTimeout)
	if err != nil {
		f.failures.Add(1)
		f.log.Warn("backend dial failed; the front does not fail over", "backend", backend, "err", err.Error())
		c.Close()
		return
	}
	p := &pair{backend: backend, client: c, server: s}
	f.mu.Lock()
	f.conns[p] = struct{}{}
	f.mu.Unlock()
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if tc, ok := dst.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}
	go pipe(s, c)
	go pipe(c, s)
	<-done
	<-done
	c.Close()
	s.Close()
	f.mu.Lock()
	delete(f.conns, p)
	f.mu.Unlock()
}

func (f *Front) persist(st State) error {
	if f.o.StatePath == "" {
		return nil
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.o.StatePath), 0o700); err != nil {
		return err
	}
	tmp := f.o.StatePath + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.o.StatePath)
}

func (f *Front) SetBackend(addr string) (Status, error) {
	if err := ValidBackend(addr); err != nil {
		return f.Status(), err
	}
	f.mu.Lock()
	next := f.state
	if next.Backend != addr {
		next = State{Address: f.state.Address, Backend: addr, Generation: f.state.Generation + 1, SwitchedAt: time.Now().UTC()}
		if err := f.persist(next); err != nil {
			f.mu.Unlock()
			return f.Status(), fmt.Errorf("apiserverfront: persisting the backend before the switch: %w", err)
		}
		f.log.Info("backend switched", "from", f.state.Backend, "to", addr, "generation", next.Generation)
		f.state = next
	}
	f.mu.Unlock()
	return f.Status(), nil
}

func (f *Front) Drain(backend string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if backend == f.state.Backend {
		return 0, fmt.Errorf("%w %s", ErrCurrentBackend, backend)
	}
	n := 0
	for p := range f.conns {
		if p.backend == backend {
			p.client.Close()
			p.server.Close()
			n++
		}
	}
	if n > 0 {
		f.log.Info("drained", "backend", backend, "closed", n)
	}
	return n, nil
}

func (f *Front) Status() Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	open := map[string]int{}
	for p := range f.conns {
		open[p.backend]++
	}
	return Status{Address: f.ln.Addr().String(), Backend: f.state.Backend, Generation: f.state.Generation, Open: open,
		Accepted: f.accepted.Load(), DialFailures: f.failures.Load(), Inherited: f.o.Inherited, PID: os.Getpid(),
		SwitchedAt: f.state.SwitchedAt}
}
