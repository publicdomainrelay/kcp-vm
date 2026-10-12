package kcpclient

import (
	"net/http"
	"strings"
	"testing"
)

func TestTheTreeIsBuiltWithTheDeclaredTypes(t *testing.T) {
	wsReads := 0
	f := &fakeKCP{t: t, respond: func(req request) (int, any) {
		switch {
		case req.Method == http.MethodGet && strings.Contains(req.Path, "/apis/socialweb.computer/v1alpha1/clusters/"):
			return http.StatusOK, clusterBody(testCluster, "Ready", mountChildURL, "inst-1", true)
		case req.Method == http.MethodGet && strings.HasSuffix(req.Path, "/workspaces/"+ChildLabel):
			return notFound()
		case req.Method == http.MethodGet && strings.Contains(req.Path, "/workspaces/"):
			wsReads++
			if wsReads == 1 {
				return notFound()
			}
			return http.StatusOK, readyWorkspaceBody(AccountHash(testDID))
		case req.Method == http.MethodPost && strings.Contains(req.Path, "/workspaces"):
			return http.StatusCreated, workspaceBody(req.json(t)["metadata"].(map[string]any)["name"].(string), false)
		case req.Method == http.MethodPost:
			return http.StatusCreated, createdBody(t, req)
		}
		return notFound()
	}}
	c := newTestClient(t, f)

	if err := c.Ensure(t.Context(), testDID, testCluster); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	types := map[string]any{}
	for _, r := range f.seen() {
		if r.Method != http.MethodPost || !strings.Contains(r.Path, "/workspaces") {
			continue
		}
		body := r.json(t)
		name := body["metadata"].(map[string]any)["name"].(string)
		spec, _ := body["spec"].(map[string]any)
		types[name] = spec["type"]
	}

	if got := typeName(t, types[AccountHash(testDID)]); got != string(AccountWorkspaceType) {
		t.Errorf("the account workspace was created with type %q, want %q. An EMPTY or "+
			"`universal` type carries no defaultAPIBindings, so the design's auto-binding "+
			"cannot fire -- and spec.type is immutable, so it never can", got, AccountWorkspaceType)
	}
	if got := typeName(t, types[testCluster]); got != string(HomeWorkspaceType) {
		t.Errorf("the home workspace was created with type %q, want %q. This is where "+
			"`defaultAPIBindings` lives, so a home without it NEVER has the Cluster kind",
			got, HomeWorkspaceType)
	}
	if got := typeName(t, types[ChildLabel]); got != "" {
		t.Errorf("the mounted child was created with type %q, want none -- a mounted "+
			"workspace has no LogicalCluster and nothing to auto-bind", got)
	}
}

func typeName(t *testing.T, spec any) string {
	t.Helper()
	m, ok := spec.(map[string]any)
	if !ok {
		return ""
	}
	tr, ok := m["name"]
	if !ok {
		return ""
	}
	s, _ := tr.(string)
	return s
}
