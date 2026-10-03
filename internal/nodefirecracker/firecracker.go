package nodefirecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type Client struct {
	sock string
	http *http.Client
}

func NewClient(sock string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", sock)
		},
		DisableKeepAlives: true,
	}
	return &Client{sock: sock, http: &http.Client{Transport: transport, Timeout: 30 * time.Second}}
}

func (c *Client) WaitForSocket(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", c.sock, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		last = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return fmt.Errorf("nodefirecracker: the firecracker API socket %s never accepted a connection "+
		"within %s (%v). The socket file existing is not the same fact as the VMM listening on it -- "+
		"a socket left behind by an earlier run of this node is a path that dials nowhere -- so "+
		"nothing may be configured against this VMM until a dial succeeds", c.sock, timeout, last)
}

func (c *Client) Put(ctx context.Context, path string, body any) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("nodefirecracker: encoding the body for PUT %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost"+path, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("nodefirecracker: building the request for PUT %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nodefirecracker: PUT %s over %s: %w -- the VMM answers its API on a "+
			"unix socket, so a failure here is the VMM gone or the socket renamed, not a network "+
			"problem, and the guest behind it was never configured", path, c.sock, err)
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return reply, fmt.Errorf("nodefirecracker: PUT %s answered HTTP %d: %s",
			path, resp.StatusCode, string(bytes.TrimSpace(reply)))
	}
	return reply, nil
}

func (c *Client) MachineConfig(ctx context.Context, vcpu, memMiB int, smt bool) error {
	_, err := c.Put(ctx, "/machine-config", map[string]any{
		"vcpu_count": vcpu, "mem_size_mib": memMiB, "smt": smt,
	})
	return err
}

func (c *Client) BootSource(ctx context.Context, kernel, bootArgs, initramfs string) error {
	body := map[string]any{"kernel_image_path": kernel, "boot_args": bootArgs}
	if initramfs != "" {
		body["initrd_path"] = initramfs
	}
	_, err := c.Put(ctx, "/boot-source", body)
	return err
}

func (c *Client) Drive(ctx context.Context, id, path string, root, readOnly bool) error {
	_, err := c.Put(ctx, "/drives/"+id, map[string]any{
		"drive_id": id, "path_on_host": path,
		"is_root_device": root, "is_read_only": readOnly,
	})
	return err
}

func (c *Client) NetworkInterface(ctx context.Context, id, hostDevice, guestMAC string) error {
	_, err := c.Put(ctx, "/network-interfaces/"+id, map[string]any{
		"iface_id": id, "host_dev_name": hostDevice, "guest_mac": guestMAC,
	})
	return err
}

func (c *Client) InstanceStart(ctx context.Context) error {
	_, err := c.Put(ctx, "/actions", map[string]any{"action_type": "InstanceStart"})
	return err
}
