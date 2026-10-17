//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/fcapi"
)

const confPath = "/var/tmp/work/hcs-shim.json"
const bridgeSock = "/var/tmp/work/hcs.sock"

type conf struct {
	Distro     string `json:"distro"`
	Kernel     string `json:"kernel"`
	Cmdline    string `json:"cmdline"`
	RootfsBase string `json:"rootfsBase"`
	NetworkID  string `json:"networkId"`
	VMRoot     string `json:"vmRoot"`
	Owner      string `json:"owner"`
	IP         string `json:"ip"`
	Prefix     int    `json:"prefix"`
}

type spec struct {
	ID         string `json:"id"`
	Owner      string `json:"owner"`
	Kernel     string `json:"kernel"`
	Cmdline    string `json:"cmdline"`
	Cpus       int    `json:"cpus"`
	MemMiB     int    `json:"memMiB"`
	RootfsBase string `json:"rootfsBase"`
	Cidata     string `json:"cidata"`
	NetworkID  string `json:"networkId"`
	MAC        string `json:"mac"`
	IP         string `json:"ip"`
	Prefix     int    `json:"prefix"`
	VMDir      string `json:"vmDir"`
	Pipe       string `json:"pipe"`
}

func guidOf(s string) string {
	h := sha256.Sum256([]byte(s))
	b := h[:16]
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "fcshim: "+format+"\n", a...)
	os.Exit(1)
}

func dial(mode string, args ...string) (net.Conn, error) {
	c, err := net.Dial("unix", bridgeSock)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(map[string]any{"mode": mode, "args": args})
	_, err = c.Write(append(raw, '\n'))
	return c, err
}

func hcs(args ...string) ([]byte, error) {
	c, err := dial("run", args...)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	all, _ := io.ReadAll(c)
	line, rest, _ := strings.Cut(string(all), "\n")
	if line != "0" {
		return []byte(rest), fmt.Errorf("hcsvm %v exited %q: %s", args, line, rest)
	}
	return []byte(rest), nil
}

func main() {
	var id, cfgFile, sock string
	for i := 1; i < len(os.Args)-1; i++ {
		switch os.Args[i] {
		case "--id":
			id = os.Args[i+1]
		case "--config-file":
			cfgFile = os.Args[i+1]
		case "--api-sock":
			sock = os.Args[i+1]
		}
	}
	if id == "" || cfgFile == "" || sock == "" {
		die("need --id, --config-file and --api-sock")
	}
	var c conf
	raw, err := os.ReadFile(confPath)
	if err != nil {
		die("%v", err)
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		die("%v", err)
	}
	var fc fcapi.Config
	raw, err = os.ReadFile(cfgFile)
	if err != nil {
		die("%v", err)
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		die("%v", err)
	}
	var cidata string
	for _, d := range fc.Drives {
		if d.DriveID == "cidata" {
			cidata = d.PathOnHost
		}
	}
	guid := guidOf(id)
	mac := strings.ToUpper(strings.ReplaceAll(fc.NetworkInterfaces[0].GuestMAC, ":", "-"))
	s := spec{
		ID: guid, Owner: c.Owner, Kernel: c.Kernel, Cmdline: c.Cmdline, Cpus: fc.MachineConfig.VcpuCount, MemMiB: fc.MachineConfig.MemSizeMiB,
		RootfsBase: c.RootfsBase, Cidata: `\\wsl.localhost\` + c.Distro + strings.ReplaceAll(cidata, "/", `\`), NetworkID: c.NetworkID,
		MAC: mac, IP: c.IP, Prefix: c.Prefix, VMDir: c.VMRoot + `\` + guid, Pipe: `\\.\pipe\socialweb-vm-` + guid + `-com1`,
	}
	work := filepath.Dir(sock)
	specFile := filepath.Join(work, "hcs-spec.json")
	raw, _ = json.Marshal(s)
	if err := os.WriteFile(specFile, raw, 0o600); err != nil {
		die("%v", err)
	}
	winSpec := `\\wsl.localhost\` + c.Distro + strings.ReplaceAll(specFile, "/", `\`)
	started := time.Now()
	out, err := hcs("start", winSpec)
	if err != nil {
		die("hcsvm start: %v: %s", err, out)
	}
	fmt.Fprintf(os.Stderr, "fcshim: hcs system %s started in %s: %s\n", guid, time.Since(started), strings.TrimSpace(string(out)))
	os.WriteFile(filepath.Join(work, "hcs.json"), []byte(guid+"\n"), 0o600)

	go func() {
		cc, err := dial("stream", "console", s.Pipe)
		if err != nil {
			return
		}
		io.Copy(os.Stdout, cc)
	}()

	var mu sync.Mutex
	state := "Running"
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		json.NewEncoder(w).Encode(fcapi.Instance{ID: id, State: state, VMMVersion: "hcs-shim", AppName: "firecracker"})
	})
	mux.HandleFunc("/actions", func(w http.ResponseWriter, r *http.Request) {
		go func() {
			hcs("shutdown", guid)
			time.Sleep(3 * time.Second)
			hcs("terminate", guid)
		}()
		w.WriteHeader(http.StatusNoContent)
	})
	os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		die("%v", err)
	}
	go http.Serve(l, mux)

	for {
		time.Sleep(1 * time.Second)
		out, err := hcs("inspect", guid)
		if err != nil {
			continue
		}
		if strings.Contains(string(out), `"Lost"`) {
			break
		}
		if strings.Contains(string(out), `"State":"Stopped"`) || strings.Contains(string(out), `"Stopped":true`) {
			break
		}
	}
	hcs("cleanup", guid, s.VMDir)
	fmt.Fprintf(os.Stderr, "fcshim: hcs system %s gone\n", guid)
}
