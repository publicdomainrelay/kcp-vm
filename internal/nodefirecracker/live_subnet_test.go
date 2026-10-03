//go:build linux

package nodefirecracker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/subnetallocator"
)

func liveProbeConfig(t *testing.T) (string, string, string) {
	t.Helper()
	livegate.Short(t, "this test boots a real firecracker guest and -short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	image, firecracker, agent := os.Getenv(liveImageEnv), os.Getenv(liveBinEnv), os.Getenv(liveAgentEnv)
	if image == "" {
		livegate.Require(t, "set %s, %s and %s to a built node image, a firecracker binary and the guest's "+
			"agent; this test boots ONE guest against nothing at all and reads the guest's own "+
			"console for what happened to the connection it opened to its cluster's gateway",
			liveImageEnv, liveBinEnv, liveAgentEnv)
	}
	return image, firecracker, agent
}

func liveLeasedNetwork(t *testing.T, cluster, iface string) (Network, subnetallocator.Lease) {
	t.Helper()
	nets, err := subnetallocator.Open(subnetallocator.Config{
		LeasePath: filepath.Join(t.TempDir(), "networks.json"),
	})
	if err != nil {
		t.Fatalf("subnetallocator.Open: %v", err)
	}
	lease, err := nets.Allocate(subnetallocator.Request{Cluster: cluster})
	if err != nil {
		t.Fatalf("the range %s is addressed in: %v", cluster, err)
	}
	address, err := lease.Host(2)
	if err != nil {
		t.Fatalf("the first node's address in %s: %v", lease, err)
	}
	return Network{
		Gateway:   lease.Gateway().String(),
		GuestIP:   address.String(),
		Prefix:    lease.PrefixBits(),
		PastaIP:   lease.Block.Masked().Addr().String(),
		GuestMAC:  fmt.Sprintf("06:00:%02x:%02x:%02x:%02x", address.As4()[0], address.As4()[1], address.As4()[2], address.As4()[3]),
		Interface: iface,
	}, lease
}

func TestLiveTheGuestsGatewayIsAnsweredOnItsOwnLink(t *testing.T) {
	image, firecracker, agent := liveProbeConfig(t)
	manifest, err := LoadManifest(image)
	if err != nil {
		t.Fatalf("the image this test boots: %v", err)
	}
	nets, _ := liveLeasedNetwork(t, "probe", manifest.GuestInterface)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := scratchDir(t)
	dataDir := filepath.Join(dir, "nodes", "probe-node1")
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte(randomToken(t)), 0o600); err != nil {
		t.Fatalf("writing the token: %v", err)
	}
	const port = 64999
	host, err := New(Options{
		Cluster:     "probe",
		Server:      fmt.Sprintf("https://%s:%d", nets.Gateway, port),
		NodeName:    "probe-node1",
		TokenFile:   token,
		DataDir:     dataDir,
		Record:      func(context.Context, string, v1alpha1.NodePhase) error { return nil },
		Agent:       agent,
		Firecracker: firecracker,
		Image: Image{
			Kernel:    filepath.Join(image, VMLinuxName),
			Initramfs: filepath.Join(image, InitramfsName),
			Rootfs:    filepath.Join(image, RootfsImageName),
		},
		Guest:   manifest.Guest(),
		Network: nets,
	})
	if err != nil {
		t.Fatalf("the substrate: %v", err)
	}
	t.Cleanup(func() {
		if err := host.Teardown(context.Background()); err != nil {
			t.Logf("teardown: %v", err)
		}
	})
	if err := host.Provision(ctx); err != nil {
		t.Fatalf("provisioning: %v", err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		console := string(mustRead(t, ConsolePath(dataDir)))
		if line := dialLine(console); line != "" {
			t.Logf("guest console dial line: %s", line)
			t.Logf("guest console addressing:\n%s", consoleAddressing(console))
			if strings.Contains(line, "no route to host") {
				t.Fatalf("the guest's ARP for its own cluster's gateway %s was never answered: %s. "+
					"pasta discards an ARP request whose target is the address it holds ITSELF, so "+
					"handing pasta's own end of the link the gateway's address leaves the guest with "+
					"no L2 path to the address its whole default route names, and the symptom is a "+
					"node that never joins", nets.Gateway, line)
			}
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("the guest never reported a dial line; its console ends:\n%s",
		lastLines(string(mustRead(t, ConsolePath(dataDir))), 30))
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return raw
}

func dialLine(console string) string {
	for _, line := range strings.Split(console, "\n") {
		if strings.Contains(line, "KCP_CONN") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func consoleAddressing(console string) string {
	var keep []string
	for _, line := range strings.Split(console, "\n") {
		if strings.Contains(line, "KCP_AGENT_IPADDR") || strings.Contains(line, "KCP_AGENT_ROUTE") ||
			strings.Contains(line, "KCP_AGENT_RESOLV") {
			keep = append(keep, strings.TrimSpace(line))
		}
	}
	return strings.Join(keep, "\n")
}
