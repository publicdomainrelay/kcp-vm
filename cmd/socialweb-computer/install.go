package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/foldmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostcontract"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/install"
)

var installOptions = []string{backendOpt, "handle", "account", "state-dir", "vm-slots", "dry-run", "adopt-account", "json"}

var uninstallOptions = []string{"account", "purge", "force", "json"}

func roleOf(verb string) hostcontract.Role {
	if strings.HasSuffix(verb, " bidder") {
		return hostcontract.RoleBidder
	}
	return hostcontract.RoleGateway
}

func splitHandles(s string) []string {
	var out []string
	for _, h := range strings.Split(s, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}

func installExit(a *app, verb string, rep install.Report, err error, asJSON bool) int {
	if asJSON {
		a.printJSON(rep)
	}
	if err == nil {
		return exitOK
	}
	fmt.Fprintf(a.stderr, "%s: %v\n", verb, err)
	switch {
	case errors.Is(err, install.ErrProcBackend), errors.Is(err, install.ErrForeignAccount), errors.Is(err, install.ErrVMsRunning),
		errors.Is(err, install.ErrManagerWithoutKVM), errors.Is(err, install.ErrNoRecord), errors.Is(err, install.ErrUnsupported):
		return exitRefused
	}
	return exitFailed
}

func runInstall(role hostcontract.Role) verbFunc {
	verb := "install " + string(role)
	return func(ctx context.Context, a *app, vals clicfg.Values, rest []string) int {
		if len(rest) > 0 {
			return a.fail(exitRefused, "%s: unexpected arguments %q", verb, rest)
		}
		if vals.String(backendOpt) == "proc" {
			return installExit(a, verb, install.Report{}, install.ErrProcBackend, false)
		}
		dry := vals.Bool("dry-run")
		if os.Geteuid() != 0 && !dry {
			return a.fail(exitRefused, "%s: the one explicit root step: run it as root (or --dry-run to print the step list)", verb)
		}
		exe, err := foldmain.ResolveExe()
		if err != nil {
			return a.fail(exitFailed, "%s: resolving this binary: %v", verb, err)
		}
		sys, err := install.NewSystem(a.stdout)
		if err != nil {
			return installExit(a, verb, install.Report{}, err, false)
		}
		rep, err := install.Install(ctx, install.Options{
			Role: role, Handles: splitHandles(vals.String("handle")), Account: vals.String("account"),
			StateDir: vals.String("state-dir"), Binary: exe, VMSlots: vals.Int("vm-slots"), DryRun: dry,
			AdoptAccount: vals.Bool("adopt-account"), HostBackend: vals.String(backendOpt), Out: a.stdout,
		}, sys)
		return installExit(a, verb, rep, err, vals.Bool("json"))
	}
}

func runUninstall(role hostcontract.Role) verbFunc {
	verb := "uninstall " + string(role)
	return func(ctx context.Context, a *app, vals clicfg.Values, rest []string) int {
		if len(rest) > 0 {
			return a.fail(exitRefused, "%s: unexpected arguments %q", verb, rest)
		}
		if os.Geteuid() != 0 {
			return a.fail(exitRefused, "%s: run it as root", verb)
		}
		sys, err := install.NewSystem(a.stdout)
		if err != nil {
			return installExit(a, verb, install.Report{}, err, false)
		}
		rep, err := install.Uninstall(ctx, install.UninstallOptions{Role: role, Account: vals.String("account"),
			Purge: vals.Bool("purge"), Force: vals.Bool("force"), Out: a.stdout}, sys)
		return installExit(a, verb, rep, err, vals.Bool("json"))
	}
}
