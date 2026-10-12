package policyengine

import (
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

const RequeueAfter = 2 * time.Second

type ExecutionState string

const (
	ExecutionRunning ExecutionState = "running"

	ExecutionExited ExecutionState = "exited"
)

type ExecutionObservation struct {
	RunID string

	State ExecutionState

	Message string
}

type Observed struct {
	Engine v1alpha1.PolicyEngine

	Execution *ExecutionObservation

	ReadinessPassed *bool

	LivenessFailed bool

	Endpoint string

	Now time.Time
}

type Op string

const (
	OpStartEngine Op = "start-engine"

	OpStopEngine Op = "stop-engine"

	OpDelete Op = "delete"

	OpRemoveFinalizer Op = "remove-finalizer"
)

type Result struct {
	Phase v1alpha1.PolicyEnginePhase

	RunID string

	Endpoint string

	Restarts int32

	Ready bool

	StartTime *metav1.Time

	CompletionTime *metav1.Time

	Message string

	Conditions []metav1.Condition

	Ops []Op

	RequeueAfter time.Duration

	RemoveFinalizer bool

	Delete bool
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
	if o.Engine.Name == "" {
		return Result{}, errors.New("policyengine: observed engine has no name")
	}
	if o.Now.IsZero() {
		o.Now = r.opts.Now()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	res := carry(o.Engine.Status)

	if o.Engine.DeletionTimestamp != nil {
		return r.teardown(o, res), nil
	}

	if o.Execution == nil {
		return r.start(o, res), nil
	}

	if o.Execution.State == ExecutionRunning {
		return r.running(o, res), nil
	}

	return r.exited(o, res), nil
}

func carry(st v1alpha1.PolicyEngineStatus) Result {
	return Result{
		Phase:          st.Phase,
		RunID:          st.RunID,
		Endpoint:       st.Endpoint,
		Restarts:       st.Restarts,
		Ready:          st.Ready,
		StartTime:      st.StartTime,
		CompletionTime: st.CompletionTime,
		Message:        st.Message,
		Conditions:     append([]metav1.Condition(nil), st.Conditions...),
	}
}

func (r *Reconciler) teardown(o Observed, res Result) Result {
	if o.Execution != nil && o.Execution.State == ExecutionRunning {
		res.Ops = append(res.Ops, OpStopEngine)
	}
	res.Ready = false
	res.Ops = append(res.Ops, OpRemoveFinalizer)
	res.RemoveFinalizer = true
	setCondition(&res, o.Engine.Generation, metav1.ConditionFalse, v1alpha1.ConditionReady,
		"Terminating", "the policy engine is being deleted")
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) start(o Observed, res Result) Result {
	if res.StartTime == nil {
		res.StartTime = timePtr(o.Now)
	}
	res.Phase = v1alpha1.PolicyEngineRunning
	res.RunID = ""
	res.Endpoint = o.Endpoint
	res.Ready = false
	res.Ops = append(res.Ops, OpStartEngine)
	setCondition(&res, o.Engine.Generation, metav1.ConditionFalse, v1alpha1.ConditionReady,
		"Starting", "the policy engine API server is starting")
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) running(o Observed, res Result) Result {
	if res.StartTime == nil {
		res.StartTime = timePtr(o.Now)
	}
	res.Phase = v1alpha1.PolicyEngineRunning
	res.RunID = o.Execution.RunID
	if o.Endpoint != "" {
		res.Endpoint = o.Endpoint
	}

	if o.LivenessFailed {
		return r.restart(o, res, "LivenessProbeFailed", "the liveness probe failed; restarting the policy engine")
	}

	ready := true
	if o.ReadinessPassed != nil {
		ready = *o.ReadinessPassed
	}
	res.Ready = ready
	if ready {
		setCondition(&res, o.Engine.Generation, metav1.ConditionTrue, v1alpha1.ConditionReady,
			"Ready", "the policy engine API server is accepting requests")
	} else {
		setCondition(&res, o.Engine.Generation, metav1.ConditionFalse, v1alpha1.ConditionReady,
			"NotReady", "the readiness probe has not passed")
	}
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) restart(o Observed, res Result, reason, message string) Result {
	if o.Execution != nil && o.Execution.State == ExecutionRunning {
		res.Ops = append(res.Ops, OpStopEngine)
	}
	res.Phase = v1alpha1.PolicyEngineRunning
	res.RunID = ""
	res.Restarts = o.Engine.Status.Restarts + 1
	res.Ready = false
	res.Message = message
	setCondition(&res, o.Engine.Generation, metav1.ConditionFalse, v1alpha1.ConditionReady,
		reason, message)
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) exited(o Observed, res Result) Result {
	policy := o.Engine.Spec.RestartPolicy
	if policy == "" {
		policy = v1alpha1.RestartAlways
	}
	if policy == v1alpha1.RestartNever {
		res.Phase = v1alpha1.PolicyEngineFailed
		res.RunID = ""
		res.Ready = false
		res.Message = o.Execution.Message
		res.CompletionTime = timePtr(o.Now)
		setCondition(&res, o.Engine.Generation, metav1.ConditionFalse, v1alpha1.ConditionReady,
			"ProcessExited", "the policy engine process exited and the restart policy is Never")
		res.RequeueAfter = RequeueAfter
		return res
	}
	return r.restart(o, res, "Restarting", "the policy engine process exited; restarting it")
}

func timePtr(t time.Time) *metav1.Time {
	v := metav1.NewTime(t)
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
