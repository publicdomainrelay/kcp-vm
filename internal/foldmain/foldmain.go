package foldmain

import (
	"io"
	"net"
	"os"
	"path/filepath"
)

type Invocation struct {
	Args   []string
	Getenv func(string) string
	Stdout io.Writer
	Stderr io.Writer
	Exe    string
	Verb   []string
	Listen func(name string, addr net.Addr)
}

func OS() Invocation {
	exe, _ := ResolveExe()
	return Invocation{Args: os.Args[1:], Getenv: os.Getenv, Stdout: os.Stdout, Stderr: os.Stderr, Exe: exe}
}

func ResolveExe() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return Resolve(p)
}

func Resolve(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func (i Invocation) MultiCall() bool { return len(i.Verb) > 0 }

func (i Invocation) Self() []string {
	return append([]string{i.Exe}, i.Verb...)
}

func (i Invocation) Reexec(verb ...string) []string {
	if !i.MultiCall() || i.Exe == "" {
		return nil
	}
	return append([]string{i.Exe}, verb...)
}

func (i Invocation) Env(name string) string {
	if i.Getenv == nil {
		return ""
	}
	return i.Getenv(name)
}

func (i Invocation) Listened(name string, addr net.Addr) {
	if i.Listen != nil {
		i.Listen(name, addr)
	}
}
