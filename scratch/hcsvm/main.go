//go:build windows

package main

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hcsapi"
)

type Spec struct {
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

func fail(err error) {
	fmt.Fprintln(os.Stderr, "hcsvm:", err)
	os.Exit(1)
}

func footer(size uint64) []byte {
	f := make([]byte, 512)
	copy(f[0:8], "conectix")
	binary.BigEndian.PutUint32(f[8:], 2)
	binary.BigEndian.PutUint32(f[12:], 0x00010000)
	binary.BigEndian.PutUint64(f[16:], 0xFFFFFFFFFFFFFFFF)
	binary.BigEndian.PutUint32(f[24:], uint32(time.Now().Unix()-946684800))
	copy(f[28:32], "win ")
	binary.BigEndian.PutUint32(f[32:], 0x000a0000)
	copy(f[36:40], "Wi2k")
	binary.BigEndian.PutUint64(f[40:], size)
	binary.BigEndian.PutUint64(f[48:], size)
	sectors := size / 512
	if sectors > 65535*16*255 {
		sectors = 65535 * 16 * 255
	}
	var spt, heads, cth uint64
	if sectors >= 65535*16*63 {
		spt, heads, cth = 255, 16, sectors/255
	} else {
		spt = 17
		cth = sectors / spt
		heads = (cth + 1023) / 1024
		if heads < 4 {
			heads = 4
		}
		if cth >= heads*1024 || heads > 16 {
			spt, heads, cth = 31, 16, sectors/31
		}
		if cth >= heads*1024 {
			spt, heads, cth = 63, 16, sectors/63
		}
	}
	cyl := cth / heads
	binary.BigEndian.PutUint16(f[56:], uint16(cyl))
	f[58] = byte(heads)
	f[59] = byte(spt)
	binary.BigEndian.PutUint32(f[60:], 2)
	rand.Read(f[68:84])
	var sum uint32
	for _, b := range f {
		sum += uint32(b)
	}
	binary.BigEndian.PutUint32(f[64:], ^sum)
	return f
}

func copyVHD(src, dst string, growTo int64) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	size := st.Size()
	if growTo > size {
		size = growTo
	}
	size = (size + 511) / 512 * 512
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Truncate(size); err != nil {
		return err
	}
	if _, err := out.WriteAt(footer(uint64(size)), size); err != nil {
		return err
	}
	return out.Sync()
}

func endpointID(id string) string {
	b := []byte(id)
	if len(b) > 0 {
		if b[0] == 'e' {
			b[0] = 'f'
		} else {
			b[0] = 'e'
		}
	}
	return string(b)
}

func controllerID(id string) string {
	b := []byte(id)
	b[len(b)-1] = 'c'
	return string(b)
}

func document(s Spec, rootfs, cidata, ep string) string {
	doc := map[string]any{
		"SchemaVersion":                     map[string]int{"Major": 2, "Minor": 2},
		"Owner":                             s.Owner,
		"ShouldTerminateOnLastHandleClosed": false,
		"VirtualMachine": map[string]any{
			"StopOnReset": true,
			"Chipset": map[string]any{"LinuxKernelDirect": map[string]string{
				"KernelFilePath": s.Kernel,
				"KernelCmdLine":  s.Cmdline,
			}},
			"ComputeTopology": map[string]any{
				"Memory":    map[string]any{"Backing": "Virtual", "SizeInMB": s.MemMiB},
				"Processor": map[string]any{"Count": s.Cpus},
			},
			"Devices": map[string]any{
				"Scsi": map[string]any{controllerID(s.ID): map[string]any{"Attachments": map[string]any{
					"0": map[string]any{"Type": "VirtualDisk", "Path": rootfs},
					"1": map[string]any{"Type": "VirtualDisk", "Path": cidata, "ReadOnly": true},
				}}},
				"NetworkAdapters": map[string]any{"eth0": map[string]string{"EndpointId": ep, "MacAddress": s.MAC}},
				"ComPorts":        map[string]any{"0": map[string]string{"NamedPipe": s.Pipe}},
			},
		},
	}
	raw, _ := json.Marshal(doc)
	return string(raw)
}

func start(specPath string) {
	raw, err := os.ReadFile(specPath)
	if err != nil {
		fail(err)
	}
	var s Spec
	if err := json.Unmarshal(raw, &s); err != nil {
		fail(err)
	}
	if err := os.MkdirAll(s.VMDir, 0o755); err != nil {
		fail(err)
	}
	rootfs := filepath.Join(s.VMDir, "rootfs.vhd")
	cidata := filepath.Join(s.VMDir, "cidata.vhd")
	t0 := time.Now()
	if err := copyVHD(s.RootfsBase, rootfs, 0); err != nil {
		fail(fmt.Errorf("rootfs: %w", err))
	}
	if err := copyVHD(s.Cidata, cidata, 0); err != nil {
		fail(fmt.Errorf("cidata: %w", err))
	}
	fmt.Fprintf(os.Stderr, "disks ready in %s\n", time.Since(t0))
	for _, f := range []string{s.Kernel, rootfs, cidata} {
		if err := hcsapi.GrantVMAccess(s.ID, f); err != nil {
			fail(fmt.Errorf("grant %s: %w", f, err))
		}
	}
	ep := endpointID(s.ID)
	epdoc := map[string]any{
		"SchemaVersion":      map[string]int{"Major": 2, "Minor": 0},
		"HostComputeNetwork": s.NetworkID,
		"Name":               "socialweb-ep-" + s.ID,
		"MacAddress":         s.MAC,
		"IpConfigurations":   []map[string]any{{"IpAddress": s.IP, "PrefixLength": s.Prefix}},
	}
	epraw, _ := json.Marshal(epdoc)
	if err := hcsapi.EndpointCreate(s.NetworkID, ep, string(epraw)); err != nil {
		fail(fmt.Errorf("endpoint: %w", err))
	}
	sys, out, err := hcsapi.Create(s.ID, document(s, rootfs, cidata, ep))
	if err != nil {
		hcsapi.EndpointDelete(ep)
		fail(fmt.Errorf("create: %w %s", err, out))
	}
	defer sys.Close()
	if out, err := sys.Start(""); err != nil {
		sys.Terminate("")
		hcsapi.EndpointDelete(ep)
		fail(fmt.Errorf("start: %w %s", err, out))
	}
	props, _ := sys.Properties("")
	fmt.Println(props)
}

func inspect(id string) {
	sys, err := hcsapi.Open(id)
	if err != nil {
		var he *hcsapi.Error
		if errors.As(err, &he) && he.HR == 0x8037010E {
			fmt.Println(`{"verdict":"Lost","reason":"NotFound"}`)
			return
		}
		fail(err)
	}
	defer sys.Close()
	props, err := sys.Properties(`{"PropertyTypes":[]}`)
	if err != nil {
		fail(err)
	}
	fmt.Println(props)
}

func stop(id string, terminate bool) {
	sys, err := hcsapi.Open(id)
	if err != nil {
		fail(err)
	}
	defer sys.Close()
	var out string
	if terminate {
		out, err = sys.Terminate("")
	} else {
		out, err = sys.Shutdown("")
	}
	if err != nil {
		fail(fmt.Errorf("%w %s", err, out))
	}
	fmt.Println(out)
}

func cleanup(id, vmDir string) {
	hcsapi.EndpointDelete(endpointID(id))
	if vmDir != "" {
		os.RemoveAll(vmDir)
	}
}

func console(pipe string) {
	var f *os.File
	var err error
	deadline := time.Now().Add(2 * time.Minute)
	for {
		f, err = os.OpenFile(pipe, os.O_RDWR, 0)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			fail(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	defer f.Close()
	io.Copy(os.Stdout, f)
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: hcsvm start <spec> | inspect <id> | shutdown <id> | terminate <id> | cleanup <id> [vmDir] | console <pipe>")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "start":
		start(os.Args[2])
	case "inspect":
		inspect(os.Args[2])
	case "shutdown":
		stop(os.Args[2], false)
	case "terminate":
		stop(os.Args[2], true)
	case "cleanup":
		dir := ""
		if len(os.Args) > 3 {
			dir = strings.Join(os.Args[3:], " ")
		}
		cleanup(os.Args[2], dir)
	case "console":
		console(os.Args[2])
	default:
		fmt.Fprintln(os.Stderr, "unknown command", os.Args[1])
		os.Exit(2)
	}
}
