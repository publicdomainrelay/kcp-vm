package providermain

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"k8s.io/client-go/rest"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tenantnet"
)

func TestTheTenantAddOnsApplyTheNodeReaderAndThePerNodeDNSBeforeAnythingElse(t *testing.T) {
	var mu sync.Mutex
	var patches []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method == http.MethodPatch {
			mu.Lock()
			patches = append(patches, r.URL.Path+" "+r.URL.Query().Get("fieldManager")+" "+r.Header.Get("Content-Type"))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := Config{StateDir: t.TempDir()}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	err := tenantAddOns(cfg, log)(context.Background(), "abcd1234-ci", &rest.Config{Host: srv.URL})
	if err == nil {
		t.Fatal("the add-on pass found no vault unseal material and still succeeded")
	}
	want := []string{
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/" + tenantnet.NodeReaderName,
		"/apis/rbac.authorization.k8s.io/v1/clusterrolebindings/" + tenantnet.NodeReaderName,
		"/api/v1/namespaces/kube-system/serviceaccounts/" + tenantnet.DNSName,
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/" + tenantnet.DNSName,
		"/apis/rbac.authorization.k8s.io/v1/clusterrolebindings/" + tenantnet.DNSName,
		"/api/v1/namespaces/kube-system/configmaps/" + tenantnet.DNSName,
		"/apis/apps/v1/namespaces/kube-system/daemonsets/" + tenantnet.DNSName,
	}
	if len(patches) != len(want) {
		t.Fatalf("the tenant add-on pass applied %v, want the node reader and the per-node DNS (%d objects) "+
			"before it needs the cluster vault", patches, len(want))
	}
	for i, w := range want {
		fields := strings.Fields(patches[i])
		if fields[0] != w || fields[1] != clusterprovider.AddOnFieldManager || fields[2] != "application/apply-patch+yaml" {
			t.Fatalf("patch %d = %q, want a server-side apply of %s by %s", i, patches[i], w, clusterprovider.AddOnFieldManager)
		}
	}
}
