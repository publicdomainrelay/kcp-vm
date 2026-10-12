package provider

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

const JobRunLabel = "deno.computer/job"

const DefaultProbeTimeout = 5 * time.Second

// ponytail: coalesce non-essential job progress writes to one per cooldown window; creates, phase changes and terminal writes still go through immediately.
const jobStatusCooldown = 500 * time.Millisecond

type Runtime interface {
	Instances

	CreateRun(ctx context.Context, logicalCluster string, run *v1alpha1.DenoRun) error

	WriteRunStatus(ctx context.Context, ref Ref, st v1alpha1.DenoRunStatus) error

	DeleteRun(ctx context.Context, ref Ref) error

	RemoveRunFinalizer(ctx context.Context, ref Ref) error

	CreatePod(ctx context.Context, logicalCluster string, pod *v1alpha1.DenoPod) error

	WritePodStatus(ctx context.Context, ref Ref, st v1alpha1.DenoPodStatus) error

	DeletePod(ctx context.Context, ref Ref) error

	RemovePodFinalizer(ctx context.Context, ref Ref) error

	CreateJob(ctx context.Context, logicalCluster string, job *v1alpha1.DenoJob) error

	WriteJobStatus(ctx context.Context, ref Ref, st v1alpha1.DenoJobStatus) error

	DeleteJob(ctx context.Context, ref Ref) error

	WriteTriggerStatus(ctx context.Context, ref Ref, st v1alpha1.RunTriggerStatus) error

	ListOpenBaos(ctx context.Context, logicalCluster, namespace string) ([]v1alpha1.OpenBao, error)

	WriteOpenBaoStatus(ctx context.Context, ref Ref, st v1alpha1.OpenBaoStatus) error

	WriteOpenBaoFinalizer(ctx context.Context, ref Ref, finalizers []string) error

	RemoveOpenBaoFinalizer(ctx context.Context, ref Ref) error

	DeleteOpenBao(ctx context.Context, ref Ref) error

	WriteEngineStatus(ctx context.Context, ref Ref, st v1alpha1.PolicyEngineStatus) error

	DeleteEngine(ctx context.Context, ref Ref) error

	RemoveEngineFinalizer(ctx context.Context, ref Ref) error

	WriteWorkflowPodStatus(ctx context.Context, ref Ref, st v1alpha1.PolicyWorkflowPodStatus) error

	DeleteWorkflowPod(ctx context.Context, ref Ref) error

	CreateWorkflowRun(ctx context.Context, logicalCluster string, run *v1alpha1.PolicyWorkflowRun) error
}

type TokenMinter interface {
	MintServiceAccountToken(ctx context.Context, logicalCluster, namespace, name string, ttl time.Duration) (string, error)
}

var _ Runtime = (*Registry)(nil)

var _ TokenMinter = (*Registry)(nil)

func thresholdOf(spec *v1alpha1.ExecProbe) int32 {
	if spec.FailureThreshold != nil {
		return *spec.FailureThreshold
	}
	return 0
}

func ownerRef(kind, name string, uid k8stypes.UID) metav1.OwnerReference {
	controller := true
	block := true
	return metav1.OwnerReference{
		APIVersion:         v1alpha1.GroupVersion.String(),
		Kind:               kind,
		Name:               name,
		UID:                uid,
		Controller:         &controller,
		BlockOwnerDeletion: &block,
	}
}

func exitCodePtr(code int32) *int32 {
	c := code
	return &c
}
