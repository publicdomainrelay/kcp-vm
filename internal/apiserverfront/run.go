package apiserverfront

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
)

type Config struct {
	Owner   string
	Address string
	Control string
	State   string
	FDStore bool
	Log     *slog.Logger
}

func (c Config) Validate() error {
	if c.Owner == "" {
		return errors.New("apiserverfront: --owner names whose front this is")
	}
	if c.Address == "" {
		return errors.New("apiserverfront: no listen address (kcp-front-address)")
	}
	if c.State == "" {
		return errors.New("apiserverfront: no state file (front-state)")
	}
	return CheckSocketPath(c.Control)
}

func Run(ctx context.Context, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	log := c.Log
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "apiserver-front", "owner", c.Owner)
	ln, inherited, err := Acquire(c.Address, c.FDStore, os.Getenv)
	if err != nil {
		return err
	}
	f, err := New(ln, Options{StatePath: c.State, Inherited: inherited, Log: log})
	if err != nil {
		ln.Close()
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctlErr := make(chan error, 1)
	go func() {
		ctlErr <- ServeControl(ctx, c.Control, f, func() {
			st := f.Status()
			log.Info("front listening", "address", st.Address, "control", c.Control, "backend", st.Backend,
				"inherited", inherited, "fdStore", c.FDStore)
		})
		cancel()
	}()
	err = f.Serve(ctx)
	cancel()
	if cerr := <-ctlErr; cerr != nil && err == nil {
		err = fmt.Errorf("apiserverfront: control socket: %w", cerr)
	}
	return err
}
