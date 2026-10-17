//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hcsapi"
)

const (
	systemID  = "6f1c1a2e-0000-4000-8000-0000000000b1"
	networkID = "6f1c1a2e-0000-4000-8000-0000000000c1"
	endpoint  = "6f1c1a2e-0000-4000-8000-0000000000d1"
	kernelSrc = `C:\swc\probe\kernel`
)

type battery struct {
	out   *os.File
	label string
}

func (b *battery) line(step string, err error, detail string) {
	status := "OK"
	if err != nil {
		status = "ERR " + err.Error()
	}
	detail = strings.ReplaceAll(detail, "\n", " ")
	if len(detail) > 300 {
		detail = detail[:300]
	}
	fmt.Fprintf(b.out, "%s\t%s\t%s\t%s\n", time.Now().UTC().Format(time.RFC3339), step, status, detail)
	b.out.Sync()
}

func identity() string {
	t := windows.GetCurrentProcessToken()
	u, err := t.GetTokenUser()
	if err != nil {
		return "unknown"
	}
	acct, _, _, _ := u.User.Sid.LookupAccount("")
	groups, _ := t.GetTokenGroups()
	var names []string
	for _, g := range groups.AllGroups() {
		a, d, _, _ := g.Sid.LookupAccount("")
		names = append(names, d+`\`+a)
	}
	return fmt.Sprintf("user=%s sid=%s elevated=%v groups=%s", acct, u.User.Sid.String(), t.IsElevated(), strings.Join(names, ";"))
}

func (b *battery) run() {
	b.line("identity", nil, identity())
	kernel := `C:\swc\probe\` + b.label + `\kernel`
	data, rerr := os.ReadFile(kernelSrc)
	if rerr == nil {
		rerr = os.WriteFile(kernel, data, 0o666)
	}
	b.line("copy kernel into own dir", rerr, kernel)
	out, err := hcsapi.Enumerate("")
	b.line("HcsEnumerateComputeSystems", err, out)
	b.line("HcsGrantVmAccess(kernel)", hcsapi.GrantVMAccess(systemID, kernel), "")
	doc := `{"SchemaVersion":{"Major":2,"Minor":2},"Owner":"socialweb-probe-` + b.label + `","ShouldTerminateOnLastHandleClosed":false,"VirtualMachine":{"StopOnReset":true,"Chipset":{"LinuxKernelDirect":{"KernelFilePath":"` + strings.ReplaceAll(kernel, `\`, `\\`) + `","KernelCmdLine":"console=ttyS0 panic=1"}},"ComputeTopology":{"Memory":{"Backing":"Virtual","SizeInMB":256},"Processor":{"Count":1}}}}`
	s, out, err := hcsapi.Create(systemID, doc)
	b.line("HcsCreateComputeSystem", err, out)
	if err == nil {
		s.Close()
	}
	s2, err := hcsapi.Open(systemID)
	b.line("HcsOpenComputeSystem", err, "")
	if err == nil {
		out, err = s2.Properties("")
		b.line("HcsGetComputeSystemProperties", err, out)
		out, err = s2.Terminate("")
		b.line("HcsTerminateComputeSystem", err, out)
		s2.Close()
	}
	b.line("HcsRevokeVmAccess(kernel)", hcsapi.RevokeVMAccess(systemID, kernel), "")
	out, err = hcsapi.Enumerate(`{"Owners":["socialweb-probe-` + b.label + `"]}`)
	b.line("HcsEnumerateComputeSystems(owner)", err, out)

	out, err = hcsapi.NetworkEnumerate("")
	b.line("HcnEnumerateNetworks", err, out)
	net := `{"SchemaVersion":{"Major":2,"Minor":0},"Name":"socialweb-probe-` + b.label + `","Type":"Private","Ipams":[{"Type":"Static","Subnets":[{"IpAddressPrefix":"10.250.0.0/24"}]}],"Flags":0}`
	err = hcsapi.NetworkCreate(networkID, net)
	b.line("HcnCreateNetwork(Private)", err, "")
	if err == nil {
		ep := `{"SchemaVersion":{"Major":2,"Minor":0},"HostComputeNetwork":"` + networkID + `","Name":"socialweb-probe-ep","MacAddress":"06-00-AC-1E-00-02","IpConfigurations":[{"IpAddress":"10.250.0.5"}]}`
		err = hcsapi.EndpointCreate(networkID, endpoint, ep)
		b.line("HcnCreateEndpoint", err, "")
		if err == nil {
			b.line("HcnDeleteEndpoint", hcsapi.EndpointDelete(endpoint), "")
		}
		b.line("HcnDeleteNetwork", hcsapi.NetworkDelete(networkID), "")
	}
	out, err = hcsapi.NetworkEnumerate("")
	b.line("HcnEnumerateNetworks(after)", err, out)
}

type service struct{ b *battery }

func (s *service) Execute(args []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop}
	s.b.run()
	status <- svc.Status{State: svc.StopPending}
	return false, 0
}

func main() {
	label := "x"
	outPath := `C:\swc\probe-results\x.txt`
	if len(os.Args) >= 3 {
		label, outPath = os.Args[1], os.Args[2]
	}
	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o666)
	if err != nil {
		os.Exit(3)
	}
	defer f.Close()
	b := &battery{out: f, label: label}
	if is, _ := svc.IsWindowsService(); is {
		svc.Run("socialweb-hcsprobe", &service{b: b})
		return
	}
	b.run()
}
