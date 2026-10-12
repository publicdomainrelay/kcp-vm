package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/capabilities"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

const (
	exitOK      = 0
	exitFailed  = 1
	exitRefused = 2
	serviceName = "socialweb-computer"
	backendOpt  = "host-backend"
)

type app struct {
	env    clicfg.Env
	stdout io.Writer
	stderr io.Writer
	spec   clicfg.Spec
	config map[string]any
	goos   string
	goarch string
	log    *slog.Logger
}

type verbFunc func(ctx context.Context, a *app, v clicfg.Values, rest []string) int

type builtVerb struct {
	options []string
	run     verbFunc
}

func builtVerbs() map[string]builtVerb {
	return map[string]builtVerb{
		"version":           {[]string{"json"}, runVersion},
		"dev up":            {[]string{backendOpt, "instance", "state-dir", "profile", "kcp-path", "kine-path", "ready-timeout", "kcp-base-workspace", "fg", "keep-state"}, runDevUp},
		"dev status":        {[]string{"instance", "state-dir"}, runDevStatus},
		"dev down":          {[]string{"instance", "state-dir", "keep-state"}, runDevDown},
		"dev ls":            {[]string{"state-dir"}, runDevLs},
		"dev logs":          {[]string{"instance", "state-dir", "tail"}, runDevLogs},
		"dev env":           {[]string{"instance", "state-dir", "json"}, runDevEnv},
		"dev check":         {nil, runDevCheck},
		"dev restart":       {[]string{"instance", "state-dir", "binary", "ready-timeout"}, runDevRestart},
		"internal devsup":   {[]string{"state", "socket", "die-with", "instance"}, runDevsup},
		"internal dev-core": {[]string{"instance-dir", "ready-timeout"}, runDevCore},
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], clicfg.OSEnv{}, os.Stdout, os.Stderr, runtime.GOOS, runtime.GOARCH)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, env clicfg.Env, stdout, stderr io.Writer, goos, goarch string) int {
	spec, err := clicfg.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailed
	}
	config, err := clicfg.ConfigFile(env)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitRefused
	}
	a := &app{env: env, stdout: stdout, stderr: stderr, spec: spec, config: config, goos: goos, goarch: goarch,
		log: slog.New(slog.NewJSONHandler(stderr, nil)).With("service", serviceName)}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		a.usage(stdout, "")
		if len(args) == 0 {
			return exitRefused
		}
		return exitOK
	}
	verb, rest, ok := capabilities.Find(args)
	if !ok {
		group := strings.Join(args, " ")
		if capabilities.Groups()[args[0]] {
			a.usage(stderr, args[0])
			return exitRefused
		}
		fmt.Fprintf(stderr, "%s: unknown subcommand %q\n", serviceName, group)
		a.usage(stderr, "")
		return exitRefused
	}
	if why, missing := capabilities.Missing(verb, goos, goarch); missing {
		fmt.Fprintln(stderr, why)
		return exitRefused
	}
	built, isBuilt := builtVerbs()[verb.Path]
	names := built.options
	if !isBuilt {
		names = nil
		for _, n := range verb.Needs {
			if n == capabilities.NeedHostBackend {
				names = append(names, backendOpt)
			}
		}
	}
	vals, rest, err := spec.Resolve(verb.Path, names, flagsFirst(spec, rest), env, config, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitRefused
	}
	for _, n := range verb.Needs {
		if n != capabilities.NeedHostBackend {
			continue
		}
		if err := spec.Require(vals, backendOpt); err != nil {
			fmt.Fprintf(stderr, "%s: %v: this verb places host processes and never probes for a backend (systemd-user | proc)\n", verb.Path, err)
			return exitRefused
		}
		if _, err := hostsvc.ParseBackend(vals.String(backendOpt)); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", verb.Path, err)
			return exitRefused
		}
	}
	if !isBuilt {
		fmt.Fprintf(stderr, "%s: not built yet; built by %s\n", verb.Path, verb.BuiltBy)
		return exitRefused
	}
	return built.run(ctx, a, vals, rest)
}

func flagsFirst(spec clicfg.Spec, args []string) []string {
	var flags, words []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			words = append(words, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			words = append(words, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if o, ok := spec.Options[name]; ok && o.Type != clicfg.TypeBoolean && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, words...)
}

func (a *app) usage(w io.Writer, group string) {
	fmt.Fprintf(w, "usage: %s <subcommand> [options]   (%s/%s)\n\n", serviceName, a.goos, a.goarch)
	verbs := capabilities.Tree()
	sort.SliceStable(verbs, func(i, j int) bool { return verbs[i].Path < verbs[j].Path })
	for _, v := range verbs {
		if group != "" && !strings.HasPrefix(v.Path, group+" ") {
			continue
		}
		var marks []string
		if why, missing := capabilities.Missing(v, a.goos, a.goarch); missing {
			marks = append(marks, "no backend here: "+why[len(v.Path)+2:])
		}
		if !v.Built() {
			marks = append(marks, "not built: "+v.BuiltBy)
		}
		mark := ""
		if len(marks) > 0 {
			mark = "  [" + strings.Join(marks, "; ") + "]"
		}
		fmt.Fprintf(w, "  %-34s %s%s\n", v.Path, v.Summary, mark)
	}
	fmt.Fprintf(w, "\nconfig: flag > env > %s file > default (internal/clicfg/options.json)\n", clicfg.ConfigPathEnv)
}
