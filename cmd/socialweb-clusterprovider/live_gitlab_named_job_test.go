package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

const (
	gitlabRefEnv = "SOCIALWEB_LIVE_GITLAB_REF"

	gitlabJobEnv = "SOCIALWEB_LIVE_GITLAB_JOB"

	gitlabPipelineFileEnv = "SOCIALWEB_LIVE_GITLAB_PIPELINE_FILE"

	gitlabJobTimeout = 45 * time.Minute

	gitlabJobPoll = 10 * time.Second

	gitlabTraceTail = 6000
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

	credDir, host := gitlabCredentialFiles(t, cfg, stack)
	t.Logf("STEP 3 minted a tenant credential in %s against %s", credDir, host)

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

	runner := gitlabStartRunner(t, runnerBin, runnerToken, gitlab.url, host, credDir)
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
	deadline := time.Now().Add(gitlabJobTimeout)
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
					if j.Status == "created" && time.Since(deadline.Add(-gitlabJobTimeout)) > 4*time.Minute {
						t.Logf("job %s is still created after 4m; every job of this pipeline that "+
							"this host cannot run is listed below:\n%s", name, g.PipelineJobs(ctx, pipeline))
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
