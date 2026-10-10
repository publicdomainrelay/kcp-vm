//go:build linux

package clusterprovider

import (
	"bufio"
	"context"
	"crypto/rand"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterlink"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

const envDumbpipe = "SOCIALWEB_DUMBPIPE"

func TestTheLinkKeyIsKeptInTheClusterVaultAndReadBackAfterTheVaultRestarts(t *testing.T) {
	skipWithoutBao(t)
	ctx := context.Background()
	dataDir := t.TempDir()
	port := freePort(t)
	pg, _ := startTestPostgres(t, "linkkeys")
	stop := writeAndStartBao(t, vaultSpecForStore(t, dataDir, port, pg.VaultDSN()))
	if _, err := EnsureVault(ctx, dataDir, port); err != nil {
		t.Fatal(err)
	}

	store, err := VaultLinkKeys(ctx, dataDir, port)
	if err != nil {
		t.Fatal(err)
	}
	first, err := clusterlink.EnsureKey(ctx, store, v1alpha1.ClusterLinkApiserver, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if created, err := store.Create(ctx, string(v1alpha1.ClusterLinkApiserver), map[string]any{clusterlink.FieldSecret: strings.Repeat("00", 32)}); err != nil || created {
		t.Fatalf("a second first-write over the stored key = %v %v; check-and-set must refuse it", created, err)
	}

	stop()
	writeAndStartBao(t, vaultSpecForStore(t, dataDir, port, pg.VaultDSN()))
	if _, err := EnsureVault(ctx, dataDir, port); err != nil {
		t.Fatal(err)
	}
	again, err := VaultLinkKeys(ctx, dataDir, port)
	if err != nil {
		t.Fatal(err)
	}
	second, err := clusterlink.EnsureKey(ctx, again, v1alpha1.ClusterLinkApiserver, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if second.EndpointID != first.EndpointID || second.Did != first.Did {
		t.Fatalf("after the vault restarted the link is %s, before it was %s", second.EndpointID, first.EndpointID)
	}
	other, err := clusterlink.EnsureKey(ctx, again, v1alpha1.ClusterLinkOpenBao, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if other.EndpointID == first.EndpointID {
		t.Fatal("the openbao link took the apiserver link's key")
	}
}

func dumbpipeBinary(t *testing.T) string {
	t.Helper()
	if path := os.Getenv(envDumbpipe); path != "" {
		return path
	}
	path, err := exec.LookPath("dumbpipe")
	if err != nil {
		livegate.Require(t, "dumbpipe is not on PATH and %s is unset: there is no iroh listener to run", envDumbpipe)
	}
	return path
}

func echoServer(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := c.LocalAddr().(*net.UDPAddr).Port
	_ = c.Close()
	return port
}

func dialThroughTicket(t *testing.T, binary, ticket string) {
	t.Helper()
	local := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "connect-tcp", "--addr", "127.0.0.1:"+strconv.Itoa(local), ticket)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	deadline := time.Now().Add(60 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(local), 2*time.Second)
		if err != nil {
			last = err
			time.Sleep(200 * time.Millisecond)
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
		if _, err := conn.Write([]byte("through the link\n")); err != nil {
			last = err
			_ = conn.Close()
			continue
		}
		line, err := bufio.NewReader(conn).ReadString('\n')
		_ = conn.Close()
		if err == nil && line == "through the link\n" {
			return
		}
		last = err
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("nothing came back through %s: %v", ticket, last)
}

func TestARealDumbpipeLinkCarriesTCPToItsTargetAndAnOldTicketStillReachesItAfterARestart(t *testing.T) {
	livegate.Short(t, "this test runs real dumbpipe listeners and dials them over iroh")
	binary := dumbpipeBinary(t)
	h := newLinkHarness(t)
	h.links.Dumbpipe = binary
	h.links.Random = rand.Reader
	cfg := linkStackConfig(t, h.links)

	apiserver, vault := echoServer(t), echoServer(t)
	leases := linkLeases(apiserver)
	leases[portallocator.PurposeOpenBao] = portallocator.Lease{Port: vault, Width: 1, Cluster: "liveweb", Purpose: portallocator.PurposeOpenBao}
	leases[portallocator.PurposeApiserverLink] = portallocator.Lease{Port: freeUDPPort(t), Width: 1, Cluster: "liveweb", Purpose: portallocator.PurposeApiserverLink}
	leases[portallocator.PurposeOpenBaoLink] = portallocator.Lease{Port: freeUDPPort(t), Width: 1, Cluster: "liveweb", Purpose: portallocator.PurposeOpenBaoLink}

	spawn := func() *procgroup.Group {
		steps, err := StepsFor(cfg)(observed(), materialiser.Leases(leases))
		if err != nil {
			t.Fatal(err)
		}
		g, err := procgroup.Spawn(context.Background(), linkSteps(t, steps), procgroup.Options{GracePeriod: 5 * time.Second})
		if err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		h.setLive(true)
		return g
	}

	g := spawn()
	api := h.recorder.await(t, "liveweb-apiserver", v1alpha1.ClusterLinkListening).status
	bao := h.recorder.await(t, "liveweb-openbao", v1alpha1.ClusterLinkListening).status
	t.Logf("apiserver link %s %s ticket %s", api.EndpointID, api.Did, api.Ticket)
	dialThroughTicket(t, binary, api.Ticket)
	dialThroughTicket(t, binary, bao.Ticket)

	h.setLive(false)
	if err := g.Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.recorder.await(t, "liveweb-apiserver", v1alpha1.ClusterLinkPending)

	g = spawn()
	defer g.Reap(context.Background())
	after := h.recorder.await(t, "liveweb-apiserver", v1alpha1.ClusterLinkListening).status
	if after.EndpointID != api.EndpointID || after.Did != api.Did {
		t.Fatalf("restarted link is %s, before it was %s", after.EndpointID, api.EndpointID)
	}
	dialThroughTicket(t, binary, api.Ticket)
}
