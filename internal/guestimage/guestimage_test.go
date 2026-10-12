package guestimage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func digest(b []byte) string {
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}

func fixture() (map[string][]byte, Pin) {
	files := map[string][]byte{
		KernelName:   []byte("an ELF kernel"),
		RootfsName:   []byte(strings.Repeat("ext4 ", 4096)),
		ManifestName: []byte(`{"vmm":"firecracker","bootArgs":["console=ttyS0"]}` + "\n"),
	}
	pin := Pin{Target: "firecracker-x86_64", Hypervisor: "firecracker", Arch: "x86_64", Files: map[string]FilePin{}}
	for name, body := range files {
		pin.Files[name] = FilePin{SHA256: digest(body), Bytes: int64(len(body))}
	}
	return files, pin
}

func serve(t *testing.T, files map[string][]byte) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, ok := files[strings.TrimPrefix(r.URL.Path, "/images/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestAnImageWhoseShaDiffersIsRefused(t *testing.T) {
	files, pin := fixture()
	tampered := map[string][]byte{}
	for k, v := range files {
		tampered[k] = v
	}
	tampered[RootfsName] = []byte(strings.Repeat("evil ", 4096))
	srv, _ := serve(t, tampered)
	store := Store{Root: t.TempDir()}
	_, err := store.Ensure(context.Background(), pin, srv.URL+"/images")
	if err == nil || !strings.Contains(err.Error(), RootfsName) {
		t.Fatalf("an image whose rootfs does not hash to its pin was accepted: %v", err)
	}
	if _, err := os.Stat(store.Dir(pin)); !os.IsNotExist(err) {
		t.Fatalf("the refused image was left in the store: %v", err)
	}
	entries, _ := os.ReadDir(store.Root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".fetch-") {
			t.Fatalf("a partial fetch was left behind: %s", e.Name())
		}
	}

	good, _ := serve(t, files)
	dir, err := store.Ensure(context.Background(), pin, good.URL+"/images")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir.Dir, KernelName), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir.Dir, KernelName), []byte("swapped on disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Verify(pin); err == nil {
		t.Fatal("a stored image whose kernel was swapped on disk verified")
	}
}

func TestAnImageIsFetchedOnceAndReusedByHash(t *testing.T) {
	files, pin := fixture()
	srv, hits := serve(t, files)
	store := Store{Root: t.TempDir()}
	first, err := store.Ensure(context.Background(), pin, srv.URL+"/images")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Fetched || hits.Load() != 3 {
		t.Fatalf("first ensure: fetched=%v hits=%d", first.Fetched, hits.Load())
	}
	second, err := store.Ensure(context.Background(), pin, srv.URL+"/images")
	if err != nil {
		t.Fatal(err)
	}
	if second.Fetched || hits.Load() != 3 || second.Dir != first.Dir {
		t.Fatalf("second ensure fetched again or moved: %+v hits=%d", second, hits.Load())
	}
	want := filepath.Join(store.Root, "firecracker", "x86_64", pin.Files[ManifestName].SHA256)
	if first.Dir != want {
		t.Fatalf("stored at %s, want keyed by hypervisor, arch and manifest sha256: %s", first.Dir, want)
	}
	for name, body := range files {
		got, err := os.ReadFile(filepath.Join(first.Dir, name))
		if err != nil || string(got) != string(body) {
			t.Fatalf("%s not stored whole: %v", name, err)
		}
	}
	local := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(local, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	other := Store{Root: t.TempDir()}
	fromFile, err := other.Ensure(context.Background(), pin, "file://"+local)
	if err != nil || !fromFile.Fetched {
		t.Fatalf("a file:// source: %+v %v", fromFile, err)
	}
}
