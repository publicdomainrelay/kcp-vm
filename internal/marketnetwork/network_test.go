package marketnetwork

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func aNetwork(id string, base int) Network {
	return Network{
		ID:         id,
		FCWorkRoot: FCWorkRootFor(id),
		Processes: map[string]Proc{
			"kine":     {PID: 100, Port: base + 1},
			"kcp":      {PID: 101, Port: base + 2},
			"openbao":  {PID: 102, Port: base + 3},
			"provider": {PID: 103},
		},
		Services: map[string]Proc{
			"global/plc":           {Address: "127.0.0.1:" + strconv.Itoa(base+4), Port: base + 4},
			"relay/dispatcher":     {Address: "relay.localhost:" + strconv.Itoa(base+5), Port: base + 5},
			"relay/dispatcher-tls": {Address: "127.0.0.1:" + strconv.Itoa(base+6), Port: base + 6},
			"bob/bidder-1":         {Address: "127.0.0.1:" + strconv.Itoa(base+7), Port: base + 7},
			"bob/bidder-2":         {Address: "127.0.0.1:" + strconv.Itoa(base+8), Port: base + 8},
		},
	}
}

func TestANetworkRecordNamesEveryPortItBound(t *testing.T) {
	network := aNetwork("b3d00001", 41000)
	if err := network.Validate(); err != nil {
		t.Fatalf("a complete record is refused: %v", err)
	}
	if got := len(network.Ports()); got != 8 {
		t.Fatalf("ports = %v, want the 3 control-plane ports and the 5 service ports", network.Ports())
	}
	unbound := aNetwork("b3d00001", 41000)
	unbound.Services["global/plc"] = Proc{Address: "127.0.0.1:0"}
	if err := unbound.Validate(); err == nil {
		t.Fatal("a service that never reported its port is accepted")
	}
	twice := aNetwork("b3d00001", 41000)
	twice.Services["bob/bidder-2"] = twice.Services["bob/bidder-1"]
	if err := twice.Validate(); err == nil {
		t.Fatal("two services of one market network on one port is accepted")
	}
	elsewhere := aNetwork("b3d00001", 41000)
	elsewhere.FCWorkRoot = "/var/lib/socialweb/guests"
	if err := elsewhere.Validate(); err == nil {
		t.Fatal("a market network whose guests share a work root with anything else is accepted")
	}
}

func TestTwoNetworkRecordsLeaseDisjointPorts(t *testing.T) {
	a, b := aNetwork("b3d00001", 41000), aNetwork("b3d00002", 42000)
	if overlap := OverlappingPorts(a, b); len(overlap) != 0 {
		t.Fatalf("two market networks on distinct ports report an overlap %v", overlap)
	}
	clash := aNetwork("b3d00002", 42000)
	clash.Services["global/plc"] = Proc{Address: "127.0.0.1:41004", Port: 41004}
	if overlap := OverlappingPorts(a, clash); len(overlap) != 1 || overlap[0] != 41004 {
		t.Fatalf("overlap = %v, want [41004]", overlap)
	}
}

func TestLoadNetworkReadsWhatAcceptWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "market-network.json")
	body := `{"networkId":"b3d00001","slot":1,"fcWorkRoot":"/var/tmp/swc-b3d00001","runnerImage":"localhost/socialweb-firecracker-runner:sha-0123456789ab",
"processes":{"kcp":{"pid":7,"port":41002}},"services":{"bob/bidder-1":{"address":"127.0.0.1:41007","port":41007,"pid":9}},
"bidders":[{"name":"bidder-1","did":"did:plc:abc","port":41007}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	network, err := LoadNetwork(path)
	if err != nil {
		t.Fatal(err)
	}
	if network.ID != "b3d00001" || network.Slot != 1 || network.Processes["kcp"].Port != 41002 || network.Bidders[0].DID != "did:plc:abc" {
		t.Fatalf("market network = %+v", network)
	}
}

func TestANetworkIDKeepsTheGuestSocketUnderTheUnixLimit(t *testing.T) {
	for _, id := range []string{"b3d00001", "abcdefghijkl"} {
		if !ValidNetworkID(id) {
			t.Fatalf("%q is refused", id)
		}
		sock := filepath.Join(FCWorkRootFor(id), "fc-000000000000-00000000", "fc.sock")
		if len(sock) > 107 {
			t.Fatalf("%s is %d bytes, over the 107-byte unix socket limit", sock, len(sock))
		}
	}
	for _, id := range []string{"", "B3D", "../x", "a b", strings.Repeat("a", 13)} {
		if ValidNetworkID(id) {
			t.Fatalf("%q is accepted as a market network id", id)
		}
	}
}

func TestTheShellSlotAndTheGoSlotAreOneLock(t *testing.T) {
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("no flock(1) on this host")
	}
	dir := t.TempDir()
	slot, err := TakeSlot(dir, 1, "b3d00001")
	if err != nil {
		t.Fatal(err)
	}
	defer slot.Release()
	err = exec.Command("bash", "-c", `exec 9>>"$1"; flock -n 9`, "_", slot.Path).Run()
	if err == nil {
		t.Fatal("accept.sh's flock took a slot the Go test holds")
	}
}
