//go:build windows

package hcsapi

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	computecore    = windows.NewLazySystemDLL("computecore.dll")
	computenetwork = windows.NewLazySystemDLL("computenetwork.dll")

	pCreateOperation  = computecore.NewProc("HcsCreateOperation")
	pCloseOperation   = computecore.NewProc("HcsCloseOperation")
	pWaitForOperation = computecore.NewProc("HcsWaitForOperationResult")
	pCreateSystem     = computecore.NewProc("HcsCreateComputeSystem")
	pOpenSystem       = computecore.NewProc("HcsOpenComputeSystem")
	pCloseSystem      = computecore.NewProc("HcsCloseComputeSystem")
	pStartSystem      = computecore.NewProc("HcsStartComputeSystem")
	pShutDownSystem   = computecore.NewProc("HcsShutDownComputeSystem")
	pTerminateSystem  = computecore.NewProc("HcsTerminateComputeSystem")
	pEnumerate        = computecore.NewProc("HcsEnumerateComputeSystems")
	pGetProperties    = computecore.NewProc("HcsGetComputeSystemProperties")
	pGrantVMAccess    = computecore.NewProc("HcsGrantVmAccess")
	pRevokeVMAccess   = computecore.NewProc("HcsRevokeVmAccess")

	pHcnCreateNetwork     = computenetwork.NewProc("HcnCreateNetwork")
	pHcnOpenNetwork       = computenetwork.NewProc("HcnOpenNetwork")
	pHcnCloseNetwork      = computenetwork.NewProc("HcnCloseNetwork")
	pHcnDeleteNetwork     = computenetwork.NewProc("HcnDeleteNetwork")
	pHcnEnumerateNetworks = computenetwork.NewProc("HcnEnumerateNetworks")
	pHcnQueryNetwork      = computenetwork.NewProc("HcnQueryNetworkProperties")
	pHcnCreateEndpoint    = computenetwork.NewProc("HcnCreateEndpoint")
	pHcnOpenEndpoint      = computenetwork.NewProc("HcnOpenEndpoint")
	pHcnCloseEndpoint     = computenetwork.NewProc("HcnCloseEndpoint")
	pHcnDeleteEndpoint    = computenetwork.NewProc("HcnDeleteEndpoint")
	pHcnEnumerateEndpts   = computenetwork.NewProc("HcnEnumerateEndpoints")
	pHcnQueryEndpoint     = computenetwork.NewProc("HcnQueryEndpointProperties")
)

const (
	GenericAll  = 0x10000000
	waitForever = 0xFFFFFFFF
)

type Error struct {
	Call   string
	HR     uint32
	Detail string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: HRESULT 0x%08X (%s) %s", e.Call, e.HR, windows.Errno(e.HR&0xFFFF).Error(), e.Detail)
}

func failed(hr uintptr) bool { return int32(hr) < 0 }

func takeString(p *uint16) string {
	if p == nil {
		return ""
	}
	s := windows.UTF16PtrToString(p)
	windows.LocalFree(windows.Handle(unsafe.Pointer(p)))
	return s
}

func utf16(s string) *uint16 {
	if s == "" {
		return nil
	}
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

func operate(name string, call func(op uintptr) uintptr) (string, error) {
	op, _, _ := pCreateOperation.Call(0, 0)
	if op == 0 {
		return "", &Error{Call: name + "/HcsCreateOperation"}
	}
	defer pCloseOperation.Call(op)
	hr := call(op)
	var res *uint16
	whr, _, _ := pWaitForOperation.Call(op, waitForever, uintptr(unsafe.Pointer(&res)))
	out := takeString(res)
	if failed(hr) {
		return out, &Error{Call: name, HR: uint32(hr), Detail: out}
	}
	if failed(whr) {
		return out, &Error{Call: name + "/wait", HR: uint32(whr), Detail: out}
	}
	return out, nil
}

type System uintptr

func Create(id, config string) (System, string, error) {
	var sys System
	out, err := operate("HcsCreateComputeSystem", func(op uintptr) uintptr {
		hr, _, _ := pCreateSystem.Call(uintptr(unsafe.Pointer(utf16(id))), uintptr(unsafe.Pointer(utf16(config))), op, 0, uintptr(unsafe.Pointer(&sys)))
		return hr
	})
	return sys, out, err
}

func Open(id string) (System, error) {
	var sys System
	hr, _, _ := pOpenSystem.Call(uintptr(unsafe.Pointer(utf16(id))), GenericAll, uintptr(unsafe.Pointer(&sys)))
	if failed(hr) {
		return 0, &Error{Call: "HcsOpenComputeSystem", HR: uint32(hr)}
	}
	return sys, nil
}

func (s System) Close() { pCloseSystem.Call(uintptr(s)) }

func (s System) Start(options string) (string, error) {
	return operate("HcsStartComputeSystem", func(op uintptr) uintptr {
		hr, _, _ := pStartSystem.Call(uintptr(s), op, uintptr(unsafe.Pointer(utf16(options))))
		return hr
	})
}

func (s System) Shutdown(options string) (string, error) {
	return operate("HcsShutDownComputeSystem", func(op uintptr) uintptr {
		hr, _, _ := pShutDownSystem.Call(uintptr(s), op, uintptr(unsafe.Pointer(utf16(options))))
		return hr
	})
}

func (s System) Terminate(options string) (string, error) {
	return operate("HcsTerminateComputeSystem", func(op uintptr) uintptr {
		hr, _, _ := pTerminateSystem.Call(uintptr(s), op, uintptr(unsafe.Pointer(utf16(options))))
		return hr
	})
}

func (s System) Properties(query string) (string, error) {
	return operate("HcsGetComputeSystemProperties", func(op uintptr) uintptr {
		hr, _, _ := pGetProperties.Call(uintptr(s), op, uintptr(unsafe.Pointer(utf16(query))))
		return hr
	})
}

func Enumerate(query string) (string, error) {
	return operate("HcsEnumerateComputeSystems", func(op uintptr) uintptr {
		hr, _, _ := pEnumerate.Call(uintptr(unsafe.Pointer(utf16(query))), op)
		return hr
	})
}

func GrantVMAccess(id, path string) error {
	hr, _, _ := pGrantVMAccess.Call(uintptr(unsafe.Pointer(utf16(id))), uintptr(unsafe.Pointer(utf16(path))))
	if failed(hr) {
		return &Error{Call: "HcsGrantVmAccess", HR: uint32(hr)}
	}
	return nil
}

func RevokeVMAccess(id, path string) error {
	hr, _, _ := pRevokeVMAccess.Call(uintptr(unsafe.Pointer(utf16(id))), uintptr(unsafe.Pointer(utf16(path))))
	if failed(hr) {
		return &Error{Call: "HcsRevokeVmAccess", HR: uint32(hr)}
	}
	return nil
}

func hcnErr(name string, hr uintptr, rec *uint16) error {
	return &Error{Call: name, HR: uint32(hr), Detail: takeString(rec)}
}

func guid(s string) (windows.GUID, error) { return windows.GUIDFromString("{" + trimBraces(s) + "}") }

func trimBraces(s string) string {
	if len(s) > 1 && s[0] == '{' {
		return s[1 : len(s)-1]
	}
	return s
}

func NetworkCreate(id, settings string) error {
	g, err := guid(id)
	if err != nil {
		return err
	}
	var h, rec uintptr
	hr, _, _ := pHcnCreateNetwork.Call(uintptr(unsafe.Pointer(&g)), uintptr(unsafe.Pointer(utf16(settings))), uintptr(unsafe.Pointer(&h)), uintptr(unsafe.Pointer(&rec)))
	if failed(hr) {
		return hcnErr("HcnCreateNetwork", hr, (*uint16)(unsafe.Pointer(rec)))
	}
	pHcnCloseNetwork.Call(h)
	return nil
}

func NetworkDelete(id string) error {
	g, err := guid(id)
	if err != nil {
		return err
	}
	var rec uintptr
	hr, _, _ := pHcnDeleteNetwork.Call(uintptr(unsafe.Pointer(&g)), uintptr(unsafe.Pointer(&rec)))
	if failed(hr) {
		return hcnErr("HcnDeleteNetwork", hr, (*uint16)(unsafe.Pointer(rec)))
	}
	return nil
}

func NetworkEnumerate(query string) (string, error) {
	var out, rec uintptr
	hr, _, _ := pHcnEnumerateNetworks.Call(uintptr(unsafe.Pointer(utf16(query))), uintptr(unsafe.Pointer(&out)), uintptr(unsafe.Pointer(&rec)))
	if failed(hr) {
		return "", hcnErr("HcnEnumerateNetworks", hr, (*uint16)(unsafe.Pointer(rec)))
	}
	return takeString((*uint16)(unsafe.Pointer(out))), nil
}

func NetworkProperties(id, query string) (string, error) {
	g, err := guid(id)
	if err != nil {
		return "", err
	}
	var h, out, rec uintptr
	hr, _, _ := pHcnOpenNetwork.Call(uintptr(unsafe.Pointer(&g)), uintptr(unsafe.Pointer(&h)), uintptr(unsafe.Pointer(&rec)))
	if failed(hr) {
		return "", hcnErr("HcnOpenNetwork", hr, (*uint16)(unsafe.Pointer(rec)))
	}
	defer pHcnCloseNetwork.Call(h)
	hr, _, _ = pHcnQueryNetwork.Call(h, uintptr(unsafe.Pointer(utf16(query))), uintptr(unsafe.Pointer(&out)), uintptr(unsafe.Pointer(&rec)))
	if failed(hr) {
		return "", hcnErr("HcnQueryNetworkProperties", hr, (*uint16)(unsafe.Pointer(rec)))
	}
	return takeString((*uint16)(unsafe.Pointer(out))), nil
}

func EndpointCreate(networkID, id, settings string) error {
	ng, err := guid(networkID)
	if err != nil {
		return err
	}
	eg, err := guid(id)
	if err != nil {
		return err
	}
	var nh, eh, rec uintptr
	hr, _, _ := pHcnOpenNetwork.Call(uintptr(unsafe.Pointer(&ng)), uintptr(unsafe.Pointer(&nh)), uintptr(unsafe.Pointer(&rec)))
	if failed(hr) {
		return hcnErr("HcnOpenNetwork", hr, (*uint16)(unsafe.Pointer(rec)))
	}
	defer pHcnCloseNetwork.Call(nh)
	hr, _, _ = pHcnCreateEndpoint.Call(nh, uintptr(unsafe.Pointer(&eg)), uintptr(unsafe.Pointer(utf16(settings))), uintptr(unsafe.Pointer(&eh)), uintptr(unsafe.Pointer(&rec)))
	if failed(hr) {
		return hcnErr("HcnCreateEndpoint", hr, (*uint16)(unsafe.Pointer(rec)))
	}
	pHcnCloseEndpoint.Call(eh)
	return nil
}

func EndpointDelete(id string) error {
	g, err := guid(id)
	if err != nil {
		return err
	}
	var rec uintptr
	hr, _, _ := pHcnDeleteEndpoint.Call(uintptr(unsafe.Pointer(&g)), uintptr(unsafe.Pointer(&rec)))
	if failed(hr) {
		return hcnErr("HcnDeleteEndpoint", hr, (*uint16)(unsafe.Pointer(rec)))
	}
	return nil
}

func EndpointEnumerate(query string) (string, error) {
	var out, rec uintptr
	hr, _, _ := pHcnEnumerateEndpts.Call(uintptr(unsafe.Pointer(utf16(query))), uintptr(unsafe.Pointer(&out)), uintptr(unsafe.Pointer(&rec)))
	if failed(hr) {
		return "", hcnErr("HcnEnumerateEndpoints", hr, (*uint16)(unsafe.Pointer(rec)))
	}
	return takeString((*uint16)(unsafe.Pointer(out))), nil
}

func EndpointProperties(id, query string) (string, error) {
	g, err := guid(id)
	if err != nil {
		return "", err
	}
	var h, out, rec uintptr
	hr, _, _ := pHcnOpenEndpoint.Call(uintptr(unsafe.Pointer(&g)), uintptr(unsafe.Pointer(&h)), uintptr(unsafe.Pointer(&rec)))
	if failed(hr) {
		return "", hcnErr("HcnOpenEndpoint", hr, (*uint16)(unsafe.Pointer(rec)))
	}
	defer pHcnCloseEndpoint.Call(h)
	hr, _, _ = pHcnQueryEndpoint.Call(h, uintptr(unsafe.Pointer(utf16(query))), uintptr(unsafe.Pointer(&out)), uintptr(unsafe.Pointer(&rec)))
	if failed(hr) {
		return "", hcnErr("HcnQueryEndpointProperties", hr, (*uint16)(unsafe.Pointer(rec)))
	}
	return takeString((*uint16)(unsafe.Pointer(out))), nil
}
