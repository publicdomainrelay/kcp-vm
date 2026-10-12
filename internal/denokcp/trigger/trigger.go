package trigger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

type RunObservation struct {
	Name string

	Phase v1alpha1.PolicyWorkflowPhase

	Outputs map[string]string
}

type Observed struct {
	Trigger v1alpha1.RunTrigger

	Run *RunObservation

	AnyRun bool

	Now time.Time
}

type Op string

const (
	OpCreateJob Op = "create-job"

	OpRemoveFinalizer Op = "remove-finalizer"
)

type Result struct {
	Phase v1alpha1.RunTriggerPhase

	Matched bool

	JobName string

	LastRun string

	Conditions []metav1.Condition

	Ops []Op

	RemoveFinalizer bool
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

// ponytail: this reconciler asks for no requeue at all. It is woken by what it depends on: the driver enqueues the trigger when a run of the pod named in its spec reaches a terminal phase, and the trigger's own object events keep the ordinary informer handler path. So no ticker discovers a run, and the run must still exist when the trigger reads it: the run TTL (docs/DENO_RUNTIME_KCP.md section 8) now has to exceed the wake latency, which is one informer delivery rather than a poll interval. The driver still adds a backstop for an event that never arrives.
func (r *Reconciler) Reconcile(ctx context.Context, o Observed) (Result, error) {
	if o.Trigger.Name == "" {
		return Result{}, errors.New("trigger: observed trigger has no name")
	}
	if o.Now.IsZero() {
		o.Now = r.opts.Now()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	res := carry(o.Trigger.Status)

	if o.Trigger.DeletionTimestamp != nil {
		res.Ops = append(res.Ops, OpRemoveFinalizer)
		res.RemoveFinalizer = true
		return res, nil
	}

	if o.Run == nil {
		if res.Phase == v1alpha1.RunTriggerTriggered {
			return res, nil
		}
		res.Phase = v1alpha1.RunTriggerPending
		message := "waiting for the referenced policy workflow pod to produce a run"
		if o.AnyRun {
			message = "the latest policy workflow run has not finished"
		}
		setCondition(&res, o.Trigger.Generation, metav1.ConditionFalse, v1alpha1.ConditionComplete,
			"Pending", message)
		return res, nil
	}

	run := o.Run
	switch run.Phase {
	case v1alpha1.PolicyWorkflowSucceeded:
		if !matches(o.Trigger.Spec.Match, run.Outputs) {
			res.Phase = v1alpha1.RunTriggerSkipped
			res.Matched = false
			setCondition(&res, o.Trigger.Generation, metav1.ConditionTrue, v1alpha1.ConditionFailed,
				"NoMatch", "the run result did not match the trigger's match map")
			return res, nil
		}
		res.Phase = v1alpha1.RunTriggerTriggered
		res.Matched = true
		res.JobName = jobName(o.Trigger.Name, run.Name)
		if res.LastRun != run.Name {
			res.LastRun = run.Name
			res.Ops = append(res.Ops, OpCreateJob)
			setCondition(&res, o.Trigger.Generation, metav1.ConditionTrue, v1alpha1.ConditionComplete,
				"Triggered", "the run matched and a deno job was created")
		}
	case v1alpha1.PolicyWorkflowCancelled:
		res.Phase = v1alpha1.RunTriggerSkipped
		res.Matched = false
		setCondition(&res, o.Trigger.Generation, metav1.ConditionTrue, v1alpha1.ConditionFailed,
			"WorkflowCancelled", "the referenced policy workflow run was cancelled")
	case v1alpha1.PolicyWorkflowFailed:
		res.Phase = v1alpha1.RunTriggerSkipped
		res.Matched = false
		setCondition(&res, o.Trigger.Generation, metav1.ConditionTrue, v1alpha1.ConditionFailed,
			"WorkflowFailed", "the referenced policy workflow run failed")
	default:
		res.Phase = v1alpha1.RunTriggerPending
		setCondition(&res, o.Trigger.Generation, metav1.ConditionFalse, v1alpha1.ConditionComplete,
			"Pending", "the referenced policy workflow run has not finished")
	}
	return res, nil
}

func carry(st v1alpha1.RunTriggerStatus) Result {
	return Result{
		Phase:      st.Phase,
		Matched:    st.Matched,
		JobName:    st.JobName,
		LastRun:    st.LastRun,
		Conditions: append([]metav1.Condition(nil), st.Conditions...),
	}
}

func matches(want, outputs map[string]string) bool {
	for k, v := range want {
		if outputs[k] != v {
			return false
		}
	}
	return true
}

func jobName(triggerName, runName string) string {
	return fmt.Sprintf("%s-%s", triggerName, runName)
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
