package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"swproof/busdial"
	"swproof/startunit"
)

func main() {
	mode := flag.String("mode", "session", "session|private")
	unit := flag.String("unit", "swproof-vm-1.service", "vm unit")
	argv := flag.String("argv", "/usr/bin/sleep infinity", "vm argv")
	children := flag.Bool("children", true, "also spawn plain children")
	flag.Parse()

	fmt.Printf("bidder pid=%d mode=%s\n", os.Getpid(), *mode)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	conn, err := busdial.Systemd(ctx, *mode, "")
	if err != nil {
		fmt.Println("connect error:", err)
		os.Exit(2)
	}
	t0 := time.Now()
	res, err := startunit.Start(ctx, conn, *unit, strings.Fields(*argv), map[string]string{})
	fmt.Printf("StartTransientUnit %s result=%q err=%v took=%s\n", *unit, res, err, time.Since(t0))
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		fmt.Println("note: unit start failed or already exists")
	}
	cancel()
	conn.Close()

	if *children {
		pd := exec.Command("/usr/bin/sleep", "infinity")
		pd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL}
		if err := pd.Start(); err != nil {
			fmt.Println("pdeathsig child:", err)
		} else {
			fmt.Printf("plain child setsid+pdeathsig pid=%d\n", pd.Process.Pid)
		}
		ss := exec.Command("/usr/bin/sleep", "infinity")
		ss.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := ss.Start(); err != nil {
			fmt.Println("setsid child:", err)
		} else {
			fmt.Printf("plain child setsid-only pid=%d\n", ss.Process.Pid)
		}
	}
	select {}
}
