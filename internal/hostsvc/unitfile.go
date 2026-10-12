package hostsvc

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	ArgsVar       = "SWC_ARGS"
	WantedBy      = "default.target"
	EnvFileSuffix = ".env"
)

type InstalledFile struct {
	Unit    []byte
	Env     []byte
	EnvPath string
}

type InstallOptions struct {
	Exec    string
	EnvFile string
}

func quoteArg(a string) (string, error) {
	if a == "" || strings.ContainsAny(a, " \t\n\"'\\$%;") {
		return "", fmt.Errorf("hostsvc: argument %q cannot travel through an EnvironmentFile word list", a)
	}
	return a, nil
}

func signalName(s int) string {
	switch s {
	case 2:
		return "SIGINT"
	case 9:
		return "SIGKILL"
	case 15:
		return "SIGTERM"
	case 3:
		return "SIGQUIT"
	}
	return strconv.Itoa(s)
}

func RenderInstalled(spec UnitSpec, o InstallOptions) (InstalledFile, error) {
	if err := ValidateSpec(spec); err != nil {
		return InstalledFile{}, err
	}
	if len(spec.Exec) == 0 {
		return InstalledFile{}, errors.New("hostsvc: an installed unit needs an executable")
	}
	exe := o.Exec
	if exe == "" {
		exe = spec.Exec[0]
	}
	if !filepath.IsAbs(exe) || !filepath.IsAbs(o.EnvFile) {
		return InstalledFile{}, fmt.Errorf("hostsvc: %s: the executable and the environment file must be absolute paths", spec.Name)
	}
	args := make([]string, 0, len(spec.Exec)-1)
	for _, a := range spec.Exec[1:] {
		q, err := quoteArg(a)
		if err != nil {
			return InstalledFile{}, fmt.Errorf("%s: %w", spec.Name, err)
		}
		args = append(args, q)
	}
	desc := spec.Description
	if desc == "" {
		desc = spec.Name
	}
	slice := spec.Slice
	if slice == "" {
		slice = DefaultSlice
	}
	restart := spec.Restart
	if restart == "" {
		restart = RestartNo
	}
	oom := spec.OOMPolicy
	if oom == "" {
		oom = "stop"
	}
	var b strings.Builder
	line := func(k, v string) { fmt.Fprintf(&b, "%s=%s\n", k, v) }
	b.WriteString("[Unit]\n")
	line("Description", desc)
	if len(spec.After) > 0 {
		line("After", strings.Join(spec.After, " "))
	}
	if len(spec.Wants) > 0 {
		line("Wants", strings.Join(spec.Wants, " "))
	}
	line("StartLimitIntervalSec", "0")
	b.WriteString("\n[Service]\n")
	line("Type", "exec")
	line("Slice", slice)
	line("KillMode", "control-group")
	line("OOMPolicy", oom)
	line("Restart", string(restart))
	if restart != RestartNo {
		line("RestartSec", "2")
	}
	if spec.WorkDir != "" {
		line("WorkingDirectory", spec.WorkDir)
	}
	for _, e := range spec.Env {
		q, err := quoteArg(e)
		if err != nil {
			return InstalledFile{}, fmt.Errorf("%s: %w", spec.Name, err)
		}
		line("Environment", q)
	}
	line("EnvironmentFile", o.EnvFile)
	line("ExecStart", exe+" $"+ArgsVar)
	if spec.KillSignal != 0 {
		line("KillSignal", signalName(int(spec.KillSignal)))
	}
	if spec.StopTimeout > 0 {
		line("TimeoutStopSec", strconv.FormatInt(int64(spec.StopTimeout.Seconds()), 10))
	}
	if spec.Output.StdoutFile != "" {
		line("StandardOutput", "append:"+spec.Output.StdoutFile)
	}
	if spec.Output.StderrFile != "" {
		line("StandardError", "append:"+spec.Output.StderrFile)
	}
	if spec.Limits.MemoryMax > 0 {
		line("MemoryMax", strconv.FormatUint(spec.Limits.MemoryMax, 10))
		line("MemorySwapMax", "0")
	}
	if spec.Limits.TasksMax > 0 {
		line("TasksMax", strconv.FormatUint(spec.Limits.TasksMax, 10))
	}
	if spec.Limits.CPUWeight > 0 {
		line("CPUWeight", strconv.FormatUint(spec.Limits.CPUWeight, 10))
	}
	if spec.FDStore > 0 {
		line("FileDescriptorStoreMax", strconv.Itoa(spec.FDStore))
		line("NotifyAccess", "main")
	}
	b.WriteString("\n[Install]\n")
	line("WantedBy", WantedBy)
	env := ArgsVar + "=" + strings.Join(args, " ") + "\n"
	return InstalledFile{Unit: []byte(b.String()), Env: []byte(env), EnvPath: o.EnvFile}, nil
}

func RenderDropIn(after, wants []string) []byte {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	if len(after) > 0 {
		fmt.Fprintf(&b, "After=%s\n", strings.Join(after, " "))
	}
	if len(wants) > 0 {
		fmt.Fprintf(&b, "Wants=%s\n", strings.Join(wants, " "))
	}
	return []byte(b.String())
}

func ParseArgsEnv(b []byte) ([]string, error) {
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, ArgsVar+"="); ok {
			return strings.Fields(v), nil
		}
	}
	return nil, fmt.Errorf("hostsvc: no %s line", ArgsVar)
}
