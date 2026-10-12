package denorun

import (
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

const DefaultBackoff int32 = 0

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

	ExitCode *int32

	Message string

	Outputs map[string]string
}

type Observed struct {
	Run v1alpha1.DenoRun

	Execution *RunObservation

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
	Phase v1alpha1.DenoRunPhase

	RunID string

	// ponytail: the provider cannot tell an id the decider never saw from one it dropped on purpose, so a cleared RunID says so itself. Without it the provider writes the id of the workload it just observed back into status.runID and the next pass spends a retry re-observing that corpse: with spec.backoff 2 the run made two attempts instead of three.
	RunIDCleared bool

	StartTime *metav1.Time

	CompletionTime *metav1.Time

	ExitCode *int32

	Message string

	Outputs map[string]string

	Retries int32

	Conditions []metav1.Condition

	Ops []Op

	RequeueAfter time.Duration

	RemoveFinalizer bool

	Delete bool
}

type Options struct {
	Now func() time.Time

	DefaultBackoff int32
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
	if o.Run.Name == "" {
		return Result{}, errors.New("denorun: observed run has no name")
	}
	if o.Now.IsZero() {
		o.Now = r.opts.Now()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	res := carry(o.Run.Status)

	if o.Run.DeletionTimestamp != nil {
		return r.teardown(o, res), nil
	}

	if terminal(res.Phase) {
		return r.finish(o, res), nil
	}

	if deadlineExceeded(o.Run, o.Now) {
		return r.deadline(o, res), nil
	}

	if o.Execution == nil {
		return r.start(o, res), nil
	}

	switch o.Execution.State {
	case RunRunning:
		return r.running(o, res), nil
	case RunSucceeded:
		return r.succeeded(o, res), nil
	case RunFailed:
		return r.failed(o, res), nil
	default:
		return Result{}, errors.New("denorun: execution observation has no state")
	}
}

func carry(st v1alpha1.DenoRunStatus) Result {
	return Result{
		Phase:          st.Phase,
		RunID:          st.RunID,
		StartTime:      st.StartTime,
		CompletionTime: st.CompletionTime,
		ExitCode:       st.ExitCode,
		Message:        st.Message,
		Outputs:        st.Outputs,
		Retries:        st.Retries,
		Conditions:     append([]metav1.Condition(nil), st.Conditions...),
	}
}

func (r *Reconciler) teardown(o Observed, res Result) Result {
	if o.Execution != nil && o.Execution.State == RunRunning {
		res.Ops = append(res.Ops, OpStopRun)
	}
	res.Ops = append(res.Ops, OpRemoveFinalizer)
	res.RemoveFinalizer = true
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) finish(o Observed, res Result) Result {
	ttl := o.Run.Spec.TTLSecondsAfterFinished
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

func (r *Reconciler) deadline(o Observed, res Result) Result {
	if o.Execution != nil && o.Execution.State == RunRunning {
		res.Ops = append(res.Ops, OpStopRun)
	}
	res.Phase = v1alpha1.DenoRunFailed
	res.RunID = ""
	res.RunIDCleared = true
	res.CompletionTime = timePtr(o.Now)
	setCondition(&res, o.Run.Generation, metav1.ConditionTrue, v1alpha1.ConditionFailed,
		"DeadlineExceeded", "activeDeadlineSeconds elapsed before the deno process finished")
	setCondition(&res, o.Run.Generation, metav1.ConditionTrue, v1alpha1.ConditionComplete,
		"DeadlineExceeded", "the deno process did not finish inside its deadline")
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) start(o Observed, res Result) Result {
	if res.StartTime == nil {
		res.StartTime = timePtr(o.Now)
	}
	res.Phase = v1alpha1.DenoRunRunning
	res.Ops = append(res.Ops, OpStartRun)
	setCondition(&res, o.Run.Generation, metav1.ConditionFalse, v1alpha1.ConditionComplete,
		"Running", "the deno process is running")
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) running(o Observed, res Result) Result {
	if res.StartTime == nil {
		res.StartTime = timePtr(o.Now)
	}
	res.Phase = v1alpha1.DenoRunRunning
	res.RunID = o.Execution.RunID
	setCondition(&res, o.Run.Generation, metav1.ConditionFalse, v1alpha1.ConditionComplete,
		"Running", "the deno process is running")
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) succeeded(o Observed, res Result) Result {
	res.Phase = v1alpha1.DenoRunSucceeded
	res.RunID = o.Execution.RunID
	res.ExitCode = o.Execution.ExitCode
	res.Outputs = o.Execution.Outputs
	res.CompletionTime = timePtr(o.Now)
	setCondition(&res, o.Run.Generation, metav1.ConditionTrue, v1alpha1.ConditionComplete,
		"Complete", "the deno process finished successfully")
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) failed(o Observed, res Result) Result {
	res.RunID = ""
	res.RunIDCleared = true
	res.Retries = o.Run.Status.Retries + 1
	res.ExitCode = o.Execution.ExitCode
	res.Message = o.Execution.Message
	limit := r.opts.DefaultBackoff
	if o.Run.Spec.Backoff != nil {
		limit = *o.Run.Spec.Backoff
	}
	if res.Retries > limit {
		res.Phase = v1alpha1.DenoRunFailed
		res.CompletionTime = timePtr(o.Now)
		setCondition(&res, o.Run.Generation, metav1.ConditionTrue, v1alpha1.ConditionFailed,
			"BackoffExceeded", "the deno process failed and no retries remain")
		setCondition(&res, o.Run.Generation, metav1.ConditionTrue, v1alpha1.ConditionComplete,
			"BackoffExceeded", "the deno process did not complete before its backoff was reached")
	} else {
		res.Phase = v1alpha1.DenoRunPending
		setCondition(&res, o.Run.Generation, metav1.ConditionFalse, v1alpha1.ConditionComplete,
			"Retrying", "the deno process failed and will be retried")
	}
	res.RequeueAfter = RequeueAfter
	return res
}

func terminal(p v1alpha1.DenoRunPhase) bool {
	return p == v1alpha1.DenoRunSucceeded || p == v1alpha1.DenoRunFailed
}

func deadlineExceeded(run v1alpha1.DenoRun, now time.Time) bool {
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
