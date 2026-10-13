package apiserverfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
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

func StickyAddress(statePath, address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "0" {
		return address
	}
	b, err := os.ReadFile(statePath)
	if err != nil {
		return address
	}
	var st State
	if json.Unmarshal(b, &st) != nil || st.Address == "" {
		return address
	}
	h, p, err := net.SplitHostPort(st.Address)
	if err != nil || h != host || p == "" || p == "0" {
		return address
	}
	return st.Address
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
	address := StickyAddress(c.State, c.Address)
	getenv := os.Getenv
	a, err := AcquireAll(address, c.FDStore, getenv)
	if err != nil && address != c.Address {
		log.Warn("the front's previous address is taken; it binds a new one", "previous", address, "err", err.Error())
		a, err = AcquireAll(c.Address, c.FDStore, getenv)
	}
	if err != nil {
		return err
	}
	store := ""
	if c.FDStore {
		store = a.Notify
	}
	f, err := New(a.Listener, Options{StatePath: c.State, Inherited: a.Inherited, Holds: a.Holds, FDStore: store, Log: log})
	if err != nil {
		a.Listener.Close()
		for _, h := range a.Holds {
			h.Close()
		}
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
