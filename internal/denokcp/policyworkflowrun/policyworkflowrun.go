package policyworkflowrun

import (
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

const DefaultBackoffLimit int32 = 6

const RequeueAfter = 2 * time.Second

type RunState string

const (
	RunRunning RunState = "running"

	RunSucceeded RunState = "succeeded"

	RunFailed RunState = "failed"
)

type RunObservation struct {
	RunID string

	State RunState

	ExitStatus string

	Outputs map[string]string

	Message string
}

type Admission struct {
	Allowed bool

	Reason string

	Message string
}

type Observed struct {
	WorkflowRun v1alpha1.PolicyWorkflowRun

	Run *RunObservation

	Admission *Admission

	EffectiveTTLSeconds *int64

	Now time.Time
}

type Op string

const (
	OpStartRun Op = "start-run"

	OpStopRun Op = "stop-run"

	OpDelete Op = "delete"

	OpRemoveFinalizer Op = "remove-finalizer"
)

type Result struct {
	Phase v1alpha1.PolicyWorkflowPhase

	RunID string

	StartTime *metav1.Time

	CompletionTime *metav1.Time

	Active int32

	Succeeded int32

	Failed int32

	Retries int32

	ExitStatus string

	Outputs map[string]string

	Conditions []metav1.Condition

	Ops []Op

	RequeueAfter time.Duration

	RemoveFinalizer bool

	Delete bool
}

type Options struct {
	Now func() time.Time

	DefaultBackoffLimit int32
}

type Reconciler struct {
	opts Options
}

func New(opts Options) *Reconciler {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.DefaultBackoffLimit == 0 {
		opts.DefaultBackoffLimit = DefaultBackoffLimit
	}
	return &Reconciler{opts: opts}
}

func (r *Reconciler) Reconcile(ctx context.Context, o Observed) (Result, error) {
	if o.WorkflowRun.Name == "" {
		return Result{}, errors.New("policyworkflowrun: observed run has no name")
	}
	if o.Now.IsZero() {
		o.Now = r.opts.Now()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	res := carry(o.WorkflowRun.Status)

	if o.WorkflowRun.DeletionTimestamp != nil {
		return r.teardown(o, res), nil
	}

	if terminal(res.Phase) {
		return r.finish(o, res), nil
	}

	if o.WorkflowRun.Spec.Cancel {
		return r.cancel(o, res), nil
	}

	if o.WorkflowRun.Spec.Suspend {
		return r.suspend(o, res), nil
	}
	clearCondition(&res, v1alpha1.ConditionSuspended)

	if deadlineExceeded(o.WorkflowRun, o.Now) {
		return r.deadline(o, res), nil
	}

	if o.Run == nil {
		if o.Admission != nil && !o.Admission.Allowed {
			return r.queued(o, res), nil
		}
		return r.start(o, res), nil
	}

	switch o.Run.State {
	case RunRunning:
		return r.running(o, res), nil
	case RunSucceeded:
		return r.succeeded(o, res), nil
	case RunFailed:
		return r.failed(o, res), nil
	default:
		return Result{}, errors.New("policyworkflowrun: run observation has no state")
	}
}

func carry(st v1alpha1.PolicyWorkflowRunStatus) Result {
	return Result{
		Phase:          st.Phase,
		RunID:          st.RunID,
		StartTime:      st.StartTime,
		CompletionTime: st.CompletionTime,
		Active:         st.Active,
		Succeeded:      st.Succeeded,
		Failed:         st.Failed,
		Retries:        st.Retries,
		ExitStatus:     st.ExitStatus,
		Outputs:        st.Outputs,
		Conditions:     append([]metav1.Condition(nil), st.Conditions...),
	}
}

func (r *Reconciler) teardown(o Observed, res Result) Result {
	if o.Run != nil && o.Run.State == RunRunning {
		res.Ops = append(res.Ops, OpStopRun)
	}
	res.Active = 0
	if !terminal(res.Phase) {
		cancelInto(&res, o, "Deleted", "the policy run was deleted before it finished")
	}
	res.Ops = append(res.Ops, OpRemoveFinalizer)
	res.RemoveFinalizer = true
	res.RequeueAfter = RequeueAfter
	return res
}

// ponytail: the gha-lite engine exposes only /request/create, /request/status/:id,
// /request/console_output/:id, /request/console_output_stream/:id, /health and
// /rate_limit; there is no cancel/delete for a submitted task, so the engine task
// runs to completion on its side. Cancellation stops our polling, marks the run
// Cancelled and releases the finalizer. Upgrade when the engine gains a
// /request/cancel/:id: add PolicyClient.Cancel and call it on OpStopRun here.
func (r *Reconciler) cancel(o Observed, res Result) Result {
	if o.Run != nil && o.Run.State == RunRunning {
		res.Ops = append(res.Ops, OpStopRun)
	}
	res.Active = 0
	cancelInto(&res, o, "Cancelled", "the policy run was cancelled by spec.cancel")
	res.Ops = append(res.Ops, OpRemoveFinalizer)
	res.RemoveFinalizer = true
	res.RequeueAfter = RequeueAfter
	return res
}

func cancelInto(res *Result, o Observed, reason, message string) {
	res.Phase = v1alpha1.PolicyWorkflowCancelled
	res.RunID = ""
	res.CompletionTime = timePtr(o.Now)
	setCondition(res, o.WorkflowRun.Generation, metav1.ConditionTrue, v1alpha1.ConditionCancelled,
		reason, message)
	setCondition(res, o.WorkflowRun.Generation, metav1.ConditionTrue, v1alpha1.ConditionComplete,
		reason, message)
}

func (r *Reconciler) queued(o Observed, res Result) Result {
	reason := o.Admission.Reason
	if reason == "" {
		reason = "Queued"
	}
	res.Phase = v1alpha1.PolicyWorkflowPending
	res.Active = 0
	setCondition(&res, o.WorkflowRun.Generation, metav1.ConditionFalse, v1alpha1.ConditionComplete,
		reason, o.Admission.Message)
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) finish(o Observed, res Result) Result {
	res.Active = 0
	ttl := o.WorkflowRun.Spec.TTLSecondsAfterFinished
	if ttl == nil {
		ttl = o.EffectiveTTLSeconds
	}
	if ttl == nil || res.CompletionTime == nil {
		return res
	}
	expiry := res.CompletionTime.Add(time.Duration(*ttl) * time.Second)
	if !o.Now.Before(expiry) {
		res.Delete = true
		res.Ops = append(res.Ops, OpDelete)
		res.RequeueAfter = RequeueAfter
		return res
	}
	res.RequeueAfter = expiry.Sub(o.Now)
	return res
}

func (r *Reconciler) suspend(o Observed, res Result) Result {
	if o.Run != nil && o.Run.State == RunRunning {
		res.Ops = append(res.Ops, OpStopRun)
		res.RunID = ""
	}
	res.Phase = v1alpha1.PolicyWorkflowPending
	res.Active = 0
	setCondition(&res, o.WorkflowRun.Generation, metav1.ConditionTrue, v1alpha1.ConditionSuspended,
		"Suspended", "the run is suspended and no execution is active")
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) deadline(o Observed, res Result) Result {
	if o.Run != nil && o.Run.State == RunRunning {
		res.Ops = append(res.Ops, OpStopRun)
	}
	res.Phase = v1alpha1.PolicyWorkflowFailed
	res.Active = 0
	res.RunID = ""
	res.CompletionTime = timePtr(o.Now)
	if res.Failed == 0 {
		res.Failed = 1
	}
	setCondition(&res, o.WorkflowRun.Generation, metav1.ConditionTrue, v1alpha1.ConditionFailed,
		"DeadlineExceeded", "activeDeadlineSeconds elapsed before the run finished")
	setCondition(&res, o.WorkflowRun.Generation, metav1.ConditionTrue, v1alpha1.ConditionComplete,
		"DeadlineExceeded", "the run did not finish inside its deadline")
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) start(o Observed, res Result) Result {
	if res.StartTime == nil {
		res.StartTime = timePtr(o.Now)
	}
	res.Phase = v1alpha1.PolicyWorkflowRunning
	res.Active = 1
	res.Ops = append(res.Ops, OpStartRun)
	setCondition(&res, o.WorkflowRun.Generation, metav1.ConditionFalse, v1alpha1.ConditionComplete,
		"Running", "the policy run is in progress")
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) running(o Observed, res Result) Result {
	if res.StartTime == nil {
		res.StartTime = timePtr(o.Now)
	}
	res.Phase = v1alpha1.PolicyWorkflowRunning
	res.RunID = o.Run.RunID
	res.Active = 1
	setCondition(&res, o.WorkflowRun.Generation, metav1.ConditionFalse, v1alpha1.ConditionComplete,
		"Running", "the policy run is in progress")
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) succeeded(o Observed, res Result) Result {
	res.Phase = v1alpha1.PolicyWorkflowSucceeded
	res.RunID = o.Run.RunID
	res.Active = 0
	res.Succeeded = 1
	res.ExitStatus = o.Run.ExitStatus
	res.Outputs = o.Run.Outputs
	res.CompletionTime = timePtr(o.Now)
	setCondition(&res, o.WorkflowRun.Generation, metav1.ConditionTrue, v1alpha1.ConditionComplete,
		"Complete", "the policy run finished successfully")
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) failed(o Observed, res Result) Result {
	res.Active = 0
	res.RunID = ""
	res.Failed = o.WorkflowRun.Status.Failed + 1
	res.Retries = o.WorkflowRun.Status.Retries + 1
	res.ExitStatus = o.Run.ExitStatus
	limit := r.opts.DefaultBackoffLimit
	if o.WorkflowRun.Spec.BackoffLimit != nil {
		limit = *o.WorkflowRun.Spec.BackoffLimit
	}
	if res.Retries > limit {
		res.Phase = v1alpha1.PolicyWorkflowFailed
		res.CompletionTime = timePtr(o.Now)
		setCondition(&res, o.WorkflowRun.Generation, metav1.ConditionTrue, v1alpha1.ConditionFailed,
			"BackoffLimitExceeded", "the policy run failed and no retries remain")
		setCondition(&res, o.WorkflowRun.Generation, metav1.ConditionTrue, v1alpha1.ConditionComplete,
			"BackoffLimitExceeded", "the policy run did not complete before its backoffLimit was reached")
	} else {
		res.Phase = v1alpha1.PolicyWorkflowPending
		setCondition(&res, o.WorkflowRun.Generation, metav1.ConditionFalse, v1alpha1.ConditionComplete,
			"Retrying", "the policy run failed and will be retried")
	}
	res.RequeueAfter = RequeueAfter
	return res
}

func terminal(p v1alpha1.PolicyWorkflowPhase) bool {
	return p == v1alpha1.PolicyWorkflowSucceeded || p == v1alpha1.PolicyWorkflowFailed ||
		p == v1alpha1.PolicyWorkflowCancelled
}

func deadlineExceeded(run v1alpha1.PolicyWorkflowRun, now time.Time) bool {
	if run.Spec.ActiveDeadlineSeconds == nil || run.Status.StartTime == nil {
		return false
	}
	deadline := run.Status.StartTime.Add(time.Duration(*run.Spec.ActiveDeadlineSeconds) * time.Second)
	return !now.Before(deadline)
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

func clearCondition(res *Result, conditionType string) {
	meta.RemoveStatusCondition(&res.Conditions, conditionType)
}
