//go:build linux

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
)

const sock = "/var/tmp/work/hcs.sock"
const hcsvm = "/mnt/c/swc/bin/hcsvm.exe"

type request struct {
	Mode string   `json:"mode"`
	Args []string `json:"args"`
}

func serve(c net.Conn) {
	defer c.Close()
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return
	}
	var r request
	if json.Unmarshal(line, &r) != nil {
		return
	}
	cmd := exec.Command(hcsvm, r.Args...)
	if r.Mode == "stream" {
		cmd.Stdout = c
		cmd.Stderr = c
		cmd.Run()
		return
	}
	out, err := cmd.Output()
	code := 0
	if err != nil {
		code = 1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
	}
	fmt.Fprintf(c, "%d\n", code)
	io.Copy(c, bytesReader(out))
}

type br struct {
	b []byte
	i int
}

func (r *br) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

func bytesReader(b []byte) io.Reader { return &br{b: b} }

func main() {
	os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Chmod(sock, 0o666)
	for {
		c, err := l.Accept()
		if err != nil {
			continue
		}
		go serve(c)
	}
}
