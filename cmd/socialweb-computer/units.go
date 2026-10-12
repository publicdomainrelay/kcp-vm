package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/assets"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc/treeaudit"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/versions"
)

type placement struct {
	manager hostsvc.Manager
	devsup  int
	runtime string
}

func (a *app) placement(ctx context.Context, vals clicfg.Values) (placement, error) {
	backend, err := hostsvc.ParseBackend(vals.String(backendOpt))
	if err != nil {
		return placement{}, err
	}
	p := placement{runtime: vals.String("asset-cache")}
	switch backend {
	case hostsvc.BackendSystemdUser:
		if p.runtime == "" && vals.String("state-dir") != "" {
			p.runtime = filepath.Join(vals.String("state-dir"), "runtime")
		}
		if p.runtime == "" {
			return p, fmt.Errorf("the systemd-user backend needs --state-dir or --asset-cache to find the pinned runtime sets (install writes both into the account's config)")
		}
		p.manager, err = hostsvc.Open(ctx, hostsvc.Config{Backend: backend})
		return p, err
	case hostsvc.BackendProc:
		l, err := a.layout(vals)
		if err != nil {
			return p, err
		}
		c, h, err := helloDevsup(ctx, l)
		if err != nil {
			return p, fmt.Errorf("the instance's devsup at %s: %w", l.Socket, err)
		}
		p.manager, p.devsup = c, h.PID
		if p.runtime == "" {
			if p.runtime, err = assets.DevCache(a.env); err != nil {
				return p, err
			}
		}
		return p, nil
	}
	return p, fmt.Errorf("no placement backend %q", backend)
}

func runUnitsList(ctx context.Context, a *app, vals clicfg.Values, rest []string) int {
	p, err := a.placement(ctx, vals)
	if err != nil {
		return a.fail(exitRefused, "units list: %v", err)
	}
	defer p.manager.Close()
	var all []hostsvc.UnitStatus
	seen := map[string]bool{}
	for _, prefix := range []string{"socialweb-vm-", "socialweb-bed-", "socialweb-bao-", "socialweb-kcp-", "socialweb-pg-", "socialweb-c-", "socialweb-computer-", "socialweb-probe-"} {
		st, err := p.manager.List(ctx, prefix)
		if err != nil {
			return a.fail(exitFailed, "units list: %v", err)
		}
		for _, s := range st {
			if !seen[s.Name] {
				seen[s.Name] = true
				all = append(all, s)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	if vals.Bool("json") {
		a.printJSON(all)
		return exitOK
	}
	for _, s := range all {
		fmt.Fprintf(a.stdout, "%-56s %-12s %-10s pid %d\n", s.Name, s.State, s.SubState, s.MainPID)
	}
	return exitOK
}

func runUnitsAudit(ctx context.Context, a *app, vals clicfg.Values, rest []string) int {
	p, err := a.placement(ctx, vals)
	if err != nil {
		return a.fail(exitRefused, "units audit: %v", err)
	}
	defer p.manager.Close()
	pins, err := versions.PlatformPins(assets.Platform())
	if err != nil {
		return a.fail(exitFailed, "units audit: %v", err)
	}
	pinned, notes := treeaudit.Pinned(p.runtime, pins, assets.TreeDigest)
	for _, n := range notes {
		fmt.Fprintln(a.stderr, "units audit: "+n)
	}
	r, err := treeaudit.Audit(ctx, p.manager, treeaudit.Host{}, p.devsup, pinned)
	if err != nil {
		return a.fail(exitFailed, "units audit: %v", err)
	}
	if vals.Bool("json") {
		a.printJSON(r)
	} else {
		treeaudit.Write(a.stdout, r)
	}
	if !r.Pass() {
		return exitFailed
	}
	return exitOK
}
