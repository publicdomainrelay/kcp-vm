package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodeprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

const (
	gitlabURLEnv = "SOCIALWEB_LIVE_GITLAB_URL"

	gitlabPATEnv = "SOCIALWEB_LIVE_GITLAB_TOKEN"

	gitlabProjectEnv = "SOCIALWEB_LIVE_GITLAB_PROJECT"

	gitlabRunnerTokenEnv = "SOCIALWEB_LIVE_GITLAB_RUNNER_TOKEN"

	gitlabRunnerBinEnv = "SOCIALWEB_LIVE_GITLAB_RUNNER_BIN"

	gitlabPipelineTimeout = 12 * time.Minute

	gitlabPipelinePoll = 5 * time.Second

	gitlabJobMarker = "gitlab-ci-ran-on"
)

func TestAGitLabPipelineRunsOnATenantCluster(t *testing.T) {
	missing := []string{}
	for _, name := range []string{
		gitlabURLEnv, gitlabPATEnv, gitlabProjectEnv, gitlabRunnerTokenEnv, gitlabRunnerBinEnv,
	} {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		livegate.Require(t, "unset: %s. These name a GitLab reachable at loopback, with a project, "+
			"a project access token and a project runner authentication token, and the gitlab-runner "+
			"binary. This is the only thing in the tree that proves a GitLab pipeline actually runs a "+
			"JOB on a tenant cluster this repo provisioned: every other proof stops at the runner's "+
			"Kubernetes API calls. It is skipped when they are unset because it needs a live GitLab "+
			"and a multi-gigabyte container, and a skip here is NOT a pass",
			strings.Join(missing, ", "))
	}

	gitlab := gitlabClient{
		url:     strings.TrimSuffix(os.Getenv(gitlabURLEnv), "/"),
		token:   os.Getenv(gitlabPATEnv),
		project: os.Getenv(gitlabProjectEnv),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
	runnerBin := os.Getenv(gitlabRunnerBinEnv)
	runnerToken := os.Getenv(gitlabRunnerTokenEnv)

	want := strings.TrimSpace(os.Getenv(fourstepSubstrateEnv))
	ran := 0
	for _, substrate := range fourstepSubstrates() {
		if want != "" && want != substrate {
			continue
		}
		ran++
		t.Run(substrate, func(t *testing.T) {
			gitlabPipelineRun(t, substrate, runnerBin, runnerToken, gitlab)
		})
	}
	if ran == 0 {
		livegate.Require(t, "%s is %q and this host is %s, which cannot run that substrate",
			fourstepSubstrateEnv, want, strings.Join(fourstepSubstrates(), ", "))
	}
}

func gitlabPipelineRun(t *testing.T, substrate, runnerBin, runnerToken string, gitlab gitlabClient) {
	bins := fourstepRequirements(t, substrate)
	ctx, cancel := context.WithTimeout(context.Background(), fourstepDeadline)
	defer cancel()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg := fourstepConfig(t, bins)
	stack := fourstepBringUp(t, ctx, cfg, bins, log)

	ready := stack.waitReady(t, ctx)
	t.Logf("STEP 1 the control plane reached %s at %s with zero nodes", ready.Phase, ready.URL)

	client, err := stack.tenantClient()
	if err != nil {
		t.Fatalf("the tenant client: %v", err)
	}
	gitlabNamespaceUp(t, ctx, client)
	t.Logf("STEP 2 namespace %s exists on the tenant cluster", gitlabNamespace)

	credDir, host := gitlabCredentialFiles(t, cfg, stack)
	t.Logf("STEP 3 minted a scoped tenant credential in %s against %s", credDir, host)

	kubeconfig := stack.kubeconfig(t)

	if err := gitlab.CommitPipelineFile(ctx, stack.bins.tenantImage); err != nil {
		t.Fatalf("putting a .gitlab-ci.yml on the project: %v", err)
	}
	t.Logf("STEP 4 the project carries a pipeline definition whose job image is %s - the image the "+
		"node already carries, so the job cannot fail on a pull", stack.bins.tenantImage)

	runner := gitlabStartRunner(t, runnerBin, runnerToken, gitlab.url, host, credDir)
	defer gitlabStopRunner(t, runner)

	online := gitlab.AwaitRunnerOnline(ctx)
	if !online.untagged {
		t.Fatalf("the project's runner is online but does not run untagged jobs (tags %v), and the "+
			"job this test defines carries no tags, so the job would sit pending until the pipeline "+
			"timed out rather than failing. A hang is not a result: set the runner to run untagged "+
			"jobs, or give both the runner and the job the same tag", online.tags)
	}
	t.Logf("STEP 5 the runner is online against %s and runs untagged jobs (tags %v)", host, online.tags)

	id, err := gitlab.TriggerPipeline(ctx)
	if err != nil {
		t.Fatalf("triggering a pipeline: %v\nrunner output:\n%s", err, runner.output())
	}
	t.Logf("STEP 6 pipeline %d triggered", id)

	placed := &placedPods{seen: map[string]string{}}
	pollCtx, pollCancel := context.WithCancel(ctx)
	defer pollCancel()
	go watchRunnerPods(pollCtx, client, placed)

	dec := stack.choose(t, ctx, scale.Provision)
	if dec.Nodes < 1 {
		t.Fatalf("the scale decision counted %d nodes for a pipeline job pod: %s", dec.Nodes, dec.Reason)
	}
	joined := stack.waitNodes(t, kubeconfig, 1)
	t.Logf("STEP 7 the job pod's own Pending state drove the scale decision (%s) and a node joined:\n%s",
		dec.Reason, joined)

	status := gitlab.AwaitPipeline(t, ctx, id)
	if status != "success" {
		t.Fatalf("pipeline %d finished %q, want success.\njobs:\n%s\nrunner output:\n%s",
			id, status, gitlab.PipelineJobs(ctx, id), runner.output())
	}
	t.Logf("STEP 8 pipeline %d succeeded", id)

	trace := gitlab.FirstJobTrace(ctx, id)
	if !strings.Contains(trace, gitlabJobMarker) {
		t.Fatalf("the pipeline succeeded but no job trace carries %q, so the job that ran is not "+
			"the one this test defines.\ntrace:\n%s", gitlabJobMarker, trace)
	}
	t.Logf("STEP 9 the job's own trace carries the marker")

	seen := placed.snapshot()
	if len(seen) == 0 {
		t.Fatalf("pipeline %d succeeded and no pod named runner-* was ever observed carrying a "+
			"spec.nodeName in %s, so the job cannot be shown to have run on a node this cluster "+
			"provisioned. The runner deletes the pod when the job ends, so it has to be caught while "+
			"it is alive", id, gitlabNamespace)
	}
	for name, node := range seen {
		t.Logf("STEP 10 GitLab's runner created pod %s, this cluster placed it on %s, and the job "+
			"exited 0", name, node)
	}
	wantNode, err := nodeprovider.NodeName(fourstepCluster, nodeID)
	if err != nil {
		t.Fatalf("nodeprovider.NodeName: %v", err)
	}
	onOurNode := false
	for _, node := range seen {
		if node == wantNode {
			onOurNode = true
		}
	}
	if !onOurNode {
		t.Fatalf("the runner's job pod ran on %v, and none of them is %s - the node the scale "+
			"decision provisioned for it. A job that ran elsewhere was not placed by this cluster",
			seen, wantNode)
	}
}

type placedPods struct {
	mu   sync.Mutex
	seen map[string]string
}

func (p *placedPods) record(name, node string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen[name] = node
}

func (p *placedPods) snapshot() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]string{}
	for k, v := range p.seen {
		out[k] = v
	}
	return out
}

func watchRunnerPods(ctx context.Context, client kubernetes.Interface, placed *placedPods) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			pods, err := client.CoreV1().Pods(gitlabNamespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				continue
			}
			for i := range pods.Items {
				pod := &pods.Items[i]
				if strings.HasPrefix(pod.Name, "runner-") && pod.Spec.NodeName != "" {
					placed.record(pod.Name, pod.Spec.NodeName)
				}
			}
		}
	}
}

func gitlabCredentialFiles(t *testing.T, cfg Config, stack *fourstepStack) (string, string) {
	t.Helper()
	lease, ok := stack.alloc.PortFor(fourstepCluster, portallocator.PurposeK3sSecure)
	if !ok {
		t.Fatalf("no apiserver lease for %s", fourstepCluster)
	}
	minted, err := clusterprovider.MintTenantCredentialForCluster(
		context.Background(), cfg.StateDir, fourstepCluster, clusterprovider.APIServerURL(lease.Port))
	if err != nil {
		t.Fatalf("minting the runner's tenant credential: %v", err)
	}
	dir := t.TempDir()
	for _, f := range []struct {
		name string
		data []byte
	}{
		{"ca.crt", minted.CAData},
		{"tls.crt", minted.CertData},
		{"tls.key", minted.KeyData},
	} {
		if len(f.data) == 0 {
			t.Fatalf("the minted credential carries no %s, so the runner would be handed a partial "+
				"set and every call it makes would fail with a TLS error that names nothing", f.name)
		}
		if err := os.WriteFile(filepath.Join(dir, f.name), f.data, 0o600); err != nil {
			t.Fatalf("writing %s: %v", f.name, err)
		}
	}
	return dir, minted.Host
}

func gitlabRunnerConfig(url, token, host, credDir string) string {
	return fmt.Sprintf(`concurrent = 1
check_interval = 3

[[runners]]
  name = "socialweb-live"
  url = %q
  token = %q
  executor = "kubernetes"
  [runners.kubernetes]
    host = %q
    cert_file = %q
    key_file = %q
    ca_file = %q
    namespace = %q
    image_pull_policy = "if-not-present"
    poll_timeout = 300
`, url, token, host,
		filepath.Join(credDir, "tls.crt"), filepath.Join(credDir, "tls.key"),
		filepath.Join(credDir, "ca.crt"), gitlabNamespace)
}

func gitlabStartRunner(t *testing.T, bin, token, url, host, credDir string) *gitlabRunner {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(gitlabRunnerConfig(url, token, host, credDir)), 0o600); err != nil {
		t.Fatalf("writing the runner config: %v", err)
	}
	r := &gitlabRunner{buf: &bytes.Buffer{}}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.cmd = exec.CommandContext(r.ctx, bin, "run", "--config", path, "--working-directory", dir)
	r.cmd.Stdout = r.buf
	r.cmd.Stderr = r.buf
	if err := r.cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", bin, err)
	}
	select {
	case <-time.After(3 * time.Second):
	case <-t.Context().Done():
	}
	t.Logf("STEP 5a the runner is up with the kubernetes executor against %s", host)
	return r
}

func gitlabStopRunner(t *testing.T, r *gitlabRunner) {
	t.Helper()
	if r == nil || r.cmd == nil || r.cmd.Process == nil {
		return
	}
	r.cancel()
	done := make(chan struct{})
	go func() {
		_, _ = r.cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		_ = r.cmd.Process.Kill()
	}
}

type gitlabRunner struct {
	cmd    *exec.Cmd
	buf    *bytes.Buffer
	ctx    context.Context
	cancel context.CancelFunc
}

func (r *gitlabRunner) output() string {
	if r == nil || r.buf == nil {
		return "(no output)"
	}
	out := r.buf.String()
	if len(out) > 8000 {
		out = out[len(out)-8000:]
	}
	return out
}

type gitlabClient struct {
	url     string
	token   string
	project string
	http    *http.Client
}

func (g gitlabClient) api(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.url+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("PRIVATE-TOKEN", g.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

func (g gitlabClient) CommitPipelineFile(ctx context.Context, image string) error {
	content := fmt.Sprintf(`stages: [prove]

variables:
  GIT_STRATEGY: none

prove-placement:
  stage: prove
  image: %s
  script:
    - echo "%s $(hostname)"
    - cat /var/run/secrets/kubernetes.io/serviceaccount/namespace
`, image, gitlabJobMarker)
	path := "/api/v4/projects/" + g.project + "/repository/files/%2Egitlab-ci%2Eyml"
	payload := map[string]any{
		"branch":         "main",
		"content":        content,
		"commit_message": "add the pipeline this repo's live test runs",
		"encoding":       "text",
	}
	raw, code, err := g.api(ctx, http.MethodPost, path, payload)
	if err != nil {
		return err
	}
	if code == http.StatusCreated || code == http.StatusOK {
		return nil
	}
	if code == http.StatusBadRequest {
		raw2, code2, err := g.api(ctx, http.MethodPut, path, payload)
		if err != nil {
			return err
		}
		if code2 == http.StatusOK {
			return nil
		}
		return fmt.Errorf("updating the pipeline file: %d %s", code2, raw2)
	}
	return fmt.Errorf("creating the pipeline file: %d %s", code, raw)
}

func (g gitlabClient) AwaitRunnerOnline(ctx context.Context) runnerState {
	deadline := time.Now().Add(2 * time.Minute)
	var last runnerState
	for time.Now().Before(deadline) {
		raw, code, err := g.api(ctx, http.MethodGet, "/api/v4/projects/"+g.project+"/runners", nil)
		if err == nil && code == http.StatusOK {
			var runners []struct {
				ID     int    `json:"id"`
				Status string `json:"status"`
			}
			if json.Unmarshal(raw, &runners) == nil {
				for _, r := range runners {
					if r.Status != "online" {
						continue
					}
					detail, dcode, derr := g.api(ctx, http.MethodGet,
						fmt.Sprintf("/api/v4/runners/%d", r.ID), nil)
					if derr != nil || dcode != http.StatusOK {
						continue
					}
					var d struct {
						RunUntagged bool     `json:"run_untagged"`
						TagList     []string `json:"tag_list"`
					}
					if json.Unmarshal(detail, &d) != nil {
						continue
					}
					return runnerState{untagged: d.RunUntagged, tags: d.TagList}
				}
			}
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(3 * time.Second):
		}
	}
	return last
}

type runnerState struct {
	untagged bool
	tags     []string
}

func (g gitlabClient) TriggerPipeline(ctx context.Context) (int, error) {
	raw, code, err := g.api(ctx, http.MethodPost,
		"/api/v4/projects/"+g.project+"/pipeline", map[string]any{"ref": "main"})
	if err != nil {
		return 0, err
	}
	if code != http.StatusCreated && code != http.StatusOK {
		return 0, fmt.Errorf("triggering the pipeline: %d %s", code, raw)
	}
	var out struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, fmt.Errorf("the trigger response is not a pipeline: %w (%s)", err, raw)
	}
	if out.ID == 0 {
		return 0, fmt.Errorf("the trigger returned no pipeline id: %s", raw)
	}
	return out.ID, nil
}

func (g gitlabClient) AwaitPipeline(t *testing.T, ctx context.Context, id int) string {
	t.Helper()
	deadline := time.Now().Add(gitlabPipelineTimeout)
	last := ""
	for time.Now().Before(deadline) {
		raw, code, err := g.api(ctx, http.MethodGet,
			fmt.Sprintf("/api/v4/projects/%s/pipelines/%d", g.project, id), nil)
		if err == nil && code == http.StatusOK {
			var out struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(raw, &out) == nil {
				switch out.Status {
				case "success", "failed", "canceled", "skipped":
					return out.Status
				}
				if out.Status != last {
					last = out.Status
					t.Logf("pipeline %d is %s", id, last)
				}
			}
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(gitlabPipelinePoll):
		}
	}
	return last
}

func (g gitlabClient) PipelineJobs(ctx context.Context, id int) string {
	raw, code, err := g.api(ctx, http.MethodGet,
		fmt.Sprintf("/api/v4/projects/%s/pipelines/%d/jobs", g.project, id), nil)
	if err != nil || code != http.StatusOK {
		return fmt.Sprintf("(listing jobs: code %d err %v)", code, err)
	}
	var jobs []struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Reason string `json:"failure_reason"`
	}
	if json.Unmarshal(raw, &jobs) != nil {
		return "(unparseable jobs)"
	}
	out := []string{}
	for _, j := range jobs {
		out = append(out, fmt.Sprintf("%s id=%d status=%s reason=%q", j.Name, j.ID, j.Status, j.Reason))
	}
	return strings.Join(out, "\n")
}

func (g gitlabClient) FirstJobTrace(ctx context.Context, id int) string {
	raw, code, err := g.api(ctx, http.MethodGet,
		fmt.Sprintf("/api/v4/projects/%s/pipelines/%d/jobs", g.project, id), nil)
	if err != nil || code != http.StatusOK {
		return fmt.Sprintf("(listing jobs: code %d err %v)", code, err)
	}
	var jobs []struct {
		ID int `json:"id"`
	}
	if json.Unmarshal(raw, &jobs) != nil || len(jobs) == 0 {
		return "(no jobs)"
	}
	trace, code, err := g.api(ctx, http.MethodGet,
		fmt.Sprintf("/api/v4/projects/%s/jobs/%d/trace", g.project, jobs[0].ID), nil)
	if err != nil || code != http.StatusOK {
		return fmt.Sprintf("(reading the trace: code %d err %v)", code, err)
	}
	return string(trace)
}
