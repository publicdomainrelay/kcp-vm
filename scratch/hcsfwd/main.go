//go:build windows

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

var portRE = regexp.MustCompile(`127\.0\.0\.1:(\d+)\s`)

func forward(listen, target string) {
	l, err := net.Listen("tcp", listen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s hcsfwd listen %s: %v\n", time.Now().UTC().Format(time.RFC3339), listen, err)
		return
	}
	fmt.Fprintf(os.Stderr, "%s hcsfwd %s -> %s\n", time.Now().UTC().Format(time.RFC3339), listen, target)
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			u, err := net.DialTimeout("tcp", target, 5*time.Second)
			if err != nil {
				return
			}
			defer u.Close()
			done := make(chan struct{}, 2)
			go func() { io.Copy(u, c); done <- struct{}{} }()
			go func() { io.Copy(c, u); done <- struct{}{} }()
			<-done
		}()
	}
}

func main() {
	addr := "192.0.2.1"
	iface := "vEthernet (socialweb-nat0)"
	distro := "socialweb-ubuntu"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	out, err := exec.Command("netsh", "interface", "ip", "add", "address", iface, addr, "255.255.255.255").CombinedOutput()
	fmt.Fprintf(os.Stderr, "netsh add address: %v %s\n", err, strings.TrimSpace(string(out)))
	seen := map[string]bool{}
	var mu sync.Mutex
	for {
		out, err := exec.Command("wsl.exe", "-d", distro, "-u", "test-0001", "--", "ss", "-ltnH").Output()
		if err == nil {
			sc := bufio.NewScanner(bytes.NewReader(bytes.ReplaceAll(out, []byte("\x00"), nil)))
			for sc.Scan() {
				m := portRE.FindStringSubmatch(sc.Text() + " ")
				if m == nil {
					continue
				}
				mu.Lock()
				if !seen[m[1]] {
					seen[m[1]] = true
					go forward(addr+":"+m[1], "127.0.0.1:"+m[1])
				}
				mu.Unlock()
			}
		}
		time.Sleep(1 * time.Second)
	}
}
