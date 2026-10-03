package portscan

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

var _ map[string][]string = Survivors{}

const procNetTCPReal = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:192C 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 317872 2 0000000000000000 100 0 0 10 0
   1: 0100007F:2811 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 318268 2 0000000000000000 100 0 0 10 0
   2: 0100007F:2810 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 319708 2 0000000000000000 100 0 0 10 0
   3: 0100007F:2813 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 318315 2 0000000000000000 100 0 0 10 0
   4: 0100007F:2812 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 319714 2 0000000000000000 100 0 0 10 0
   5: 0100007F:2809 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 319711 2 0000000000000000 100 0 0 10 0
   6: 0100007F:2808 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 318292 2 0000000000000000 100 0 0 10 0
   7: 3600007F:0035 00000000:0000 0A 00000000:00000000 00:00000000 00000000   989        0 20121 1 0000000000000000 100 0 0 10 5
   8: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 17483 1 0000000000000000 100 0 0 10 0
   9: 0100007F:271A 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 318122 2 0000000000000000 100 0 0 10 0
  10: 3500007F:0035 00000000:0000 0A 00000000:00000000 00:00000000 00000000   989        0 20119 1 0000000000000000 100 0 0 10 5
  11: 0100007F:192C 0100007F:DBDE 01 00000000:00000000 02:00001F06 00000000     0        0 320422 2 0000000000000000 20 4 19 17 18
  12: 01012A0A:9B74 05012A0A:1FF5 06 00000000:00000000 03:0000137F 00000000     0        0 0 3 0000000000000000
  13: 0F05A8C0:AF9E 0205A8C0:1538 01 00000000:00000000 02:000003F2 00000000     0        0 377555 2 0000000000000000 20 4 26 10 -1
`

const procNetTCP6Real = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:0016 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 17485 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000000000000:280A 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 318295 2 0000000000000000 100 0 0 10 0
   2: 00000000000000000000000000000000:192B 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 317384 2 0000000000000000 100 0 0 10 0
   3: 0000000000000000FFFF00000100007F:192B 0000000000000000FFFF00000100007F:BBFC 01 00000000:00000000 02:0000178B 00000000     0        0 317399 2 0000000000000000 20 4 29 10 -1
   4: 0000000000000000FFFF00000100007F:192B 0000000000000000FFFF00000100007F:BCD0 01 00000000:00000000 02:0000155F 00000000     0        0 318790 2 0000000000000000 21 4 29 10 -1
   5: 0000000000000000FFFF00000F05A8C0:192B 0000000000000000FFFF000004012A0A:D320 01 00000000:00000000 02:00004440 00000000     0        0 318749 2 0000000000000000 20 4 30 10 -1
   6: 0000000000000000FFFF00000100007F:192B 0000000000000000FFFF00000100007F:BCF0 01 00000000:00000000 02:00003CEA 00000000     0        0 318279 2 0000000000000000 20 4 30 10 -1
   7: 0000000000000000FFFF00000F05A8C0:192B 0000000000000000FFFF000006012A0A:E81C 01 00000000:00000000 02:00000754 00000000     0        0 320381 2 0000000000000000 20 4 30 10 -1
   8: 0000000000000000FFFF00000F05A8C0:192B 0000000000000000FFFF000005012A0A:D262 01 00000000:00000000 02:000026DE 00000000     0        0 320411 2 0000000000000000 20 4 31 10 -1
   9: 0000000000000000FFFF00000100007F:192B 0000000000000000FFFF00000100007F:8D5A 01 00000000:00000000 02:0000155B 00000000     0        0 382577 2 0000000000000000 20 4 31 10 -1
  10: 0000000000000000FFFF00000F05A8C0:280A 0000000000000000FFFF000004012A0A:E0FA 01 00000000:00000000 02:000001B0 00000000     0        0 320370 2 0000000000000000 20 4 31 10 -1
  11: 0000000000000000FFFF00000100007F:192B 0000000000000000FFFF00000100007F:BD04 01 00000000:00000000 02:000012A4 00000000     0        0 318282 2 0000000000000000 20 4 31 10 -1
  12: 0000000000000000FFFF00000F05A8C0:192B 0000000000000000FFFF00000F05A8C0:A622 01 00000000:00000000 02:0000172E 00000000     0        0 319117 2 0000000000000000 20 4 28 10 -1
  13: 0000000000000000FFFF00000100007F:192B 0000000000000000FFFF00000100007F:BD06 01 00000000:00000000 02:0000128E 00000000     0        0 318311 2 0000000000000000 20 4 31 10 -1
  14: 0000000000000000FFFF00000F05A8C0:192B 0000000000000000FFFF000003012A0A:DEC0 01 00000000:00000000 02:00002379 00000000     0        0 319463 2 0000000000000000 20 4 31 10 -1
`

const lsofReal = `p962
crapportd
f14
n*:49731
f15
n*:49731
p1067
cControlCenter
f9
n*:7000
f10
n*:7000
f11
n*:5000
f12
n*:5000
p9548
cPython
f3
n*:19998
p12633
cvmctl
f10
n127.0.0.1:60747
f15
n127.0.0.1:6443
f16
n127.0.0.1:2380
f18
n127.0.0.1:2379
f26
n127.0.0.1:60746
f39
n127.0.0.1:7443
p31534
cllama-server
f3
n127.0.0.1:8080
p34210
cdeno
f26
n*:57795
f29
n*:57796
p35605
cdeno
f32
n*:50058
f33
n*:50059
p38134
cdumbpipe
f14
n127.0.0.1:8000
p51037
cpostgres
f6
n*:5432
f7
n*:5432
p52719
cvmctl
f10
n127.0.0.1:53813
f12
n127.0.0.1:10257
f22
n127.0.0.1:53812
f24
n127.0.0.1:6444
f25
n127.0.0.1:10010
f33
n127.0.0.1:10248
f35
n127.0.0.1:10250
f36
n127.0.0.1:10259
f42
n127.0.0.1:10256
f43
n127.0.0.1:10249
f44
n127.0.0.1:10258
f60
n*:80
f61
n*:443
p63929
cdeno
f20
n*:18445
p68661
cmain
f4
n127.0.0.1:54920
`

func TestParseProcNetTCPOnTheRealGuestTable(t *testing.T) {
	got, err := parseProcNetTCP(strings.NewReader(procNetTCPReal))
	if err != nil {
		t.Fatalf("parseProcNetTCP: %v", err)
	}
	want := []listenSocket{
		{Port: 6444, Inode: 317872},
		{Port: 10257, Inode: 318268},
		{Port: 10256, Inode: 319708},
		{Port: 10259, Inode: 318315},
		{Port: 10258, Inode: 319714},
		{Port: 10249, Inode: 319711},
		{Port: 10248, Inode: 318292},
		{Port: 53, Inode: 20121},
		{Port: 22, Inode: 17483},
		{Port: 10010, Inode: 318122},
		{Port: 53, Inode: 20119},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseProcNetTCP =\n%+v\nwant\n%+v", got, want)
	}
	if slices.ContainsFunc(got, func(s listenSocket) bool { return s.Port == 16 }) {
		t.Fatalf("0016 was read as decimal 16: %+v", got)
	}
	if slices.ContainsFunc(got, func(s listenSocket) bool { return s.Inode == 320422 }) {
		t.Fatalf("a non-listening socket was reported as a listener: %+v", got)
	}
	for _, inode := range []uint64{377555, 0} {
		if slices.ContainsFunc(got, func(s listenSocket) bool { return s.Inode == inode }) {
			t.Fatalf("a non-listening socket (%d) was reported as a listener: %+v", inode, got)
		}
	}
}

func TestParseProcNetTCP6OnTheRealGuestTable(t *testing.T) {
	got, err := parseProcNetTCP(strings.NewReader(procNetTCP6Real))
	if err != nil {
		t.Fatalf("parseProcNetTCP: %v", err)
	}
	want := []listenSocket{
		{Port: 22, Inode: 17485},
		{Port: 10250, Inode: 318295},
		{Port: 6443, Inode: 317384},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseProcNetTCP =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseProcNetTCPRefusesARowItCannotRead(t *testing.T) {
	for _, body := range []string{
		"   0: 0100007F:192C\n",
		"   0: 0100007F:192C 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0\n",
		"   0: 0100007F:ZZZZ 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 317872 2 0000000000000000\n",
		"   0: 01007F 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 317872 2 0000000000000000\n",
		"   0: 0100007F:192C 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 notanumber 2 0000000000000000\n",
	} {
		if _, err := parseProcNetTCP(strings.NewReader(body)); err == nil {
			t.Fatalf("parseProcNetTCP(%q) returned no error; a row it cannot read must never read as unowned", body)
		}
	}
}

func TestParseLsofOnTheRealCapture(t *testing.T) {
	got, err := parseLsof([]byte(lsofReal))
	if err != nil {
		t.Fatalf("parseLsof: %v", err)
	}
	if len(got) != 32 {
		t.Fatalf("parseLsof found %d ports, want 32: %v", len(got), got)
	}
	for port, want := range map[int][]int{
		6443:  {12633},
		2379:  {12633},
		49731: {962},
		5432:  {51037},
		80:    {52719},
		443:   {52719},
		8080:  {31534},
		8000:  {38134},
		54920: {68661},
		19998: {9548},
	} {
		if !slices.Equal(got[port], want) {
			t.Fatalf("port %d = %v, want %v", port, got[port], want)
		}
	}
	if n := len(got[49731]); n != 1 {
		t.Fatalf("port 49731 has %d owners, want 1: %v", n, got[49731])
	}
	if n := len(got[5432]); n != 1 {
		t.Fatalf("port 5432 has %d owners, want 1: %v", n, got[5432])
	}
}

func TestLsofPortReadsBothAddressForms(t *testing.T) {
	for name, want := range map[string]int{
		"*:49731":        49731,
		"127.0.0.1:6443": 6443,
		"[::1]:6443":     6443,
		"[fe80::1]:8080": 8080,
	} {
		got, ok := lsofPort(name)
		if !ok || got != want {
			t.Fatalf("lsofPort(%q) = %d, %v; want %d, true", name, got, ok, want)
		}
	}
	for _, name := range []string{"", "no-colon", "127.0.0.1:"} {
		if _, ok := lsofPort(name); ok {
			t.Fatalf("lsofPort(%q) claimed a port", name)
		}
	}
}

func fixtureTable(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"tcp": procNetTCPReal, "tcp6": procNetTCP6Real} {
		if err := os.WriteFile(filepath.Join(root, "net", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(pid, fd, target string) {
		t.Helper()
		dir := filepath.Join(root, pid, "fd")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, fd)); err != nil {
			t.Fatal(err)
		}
	}
	link("4242", "3", "socket:[317872]")
	link("4243", "7", "socket:[317384]")
	link("4243", "8", "socket:[317872]")
	link("4243", "9", "anon_inode:[eventfd]")
	link("4243", "10", "/dev/null")
	link("4244", "5", "socket:[320422]")
	if err := os.MkdirAll(filepath.Join(root, "4245"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestListenersInOnAFixtureProcessTable(t *testing.T) {
	got, err := listenersIn(fixtureTable(t))
	if err != nil {
		t.Fatalf("listenersIn: %v", err)
	}
	want := map[int][]int{6444: {4242, 4243}, 6443: {4243}}
	if !reflect.DeepEqual(normalize(got), normalize(want)) {
		t.Fatalf("listenersIn =\n%v\nwant\n%v", normalize(got), normalize(want))
	}
	for _, port := range []int{22, 53, 10010, 10248, 10249, 10250, 10256, 10257, 10258, 10259} {
		if _, ok := got[port]; ok {
			t.Fatalf("port %d has no bound socket and must contribute nothing: %v", port, got[port])
		}
	}
	if _, ok := got[320422]; ok {
		t.Fatal("the connection inode became a port entry")
	}
}

func TestListenersInNeedsTheV4Table(t *testing.T) {
	root := fixtureTable(t)
	if err := os.Remove(filepath.Join(root, "net", "tcp")); err != nil {
		t.Fatal(err)
	}
	if _, err := listenersIn(root); err == nil {
		t.Fatal("listenersIn without /proc/net/tcp returned no error; it did not scan and must not look as if it did")
	}
	root = fixtureTable(t)
	if err := os.Remove(filepath.Join(root, "net", "tcp6")); err != nil {
		t.Fatal(err)
	}
	got, err := listenersIn(root)
	if err != nil {
		t.Fatalf("listenersIn without /proc/net/tcp6: %v", err)
	}
	if !reflect.DeepEqual(normalize(got), normalize(map[int][]int{6444: {4242, 4243}})) {
		t.Fatalf("listenersIn without the v6 table = %v", got)
	}
}

func TestWalkInodeOwnersSkipsWhatItCannotRead(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "not-a-pid", "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := walkInodeOwners(root, map[uint64]bool{1: true})
	if err != nil {
		t.Fatalf("walkInodeOwners: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("walkInodeOwners = %v, want nothing", got)
	}
	if _, err := walkInodeOwners(filepath.Join(root, "absent"), map[uint64]bool{}); err == nil {
		t.Fatal("walkInodeOwners on a process table that does not exist returned no error")
	}
}

func TestAssociateFoldsPortsIntoClusters(t *testing.T) {
	leases := []portallocator.Lease{
		{Cluster: "alpha", Purpose: portallocator.PurposePostgres, Port: 20004, Width: 1},
		{Cluster: "alpha", Purpose: portallocator.PurposeOpenBao, Port: 20002, Width: 1},
		{Cluster: "beta", Purpose: portallocator.PurposeKine, Port: 20005, Width: 1},
		{Cluster: "gamma", Purpose: portallocator.PurposeKine, Port: 20006, Width: 1},
	}
	owners := map[claim][]int{
		{Cluster: "alpha", Port: 20002}: {200, 100},
		{Cluster: "alpha", Port: 20003}: {100},
		{Cluster: "beta", Port: 20005}:  {300},
		{Cluster: "alpha", Port: 20099}: {999},
	}
	got := associate(leases, owners)
	want := Survivors{"alpha": {"100", "200"}, "beta": {"300"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("associate = %v, want %v", got, want)
	}
	if _, ok := got["gamma"]; ok {
		t.Fatalf("a cluster with nothing bound got an entry: %v", got["gamma"])
	}
}

func TestAssociateNeverMovesAHandleToAnotherClustersName(t *testing.T) {
	leases := []portallocator.Lease{
		{Cluster: "alpha", Purpose: portallocator.PurposeK3sSecure, Port: 20011, Width: 1},
		{Cluster: "beta", Purpose: portallocator.PurposeK3sSecure, Port: 20011, Width: 1},
	}
	owners := map[claim][]int{
		{Cluster: "alpha", Port: 20011}: {4242},
	}
	got := associate(leases, owners)
	if !reflect.DeepEqual(got, Survivors{"alpha": {"4242"}}) {
		t.Fatalf("associate = %v, want alpha alone; a handle that matched alpha's argv must never "+
			"be reported under beta, whose argv it did not match, because beta's reap would then "+
			"SIGTERM alpha's apiserver", got)
	}
}

func TestAssociateWithNothingBoundIsEmpty(t *testing.T) {
	got := associate([]portallocator.Lease{{Cluster: "alpha", Port: 20001, Width: 1}}, map[claim][]int{})
	if len(got) != 0 {
		t.Fatalf("associate = %v, want no handles", got)
	}
}

func TestSocketInodeReadsOnlySocketLinks(t *testing.T) {
	for link, want := range map[string]uint64{
		"socket:[317872]": 317872,
		"socket:[0]":      0,
	} {
		got, ok := socketInode(link)
		if !ok || got != want {
			t.Fatalf("socketInode(%q) = %d, %v; want %d, true", link, got, ok, want)
		}
	}
	for _, link := range []string{"/dev/null", "anon_inode:[eventfd]", "pipe:[123]", "socket:[abc]", "socket:[1"} {
		if _, ok := socketInode(link); ok {
			t.Fatalf("socketInode(%q) claimed a socket", link)
		}
	}
}

func standInSpec(t *testing.T) clusterstack.Spec {
	t.Helper()
	dir := t.TempDir()
	return clusterstack.Spec{
		DataDir:                       filepath.Join(dir, "cluster"),
		PostgresPort:                  20004,
		OpenBaoConfig:                 filepath.Join(dir, "openbao.hcl"),
		StoreEndpoint:                 "s3://store/clusters",
		DatastoreEndpoint:             "http://127.0.0.1:2379",
		KineListen:                    "127.0.0.1:22379",
		AdvertiseAddress:              "192.168.5.2",
		BindAddress:                   "127.0.0.1",
		AdvertisePort:                 20006,
		K3sSecurePort:                 20011,
		K3sControllerManagerPort:      20013,
		K3sSchedulerPort:              20014,
		K3sCloudControllerManagerPort: 20015,
		TokenFile:                     filepath.Join(dir, "token"),
		Audit: clusterstack.Audit{
			PolicyPath: filepath.Join(dir, "audit-policy.yaml"),
			LogPath:    filepath.Join(dir, "audit.log"),
		},
	}
}

func spawnArgv(t *testing.T, s Stack, c procgroup.Component) []string {
	t.Helper()
	argv, err := s.Spec.Argv(c)
	if err != nil {
		t.Fatalf("Argv(%s): %v", c, err)
	}
	return append(append([]string{}, s.Prefix...), argv...)
}

func mustExpect(t *testing.T, stacks []Stack) map[string][][]string {
	t.Helper()
	exp, err := expectations(stacks)
	if err != nil {
		t.Fatalf("expectations: %v", err)
	}
	return exp
}

func argvOf(table map[int][]string) func(int) ([]string, error) {
	return func(pid int) ([]string, error) { return table[pid], nil }
}

func TestScanDropsAProcessThatIsNotTheClustersComponent(t *testing.T) {
	stack := Stack{Cluster: "alpha", Spec: standInSpec(t)}
	exp := mustExpect(t, []Stack{stack})
	leases := []portallocator.Lease{
		{Cluster: "alpha", Purpose: portallocator.PurposeKine, Port: 20006, Width: 1},
	}
	stranger := []string{"wrap", "run", "beta-other-instance", "--", "k3s", "server"}

	got, dropped := scan(leases, exp, map[int][]int{20006: {4242}}, argvOf(map[int][]string{4242: stranger}))
	if len(got) != 0 {
		t.Fatalf("a stranger bound to a leased port was reported as a survivor: %v -- Kill "+
			"SIGTERMs this, and on the gateway that is a VM host", got)
	}
	if _, ok := got["alpha"]; ok {
		t.Fatalf("alpha got an entry from a process that is not alpha's: %v", got["alpha"])
	}
	want := []Drop{{
		Cluster: "alpha",
		Port:    20006,
		PID:     4242,
		Argv:    stranger,
		Reason:  "the command line is not any of this cluster's components as the stack spawns them",
	}}
	if !reflect.DeepEqual(dropped, want) {
		t.Fatalf("dropped = %+v, want %+v", dropped, want)
	}
}

func TestScanReportsOnlyAProcessThatIsTheWholeComponent(t *testing.T) {
	stack := Stack{Cluster: "alpha", Spec: standInSpec(t)}
	exp := mustExpect(t, []Stack{stack})
	leases := []portallocator.Lease{
		{Cluster: "alpha", Purpose: portallocator.PurposeKine, Port: 20006, Width: 1},
	}
	ours := spawnArgv(t, stack, procgroup.ComponentK3s)
	partial := []string{"k3s", "server", "--disable-agent"}

	got, dropped := scan(leases, exp, map[int][]int{20006: {4242, 4243}}, argvOf(map[int][]string{
		4242: ours,
		4243: partial,
	}))
	if !reflect.DeepEqual(got, Survivors{"alpha": {"4242"}}) {
		t.Fatalf("scan = %v, want alpha 4242 and nothing else -- the full vector identifies the "+
			"cluster, and a prefix of it identifies every cluster at once", got)
	}
	if len(dropped) != 1 || dropped[0].PID != 4243 {
		t.Fatalf("dropped = %+v, want only 4243", dropped)
	}
}

func TestScanDropsAClusterTheCallerSuppliedNoStackFor(t *testing.T) {
	stack := Stack{Cluster: "alpha", Spec: standInSpec(t)}
	exp := mustExpect(t, []Stack{stack})
	leases := []portallocator.Lease{
		{Cluster: "beta", Purpose: portallocator.PurposePostgres, Port: 20004, Width: 1},
	}

	got, dropped := scan(leases, exp, map[int][]int{20004: {7000}}, argvOf(map[int][]string{
		7000: {"postgres", "-D", "/somewhere"},
	}))
	if len(got) != 0 {
		t.Fatalf("beta's port owner was reported although beta was never described: %v", got)
	}
	if len(dropped) != 1 || dropped[0].Cluster != "beta" || dropped[0].PID != 7000 {
		t.Fatalf("dropped = %+v, want beta 7000", dropped)
	}
	if !strings.Contains(dropped[0].Reason, "no stack was supplied") {
		t.Fatalf("the reason does not say the cluster was never described: %q", dropped[0].Reason)
	}
}

func TestScanDropsAProcessWhoseCommandLineCannotBeRead(t *testing.T) {
	stack := Stack{Cluster: "alpha", Spec: standInSpec(t)}
	exp := mustExpect(t, []Stack{stack})
	leases := []portallocator.Lease{
		{Cluster: "alpha", Purpose: portallocator.PurposeKine, Port: 20005, Width: 1},
	}
	boom := errors.New("no such process")
	read := func(int) ([]string, error) { return nil, boom }

	got, dropped := scan(leases, exp, map[int][]int{20005: {4242}}, read)
	if len(got) != 0 {
		t.Fatalf("a process whose own command line could not be read was reported: %v", got)
	}
	if len(dropped) != 1 || !strings.Contains(dropped[0].Reason, boom.Error()) {
		t.Fatalf("dropped = %+v, want one drop carrying %q", dropped, boom)
	}
}

func TestExpectationsComeFromTheSpawnArgvsTheRunningPathAlreadyDerived(t *testing.T) {
	stack := Stack{
		Cluster: "alpha",
		Prefix:  []string{"wrap", "run"},
		Argvs:   [][]string{{"k3s", "server", "--data-dir", "/clusters/alpha"}},
	}
	exp := mustExpect(t, []Stack{stack})
	want := [][]string{{"wrap", "run", "k3s", "server", "--data-dir", "/clusters/alpha"}}
	if !reflect.DeepEqual(exp["alpha"], want) {
		t.Fatalf("expectations = %v, want %v; the spec is empty, so this stack can only have "+
			"come from the argvs the caller derived", exp["alpha"], want)
	}
	if _, err := expectations([]Stack{{Cluster: "alpha", Argvs: [][]string{{}}}}); err == nil {
		t.Fatal("a component with an empty argv was accepted: it identifies no process, and the " +
			"stack the caller described is then not the one it spawns")
	}
	if _, err := expectations([]Stack{{Cluster: "alpha", Argvs: [][]string{{"k3s"}, {}}}}); err == nil {
		t.Fatal("one empty vector among good ones was accepted")
	}
}

func TestScanAttributesAgainstTheArgvsTheCallerSupplied(t *testing.T) {
	root := fixtureTable(t)
	ours := []string{"k3s", "server", "--data-dir", "/clusters/alpha"}
	exp := mustExpect(t, []Stack{{Cluster: "alpha", Argvs: [][]string{ours}}})

	writeCmdline(t, root, 4242, ours)
	writeCmdline(t, root, 4243, []string{"k3s", "server", "--data-dir", "/clusters/beta"})

	owners, err := listenersIn(root)
	if err != nil {
		t.Fatalf("listenersIn: %v", err)
	}
	leases := []portallocator.Lease{
		{Cluster: "alpha", Purpose: portallocator.PurposeK3sSecure, Port: 6444, Width: 1},
	}
	got, dropped := scan(leases, exp, owners, func(pid int) ([]string, error) {
		return cmdlineIn(root, pid)
	})
	if !reflect.DeepEqual(got, Survivors{"alpha": {"4242"}}) {
		t.Fatalf("scan = %v, want alpha 4242 alone; both hold alpha's leased port and only one "+
			"of them was spawned as alpha's component", got)
	}
	if len(dropped) != 1 || dropped[0].PID != 4243 {
		t.Fatalf("dropped = %+v, want exactly one drop for 4243", dropped)
	}
}

func TestScanRefusesWithoutAnyStack(t *testing.T) {
	leases := []portallocator.Lease{{Cluster: "alpha", Port: 20006, Width: 1}}
	for name, stacks := range map[string][]Stack{
		"no stacks at all":     nil,
		"a stack with no name": {{Spec: standInSpec(t)}},
	} {
		t.Run(name, func(t *testing.T) {
			got, dropped, err := Scan(leases, stacks)
			if err == nil {
				t.Fatalf("Scan without a usable stack returned %v and no error", got)
			}
			if got != nil || dropped != nil {
				t.Fatalf("Scan refused AND returned %v / %+v", got, dropped)
			}
		})
	}
	if _, _, err := Scan(leases, nil); !errors.Is(err, ErrNoAttribution) {
		t.Fatalf("Scan(leases, nil) = %v, want ErrNoAttribution", err)
	}
}

func TestExpectationsAreTheSpawnArgvWithThePrefix(t *testing.T) {
	prefix := []string{"wrap", "run", "alpha-vm", "--"}
	stack := Stack{Cluster: "alpha", Spec: standInSpec(t), Prefix: prefix}
	exp := mustExpect(t, []Stack{stack})

	want := [][]string{
		spawnArgv(t, stack, procgroup.ComponentK3s),
		spawnArgv(t, stack, procgroup.ComponentKine),
		spawnArgv(t, stack, procgroup.ComponentOpenBao),
	}
	if !reflect.DeepEqual(normalizeVectors(exp["alpha"]), normalizeVectors(want)) {
		t.Fatalf("expectations =\n%v\nwant\n%v", normalizeVectors(exp["alpha"]), normalizeVectors(want))
	}
	for _, v := range exp["alpha"] {
		if strings.Contains(strings.Join(v, " "), "postgres") {
			t.Fatalf("embedded-postgres produced an expectation: %v -- it is a Go library with no "+
				"argv, and a Step cannot carry it", v)
		}
	}
	if _, err := expectations([]Stack{{Cluster: "alpha"}}); err == nil {
		t.Fatal("an empty Spec produced an expectation; it must refuse")
	}
}

func TestAttributionTellsTwoInstancesApart(t *testing.T) {
	alpha := Stack{
		Cluster: "alpha",
		Spec:    standInSpec(t),
		Prefix:  []string{"wrap", "run", "alpha-vm", "--"},
	}
	exp := mustExpect(t, []Stack{alpha})

	if !attributed(exp["alpha"], spawnArgv(t, alpha, procgroup.ComponentK3s)) {
		t.Fatal("the wrapper carrying this cluster's own k3s argv was not attributed to it")
	}
	other := spawnArgv(t, alpha, procgroup.ComponentK3s)
	other[2] = "beta-vm"
	if attributed(exp["alpha"], other) {
		t.Fatalf("another instance holding a leased port was attributed to alpha: %v -- "+
			"this is the exact process measured holding 6443 on the developer machine", other)
	}
}

func TestAttributionRefusesASharedTokenOnItsOwn(t *testing.T) {
	stack := Stack{Cluster: "alpha", Spec: standInSpec(t)}
	exp := mustExpect(t, []Stack{stack})
	for _, argv := range [][]string{
		{"k3s"},
		{"k3s", "server", "--data-dir", "/var/lib/rancher/k3s"},
		{"bao", "server", "-config=/etc/bao.hcl"},
		nil,
	} {
		if attributed(exp["alpha"], argv) {
			t.Fatalf("attributed %v to alpha on a shared token alone", argv)
		}
	}
	if containsAll([]string{"anything"}, nil) {
		t.Fatal("an empty token list matched; the only input that can produce it is a bug, and " +
			"its output is a kill list")
	}
}

func TestScanOnAFixtureProcessTableWithRealCommandLines(t *testing.T) {
	root := fixtureTable(t)
	stack := Stack{Cluster: "alpha", Spec: standInSpec(t)}
	exp := mustExpect(t, []Stack{stack})

	writeCmdline(t, root, 4242, spawnArgv(t, stack, procgroup.ComponentK3s))
	writeCmdline(t, root, 4243, []string{"wrap", "run", "beta-vm", "--", "k3s", "server"})

	leases := []portallocator.Lease{
		{Cluster: "alpha", Purpose: portallocator.PurposeKine, Port: 6443, Width: 1},
		{Cluster: "alpha", Purpose: portallocator.PurposeOpenBao, Port: 6444, Width: 1},
	}
	owners, err := listenersIn(root)
	if err != nil {
		t.Fatalf("listenersIn: %v", err)
	}
	got, dropped := scan(leases, exp, owners, func(pid int) ([]string, error) {
		return cmdlineIn(root, pid)
	})

	if !reflect.DeepEqual(got, Survivors{"alpha": {"4242"}}) {
		t.Fatalf("scan = %v, want alpha 4242 -- 4243 holds BOTH of alpha's leased ports and is "+
			"not alpha's", got)
	}
	if len(dropped) != 1 || dropped[0].PID != 4243 {
		t.Fatalf("dropped = %+v, want exactly one drop for 4243", dropped)
	}
}

func writeCmdline(t *testing.T, root string, pid int, argv []string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseProcCmdlineOnARealGuestShape(t *testing.T) {
	got := parseProcCmdline([]byte("k3s\x00server\x00--disable-agent\x00--data-dir\x00/var/lib/rancher/k3s\x00"))
	want := []string{"k3s", "server", "--disable-agent", "--data-dir", "/var/lib/rancher/k3s"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseProcCmdline = %q, want %q", got, want)
	}
	if got := parseProcCmdline(nil); len(got) != 0 {
		t.Fatalf("an empty cmdline parsed to %q, want nothing", got)
	}
	if got := parseProcCmdline([]byte("k3s")); !reflect.DeepEqual(got, []string{"k3s"}) {
		t.Fatalf("a cmdline with no terminator = %q", got)
	}
}

func TestParsePSCommandKeepsTheLineWhole(t *testing.T) {
	argv := []string{"k3s", "server", "--data-dir", "/Users/some one/cluster"}
	got := parsePSCommand([]byte(strings.Join(argv, " ") + "\n"))
	want := []string{"/Users/some one/cluster"}
	if len(got) != 1 || !strings.Contains(got[0], want[0]) {
		t.Fatalf("parsePSCommand = %q, want one line containing %q", got, want[0])
	}
	if got := parsePSCommand([]byte("\n")); len(got) != 0 {
		t.Fatalf("an empty ps output parsed to %q", got)
	}
}

func TestCmdlineInOnAFixtureProcess(t *testing.T) {
	root := t.TempDir()
	writeCmdline(t, root, 4242, []string{"kine", "--endpoint", "s3://store/clusters"})

	got, err := cmdlineIn(root, 4242)
	if err != nil {
		t.Fatalf("cmdlineIn: %v", err)
	}
	if want := []string{"kine", "--endpoint", "s3://store/clusters"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cmdlineIn = %q, want %q", got, want)
	}
	if _, err := cmdlineIn(root, 4243); err == nil {
		t.Fatal("cmdlineIn on a pid with no cmdline returned no error")
	}
	if _, err := cmdlineIn(filepath.Join(root, "absent"), 1); err == nil {
		t.Fatal("cmdlineIn on a process table that does not exist returned no error")
	}
}

func normalizeVectors(v [][]string) [][]string {
	out := make([][]string, len(v))
	for i, one := range v {
		out[i] = append([]string(nil), one...)
	}
	sort.Slice(out, func(i, j int) bool { return strings.Join(out[i], " ") < strings.Join(out[j], " ") })
	return out
}

func normalize(m map[int][]int) map[int][]int {
	out := make(map[int][]int, len(m))
	for port, pids := range m {
		cp := append([]int(nil), pids...)
		sort.Ints(cp)
		out[port] = cp
	}
	return out
}
