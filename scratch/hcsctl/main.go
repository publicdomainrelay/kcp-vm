//go:build windows

package main

import (
	"fmt"
	"os"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hcsapi"
)

func readArg(s string) string {
	if len(s) > 0 && s[0] == '@' {
		b, err := os.ReadFile(s[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "read", s, err)
			os.Exit(2)
		}
		return string(b)
	}
	return s
}

func arg(i int) string {
	if len(os.Args) > i {
		return readArg(os.Args[i])
	}
	return ""
}

func report(label string, out string, err error) {
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if err != nil {
		fmt.Printf("%s %s ERR %v\n", ts, label, err)
		if out != "" {
			fmt.Println(out)
		}
		os.Exit(1)
	}
	fmt.Printf("%s %s OK\n", ts, label)
	if out != "" {
		fmt.Println(out)
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: hcsctl enum|props|create|createstart|open|start|shutdown|terminate|grant|revoke|netcreate|netdelete|netenum|netprops|epcreate|epdelete|epenum|epprops")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "enum":
		out, err := hcsapi.Enumerate(arg(2))
		report("enum", out, err)
	case "props":
		s, err := hcsapi.Open(arg(2))
		if err != nil {
			report("open", "", err)
		}
		defer s.Close()
		out, err := s.Properties(arg(3))
		report("props", out, err)
	case "open":
		s, err := hcsapi.Open(arg(2))
		if err == nil {
			s.Close()
		}
		report("open", "", err)
	case "create", "createstart":
		s, out, err := hcsapi.Create(arg(2), arg(3))
		report("create", out, err)
		if os.Args[1] == "createstart" {
			out, err = s.Start("")
			report("start", out, err)
		}
		s.Close()
	case "start", "shutdown", "terminate":
		s, err := hcsapi.Open(arg(2))
		if err != nil {
			report("open", "", err)
		}
		defer s.Close()
		var out string
		switch os.Args[1] {
		case "start":
			out, err = s.Start(arg(3))
		case "shutdown":
			out, err = s.Shutdown(arg(3))
		default:
			out, err = s.Terminate(arg(3))
		}
		report(os.Args[1], out, err)
	case "grant":
		report("grant", "", hcsapi.GrantVMAccess(arg(2), arg(3)))
	case "revoke":
		report("revoke", "", hcsapi.RevokeVMAccess(arg(2), arg(3)))
	case "netcreate":
		report("netcreate", "", hcsapi.NetworkCreate(arg(2), arg(3)))
	case "netdelete":
		report("netdelete", "", hcsapi.NetworkDelete(arg(2)))
	case "netenum":
		out, err := hcsapi.NetworkEnumerate(arg(2))
		report("netenum", out, err)
	case "netprops":
		out, err := hcsapi.NetworkProperties(arg(2), arg(3))
		report("netprops", out, err)
	case "epcreate":
		report("epcreate", "", hcsapi.EndpointCreate(arg(2), arg(3), arg(4)))
	case "epdelete":
		report("epdelete", "", hcsapi.EndpointDelete(arg(2)))
	case "epenum":
		out, err := hcsapi.EndpointEnumerate(arg(2))
		report("epenum", out, err)
	case "epprops":
		out, err := hcsapi.EndpointProperties(arg(2), arg(3))
		report("epprops", out, err)
	default:
		fmt.Fprintln(os.Stderr, "unknown command", os.Args[1])
		os.Exit(2)
	}
}
