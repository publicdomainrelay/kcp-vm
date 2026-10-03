//go:build linux

package clusterprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeReachableOnlyWhenApiserverAnswers(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		reachable bool
	}{
		{"ok", http.StatusOK, true},
		{"unauthorized", http.StatusUnauthorized, true},
		{"not-ready-503", http.StatusServiceUnavailable, false},
		{"server-error-500", http.StatusInternalServerError, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			code, reachable := Probe(context.Background(), srv.URL)
			if code != tc.status {
				t.Errorf("Probe status = %d, want %d", code, tc.status)
			}
			if reachable != tc.reachable {
				t.Errorf("defect REL-1: Probe reported reachable=%v for HTTP %d; a control plane answering %d is not ready auditable and must not be routed to", reachable, tc.status, tc.status)
			}
		})
	}
}
