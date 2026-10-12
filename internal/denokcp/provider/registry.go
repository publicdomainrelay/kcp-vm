package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/ref"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/impl/kcpstore"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

type Ref = ref.Ref

type RegistryOptions struct {
	Host string

	RestConfig *rest.Config

	Transport http.RoundTripper
}

type Registry struct {
	store *kcpstore.Store

	workflowRuns *kcpstore.Resource[v1alpha1.PolicyWorkflowRun]

	runs *kcpstore.Resource[v1alpha1.DenoRun]

	pods *kcpstore.Resource[v1alpha1.DenoPod]

	jobs *kcpstore.Resource[v1alpha1.DenoJob]

	triggers *kcpstore.Resource[v1alpha1.RunTrigger]

	engines *kcpstore.Resource[v1alpha1.PolicyEngine]

	workflowPods *kcpstore.Resource[v1alpha1.PolicyWorkflowPod]

	openBaos *kcpstore.Resource[v1alpha1.OpenBao]
}

func NewRegistry(opts RegistryOptions) (*Registry, error) {
	if opts.Host == "" {
		return nil, errors.New("provider: Host is required")
	}
	store, err := kcpstore.New(kcpstore.Options{
		Host:       opts.Host,
		RestConfig: opts.RestConfig,
		Transport:  opts.Transport,
	})
	if err != nil {
		return nil, err
	}
	return &Registry{
		store:        store,
		workflowRuns: kcpstore.Of[v1alpha1.PolicyWorkflowRun](store, denoGVR("policyworkflowruns")),
		runs:         kcpstore.Of[v1alpha1.DenoRun](store, denoGVR("denoruns")),
		pods:         kcpstore.Of[v1alpha1.DenoPod](store, denoGVR("denopods")),
		jobs:         kcpstore.Of[v1alpha1.DenoJob](store, denoGVR("denojobs")),
		triggers:     kcpstore.Of[v1alpha1.RunTrigger](store, denoGVR("runtriggers")),
		engines:      kcpstore.Of[v1alpha1.PolicyEngine](store, denoGVR("policyengines")),
		workflowPods: kcpstore.Of[v1alpha1.PolicyWorkflowPod](store, denoGVR("policyworkflowpods")),
		openBaos:     kcpstore.Of[v1alpha1.OpenBao](store, denoGVR("openbaos")),
	}, nil
}

func denoGVR(resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    v1alpha1.GroupVersion.Group,
		Version:  v1alpha1.GroupVersion.Version,
		Resource: resource,
	}
}

func storeOf(registry Instances) *kcpstore.Store {
	if r, ok := registry.(*Registry); ok {
		return r.store
	}
	return nil
}

func (r *Registry) Read(ctx context.Context, ref Ref) (*v1alpha1.PolicyWorkflowRun, error) {
	return r.workflowRuns.Get(ctx, ref)
}

func (r *Registry) WriteStatus(ctx context.Context, ref Ref, st v1alpha1.PolicyWorkflowRunStatus) error {
	body, err := statusPatch(st)
	if err != nil {
		return err
	}
	return r.workflowRuns.PatchStatus(ctx, ref, body)
}

func (r *Registry) Delete(ctx context.Context, ref Ref) error {
	return r.workflowRuns.Delete(ctx, ref)
}

func (r *Registry) RemoveFinalizer(ctx context.Context, ref Ref) error {
	return r.workflowRuns.RemoveFinalizer(ctx, ref, v1alpha1.FinalizerPolicyWorkflowRun)
}

func (r *Registry) CreateWorkflowRun(ctx context.Context, logicalCluster string, run *v1alpha1.PolicyWorkflowRun) error {
	return r.workflowRuns.Create(ctx, logicalCluster, run)
}

func (r *Registry) ListWorkflowRuns(ctx context.Context, logicalCluster string) ([]v1alpha1.PolicyWorkflowRun, error) {
	return r.workflowRuns.List(ctx, logicalCluster)
}

func statusPatch(st v1alpha1.PolicyWorkflowRunStatus) ([]byte, error) {
	status := map[string]any{
		"phase":      string(st.Phase),
		"runID":      st.RunID,
		"active":     st.Active,
		"succeeded":  st.Succeeded,
		"failed":     st.Failed,
		"retries":    st.Retries,
		"exitStatus": st.ExitStatus,
	}
	if st.StartTime != nil {
		status["startTime"] = st.StartTime
	} else {
		status["startTime"] = nil
	}
	if st.CompletionTime != nil {
		status["completionTime"] = st.CompletionTime
	} else {
		status["completionTime"] = nil
	}
	if st.Outputs != nil {
		status["outputs"] = st.Outputs
	} else {
		status["outputs"] = nil
	}
	if st.Conditions != nil {
		status["conditions"] = st.Conditions
	} else {
		status["conditions"] = nil
	}
	return json.Marshal(map[string]any{"status": status})
}
