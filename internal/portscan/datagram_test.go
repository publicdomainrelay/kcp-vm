package portscan

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
)

const procNetUDPReal = `   sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
21819: 3600007F:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000   193        0 18860 2 00000000775e8bce 0
21834: 1400A8C0:0044 0100A8C0:0043 01 00000000:00000000 00:00000000 00000000     0        0 20005 2 00000000bb08a115 0
21900: 00000000:4E21 00000000:0000 07 00000000:00000000 00:00000000 00000000  1000        0 41001 2 0000000000000000 0
`

const procNetUDP6Real = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
22209: 00000000000000000000000000000000:01BB 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000   949        0 24739 2 000000008903bb9d 0
`

func TestParseProcNetUDPReadsOnlyBoundUnconnectedSockets(t *testing.T) {
	got, err := parseProcNetUDP(strings.NewReader(procNetUDPReal))
	if err != nil {
		t.Fatal(err)
	}
	want := []listenSocket{{Port: 53, Inode: 18860}, {Port: 20001, Inode: 41001}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseProcNetUDP =\n%+v\nwant\n%+v (a connected socket, state 01, is a client and holds no port)", got, want)
	}
}

func TestDatagramListenersInOnAFixtureProcessTable(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"udp": procNetUDPReal, "udp6": procNetUDP6Real, "tcp": procNetTCPReal} {
		if err := os.WriteFile(filepath.Join(root, "net", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(root, "5150", "fd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[41001]", filepath.Join(dir, "9")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[317872]", filepath.Join(dir, "10")); err != nil {
		t.Fatal(err)
	}
	got, err := datagramListenersIn(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[int][]int{20001: {5150}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("datagramListenersIn = %v, want %v (the TCP listener on 6444 is not a datagram socket)", got, want)
	}
}

func TestALinkLeaseTakesItsOwnersFromTheDatagramTableAndEveryOtherLeaseFromTheStreamTable(t *testing.T) {
	leases := []portallocator.Lease{
		{Cluster: "a", Purpose: portallocator.PurposeKine, Port: 20001, Width: 1},
		{Cluster: "a", Purpose: portallocator.PurposeApiserverLink, Port: 20002, Width: 1},
		{Cluster: "a", Purpose: portallocator.PurposeOpenBaoLink, Port: 20003, Width: 1},
	}
	stream := map[int][]int{20001: {11}, 20002: {12}, 20003: {13}}
	datagram := map[int][]int{20001: {21}, 20002: {22}}
	got := byTransport(leases, stream, datagram)
	want := map[int][]int{20001: {11}, 20002: {22}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("byTransport = %v, want %v", got, want)
	}
}

func TestASurvivingLinkIsAttributedToItsClusterLikeEveryOtherComponent(t *testing.T) {
	leases := []portallocator.Lease{
		{Cluster: "a", Purpose: portallocator.PurposeK3sSecure, Port: 20010, Width: 2},
		{Cluster: "a", Purpose: portallocator.PurposeApiserverLink, Port: 20012, Width: 1},
	}
	link := []string{"/usr/local/bin/dumbpipe", "listen-tcp", "--host", "127.0.0.1:20010", "--ipv4-addr", "0.0.0.0:20012"}
	expected := map[string][][]string{"a": {{"k3s", "server", "--https-listen-port", "20010"}, link}}
	owners := byTransport(leases, map[int][]int{20010: {300}}, map[int][]int{20012: {301}})
	argvs := map[int][]string{300: {"k3s", "server", "--https-listen-port", "20010"}, 301: link}
	survivors, dropped := scan(leases, expected, owners, func(pid int) ([]string, error) { return argvs[pid], nil })
	if len(dropped) != 0 {
		t.Fatalf("dropped %+v", dropped)
	}
	if want := (Survivors{"a": {"300", "301"}}); !reflect.DeepEqual(survivors, want) {
		t.Fatalf("survivors = %v, want %v: a dumbpipe left by a previous provider holds the link's identity and would answer beside the new one", survivors, want)
	}
}
