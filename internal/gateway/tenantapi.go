package gateway

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/revocation"
)

type ApplyRequest struct {
	Cluster      string          `json:"cluster"`
	Namespace    string          `json:"namespace,omitempty"`
	Name         string          `json:"name,omitempty"`
	FieldManager string          `json:"fieldManager,omitempty"`
	Force        bool            `json:"force,omitempty"`
	DryRun       bool            `json:"dryRun,omitempty"`
	Manifest     json.RawMessage `json:"manifest"`
}

type ApplyResponse struct {
	APIVersion      string          `json:"apiVersion"`
	Kind            string          `json:"kind"`
	Namespace       string          `json:"namespace,omitempty"`
	Name            string          `json:"name"`
	UID             string          `json:"uid,omitempty"`
	ResourceVersion string          `json:"resourceVersion,omitempty"`
	Generation      int64           `json:"generation,omitempty"`
	DryRun          bool            `json:"dryRun,omitempty"`
	Object          json.RawMessage `json:"object"`
}

type JobRequest struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	TailLines *int64 `json:"tailLines,omitempty"`
}

type JobCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type JobContainer struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
	ExitCode *int64 `json:"exitCode,omitempty"`
	Log      string `json:"log,omitempty"`
	LogError string `json:"logError,omitempty"`
}

type JobPod struct {
	Name       string         `json:"name"`
	Phase      string         `json:"phase"`
	Node       string         `json:"node,omitempty"`
	Containers []JobContainer `json:"containers"`
}

type JobStatus struct {
	Namespace      string         `json:"namespace"`
	Name           string         `json:"name"`
	UID            string         `json:"uid"`
	Phase          string         `json:"phase"`
	Active         int64          `json:"active"`
	Succeeded      int64          `json:"succeeded"`
	Failed         int64          `json:"failed"`
	StartTime      string         `json:"startTime,omitempty"`
	CompletionTime string         `json:"completionTime,omitempty"`
	Conditions     []JobCondition `json:"conditions"`
	Pods           []JobPod       `json:"pods"`
}

const (
	JobPending   = "Pending"
	JobRunning   = "Running"
	JobSucceeded = "Succeeded"
	JobFailed    = "Failed"

	DefaultJobTailLines = 100
	MaxJobTailLines     = 5000
)

type TenantAPI interface {
	Apply(ctx context.Context, did, cluster string, req ApplyRequest) (*ApplyResponse, error)
	Job(ctx context.Context, did, cluster string, req JobRequest) (*JobStatus, error)
}

var (
	ErrClusterNotRunning = &XRPCError{
		"ClusterNotRunning", "the control plane is stopped; call controlPlaneUp first", 409,
	}
	ErrNoTenantAPI = &XRPCError{
		"ClusterUnavailable", "this gateway was built without a tenant API client", 503,
	}
)

func (s *Server) KubectlApplyServerSide(ctx context.Context, caller Caller, req ApplyRequest) (*ApplyResponse, error) {
	if err := s.reachable(ctx, caller, req.Cluster); err != nil {
		return nil, err
	}
	if len(req.Manifest) == 0 {
		return nil, ErrInvalidRequest
	}
	return s.Tenant.Apply(ctx, string(caller), req.Cluster, req)
}

func (s *Server) GetJob(ctx context.Context, caller Caller, req JobRequest) (*JobStatus, error) {
	if req.Namespace == "" || req.Name == "" {
		return nil, ErrInvalidRequest
	}
	if req.TailLines != nil && (*req.TailLines < 0 || *req.TailLines > MaxJobTailLines) {
		return nil, ErrInvalidRequest
	}
	if err := s.reachable(ctx, caller, req.Cluster); err != nil {
		return nil, err
	}
	return s.Tenant.Job(ctx, string(caller), req.Cluster, req)
}

func (s *Server) reachable(ctx context.Context, caller Caller, cluster string) error {
	if s.Tenant == nil {
		return ErrNoTenantAPI
	}
	if caller == "" {
		return ErrUnauthorized
	}
	permit, err := revocation.Admit(ctx, s.Deny, string(caller))
	if err != nil {
		if x := errForDeny(err); x != nil {
			return x
		}
		return err
	}
	if permit.Admitted() != string(caller) {
		return errors.New("gateway: the admit permit is for another identity")
	}
	view, err := s.lookup(ctx, caller, NameRequest{Name: cluster})
	if err != nil {
		return err
	}
	switch {
	case view.Phase == "Draining":
		return ErrClusterDraining
	case !view.Running:
		return ErrClusterNotRunning
	case view.Phase != "Ready":
		return ErrClusterInitializing
	}
	return nil
}
