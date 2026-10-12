//go:build unix

package devinstance

import (
	"os"
	"syscall"
)

func SpawnDetached(argv []string, logPath string) (int, error) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		return 0, err
	}
	defer null.Close()
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer log.Close()
	p, err := os.StartProcess(argv[0], argv, &os.ProcAttr{Files: []*os.File{null, log, log},
		Sys: &syscall.SysProcAttr{Setsid: true}})
	if err != nil {
		return 0, err
	}
	pid := p.Pid
	p.Release()
	return pid, nil
}
