package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/foldmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostcontract"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/install"
)

var installOptions = []string{backendOpt, "handle", "account", "state-dir", "vm-slots", "dry-run", "adopt-account", "json"}

var uninstallOptions = []string{"account", "purge", "force", "json"}

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

var upgradeOptions = []string{"binary", "sha256", "state-dir"}

func (a *app) upgradeOptions(verb string, vals clicfg.Values) (install.UpgradeOptions, error) {
	if os.Geteuid() == 0 {
		return install.UpgradeOptions{}, fmt.Errorf("%s runs as the installed account, never as root", verb)
	}
	state := vals.String("state-dir")
	if state == "" {
		return install.UpgradeOptions{}, fmt.Errorf("%s: --state-dir (SOCIALWEB_STATE_DIR) names the account's state dir; the core unit's config.json carries it", verb)
	}
	u, err := user.Current()
	if err != nil {
		return install.UpgradeOptions{}, err
	}
	return install.UpgradeOptions{StateDir: state, Home: u.HomeDir, Out: a.stdout, SelfCheck: install.SelfCheck, Restart: install.RestartOwnUnits}, nil
}

func runUpgrade(ctx context.Context, a *app, vals clicfg.Values, rest []string) int {
	o, err := a.upgradeOptions("upgrade", vals)
	if err != nil {
		return a.fail(exitRefused, "%v", err)
	}
	o.Binary, o.SHA256 = vals.String("binary"), vals.String("sha256")
	if o.Binary == "" {
		return a.fail(exitRefused, "upgrade: --binary names the new socialweb-computer executable")
	}
	if err := install.Upgrade(ctx, o); err != nil {
		code := exitFailed
		if errors.Is(err, install.ErrManifestMismatch) || errors.Is(err, install.ErrSelfCheck) {
			code = exitRefused
		}
		return a.fail(code, "%v", err)
	}
	return exitOK
}

func runRollback(ctx context.Context, a *app, vals clicfg.Values, rest []string) int {
	o, err := a.upgradeOptions("rollback", vals)
	if err != nil {
		return a.fail(exitRefused, "%v", err)
	}
	if err := install.Rollback(ctx, o); err != nil {
		code := exitFailed
		if errors.Is(err, install.ErrNoPrevious) {
			code = exitRefused
		}
		return a.fail(code, "%v", err)
	}
	return exitOK
}
