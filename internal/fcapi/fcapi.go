package fcapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

type BootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args"`
}

type Drive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

type MachineConfig struct {
	VcpuCount  int  `json:"vcpu_count"`
	MemSizeMiB int  `json:"mem_size_mib"`
	SMT        bool `json:"smt"`
}

type NetworkInterface struct {
	IfaceID     string `json:"iface_id"`
	HostDevName string `json:"host_dev_name"`
	GuestMAC    string `json:"guest_mac"`
}

type Config struct {
	BootSource        BootSource         `json:"boot-source"`
	Drives            []Drive            `json:"drives"`
	MachineConfig     MachineConfig      `json:"machine-config"`
	NetworkInterfaces []NetworkInterface `json:"network-interfaces"`
}

func WriteConfig(path string, c Config) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

func Argv(firecracker, vmmID, configFile, socket string) []string {
	return []string{firecracker, "--id", vmmID, "--config-file", configFile, "--api-sock", socket}
}

type Instance struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	VMMVersion string `json:"vmm_version"`
	AppName    string `json:"app_name"`
}

const StateRunning = "Running"

type Client struct {
	socket string
	http   *http.Client
}

func NewClient(socket string, timeout time.Duration) *Client {
	t := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		DisableKeepAlives: true,
	}
	return &Client{socket: socket, http: &http.Client{Transport: t, Timeout: timeout}}
}

func (c *Client) Describe(ctx context.Context) (Instance, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/", nil)
	if err != nil {
		return Instance{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Instance{}, fmt.Errorf("fcapi: GET / over %s: %w", c.socket, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Instance{}, fmt.Errorf("fcapi: GET / answered %s: %s", resp.Status, bytes.TrimSpace(body))
	}
	var in Instance
	if err := json.Unmarshal(body, &in); err != nil {
		return Instance{}, fmt.Errorf("fcapi: GET /: %w", err)
	}
	return in, nil
}

func (c *Client) SendCtrlAltDel(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost/actions",
		bytes.NewReader([]byte(`{"action_type":"SendCtrlAltDel"}`)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("fcapi: PUT /actions SendCtrlAltDel over %s: %w", c.socket, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("fcapi: SendCtrlAltDel answered %s: %s", resp.Status, bytes.TrimSpace(body))
	}
	return nil
}
