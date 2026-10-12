package policyworkflowpod

import (
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

const RequeueAfter = 2 * time.Second

type Observed struct {
	Pod v1alpha1.PolicyWorkflowPod

	EngineReady bool

	EngineEndpoint string

	Active int32

	Now time.Time
}

type Result struct {
	Phase v1alpha1.PolicyWorkflowPodPhase

	Endpoint string

	Active int32

	Conditions []metav1.Condition

	RequeueAfter time.Duration
}

type Options struct {
	Now func() time.Time
}

type Reconciler struct {
	opts Options
}

func New(opts Options) *Reconciler {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Reconciler{opts: opts}
}

func (r *Reconciler) Reconcile(ctx context.Context, o Observed) (Result, error) {
	if o.Pod.Name == "" {
		return Result{}, errors.New("policyworkflowpod: observed pod has no name")
	}
	if o.Now.IsZero() {
		o.Now = r.opts.Now()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	res := carry(o.Pod.Status)
	res.Active = o.Active

	if o.Pod.DeletionTimestamp != nil {
		return res, nil
	}

	if !o.EngineReady || o.EngineEndpoint == "" {
		res.Phase = v1alpha1.PolicyWorkflowPodPending
		setCondition(&res, o.Pod.Generation, metav1.ConditionFalse, v1alpha1.ConditionReady,
			"EngineNotReady", "the referenced policy engine is not reachable yet")
		res.RequeueAfter = RequeueAfter
		return res, nil
	}

	res.Phase = v1alpha1.PolicyWorkflowPodRunning
	res.Endpoint = o.EngineEndpoint
	setCondition(&res, o.Pod.Generation, metav1.ConditionTrue, v1alpha1.ConditionReady,
		"Ready", "the referenced policy engine is reachable and the pod accepts runs")
	res.RequeueAfter = RequeueAfter
	return res, nil
}

func carry(st v1alpha1.PolicyWorkflowPodStatus) Result {
	return Result{
		Phase:      st.Phase,
		Endpoint:   st.Endpoint,
		Active:     st.Active,
		Conditions: append([]metav1.Condition(nil), st.Conditions...),
	}
}

func Capacity(policy v1alpha1.ConcurrencyPolicy, maxConcurrent *int32) (int32, bool) {
	if policy == v1alpha1.ConcurrencyAllow {
		if maxConcurrent == nil || *maxConcurrent <= 0 {
			return 0, true
		}
		return *maxConcurrent, false
	}
	return 1, false
}

func RunTTL(pod *v1alpha1.PolicyWorkflowPod, providerDefault *int64) *int64 {
	ttl := providerDefault
	if pod != nil && pod.Spec.RunTTLSecondsAfterFinished != nil {
		ttl = pod.Spec.RunTTLSecondsAfterFinished
	}
	if ttl == nil || *ttl < 0 {
		return nil
	}
	v := *ttl
	return &v
}

func setCondition(res *Result, generation int64, status metav1.ConditionStatus, conditionType, reason, message string) {
	meta.SetStatusCondition(&res.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
}
