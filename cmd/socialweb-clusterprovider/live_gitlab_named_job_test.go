package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

const (
	gitlabRefEnv = "SOCIALWEB_LIVE_GITLAB_REF"

	gitlabJobEnv = "SOCIALWEB_LIVE_GITLAB_JOB"

	gitlabPipelineFileEnv = "SOCIALWEB_LIVE_GITLAB_PIPELINE_FILE"

	gitlabJobTimeout = 110 * time.Minute

	gitlabJobPoll = 10 * time.Second

	gitlabTraceTail = 6000

	gitlabJobCPURequest = "100m"

	gitlabJobMemoryRequest = "128Mi"

	gitlabJobCPULimit = "10"

	gitlabJobMemoryLimit = "20Gi"

	gitlabHelperCPURequest = "100m"

	gitlabHelperMemoryRequest = "128Mi"

	gitlabHelperCPULimit = "1"

	gitlabHelperMemoryLimit = "1Gi"

	gitlabJobPollTimeout = 1800

	gitlabPreBuildScript = "if ! command -v pnpm >/dev/null 2>&1 && command -v npm >/dev/null 2>&1; then npm install -g pnpm@10.11.0; fi; true"

	gitlabPreSourceScript = `if command -v getent >/dev/null 2>&1; then R='getent hosts'; elif command -v nslookup >/dev/null 2>&1; then R=nslookup; fi; i=0; while [ -n "$R" ] && [ $i -lt 180 ]; do $R "$CI_SERVER_HOST" >/dev/null 2>&1 && break; i=$((i+1)); sleep 1; done; echo "kcp dns gate: ${i}s"; true`

	gitlabJobConcurrency = 8

	gitlabSpreadLabelKey = "project.runner.gitlab.com/id"

	gitlabOutputLimitKB = 51200

	gitlabRunnerFatalWait = 20 * time.Second

	gitlabRunnerName = "gitlab-runner"

	gitlabRunnerGroup = "gitlab-runner"
)

func TestALiveGitLabJobRunsOnATenantCluster(t *testing.T) {
	missing := []string{}
	for _, name := range []string{
		gitlabURLEnv, gitlabPATEnv, gitlabProjectEnv, gitlabRunnerTokenEnv, gitlabRunnerBinEnv,
		gitlabRefEnv, gitlabJobEnv,
	} {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		livegateNamedJob(t, strings.Join(missing, ", "))
	}

	gitlab := gitlabClient{
		url:     strings.TrimSuffix(os.Getenv(gitlabURLEnv), "/"),
		token:   os.Getenv(gitlabPATEnv),
		project: os.Getenv(gitlabProjectEnv),
		http:    &http.Client{Timeout: 60 * time.Second},
	}
	substrate := strings.TrimSpace(os.Getenv(fourstepSubstrateEnv))
	ran := 0
	for _, s := range fourstepSubstrates() {
		if substrate != "" && substrate != s {
			continue
		}
		ran++
		t.Run(s, func(t *testing.T) {
			gitlabNamedJobRun(t, s, os.Getenv(gitlabRunnerBinEnv), os.Getenv(gitlabRunnerTokenEnv), gitlab,
				strings.TrimSpace(os.Getenv(gitlabRefEnv)), strings.TrimSpace(os.Getenv(gitlabJobEnv)),
				strings.TrimSpace(os.Getenv(gitlabPipelineFileEnv)))
		})
	}
	if ran == 0 {
		livegateNamedJob(t, fmt.Sprintf("%s is %q and this host is not one of %s",
			fourstepSubstrateEnv, substrate, strings.Join(fourstepSubstrates(), ", ")))
	}
}

func livegateNamedJob(t *testing.T, why string) {
	t.Helper()
	livegate.Require(t, "unset or unusable: %s. This test runs ONE NAMED JOB of a real GitLab "+
		"project as a Pod on a tenant cluster this repo provisions on this host, which is the only "+
		"instrument that reads the job's own trace for a pipeline whose other jobs this host cannot "+
		"run. It is skipped when the inputs are unset because it needs a live GitLab, a registered "+
		"runner token and a real guest, and a skip here is NOT a pass", why)
}

func gitlabNamedJobRun(t *testing.T, substrate, runnerBin, runnerToken string, gitlab gitlabClient,
	ref, jobName, pipelineFile string) {
	bins := fourstepRequirements(t, substrate)
	ctx, cancel := context.WithTimeout(context.Background(), fourstepDeadline+gitlabJobTimeout)
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

	credDir, host := gitlabScopedCredentialFiles(t, cfg, stack)
	gitlabScopedRBACUp(t, ctx, client)
	t.Logf("STEP 3 minted the runner its own scoped tenant credential in %s against %s, and the "+
		"cluster admits the group %s it carries", credDir, host, gitlabRunnerGroup)

	kubeconfig := stack.kubeconfig(t)

	if pipelineFile != "" {
		raw, err := os.ReadFile(pipelineFile)
		if err != nil {
			t.Fatalf("reading the pipeline definition to place on %s at %s: %v", gitlab.project, ref, err)
		}
		if err := gitlab.CommitPipelineFileOn(ctx, ref, string(raw)); err != nil {
			t.Fatalf("placing %s on %s at %s: %v", pipelineFile, gitlab.project, ref, err)
		}
		t.Logf("STEP 4 the project carries the pipeline definition from %s on %s", pipelineFile, ref)
	}

	runner := gitlabStartJobRunner(t, runnerBin, runnerToken, gitlab.url, host, credDir)
	defer gitlabStopRunner(t, runner)

	online := gitlab.AwaitRunnerOnline(ctx)
	t.Logf("STEP 5 the runner is online against %s (untagged=%t tags %v)", host, online.untagged, online.tags)

	id, err := gitlab.TriggerPipelineOn(ctx, ref)
	if err != nil {
		t.Fatalf("triggering a pipeline for %s: %v\nrunner output:\n%s", ref, err, runner.output())
	}
	t.Logf("STEP 6 pipeline %d triggered for %s", id, ref)

	placed := &placedPods{seen: map[string]string{}}
	pollCtx, pollCancel := context.WithCancel(ctx)
	defer pollCancel()
	go watchRunnerPods(pollCtx, client, placed)

	dec := stack.choose(t, ctx, scale.Provision)
	if dec.Nodes < 1 {
		t.Fatalf("the scale decision counted %d nodes for the job pod: %s", dec.Nodes, dec.Reason)
	}
	joined := stack.waitNodes(t, kubeconfig, 1)
	t.Logf("STEP 7 the job pod's own Pending state drove the scale decision (%s) and a node joined:\n%s",
		dec.Reason, joined)

	job := gitlab.AwaitJob(t, ctx, id, jobName)
	t.Logf("STEP 8 job %s (id %d) finished %q after %.1fs", jobName, job.id, job.status, job.duration)
	if job.status != "success" {
		t.Fatalf("job %s of pipeline %d finished %q, want success.\nall jobs:\n%s\ntrace tail:\n%s\n"+
			"runner output:\n%s", jobName, id, job.status, gitlab.PipelineJobs(ctx, id),
			tailOf(gitlab.JobTrace(ctx, job.id), gitlabTraceTail), runner.output())
	}

	trace := gitlab.JobTrace(ctx, job.id)
	t.Logf("STEP 9 job %s's own trace, tail:\n%s", jobName, tailOf(trace, gitlabTraceTail))

	seen := placed.snapshot()
	t.Logf("STEP 10 job %s ran in pod(s) %v, placed by this cluster", jobName, seen)
}

func gitlabStartJobRunner(t *testing.T, bin, token, url, host, credDir string) *gitlabRunner {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := fmt.Sprintf(`concurrent = %d
check_interval = 3

[[runners]]
  name = "socialweb-live-job"
  url = %q
  token = %q
  executor = "kubernetes"
  pre_get_sources_script = %q
  pre_build_script = %q
  output_limit = %d
  [runners.kubernetes]
    host = %q
    cert_file = %q
    key_file = %q
    ca_file = %q
    namespace = %q
    image_pull_policy = "if-not-present"
    cpu_request = %q
    memory_request = %q
    cpu_limit = %q
    memory_limit = %q
    helper_cpu_request = %q
    helper_memory_request = %q
    helper_cpu_limit = %q
    helper_memory_limit = %q
    poll_timeout = %d
    cleanup_grace_period_seconds = 30
    [runners.kubernetes.affinity]
      [runners.kubernetes.affinity.pod_anti_affinity]
        [[runners.kubernetes.affinity.pod_anti_affinity.required_during_scheduling_ignored_during_execution]]
          topology_key = "kubernetes.io/hostname"
          [runners.kubernetes.affinity.pod_anti_affinity.required_during_scheduling_ignored_during_execution.label_selector]
            [[runners.kubernetes.affinity.pod_anti_affinity.required_during_scheduling_ignored_during_execution.label_selector.match_expressions]]
              key = %q
              operator = "Exists"
`, gitlabJobConcurrency, url, token, gitlabPreSourceScript, gitlabPreBuildScript, gitlabOutputLimitKB, host,
		filepath.Join(credDir, "tls.crt"), filepath.Join(credDir, "tls.key"),
		filepath.Join(credDir, "ca.crt"), gitlabNamespace,
		gitlabJobCPURequest, gitlabJobMemoryRequest, gitlabJobCPULimit, gitlabJobMemoryLimit,
		gitlabHelperCPURequest, gitlabHelperMemoryRequest,
		gitlabHelperCPULimit, gitlabHelperMemoryLimit,
		gitlabJobPollTimeout, gitlabSpreadLabelKey)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
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
	deadline := time.Now().Add(gitlabRunnerFatalWait)
	for {
		out := r.buf.String()
		if strings.Contains(out, "FATAL") {
			t.Fatalf("the runner refused its own configuration and exited, so nothing would ever "+
				"pick the job up and the wait that follows would read as a job that never ran rather "+
				"than as a runner that never started. A gitlab-runner config is decoded strictly and "+
				"a key of the wrong type is a startup failure, not a warning.\n%s", out)
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-time.After(200 * time.Millisecond):
		case <-t.Context().Done():
		}
	}
	t.Logf("STEP 5a the runner is up with the kubernetes executor against %s, concurrent=%d, "+
		"requesting %s cpu and %s memory", host, gitlabJobConcurrency,
		gitlabJobCPURequest, gitlabJobMemoryRequest)
	return r
}

func gitlabScopedCredentialFiles(t *testing.T, cfg Config, stack *fourstepStack) (string, string) {
	t.Helper()
	lease, ok := stack.alloc.PortFor(fourstepCluster, portallocator.PurposeK3sSecure)
	if !ok {
		t.Fatalf("no apiserver lease for %s", fourstepCluster)
	}
	minted, err := clusterprovider.MintTenantCredentialForClusterAs(
		context.Background(), cfg.StateDir, fourstepCluster,
		clusterprovider.APIServerURL(lease.Port), gitlabRunnerName, []string{gitlabRunnerGroup}, 0)
	if err != nil {
		t.Fatalf("minting the runner its OWN scoped tenant credential: %v. docs/gitlab/01-kcp-server.md "+
			"is explicit that a runner is never handed the cluster's superuser identity -- "+
			"system:admin in system:masters makes every RBAC rule below decorative -- so this "+
			"refuses rather than falling back to the credential that would always work", err)
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

func gitlabScopedRBACUp(t *testing.T, ctx context.Context, client kubernetes.Interface) {
	t.Helper()
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: gitlabRunnerName},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods", "pods/exec", "pods/attach", "pods/log"},
				Verbs:     []string{"create", "delete", "get", "list", "watch", "patch", "update"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"secrets", "configmaps", "services", "serviceaccounts", "events", "persistentvolumeclaims"},
				Verbs:     []string{"create", "delete", "get", "list", "watch", "update"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"deployments"},
				Verbs:     []string{"create", "delete", "get", "list", "update"},
			},
			{
				APIGroups: []string{"scheduling.k8s.io"},
				Resources: []string{"priorityclasses"},
				Verbs:     []string{"get", "create"},
			},
		},
	}
	if _, err := client.RbacV1().ClusterRoles().Create(ctx, role, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			t.Fatalf("granting the runner's group the rules it needs in the tenant cluster: %v. "+
				"priorityclasses is cluster-scoped, so a namespaced Role cannot grant it and a "+
				"missing grant reads as a runner that cannot start a job", err)
		}
		if _, err := client.RbacV1().ClusterRoles().Update(ctx, role, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("updating the runner's ClusterRole: %v", err)
		}
	}
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: gitlabRunnerName},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     gitlabRunnerName,
		},
		Subjects: []rbacv1.Subject{{
			Kind:     "Group",
			Name:     gitlabRunnerGroup,
			APIGroup: "rbac.authorization.k8s.io",
		}},
	}
	if _, err := client.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			t.Fatalf("binding the runner's certificate group to its ClusterRole: %v. The subject "+
				"is the ORGANIZATIONS value the credential was minted with, and the two disagreeing "+
				"is a 403 that reads like a broken runner", err)
		}
		if _, err := client.RbacV1().ClusterRoleBindings().Update(ctx, binding, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("updating the runner's ClusterRoleBinding: %v", err)
		}
	}
}

func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

type jobState struct {
	id       int
	status   string
	duration float64
}

func (g gitlabClient) CommitPipelineFileOn(ctx context.Context, ref, content string) error {
	path := "/api/v4/projects/" + g.project + "/repository/files/%2Egitlab-ci%2Eyml"
	payload := map[string]any{
		"branch":         ref,
		"content":        content,
		"commit_message": "place the pipeline this repo's live test runs",
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
		return fmt.Errorf("updating the pipeline file on %s: %d %s", ref, code2, raw2)
	}
	return fmt.Errorf("creating the pipeline file on %s: %d %s", ref, code, raw)
}

func (g gitlabClient) TriggerPipelineOn(ctx context.Context, ref string) (int, error) {
	raw, code, err := g.api(ctx, http.MethodPost,
		"/api/v4/projects/"+g.project+"/pipeline", map[string]any{"ref": ref})
	if err != nil {
		return 0, err
	}
	if code != http.StatusCreated && code != http.StatusOK {
		return 0, fmt.Errorf("triggering the pipeline on %s: %d %s", ref, code, raw)
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

func (g gitlabClient) AwaitJob(t *testing.T, ctx context.Context, pipeline int, name string) jobState {
	t.Helper()
	start := time.Now()
	deadline := start.Add(gitlabJobTimeout)
	last := ""
	for time.Now().Before(deadline) {
		raw, code, err := g.api(ctx, http.MethodGet,
			fmt.Sprintf("/api/v4/projects/%s/pipelines/%d/jobs", g.project, pipeline), nil)
		if err == nil && code == http.StatusOK {
			var jobs []struct {
				ID       int     `json:"id"`
				Name     string  `json:"name"`
				Status   string  `json:"status"`
				Duration float64 `json:"duration"`
			}
			if json.Unmarshal(raw, &jobs) == nil {
				for _, j := range jobs {
					if j.Name != name {
						continue
					}
					if j.Status != last {
						last = j.Status
						t.Logf("job %s (%d) is %s", j.Name, j.ID, j.Status)
					}
					switch j.Status {
					case "success", "failed", "canceled", "skipped", "manual":
						return jobState{id: j.ID, status: j.Status, duration: j.Duration}
					}
					if j.Status == "created" && time.Since(start) > 4*time.Minute {
						t.Logf("job %s is still created after %s; this host can run one named job "+
							"of this pipeline and nothing else in it, so a job that never leaves "+
							"created is a runner that did not take it:\n%s",
							name, time.Since(start).Round(time.Second), g.PipelineJobs(ctx, pipeline))
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return jobState{status: last}
		case <-time.After(gitlabJobPoll):
		}
	}
	return jobState{status: last}
}

func (g gitlabClient) JobTrace(ctx context.Context, id int) string {
	if id == 0 {
		return "(no job id)"
	}
	raw, code, err := g.api(ctx, http.MethodGet,
		fmt.Sprintf("/api/v4/projects/%s/jobs/%d/trace", g.project, id), nil)
	if err != nil || code != http.StatusOK {
		return fmt.Sprintf("(reading the trace of job %d: code %d err %v)", id, code, err)
	}
	return string(raw)
}
