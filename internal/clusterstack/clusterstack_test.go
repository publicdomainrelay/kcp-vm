package clusterstack

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

func k3sOnPath(t *testing.T) string {
	t.Helper()
	livegate.Short(t, "this test probes the real k3s binary and -short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	bin, err := exec.LookPath("k3s")
	if err != nil {
		livegate.RequireLocal(t, "k3s is not on PATH (%v): this test probes the real binary, and a skip here is "+
			"NOT a pass -- nothing else in the tree can tell an accepted flag from an ignored one", err)
	}
	return bin
}

func k3sVerdict(bin string, flags []string) (bool, string) {
	args := append([]string{"server"}, flags...)
	out, err := exec.Command(bin, append(args, "--help")...).CombinedOutput()
	return err == nil, string(out)
}

func valid() Spec {
	return Spec{
		DataDir:                       "/var/lib/socialweb/clusters/a",
		PostgresPort:                  15432,
		OpenBaoConfig:                 "/etc/socialweb/bao-a.hcl",
		StoreEndpoint:                 "t4://cluster-a",
		KineListen:                    "127.0.0.1:12379",
		DatastoreEndpoint:             "http://127.0.0.1:12379",
		AdvertiseAddress:              "127.0.0.1",
		BindAddress:                   "127.0.0.1",
		AdvertisePort:                 16443,
		K3sSecurePort:                 16445,
		K3sControllerManagerPort:      16447,
		K3sSchedulerPort:              16448,
		K3sCloudControllerManagerPort: 16449,
		TokenFile:                     "/var/lib/socialweb/clusters/a/token",
		Audit: Audit{
			PolicyPath: "/etc/socialweb/audit-policy.yaml",
			LogPath:    "/var/lib/socialweb/clusters/a/audit.log",
		},
	}
}

func TestK3sDialsKineAndKineStoresIntoTheBucket(t *testing.T) {
	s := valid()
	server, err := s.ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	kine, err := s.KineArgv()
	if err != nil {
		t.Fatalf("KineArgv: %v", err)
	}

	if !hasFlag(kine, "--endpoint", s.StoreEndpoint) {
		t.Fatalf("kine argv = %v, want --endpoint %s -- kine is what consumes the bucket, "+
			"and its backend is selected by that endpoint's scheme", kine, s.StoreEndpoint)
	}
	if !hasFlag(server, "--datastore-endpoint", s.DatastoreEndpoint) {
		t.Fatalf("k3s argv = %v, want --datastore-endpoint %s -- k3s consumes KINE, not the "+
			"bucket; the kine ADDRESS is what it dials", server, s.DatastoreEndpoint)
	}
	if s.StoreEndpoint == s.DatastoreEndpoint {
		t.Fatal("the two endpoints are one string, which is the conflation the second field " +
			"exists to end -- this fixture must not be able to prove the split while " +
			"reproducing the merge")
	}

	for _, a := range server {
		if strings.Contains(a, s.StoreEndpoint) {
			t.Fatalf("k3s argv carries the store endpoint (%q in %q): k3s would be pointed at "+
				"the S3 bucket rather than at kine. MEASURED against k3s v1.36.4+k3s1: the S3 "+
				"form is refused at startup with 'unknown driver', and the t4 form makes k3s "+
				"start its OWN kine against the same bucket instead of dialing ours",
				s.StoreEndpoint, a)
		}
	}
	if !strings.Contains(strings.Join(kine, " "), s.StoreEndpoint) {
		t.Fatalf("kine argv = %v does not carry the store endpoint either, so the assertion "+
			"above proves nothing about which argv it belongs in", kine)
	}
}

func TestAClusterWithNoAuditTrailIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		audit Audit
		want  error
	}{
		{"no audit at all", Audit{}, ErrNoAuditPolicy},
		{"a policy with no log", Audit{PolicyPath: "/etc/p.yaml"}, ErrNoAuditLog},
		{"a log with no policy", Audit{LogPath: "/var/log/a.log"}, ErrNoAuditPolicy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := valid()
			s.Audit = tc.audit
			if _, err := s.ServerArgv(); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestTheAuditFlagsTravelTogether(t *testing.T) {
	argv, err := valid().ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	for _, want := range []string{
		"--kube-apiserver-arg=audit-policy-file=/etc/socialweb/audit-policy.yaml",
		"--kube-apiserver-arg=audit-log-path=/var/lib/socialweb/clusters/a/audit.log",
		"--kube-apiserver-arg=audit-log-maxsize=100",
	} {
		if !contains(argv, want) {
			t.Fatalf("argv = %v\n\nwant %s. k3s SKIPS an unrecognised flag with only a "+
				"warning, so a direct --audit-policy-file leaves the cluster SILENTLY "+
				"UNAUDITED and starts normally. See docs/ARCHITECTURE.md, the audit trail",
				argv, want)
		}
	}
	for _, a := range argv {
		if strings.HasPrefix(a, "--audit-") {
			t.Fatalf("argv carries the DIRECT flag %q, which k3s skips as unknown -- the "+
				"cluster starts and audits nothing", a)
		}
	}
}

func TestTheAuditLogPathIsPerCluster(t *testing.T) {
	a, err := valid().ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	other := valid()
	other.DataDir = "/var/lib/socialweb/clusters/b"
	other.Audit.LogPath = "/var/lib/socialweb/clusters/b/audit.log"
	b, err := other.ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	if hasFlag(b, "--kube-apiserver-arg=audit-log-path", "/var/lib/socialweb/clusters/a/audit.log") {
		t.Fatal("a second cluster reused the first cluster's audit log; two tenants' " +
			"records in one file is the postgres-DSN collision again")
	}
	if hasFlag(a, "--kube-apiserver-arg=audit-log-path", "/var/lib/socialweb/clusters/b/audit.log") {
		t.Fatal("the first cluster's argv names the second cluster's log")
	}
}

func TestK3sAcceptsEveryFlagThisPackageEmits(t *testing.T) {
	bin := k3sOnPath(t)
	argv, err := valid().ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	if len(argv) < 2 || argv[0] != "k3s" || argv[1] != "server" {
		t.Fatalf("argv = %v; this test feeds argv[2:] to k3s server", argv)
	}
	if ok, out := k3sVerdict(bin, argv[2:]); !ok {
		t.Fatalf("k3s REFUSED this package's own argv:\n  %v\n\n%s\n\nTHE ASSERTIONS ABOVE "+
			"CANNOT SEE THIS: they compare this package's output with this package's "+
			"expectation, so a flag the CONSUMER rejects passes them all. Measured on this "+
			"host, k3s prints `Incorrect Usage: flag provided but not defined` and exits 1.", argv[2:], out)
	}
}

func TestTheK3sProbeDistinguishesAcceptedFromIgnored(t *testing.T) {
	bin := k3sOnPath(t)
	if ok, out := k3sVerdict(bin, []string{"--totally-made-up-flag"}); ok {
		t.Fatalf("the probe passed a flag k3s does not define, so a green result from it means "+
			"nothing about any other flag:\n%s", out)
	}
	if ok, out := k3sVerdict(bin, []string{"--disable-agent"}); !ok {
		t.Fatalf("the probe refused --disable-agent, which k3s accepts and does NOT list in "+
			"--help: a flag grepped out of --help is not a flag the binary lacks.\n%s", out)
	}
}

func TestK3sRefusesTheDirectAuditFlags(t *testing.T) {
	bin := k3sOnPath(t)
	for _, direct := range []string{
		"--audit-policy-file=/etc/socialweb/audit-policy.yaml",
		"--audit-log-path=/var/lib/socialweb/clusters/a/audit.log",
	} {
		if ok, _ := k3sVerdict(bin, []string{direct}); ok {
			t.Fatalf("k3s ACCEPTED the direct flag %s, so the --kube-apiserver-arg form this "+
				"package emits is a preference rather than a requirement -- and the negative "+
				"half of TestTheAuditFlagsTravelTogether asserts nothing", direct)
		}
	}
}

func TestOneOfTheFourIsNotAProcess(t *testing.T) {
	s := valid()
	refused := 0
	for _, c := range procgroup.StackOrder {
		argv, err := s.Argv(c)
		if errors.Is(err, ErrNotAProcess) {
			refused++
			if c != procgroup.ComponentPostgres {
				t.Fatalf("%s refused as a non-process; only Postgres is a library", c)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Argv(%s): %v", c, err)
		}
		if len(argv) == 0 {
			t.Fatalf("Argv(%s) is empty and did not refuse", c)
		}
	}
	if refused != 1 {
		t.Fatalf("%d of %d components refused; exactly one is a library and the rest are "+
			"binaries -- a second refusal means a builder went missing, and walking the REAL "+
			"bring-up order is what makes that fail", refused, len(procgroup.StackOrder))
	}
}

func TestEveryNonPostgresComponentHasAnArgv(t *testing.T) {
	s := valid()
	for _, c := range procgroup.StackOrder {
		if c == procgroup.ComponentPostgres {
			continue
		}
		if _, err := s.Argv(c); err != nil {
			t.Fatalf("Argv(%s): %v -- StackOrder is the list a stack's components are walked "+
				"in, so every member but Postgres must answer", c, err)
		}
	}
}

func TestPostgresIsConfiguredNotSpawned(t *testing.T) {
	pg, err := valid().PostgresConfig()
	if err != nil {
		t.Fatalf("PostgresConfig: %v", err)
	}
	if pg.Port != 15432 {
		t.Fatalf("port = %d, want the allocated 15432 -- the library's own default is 5432 "+
			"and every cluster sharing it is a silent cross-tenant read", pg.Port)
	}
	if _, err := (Spec{DataDir: valid().DataDir}).PostgresConfig(); !errors.Is(err, ErrMissingPort) {
		t.Fatalf("a spec with no postgres port produced a config: %v", err)
	}
}

func TestTheApiserverBindsLoopbackAndAdvertisesSomethingElse(t *testing.T) {
	s := valid()
	s.AdvertiseAddress = "192.0.2.10"
	argv, err := s.ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	if !hasFlag(argv, "--bind-address", "127.0.0.1") {
		t.Fatalf("argv = %v, want --bind-address 127.0.0.1. k3s defaults it to 0.0.0.0, which puts "+
			"EVERY tenant's apiserver on every interface of the host", argv)
	}
	if hasFlag(argv, "--bind-address", s.AdvertiseAddress) {
		t.Fatalf("argv = %v: the listener must NOT be the address this cluster advertises. The "+
			"apiserver rejects a loopback address for the in-cluster kubernetes Service endpoint, so "+
			"the advertised address is one the host carries -- and binding what is advertised is how "+
			"every tenant's control plane ends up reachable from the network", argv)
	}
	if !hasFlag(argv, "--advertise-address", s.AdvertiseAddress) {
		t.Fatalf("argv = %v, want --advertise-address %s", argv, s.AdvertiseAddress)
	}

	missing := valid()
	missing.BindAddress = ""
	if _, err := missing.ServerArgv(); !errors.Is(err, ErrMissingBindAddress) {
		t.Fatalf("an empty BindAddress was resolved to something: %v", err)
	}
}

func TestTheTLSSANsCarryTheAddressAGuestDials(t *testing.T) {
	s := valid()
	s.TLSSANs = []string{"192.0.2.7", "gateway.local"}
	argv, err := s.ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	for _, san := range s.TLSSANs {
		if !hasFlag(argv, "--tls-san", san) {
			t.Fatalf("argv = %v, want --tls-san %s. A node inside a guest dials the host through a "+
				"gateway address that is not one the host carries, so without this SAN the guest's "+
				"TLS verification fails and the failure reads as a certificate problem rather than "+
				"as a missing SAN", argv, san)
		}
	}
	plain := valid()
	argv, err = plain.ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	if hasFlag(argv, "--tls-san", "") {
		t.Fatalf("argv = %v carries an empty SAN", argv)
	}
}

func TestTheAdvertisePortIsOursNotADefault(t *testing.T) {
	argv, err := valid().ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	if !hasFlag(argv, "--advertise-port", "16443") {
		t.Fatalf("argv = %v, want --advertise-port 16443", argv)
	}

	other := valid()
	other.AdvertisePort = 26443
	argv, err = other.ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	if hasFlag(argv, "--advertise-port", "16443") {
		t.Fatal("a second cluster reused the first cluster's advertise port; two clusters " +
			"sharing it is a silent misroute, not a collision error")
	}
}

func TestTheK3sControlPlanePortsAreOursNotTheHosts(t *testing.T) {
	s := valid()
	argv, err := s.ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	for _, want := range []struct{ flag, value, why string }{
		{"--https-listen-port", "16445",
			"k3s defaults this to 6443, ONE PORT FOR THE WHOLE HOST, and it binds 16446 as well"},
		{"--kube-controller-manager-arg=secure-port", "16447",
			"kube-controller-manager defaults this to 10257 on 127.0.0.1, one for the host"},
		{"--kube-scheduler-arg=secure-port", "16448",
			"kube-scheduler defaults this to 10259 on 127.0.0.1, one for the host"},
		{"--kube-cloud-controller-manager-arg=secure-port", "16449",
			"cloud-controller-manager defaults this to 10258 on 127.0.0.1, one for the host"},
	} {
		if !hasFlag(argv, want.flag, want.value) {
			t.Fatalf("argv = %v\n\nwant %s=%s -- %s. MEASURED, all three control-plane ports are\n"+
				"held by the ONE k3s-server process, so a second cluster refuses to start",
				argv, want.flag, want.value, want.why)
		}
	}

	for _, a := range argv {
		if strings.HasPrefix(a, "--kube-apiserver-arg=secure-port") {
			t.Fatalf("argv carries %q: the apiserver's secure port is DERIVED by k3s as\n"+
				"https-listen-port+1 and is not one of the components this flag moves", a)
		}
	}
	if contains(argv, "--secure-port") {
		t.Fatalf("argv = %v\n\nwant the component ports VIA their passthroughs; --secure-port is "+
			"not a k3s flag and a bad command-line flag makes k3s refuse to start", argv)
	}

	if contains(argv, "--supervisor-port") {
		t.Fatalf("argv = %v\n\n--supervisor-port must NOT be emitted: it moves the supervisor "+
			"off the port the apiserver advertises, and setting it to that port's neighbour is "+
			"fatal (\"bind: address already in use\"). --https-listen-port already covers both "+
			"ports. See portallocator.PurposeK3sSecure", argv)
	}

	other := valid()
	other.K3sSecurePort = 26445
	other.K3sControllerManagerPort = 26447
	other.K3sSchedulerPort = 26448
	other.K3sCloudControllerManagerPort = 26449
	b, err := other.ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	for _, a := range b {
		for _, shared := range []string{"16445", "16447", "16448", "16449"} {
			if strings.HasSuffix(a, "="+shared) {
				t.Fatalf("a second cluster's argv carries the first cluster's %s in %q; two "+
					"clusters sharing a control-plane port is a bind failure at best", shared, a)
			}
		}
	}
}

func TestEgressSelectorIsCluster(t *testing.T) {
	argv, err := valid().ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	if !hasFlag(argv, "--egress-selector-mode", "cluster") {
		t.Fatalf("argv = %v, want --egress-selector-mode cluster", argv)
	}
}

func TestTheAgentIsDisabled(t *testing.T) {
	argv, err := valid().ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	if !contains(argv, "--disable-agent") {
		t.Fatalf("argv = %v, want --disable-agent", argv)
	}
}

func TestDisablingTheAgentDoesNotDisableTheAddons(t *testing.T) {
	argv, err := valid().ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	if !hasFlag(argv, "--disable", "traefik") && !contains(argv, "--disable=traefik") {
		t.Fatalf("argv = %v\n\nwant --disable=traefik. --disable-agent makes the server "+
			"not-a-node; it does NOT stop the manifest controller applying the bundled "+
			"addons, and traefik in a cluster with zero nodes is the design contradicting "+
			"itself", argv)
	}
}

func TestTheTokenTravelsAsAPathAndNeverAsAValue(t *testing.T) {
	s := valid()
	argv, err := s.ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	if !hasFlag(argv, "--token-file", s.TokenFile) {
		t.Fatalf("argv = %v\n\nwant --token-file %s", argv, s.TokenFile)
	}
	for _, a := range argv {
		if strings.HasPrefix(a, "--token=") || a == "--token" {
			t.Fatalf("argv = %v\n\nTHE TOKEN IS ON ARGV. The instance token is a live "+
				"credential and argv is world-readable in the process table; it travels "+
				"as a path to a 0600 file", argv)
		}
	}
}

func TestAMissingTokenFileIsRefused(t *testing.T) {
	s := valid()
	s.TokenFile = ""
	if _, err := s.ServerArgv(); !errors.Is(err, ErrMissingTokenFile) {
		t.Fatalf("ServerArgv with no TokenFile = %v, want ErrMissingTokenFile", err)
	}
}

func TestTheMeasuredServiceAccountFlags(t *testing.T) {
	s := valid()
	s.ServiceAccountIssuer = "https://kubernetes.default.svc.cluster.local"
	s.APIAudiences = []string{"https://kubernetes.default.svc.cluster.local", "k3s"}

	argv, err := s.ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	if !hasFlag(argv, "--kube-apiserver-arg=service-account-issuer",
		"https://kubernetes.default.svc.cluster.local") {
		t.Fatalf("argv = %v\n\nwant the measured issuer, VIA --kube-apiserver-arg: k3s "+
			"rejects --service-account-issuer as an unknown flag and will not start", argv)
	}
	if !hasFlag(argv, "--kube-apiserver-arg=api-audiences",
		"https://kubernetes.default.svc.cluster.local,k3s") {
		t.Fatalf("argv = %v\n\nwant both audiences comma-joined, VIA --kube-apiserver-arg", argv)
	}
	for _, a := range argv {
		if a == "--service-account-issuer" || a == "--api-audiences" {
			t.Fatalf("argv carries the DIRECT flag %q, which k3s rejects outright -- the "+
				"cluster would not start", a)
		}
	}

	bare := valid()
	argv, err = bare.ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	if contains(argv, "--service-account-issuer") {
		t.Fatalf("argv = %v, want the issuer OMITTED when unset rather than passed empty", argv)
	}
}

func TestValidateRefusesRatherThanDefaulting(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Spec)
		want error
	}{
		{"no data dir", func(s *Spec) { s.DataDir = "" }, ErrMissingDataDir},
		{"no postgres port", func(s *Spec) { s.PostgresPort = 0 }, ErrMissingPort},
		{"port out of range", func(s *Spec) { s.PostgresPort = 70000 }, ErrMissingPort},
		{"no store endpoint", func(s *Spec) { s.StoreEndpoint = "" }, ErrMissingEndpoint},
		{"no datastore endpoint", func(s *Spec) { s.DatastoreEndpoint = "" }, ErrMissingDatastoreEndpoint},
		{"no advertise address", func(s *Spec) { s.AdvertiseAddress = "" }, ErrMissingAdvertise},
		{"no advertise port", func(s *Spec) { s.AdvertisePort = 0 }, ErrMissingAdvertise},
		{"no k3s secure port", func(s *Spec) { s.K3sSecurePort = 0 }, ErrMissingK3sSecurePort},
		{"k3s secure port out of range", func(s *Spec) { s.K3sSecurePort = 70000 }, ErrMissingK3sSecurePort},
		{"no controller-manager port", func(s *Spec) { s.K3sControllerManagerPort = 0 }, ErrMissingK3sControlPlanePort},
		{"controller-manager port out of range", func(s *Spec) { s.K3sControllerManagerPort = 70000 }, ErrMissingK3sControlPlanePort},
		{"no scheduler port", func(s *Spec) { s.K3sSchedulerPort = 0 }, ErrMissingK3sControlPlanePort},
		{"scheduler port out of range", func(s *Spec) { s.K3sSchedulerPort = 70000 }, ErrMissingK3sControlPlanePort},
		{"no cloud-controller-manager port", func(s *Spec) { s.K3sCloudControllerManagerPort = 0 }, ErrMissingK3sControlPlanePort},
		{"cloud-controller-manager port out of range", func(s *Spec) { s.K3sCloudControllerManagerPort = 70000 }, ErrMissingK3sControlPlanePort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := valid()
			tc.mut(&s)
			if _, err := s.ServerArgv(); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestTheBundleDirsAgreeWithTheServer(t *testing.T) {
	s := valid()
	argv, err := s.ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	if !hasFlag(argv, "--data-dir", s.DataDir) {
		t.Fatalf("argv = %v, want --data-dir %s", argv, s.DataDir)
	}
	if got, want := TLSDir(s.DataDir), s.DataDir+"/server/tls"; got != want {
		t.Fatalf("TLSDir = %q, want %q -- the bundle lives under the data dir the server was "+
			"given, and nothing else may decide that", got, want)
	}
}

func TestKineCarriesNoSeparateT4(t *testing.T) {
	s := valid()
	argv, err := s.KineArgv()
	if err != nil {
		t.Fatalf("KineArgv: %v", err)
	}
	if !hasFlag(argv, "--endpoint", s.StoreEndpoint) {
		t.Fatalf("argv = %v, want --endpoint %s", argv, s.StoreEndpoint)
	}
	if !hasFlag(argv, "--listen-address", s.KineListen) {
		t.Fatalf("argv = %v\n\nkine defaults to 0.0.0.0:2379, ONE PORT FOR THE WHOLE "+
			"HOST, so the first cluster takes it and every later one FATALs at startup. "+
			"Pass --listen-address from portallocator.PurposeKine", argv)
	}
	if !contains(argv, "--metrics-bind-address=0") {
		t.Fatalf("argv = %v\n\nkine's metrics listener defaults to :8080, which is ONE "+
			"PORT FOR THE WHOLE HOST: the first cluster's kine takes it and every later "+
			"one FATALs at startup. Pass --metrics-bind-address=0", argv)
	}
	for _, a := range argv {
		if strings.Contains(a, "t4") && a != s.StoreEndpoint {
			t.Fatalf("kine's argv names t4 separately (%q); the backend is selected by the "+
				"endpoint scheme and a second process would be an untracked child", a)
		}
	}
}

func TestAnUnknownComponentIsRefused(t *testing.T) {
	if _, err := valid().Argv(procgroup.Component("kubelet")); err == nil {
		t.Fatal("an unknown component returned no error")
	}
}

func contains(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

func hasFlag(argv []string, flag, value string) bool {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) && argv[i+1] == value {
			return true
		}
		if a == flag+"="+value {
			return true
		}
	}
	return false
}
