package main

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

var build = "unset"

func main() {
	exe, err := os.Executable()
	link, _ := os.Readlink("/proc/self/exe")
	var st syscall.Stat_t
	_ = syscall.Stat("/proc/self/exe", &st)
	stage := "start"
	if len(os.Args) > 1 {
		stage = os.Args[1]
	}
	fmt.Printf("[%s] build=%s pid=%d os.Executable=%q err=%v /proc/self/exe->%q inode=%d\n", stage, build, os.Getpid(), exe, err, link, st.Ino)
	if stage != "start" {
		return
	}
	fmt.Println("[start] waiting for rename-over signal file")
	for {
		if _, err := os.Stat(os.Getenv("SIGNAL_FILE")); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	exe2, err2 := os.Executable()
	link2, _ := os.Readlink("/proc/self/exe")
	fmt.Printf("[after-rename] os.Executable=%q err=%v /proc/self/exe->%q\n", exe2, err2, link2)
	target := exe2
	if os.Getenv("REEXEC") == "procself" {
		target = "/proc/self/exe"
	}
	fmt.Printf("[after-rename] execve(%q)\n", target)
	if err := syscall.Exec(target, []string{target, "reexec-via-" + os.Getenv("REEXEC")}, os.Environ()); err != nil {
		fmt.Println("exec:", err)
	}
}
