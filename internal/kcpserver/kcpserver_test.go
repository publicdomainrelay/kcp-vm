package kcpserver

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func cfg() SlotConfig {
	return SlotConfig{Unit: "socialweb-kcp-abcd1234-slot-a.service", Exec: "/opt/kcp", Root: "/s/kcp/slot-a",
		EtcdServers: "http://127.0.0.1:41234", Shape: ShapeBlueGreen, FrontURL: "https://127.0.0.1:45001"}
}

func TestTheSlotArgvCarriesLeaderElectionAndPortZeroAndItsKine(t *testing.T) {
	argv, err := SlotArgv(cfg())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"start", LeaderElection, WorkspaceMountsGate, "--secure-port=0", "--experimental-bind-free-port",
		"--etcd-servers=http://127.0.0.1:41234", "--bind-address=127.0.0.1", "--root-directory=/s/kcp/slot-a"} {
		if !slices.Contains(argv, want) {
			t.Errorf("argv lacks %s: %v", want, argv)
		}
	}
	c := cfg()
	c.FrontURL = "https://127.0.0.1:45000"
	argv, _ = SlotArgv(c)
	if !slices.Contains(argv, "--shard-base-url=https://127.0.0.1:45000") {
		t.Errorf("a front URL is not the shard base URL: %v", argv)
	}
}

func TestKcpIsRefusedWithoutTheWorkspaceMountsGate(t *testing.T) {
	argv, _ := SlotArgv(cfg())
	var cut []string
	for _, a := range argv {
		if a != WorkspaceMountsGate {
			cut = append(cut, a)
		}
	}
	if err := CheckArgv(cut, ShapeBlueGreen); err == nil || !strings.Contains(err.Error(), "WorkspaceMounts") {
		t.Fatalf("%v", err)
	}
	c := cfg()
	c.Exec = "kcp"
	if _, err := SlotArgv(c); err == nil {
		t.Fatal("a PATH lookup name was accepted")
	}
	c = cfg()
	c.EtcdServers = ""
	if _, err := SlotArgv(c); err == nil {
		t.Fatal("a slot without its kine was accepted")
	}
}

func writeKubeconfig(t *testing.T, srv *httptest.Server, server string) string {
	t.Helper()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	c := clientcmdapi.NewConfig()
	c.Clusters["base"] = &clientcmdapi.Cluster{Server: server, CertificateAuthorityData: ca}
	c.AuthInfos["shard-admin"] = &clientcmdapi.AuthInfo{Token: "shard-token"}
	c.AuthInfos["kcp-admin"] = &clientcmdapi.AuthInfo{Token: "volatile"}
	c.Contexts["system:admin"] = &clientcmdapi.Context{Cluster: "base", AuthInfo: "shard-admin"}
	c.Contexts["root"] = &clientcmdapi.Context{Cluster: "base", AuthInfo: "kcp-admin"}
	c.CurrentContext = "root"
	p := filepath.Join(t.TempDir(), "admin.kubeconfig")
	if err := clientcmd.WriteToFile(*c, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func readyServer(t *testing.T) (*httptest.Server, int) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" || r.Header.Get("Authorization") != "Bearer shard-token" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	port, err := PortOf(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return srv, port
}

func TestReadinessIsReadOnTheSlotsOwnPortWithTheShardAdminToken(t *testing.T) {
	srv, port := readyServer(t)
	kc := writeKubeconfig(t, srv, "https://127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := WaitReady(ctx, port, kc, time.Time{}, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestReadinessIsNeverReadThroughACopiedAdminKubeconfig(t *testing.T) {
	srv, _ := readyServer(t)
	kc := writeKubeconfig(t, srv, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := WaitReady(ctx, 1, kc, time.Time{}, 20*time.Millisecond); err == nil {
		t.Fatal("a kubeconfig naming a live server made a dead slot port Ready")
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(kc, old, old)
	_, port := readyServer(t)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	if _, err := WaitReady(ctx2, port, kc, time.Now(), 20*time.Millisecond); err == nil {
		t.Fatal("a kubeconfig older than this start was trusted")
	}
}
