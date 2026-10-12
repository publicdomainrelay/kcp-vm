package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

type recordedRequest struct {
	Method string

	Path string

	ContentType string

	Body string
}

func newRegistryServer(t *testing.T, respond func(w http.ResponseWriter, r *http.Request, index int)) (*Registry, *[]recordedRequest) {
	t.Helper()
	seen := &[]recordedRequest{}
	index := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*seen = append(*seen, recordedRequest{
			Method:      r.Method,
			Path:        r.URL.Path,
			ContentType: r.Header.Get("Content-Type"),
			Body:        string(body),
		})
		respond(w, r, index)
		index++
	}))
	t.Cleanup(server.Close)
	registry, err := NewRegistry(RegistryOptions{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return registry, seen
}

func registryWriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func TestRegistryReadsThroughTheClusterPath(t *testing.T) {
	registry, seen := newRegistryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		registryWriteJSON(w, 200, map[string]any{
			"metadata": map[string]any{"name": "run-1", "namespace": "default"},
			"status":   map[string]any{"runID": "abc"},
		})
	})
	run, err := registry.Read(context.Background(), Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Name != "run-1" || run.Status.RunID != "abc" {
		t.Fatalf("run = %+v", run)
	}
	want := "/clusters/root:alice/apis/deno.computer/v1alpha1/namespaces/default/policyworkflowruns/run-1"
	if (*seen)[0].Method != "GET" || (*seen)[0].Path != want {
		t.Fatalf("request = %s %s, want GET %s", (*seen)[0].Method, (*seen)[0].Path, want)
	}
}

func TestRegistryListsRunsClusterWide(t *testing.T) {
	registry, seen := newRegistryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		registryWriteJSON(w, 200, map[string]any{"items": []any{
			map[string]any{"metadata": map[string]any{"name": "run-1"}},
			map[string]any{"metadata": map[string]any{"name": "run-2"}},
		}})
	})
	runs, err := registry.ListRuns(context.Background(), "root:alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].Name != "run-1" {
		t.Fatalf("runs = %+v", runs)
	}
	want := "/clusters/root:alice/apis/deno.computer/v1alpha1/denoruns"
	if (*seen)[0].Method != "GET" || (*seen)[0].Path != want {
		t.Fatalf("request = %s %s, want GET %s", (*seen)[0].Method, (*seen)[0].Path, want)
	}
}

func TestRegistryCreatesRunsInTheirNamespace(t *testing.T) {
	registry, seen := newRegistryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		registryWriteJSON(w, 201, map[string]any{"metadata": map[string]any{"name": "run-1"}})
	})
	run := &v1alpha1.DenoRun{ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "default"}}
	if err := registry.CreateRun(context.Background(), "root:alice", run); err != nil {
		t.Fatal(err)
	}
	want := "/clusters/root:alice/apis/deno.computer/v1alpha1/namespaces/default/denoruns"
	if (*seen)[0].Method != "POST" || (*seen)[0].Path != want {
		t.Fatalf("request = %s %s, want POST %s", (*seen)[0].Method, (*seen)[0].Path, want)
	}
	if !strings.Contains((*seen)[0].Body, `"name":"run-1"`) {
		t.Fatalf("body = %s", (*seen)[0].Body)
	}
}

func TestRegistryDeletesWithTheBackgroundPolicyAndToleratesNotFound(t *testing.T) {
	registry, seen := newRegistryServer(t, func(w http.ResponseWriter, _ *http.Request, index int) {
		if index == 0 {
			registryWriteJSON(w, 200, map[string]any{"kind": "Status", "status": "Success"})
			return
		}
		registryWriteJSON(w, 404, map[string]any{"kind": "Status", "reason": "NotFound", "code": 404})
	})
	ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "run-1"}
	if err := registry.DeleteRun(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	want := "/clusters/root:alice/apis/deno.computer/v1alpha1/namespaces/default/denoruns/run-1"
	if (*seen)[0].Method != "DELETE" || (*seen)[0].Path != want {
		t.Fatalf("request = %s %s, want DELETE %s", (*seen)[0].Method, (*seen)[0].Path, want)
	}
	if !strings.Contains((*seen)[0].Body, `"propagationPolicy":"Background"`) {
		t.Fatalf("body = %s", (*seen)[0].Body)
	}
	if err := registry.DeleteRun(context.Background(), ref); err != nil {
		t.Fatalf("a delete of a missing object is not an error: %v", err)
	}
}

func TestRegistryWritesRunStatusAsAMergePatchWithEveryManagedField(t *testing.T) {
	registry, seen := newRegistryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		registryWriteJSON(w, 200, map[string]any{})
	})
	ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "run-1", ResourceVersion: "42"}
	st := v1alpha1.DenoRunStatus{Phase: v1alpha1.DenoRunRunning, RunID: "abc"}
	if err := registry.WriteRunStatus(context.Background(), ref, st); err != nil {
		t.Fatal(err)
	}
	request := (*seen)[0]
	want := "/clusters/root:alice/apis/deno.computer/v1alpha1/namespaces/default/denoruns/run-1/status"
	if request.Method != "PATCH" || request.Path != want {
		t.Fatalf("request = %s %s, want PATCH %s", request.Method, request.Path, want)
	}
	if !strings.Contains(request.ContentType, "merge-patch") {
		t.Fatalf("content type = %q", request.ContentType)
	}
	var patch struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Status map[string]any `json:"status"`
	}
	if err := json.Unmarshal([]byte(request.Body), &patch); err != nil {
		t.Fatal(err)
	}
	if patch.Metadata.ResourceVersion != "42" {
		t.Fatalf("resourceVersion = %q, want the Ref's 42", patch.Metadata.ResourceVersion)
	}
	for _, key := range []string{"phase", "runID", "message", "exitCode", "retries",
		"startTime", "completionTime", "outputs", "conditions"} {
		if _, ok := patch.Status[key]; !ok {
			t.Fatalf("status field %q missing: %v", key, patch.Status)
		}
	}
	if patch.Status["phase"] != "Running" {
		t.Fatalf("phase = %v", patch.Status["phase"])
	}
}

func TestRegistryRemovesARunFinalizerWithATestOp(t *testing.T) {
	registry, seen := newRegistryServer(t, func(w http.ResponseWriter, _ *http.Request, index int) {
		if index == 0 {
			registryWriteJSON(w, 200, map[string]any{
				"metadata": map[string]any{"name": "run-1", "finalizers": []string{v1alpha1.FinalizerDenoRun, "b"}},
			})
			return
		}
		registryWriteJSON(w, 200, map[string]any{})
	})
	ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "run-1"}
	if err := registry.RemoveRunFinalizer(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if (*seen)[0].Method != "GET" {
		t.Fatalf("first request = %s, want the read back of the object", (*seen)[0].Method)
	}
	patchRequest := (*seen)[1]
	if patchRequest.Method != "PATCH" || !strings.Contains(patchRequest.ContentType, "json-patch") {
		t.Fatalf("patch request = %s %q", patchRequest.Method, patchRequest.ContentType)
	}
	if !strings.Contains(patchRequest.Body, `"op":"test"`) {
		t.Fatalf("patch = %s", patchRequest.Body)
	}
	if !strings.Contains(patchRequest.Body, `"value":["`+v1alpha1.FinalizerDenoRun+`","b"]`) {
		t.Fatalf("patch does not test the current list: %s", patchRequest.Body)
	}
	if !strings.Contains(patchRequest.Body, `"value":["b"]`) {
		t.Fatalf("patch does not remove exactly the one finalizer: %s", patchRequest.Body)
	}
}

func TestRegistryListsOpenBaosInOneNamespace(t *testing.T) {
	registry, seen := newRegistryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		registryWriteJSON(w, 200, map[string]any{"items": []any{
			map[string]any{"metadata": map[string]any{"name": "bao-1", "namespace": "ns"}},
		}})
	})
	baos, err := registry.ListOpenBaos(context.Background(), "root:alice", "ns")
	if err != nil {
		t.Fatal(err)
	}
	if len(baos) != 1 || baos[0].Name != "bao-1" {
		t.Fatalf("openbaos = %+v", baos)
	}
	want := "/clusters/root:alice/apis/deno.computer/v1alpha1/namespaces/ns/openbaos"
	if (*seen)[0].Method != "GET" || (*seen)[0].Path != want {
		t.Fatalf("request = %s %s, want GET %s", (*seen)[0].Method, (*seen)[0].Path, want)
	}
}

func TestRegistryReadsEachKindFromItsResource(t *testing.T) {
	cases := []struct {
		name     string
		resource string
		read     func(*Registry, context.Context, Ref) error
	}{
		{"policyworkflowrun", "policyworkflowruns", func(r *Registry, ctx context.Context, ref Ref) error {
			_, err := r.Read(ctx, ref)
			return err
		}},
		{"denorun", "denoruns", func(r *Registry, ctx context.Context, ref Ref) error {
			_, err := r.ReadRun(ctx, ref)
			return err
		}},
		{"denopod", "denopods", func(r *Registry, ctx context.Context, ref Ref) error {
			_, err := r.ReadPod(ctx, ref)
			return err
		}},
		{"denojob", "denojobs", func(r *Registry, ctx context.Context, ref Ref) error {
			_, err := r.ReadJob(ctx, ref)
			return err
		}},
		{"runtrigger", "runtriggers", func(r *Registry, ctx context.Context, ref Ref) error {
			_, err := r.ReadTrigger(ctx, ref)
			return err
		}},
		{"policyengine", "policyengines", func(r *Registry, ctx context.Context, ref Ref) error {
			_, err := r.ReadEngine(ctx, ref)
			return err
		}},
		{"policyworkflowpod", "policyworkflowpods", func(r *Registry, ctx context.Context, ref Ref) error {
			_, err := r.ReadWorkflowPod(ctx, ref)
			return err
		}},
		{"openbao", "openbaos", func(r *Registry, ctx context.Context, ref Ref) error {
			_, err := r.ReadOpenBao(ctx, ref)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry, seen := newRegistryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
				registryWriteJSON(w, 200, map[string]any{"metadata": map[string]any{"name": "obj-1", "namespace": "default"}})
			})
			ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "obj-1"}
			if err := tc.read(registry, context.Background(), ref); err != nil {
				t.Fatal(err)
			}
			want := "/clusters/root:alice/apis/deno.computer/v1alpha1/namespaces/default/" + tc.resource + "/obj-1"
			if (*seen)[0].Method != "GET" || (*seen)[0].Path != want {
				t.Fatalf("request = %s %s, want GET %s", (*seen)[0].Method, (*seen)[0].Path, want)
			}
		})
	}
}

func TestRegistryListsWorkflowRunsClusterWide(t *testing.T) {
	registry, seen := newRegistryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		registryWriteJSON(w, 200, map[string]any{"items": []any{
			map[string]any{"metadata": map[string]any{"name": "run-1"}},
			map[string]any{"metadata": map[string]any{"name": "run-2"}},
		}})
	})
	runs, err := registry.ListWorkflowRuns(context.Background(), "root:alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].Name != "run-1" {
		t.Fatalf("runs = %+v", runs)
	}
	want := "/clusters/root:alice/apis/deno.computer/v1alpha1/policyworkflowruns"
	if (*seen)[0].Method != "GET" || (*seen)[0].Path != want {
		t.Fatalf("request = %s %s, want GET %s", (*seen)[0].Method, (*seen)[0].Path, want)
	}
}

func TestRegistryWritesEachKindStatusAsAMergePatch(t *testing.T) {
	cases := []struct {
		name     string
		resource string
		keys     []string
		write    func(*Registry, context.Context, Ref) error
	}{
		{"policyworkflowrun", "policyworkflowruns",
			[]string{"phase", "runID", "active", "succeeded", "failed", "retries", "exitStatus", "startTime", "completionTime", "outputs", "conditions"},
			func(r *Registry, ctx context.Context, ref Ref) error {
				return r.WriteStatus(ctx, ref, v1alpha1.PolicyWorkflowRunStatus{Phase: v1alpha1.PolicyWorkflowSucceeded})
			}},
		{"denorun", "denoruns",
			[]string{"phase", "runID", "message", "exitCode", "retries", "startTime", "completionTime", "outputs", "conditions"},
			func(r *Registry, ctx context.Context, ref Ref) error {
				return r.WriteRunStatus(ctx, ref, v1alpha1.DenoRunStatus{Phase: v1alpha1.DenoRunSucceeded})
			}},
		{"denopod", "denopods",
			[]string{"phase", "runID", "restarts", "message", "exitCode", "ready", "startTime", "completionTime", "outputs", "conditions"},
			func(r *Registry, ctx context.Context, ref Ref) error {
				return r.WritePodStatus(ctx, ref, v1alpha1.DenoPodStatus{Phase: v1alpha1.DenoPodRunning})
			}},
		{"denojob", "denojobs",
			[]string{"phase", "runName", "active", "ready", "succeeded", "failed", "retries", "exitCode", "runs", "startTime", "completionTime", "outputs", "conditions"},
			func(r *Registry, ctx context.Context, ref Ref) error {
				return r.WriteJobStatus(ctx, ref, v1alpha1.DenoJobStatus{Phase: v1alpha1.DenoJobRunning})
			}},
		{"runtrigger", "runtriggers",
			[]string{"phase", "matched", "jobName", "lastRun", "conditions"},
			func(r *Registry, ctx context.Context, ref Ref) error {
				return r.WriteTriggerStatus(ctx, ref, v1alpha1.RunTriggerStatus{Phase: v1alpha1.RunTriggerTriggered})
			}},
		{"policyengine", "policyengines",
			[]string{"phase", "runID", "endpoint", "restarts", "ready", "message", "startTime", "completionTime", "conditions"},
			func(r *Registry, ctx context.Context, ref Ref) error {
				return r.WriteEngineStatus(ctx, ref, v1alpha1.PolicyEngineStatus{Phase: v1alpha1.PolicyEngineRunning})
			}},
		{"policyworkflowpod", "policyworkflowpods",
			[]string{"phase", "endpoint", "active", "conditions"},
			func(r *Registry, ctx context.Context, ref Ref) error {
				return r.WriteWorkflowPodStatus(ctx, ref, v1alpha1.PolicyWorkflowPodStatus{Phase: v1alpha1.PolicyWorkflowPodRunning})
			}},
		{"openbao", "openbaos",
			[]string{"namespace", "serial", "chain", "ready", "message", "conditions"},
			func(r *Registry, ctx context.Context, ref Ref) error {
				return r.WriteOpenBaoStatus(ctx, ref, v1alpha1.OpenBaoStatus{Namespace: "alice.default"})
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry, seen := newRegistryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
				registryWriteJSON(w, 200, map[string]any{})
			})
			ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "obj-1", ResourceVersion: "42"}
			if err := tc.write(registry, context.Background(), ref); err != nil {
				t.Fatal(err)
			}
			request := (*seen)[0]
			want := "/clusters/root:alice/apis/deno.computer/v1alpha1/namespaces/default/" + tc.resource + "/obj-1/status"
			if request.Method != "PATCH" || request.Path != want {
				t.Fatalf("request = %s %s, want PATCH %s", request.Method, request.Path, want)
			}
			if !strings.Contains(request.ContentType, "merge-patch") {
				t.Fatalf("content type = %q", request.ContentType)
			}
			var patch struct {
				Metadata struct {
					ResourceVersion string `json:"resourceVersion"`
				} `json:"metadata"`
				Status map[string]any `json:"status"`
			}
			if err := json.Unmarshal([]byte(request.Body), &patch); err != nil {
				t.Fatal(err)
			}
			if patch.Metadata.ResourceVersion != "42" {
				t.Fatalf("resourceVersion = %q, want the Ref's 42", patch.Metadata.ResourceVersion)
			}
			for _, key := range tc.keys {
				if _, ok := patch.Status[key]; !ok {
					t.Fatalf("status field %q missing: %v", key, patch.Status)
				}
			}
		})
	}
}

func TestRegistryCreatesEachKindInItsNamespace(t *testing.T) {
	cases := []struct {
		name     string
		resource string
		create   func(*Registry, context.Context, string) error
	}{
		{"policyworkflowrun", "policyworkflowruns", func(r *Registry, ctx context.Context, logicalCluster string) error {
			return r.CreateWorkflowRun(ctx, logicalCluster, &v1alpha1.PolicyWorkflowRun{
				ObjectMeta: metav1.ObjectMeta{Name: "obj-1", Namespace: "default"},
			})
		}},
		{"denopod", "denopods", func(r *Registry, ctx context.Context, logicalCluster string) error {
			return r.CreatePod(ctx, logicalCluster, &v1alpha1.DenoPod{
				ObjectMeta: metav1.ObjectMeta{Name: "obj-1", Namespace: "default"},
			})
		}},
		{"denojob", "denojobs", func(r *Registry, ctx context.Context, logicalCluster string) error {
			return r.CreateJob(ctx, logicalCluster, &v1alpha1.DenoJob{
				ObjectMeta: metav1.ObjectMeta{Name: "obj-1", Namespace: "default"},
			})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry, seen := newRegistryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
				registryWriteJSON(w, 201, map[string]any{"metadata": map[string]any{"name": "obj-1"}})
			})
			if err := tc.create(registry, context.Background(), "root:alice"); err != nil {
				t.Fatal(err)
			}
			request := (*seen)[0]
			want := "/clusters/root:alice/apis/deno.computer/v1alpha1/namespaces/default/" + tc.resource
			if request.Method != "POST" || request.Path != want {
				t.Fatalf("request = %s %s, want POST %s", request.Method, request.Path, want)
			}
			if !strings.Contains(request.Body, `"name":"obj-1"`) {
				t.Fatalf("body = %s", request.Body)
			}
		})
	}
}

func TestRegistryDeletesEachKindAndToleratesNotFound(t *testing.T) {
	cases := []struct {
		name     string
		resource string
		delete   func(*Registry, context.Context, Ref) error
	}{
		{"policyworkflowrun", "policyworkflowruns", func(r *Registry, ctx context.Context, ref Ref) error {
			return r.Delete(ctx, ref)
		}},
		{"denorun", "denoruns", func(r *Registry, ctx context.Context, ref Ref) error {
			return r.DeleteRun(ctx, ref)
		}},
		{"denopod", "denopods", func(r *Registry, ctx context.Context, ref Ref) error {
			return r.DeletePod(ctx, ref)
		}},
		{"denojob", "denojobs", func(r *Registry, ctx context.Context, ref Ref) error {
			return r.DeleteJob(ctx, ref)
		}},
		{"policyengine", "policyengines", func(r *Registry, ctx context.Context, ref Ref) error {
			return r.DeleteEngine(ctx, ref)
		}},
		{"policyworkflowpod", "policyworkflowpods", func(r *Registry, ctx context.Context, ref Ref) error {
			return r.DeleteWorkflowPod(ctx, ref)
		}},
		{"openbao", "openbaos", func(r *Registry, ctx context.Context, ref Ref) error {
			return r.DeleteOpenBao(ctx, ref)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry, seen := newRegistryServer(t, func(w http.ResponseWriter, _ *http.Request, index int) {
				if index == 0 {
					registryWriteJSON(w, 200, map[string]any{"kind": "Status", "status": "Success"})
					return
				}
				registryWriteJSON(w, 404, map[string]any{"kind": "Status", "reason": "NotFound", "code": 404})
			})
			ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "obj-1"}
			if err := tc.delete(registry, context.Background(), ref); err != nil {
				t.Fatal(err)
			}
			request := (*seen)[0]
			want := "/clusters/root:alice/apis/deno.computer/v1alpha1/namespaces/default/" + tc.resource + "/obj-1"
			if request.Method != "DELETE" || request.Path != want {
				t.Fatalf("request = %s %s, want DELETE %s", request.Method, request.Path, want)
			}
			if !strings.Contains(request.Body, `"propagationPolicy":"Background"`) {
				t.Fatalf("body = %s", request.Body)
			}
			if err := tc.delete(registry, context.Background(), ref); err != nil {
				t.Fatalf("a delete of a missing %s is not an error: %v", tc.name, err)
			}
		})
	}
}

func TestRegistryRemovesEachKindFinalizerWithATestOp(t *testing.T) {
	cases := []struct {
		name      string
		resource  string
		finalizer string
		remove    func(*Registry, context.Context, Ref) error
	}{
		{"policyworkflowrun", "policyworkflowruns", v1alpha1.FinalizerPolicyWorkflowRun, func(r *Registry, ctx context.Context, ref Ref) error {
			return r.RemoveFinalizer(ctx, ref)
		}},
		{"denorun", "denoruns", v1alpha1.FinalizerDenoRun, func(r *Registry, ctx context.Context, ref Ref) error {
			return r.RemoveRunFinalizer(ctx, ref)
		}},
		{"denopod", "denopods", v1alpha1.FinalizerDenoPod, func(r *Registry, ctx context.Context, ref Ref) error {
			return r.RemovePodFinalizer(ctx, ref)
		}},
		{"policyengine", "policyengines", v1alpha1.FinalizerPolicyEngine, func(r *Registry, ctx context.Context, ref Ref) error {
			return r.RemoveEngineFinalizer(ctx, ref)
		}},
		{"openbao", "openbaos", v1alpha1.FinalizerOpenBao, func(r *Registry, ctx context.Context, ref Ref) error {
			return r.RemoveOpenBaoFinalizer(ctx, ref)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry, seen := newRegistryServer(t, func(w http.ResponseWriter, _ *http.Request, index int) {
				if index == 0 {
					registryWriteJSON(w, 200, map[string]any{
						"metadata": map[string]any{"name": "obj-1", "finalizers": []string{tc.finalizer, "other"}},
					})
					return
				}
				registryWriteJSON(w, 200, map[string]any{})
			})
			ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "obj-1"}
			if err := tc.remove(registry, context.Background(), ref); err != nil {
				t.Fatal(err)
			}
			objectPath := "/clusters/root:alice/apis/deno.computer/v1alpha1/namespaces/default/" + tc.resource + "/obj-1"
			if (*seen)[0].Method != "GET" || (*seen)[0].Path != objectPath {
				t.Fatalf("first request = %s %s, want GET %s", (*seen)[0].Method, (*seen)[0].Path, objectPath)
			}
			patchRequest := (*seen)[1]
			if patchRequest.Method != "PATCH" || patchRequest.Path != objectPath || !strings.Contains(patchRequest.ContentType, "json-patch") {
				t.Fatalf("patch request = %s %s %q", patchRequest.Method, patchRequest.Path, patchRequest.ContentType)
			}
			if !strings.Contains(patchRequest.Body, `"op":"test"`) {
				t.Fatalf("patch = %s", patchRequest.Body)
			}
			if !strings.Contains(patchRequest.Body, `"value":["`+tc.finalizer+`","other"]`) {
				t.Fatalf("patch does not test the current list: %s", patchRequest.Body)
			}
			if !strings.Contains(patchRequest.Body, `"value":["other"]`) {
				t.Fatalf("patch does not remove exactly the one finalizer: %s", patchRequest.Body)
			}
		})
	}
}

func TestRegistryRemovesAKnownRunFinalizerWithoutARead(t *testing.T) {
	registry, seen := newRegistryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		registryWriteJSON(w, 200, map[string]any{})
	})
	ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "run-1"}
	if err := registry.RemoveRunFinalizerKnown(context.Background(), ref, []string{v1alpha1.FinalizerDenoRun, "b"}); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 {
		t.Fatalf("a known finalizer needs no read, got %d requests", len(*seen))
	}
	request := (*seen)[0]
	if request.Method != "PATCH" || !strings.Contains(request.ContentType, "json-patch") {
		t.Fatalf("request = %s %q", request.Method, request.ContentType)
	}
	if !strings.Contains(request.Body, `"value":["`+v1alpha1.FinalizerDenoRun+`","b"]`) {
		t.Fatalf("patch does not test the known list: %s", request.Body)
	}
	if !strings.Contains(request.Body, `"value":["b"]`) {
		t.Fatalf("patch does not remove exactly the one finalizer: %s", request.Body)
	}
}

func TestRegistryAddsAnOpenBaoFinalizer(t *testing.T) {
	registry, seen := newRegistryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		registryWriteJSON(w, 200, map[string]any{})
	})
	ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "bao-1"}
	if err := registry.WriteOpenBaoFinalizer(context.Background(), ref, []string{v1alpha1.FinalizerOpenBao}); err != nil {
		t.Fatal(err)
	}
	request := (*seen)[0]
	want := "/clusters/root:alice/apis/deno.computer/v1alpha1/namespaces/default/openbaos/bao-1"
	if request.Method != "PATCH" || request.Path != want || !strings.Contains(request.ContentType, "json-patch") {
		t.Fatalf("request = %s %s %q, want PATCH %s", request.Method, request.Path, request.ContentType, want)
	}
	if !strings.Contains(request.Body, `"op":"add"`) || !strings.Contains(request.Body, v1alpha1.FinalizerOpenBao) {
		t.Fatalf("patch = %s", request.Body)
	}
}

func TestRegistryMintsAServiceAccountToken(t *testing.T) {
	cases := []struct {
		name      string
		namespace string
		wantNS    string
	}{
		{"explicit namespace", "ns", "ns"},
		{"default namespace", "", "default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry, seen := newRegistryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
				registryWriteJSON(w, 200, map[string]any{"status": map[string]any{"token": "TOKEN"}})
			})
			token, err := registry.MintServiceAccountToken(context.Background(), "root:alice", tc.namespace, "deno-runner", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if token != "TOKEN" {
				t.Fatalf("token = %q", token)
			}
			request := (*seen)[0]
			want := "/clusters/root:alice/api/v1/namespaces/" + tc.wantNS + "/serviceaccounts/deno-runner/token"
			if request.Method != "POST" || request.Path != want {
				t.Fatalf("request = %s %s, want POST %s", request.Method, request.Path, want)
			}
			for _, want := range []string{`"kind":"TokenRequest"`, `"apiVersion":"authentication.k8s.io/v1"`, `"expirationSeconds":3600`} {
				if !strings.Contains(request.Body, want) {
					t.Fatalf("body %s must contain %s", request.Body, want)
				}
			}
		})
	}
}

func TestRegistryReadsTheClusterPathAnnotation(t *testing.T) {
	registry, seen := newRegistryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		registryWriteJSON(w, 200, map[string]any{"metadata": map[string]any{"annotations": map[string]any{"kcp.io/path": "root:alice"}}})
	})
	path, err := registry.ClusterPath(context.Background(), "2j35eh7jjhsc8ny9")
	if err != nil {
		t.Fatal(err)
	}
	if path != "root:alice" {
		t.Fatalf("path = %q, want the kcp.io/path annotation", path)
	}
	want := "/clusters/2j35eh7jjhsc8ny9/apis/core.kcp.io/v1alpha1/logicalclusters/cluster"
	if (*seen)[0].Method != "GET" || (*seen)[0].Path != want {
		t.Fatalf("request = %s %s, want GET %s", (*seen)[0].Method, (*seen)[0].Path, want)
	}
}

func TestRegistryClusterPathErrorsWithoutTheAnnotation(t *testing.T) {
	registry, _ := newRegistryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		registryWriteJSON(w, 200, map[string]any{"metadata": map[string]any{"annotations": map[string]any{}}})
	})
	if _, err := registry.ClusterPath(context.Background(), "root:alice"); err == nil {
		t.Fatal("a logical cluster with no kcp.io/path annotation must be an error")
	}
}
