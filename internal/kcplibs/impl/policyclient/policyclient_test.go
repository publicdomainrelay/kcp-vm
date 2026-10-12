package policyclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/policy"
)

func TestSubmitRequiresAValidWorkflowAndEndpoint(t *testing.T) {
	client := New()
	if _, err := client.Submit(context.Background(), "", []byte(`{}`), nil); err == nil {
		t.Fatal("an empty endpoint must be refused")
	}
	if _, err := client.Submit(context.Background(), "http://engine.invalid", []byte(`not json`), nil); err == nil {
		t.Fatal("a workflow that is not JSON must be refused")
	}
}

func TestSubmitPostsTheWorkflowAndReturnsTheTaskID(t *testing.T) {
	var seen struct {
		method string

		path string

		body map[string]any
	}
	instance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.method = r.Method
		seen.path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &seen.body)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "detail": map[string]any{"id": "task-7"}})
	}))
	t.Cleanup(instance.Close)

	client := newWithClient(instance.Client())
	id, err := client.Submit(context.Background(), instance.URL+"/", []byte(`{"jobs":{"a":{}}}`), map[string]string{"perspective": "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "task-7" {
		t.Fatalf("id = %q", id)
	}
	if seen.method != http.MethodPost || seen.path != "/request/create" {
		t.Fatalf("request = %s %s", seen.method, seen.path)
	}
	inputs, _ := seen.body["inputs"].(map[string]any)
	if inputs["perspective"] != "alice" {
		t.Fatalf("body = %v", seen.body)
	}
}

func TestSubmitReportsAnEngineRefusal(t *testing.T) {
	instance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte("bad workflow"))
	}))
	t.Cleanup(instance.Close)
	client := newWithClient(instance.Client())
	_, err := client.Submit(context.Background(), instance.URL, []byte(`{}`), nil)
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("err = %v", err)
	}
}

func statusServer(t *testing.T, body string, code int) (*Client, string) {
	t.Helper()
	instance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(instance.Close)
	return newWithClient(instance.Client()), instance.URL
}

func TestStatusIsRunningUntilTheEngineReportsATerminalState(t *testing.T) {
	client, url := statusServer(t, `{"status":"in_progress","detail":{}}`, 200)
	task, err := client.Status(context.Background(), url, "task-7")
	if err != nil {
		t.Fatal(err)
	}
	if task.State != policy.StateRunning {
		t.Fatalf("task = %+v", task)
	}
}

func TestStatusMapsSuccess(t *testing.T) {
	client, url := statusServer(t, `{"status":"completed","detail":{"exit_status":"success","outputs":{"answer":42}}}`, 200)
	task, err := client.Status(context.Background(), url, "task-7")
	if err != nil {
		t.Fatal(err)
	}
	if task.State != policy.StateSucceeded {
		t.Fatalf("task = %+v", task)
	}
	if task.Outputs["answer"] != "42" {
		t.Fatalf("outputs = %v", task.Outputs)
	}
}

func TestStatusMapsFailure(t *testing.T) {
	client, url := statusServer(t, `{"status":"completed","detail":{"exit_status":"failure"}}`, 200)
	task, err := client.Status(context.Background(), url, "task-7")
	if err != nil {
		t.Fatal(err)
	}
	if task.State != policy.StateFailed || task.Message == "" {
		t.Fatalf("task = %+v", task)
	}
}

func TestStatusReadsTheSinglePolicyVerdict(t *testing.T) {
	body := `{"status":"completed","detail":{"exit_status":"success","cache":{
		"policy/allow":{"result.json":{"data":"{\"allow\":true,\"violations\":[{\"rule\":\"r\"}]}","encoding":"utf-8"}}
	}}}`
	client, url := statusServer(t, body, 200)
	task, err := client.Status(context.Background(), url, "task-7")
	if err != nil {
		t.Fatal(err)
	}
	if task.Outputs["allow"] != "true" {
		t.Fatalf("outputs = %v", task.Outputs)
	}
	if !strings.Contains(task.Outputs["violations"], `"rule":"r"`) {
		t.Fatalf("violations = %q", task.Outputs["violations"])
	}
	if _, prefixed := task.Outputs["allow/allow"]; prefixed {
		t.Fatalf("a single policy needs no name prefix: %v", task.Outputs)
	}
}

func TestStatusPrefixesMultiplePolicyVerdicts(t *testing.T) {
	body := `{"status":"completed","detail":{"exit_status":"success","cache":{
		"policy/one":{"result.json":{"data":"{\"allow\":true}","encoding":"utf-8"}},
		"policy/two":{"result.json":{"data":"{\"allow\":false}","encoding":"utf-8"}}
	}}}`
	client, url := statusServer(t, body, 200)
	task, err := client.Status(context.Background(), url, "task-7")
	if err != nil {
		t.Fatal(err)
	}
	if task.Outputs["one/allow"] != "true" || task.Outputs["two/allow"] != "false" {
		t.Fatalf("outputs = %v", task.Outputs)
	}
}

func TestStatusIsRunningWhenTheEngineIsUnreachable(t *testing.T) {
	client := newWithClient(&http.Client{Timeout: time.Millisecond})
	task, err := client.Status(context.Background(), "http://127.0.0.1:1", "task-7")
	if err != nil {
		t.Fatal(err)
	}
	if task.State != policy.StateRunning {
		t.Fatalf("an unreachable engine must not fail the task: %+v", task)
	}
}

func TestPolicyName(t *testing.T) {
	if got := PolicyName("policy/allow/result.json"); got != "allow" {
		t.Fatalf("name = %q", got)
	}
	if got := PolicyName("result.json"); got != "policy" {
		t.Fatalf("name = %q", got)
	}
}
