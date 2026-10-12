package denojob

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

const DefaultBackoffLimit int32 = 6

const RequeueAfter = 2 * time.Second

type RunObservation struct {
	Name string

	Phase v1alpha1.DenoRunPhase

	ExitCode *int32

	Message string

	Outputs map[string]string
}

type Observed struct {
	Job v1alpha1.DenoJob

	Runs []RunObservation

	Now time.Time
}

type Result struct {
	Phase v1alpha1.DenoJobPhase

	RunNames []string

	RunName string

	CreateRuns []string

	StopRuns []string

	StartTime *metav1.Time

	CompletionTime *metav1.Time

	Active int32

	Ready int32

	Succeeded int32

	Failed int32

	Retries int32

	ExitCode *int32

	Outputs map[string]string

	Conditions []metav1.Condition

	RequeueAfter time.Duration

	Delete bool

	DeleteJob bool
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
	if o.Job.Name == "" {
		return Result{}, errors.New("denojob: observed job has no name")
	}
	if o.Now.IsZero() {
		o.Now = r.opts.Now()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	res := carry(o.Job.Status)

	if o.Job.DeletionTimestamp != nil {
		return r.teardown(o, res), nil
	}

	if terminal(res.Phase) {
		return r.finish(o, res), nil
	}

	if o.Job.Spec.Suspend {
		return r.suspend(o, res), nil
	}
	clearCondition(&res, v1alpha1.ConditionSuspended)

	if deadlineExceeded(o.Job, o.Now) {
		return r.deadline(o, res), nil
	}

	return r.run(o, res), nil
}

func carry(st v1alpha1.DenoJobStatus) Result {
	names := append([]string(nil), st.Runs...)
	if len(names) == 0 && st.RunName != "" {
		names = []string{st.RunName}
	}
	return Result{
		Phase:          st.Phase,
		RunNames:       names,
		RunName:        st.RunName,
		StartTime:      st.StartTime,
		CompletionTime: st.CompletionTime,
		Active:         st.Active,
		Ready:          st.Ready,
		Succeeded:      st.Succeeded,
		Failed:         st.Failed,
		Retries:        st.Retries,
		ExitCode:       st.ExitCode,
		Outputs:        st.Outputs,
		Conditions:     append([]metav1.Condition(nil), st.Conditions...),
	}
}

func (r *Reconciler) teardown(o Observed, res Result) Result {
	res.Active = 0
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) finish(o Observed, res Result) Result {
	res.Active = 0
	res.RunNames = knownNames(res.RunNames, o.Runs)
	for _, run := range o.Runs {
		if !runTerminal(run.Phase) {
			res.StopRuns = append(res.StopRuns, run.Name)
		}
	}
	ttl := o.Job.Spec.TTLSecondsAfterFinished
	if ttl == nil || res.CompletionTime == nil {
		if len(res.StopRuns) > 0 {
			res.RequeueAfter = RequeueAfter
		}
		return res
	}
	expiry := res.CompletionTime.Add(time.Duration(*ttl) * time.Second)
	if !o.Now.Before(expiry) {
		res.Delete = true
		res.DeleteJob = true
		res.StopRuns = namesOf(o.Runs)
		return res
	}
	res.RequeueAfter = expiry.Sub(o.Now)
	return res
}

func (r *Reconciler) suspend(o Observed, res Result) Result {
	res.StopRuns = append(res.StopRuns, activeNames(o.Runs)...)
	res.Phase = v1alpha1.DenoJobPending
	res.Active = 0
	setCondition(&res, o.Job.Generation, metav1.ConditionTrue, v1alpha1.ConditionSuspended,
		"Suspended", "the job is suspended and no run is active")
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) deadline(o Observed, res Result) Result {
	res.StopRuns = append(res.StopRuns, activeNames(o.Runs)...)
	res.Phase = v1alpha1.DenoJobFailed
	res.Active = 0
	res.CompletionTime = timePtr(o.Now)
	if res.Failed == 0 {
		res.Failed = 1
	}
	setCondition(&res, o.Job.Generation, metav1.ConditionTrue, v1alpha1.ConditionFailed,
		"DeadlineExceeded", "activeDeadlineSeconds elapsed before the job finished")
	setCondition(&res, o.Job.Generation, metav1.ConditionTrue, v1alpha1.ConditionComplete,
		"DeadlineExceeded", "the job did not finish inside its deadline")
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) run(o Observed, res Result) Result {
	if res.StartTime == nil && len(o.Runs) > 0 {
		res.StartTime = timePtr(o.Now)
	}
	res.RunNames = knownNames(res.RunNames, o.Runs)

	succeeded := countPhase(o.Runs, v1alpha1.DenoRunSucceeded)
	if res.Succeeded > succeeded {
		succeeded = res.Succeeded
	}
	completions := completionsOf(o.Job)
	if succeeded > completions {
		succeeded = completions
	}
	failed := countPhase(o.Runs, v1alpha1.DenoRunFailed)
	if res.Failed > failed {
		failed = res.Failed
	}
	res.Succeeded = succeeded
	res.Failed = failed
	if failed > res.Retries {
		res.Retries = failed
	}
	res.Ready = countPhase(o.Runs, v1alpha1.DenoRunRunning)

	if succeeded >= completions {
		return r.succeed(o, res)
	}
	if failed > backoffLimitOf(o.Job, r.opts.DefaultBackoffLimit) {
		return r.fail(o, res)
	}

	active := activeCount(o.Runs)
	remaining := completions - succeeded
	desired := parallelismOf(o.Job)
	if desired > remaining {
		desired = remaining
	}
	start := desired - active
	if start < 0 {
		start = 0
	}
	if start > 0 {
		created := nextRunNames(o.Job.Name, res.RunNames, start)
		res.CreateRuns = created
		res.RunNames = append(res.RunNames, created...)
		res.RunName = created[len(created)-1]
		if res.StartTime == nil {
			res.StartTime = timePtr(o.Now)
		}
	}
	res.Active = active + int32(len(res.CreateRuns))
	if active > 0 {
		res.Phase = v1alpha1.DenoJobRunning
		setCondition(&res, o.Job.Generation, metav1.ConditionFalse, v1alpha1.ConditionComplete,
			"Running", "the job runs are in progress")
	} else {
		res.Phase = v1alpha1.DenoJobPending
		setCondition(&res, o.Job.Generation, metav1.ConditionFalse, v1alpha1.ConditionComplete,
			"Pending", "the job is waiting for its runs")
	}
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) succeed(o Observed, res Result) Result {
	res.Phase = v1alpha1.DenoJobSucceeded
	res.CompletionTime = timePtr(o.Now)
	res.StopRuns = append(res.StopRuns, activeNames(o.Runs)...)
	res.Active = 0
	if last, ok := lastPhase(o.Runs, v1alpha1.DenoRunSucceeded); ok {
		res.RunName = last.Name
		res.ExitCode = last.ExitCode
		res.Outputs = last.Outputs
	}
	setCondition(&res, o.Job.Generation, metav1.ConditionTrue, v1alpha1.ConditionComplete,
		"Complete", "the job reached its required completions")
	res.RequeueAfter = RequeueAfter
	return res
}

func (r *Reconciler) fail(o Observed, res Result) Result {
	res.Phase = v1alpha1.DenoJobFailed
	res.CompletionTime = timePtr(o.Now)
	res.StopRuns = append(res.StopRuns, activeNames(o.Runs)...)
	res.Active = 0
	if last, ok := lastPhase(o.Runs, v1alpha1.DenoRunFailed); ok {
		res.RunName = last.Name
		res.ExitCode = last.ExitCode
	}
	setCondition(&res, o.Job.Generation, metav1.ConditionTrue, v1alpha1.ConditionFailed,
		"BackoffLimitExceeded", "the job runs failed and no retries remain")
	setCondition(&res, o.Job.Generation, metav1.ConditionTrue, v1alpha1.ConditionComplete,
		"BackoffLimitExceeded", "the job did not complete before its backoffLimit was reached")
	res.RequeueAfter = RequeueAfter
	return res
}

func terminal(p v1alpha1.DenoJobPhase) bool {
	return p == v1alpha1.DenoJobSucceeded || p == v1alpha1.DenoJobFailed
}

func runTerminal(p v1alpha1.DenoRunPhase) bool {
	return p == v1alpha1.DenoRunSucceeded || p == v1alpha1.DenoRunFailed
}

func completionsOf(job v1alpha1.DenoJob) int32 {
	if job.Spec.Completions == nil || *job.Spec.Completions < 1 {
		return 1
	}
	return *job.Spec.Completions
}

func parallelismOf(job v1alpha1.DenoJob) int32 {
	if job.Spec.Parallelism == nil {
		return 1
	}
	if *job.Spec.Parallelism < 0 {
		return 0
	}
	return *job.Spec.Parallelism
}

func backoffLimitOf(job v1alpha1.DenoJob, def int32) int32 {
	if job.Spec.BackoffLimit != nil {
		return *job.Spec.BackoffLimit
	}
	return def
}

func countPhase(runs []RunObservation, phase v1alpha1.DenoRunPhase) int32 {
	var n int32
	for _, run := range runs {
		if run.Phase == phase {
			n++
		}
	}
	return n
}

func activeCount(runs []RunObservation) int32 {
	var n int32
	for _, run := range runs {
		if !runTerminal(run.Phase) {
			n++
		}
	}
	return n
}

func activeNames(runs []RunObservation) []string {
	var out []string
	for _, run := range runs {
		if !runTerminal(run.Phase) {
			out = append(out, run.Name)
		}
	}
	return out
}

func namesOf(runs []RunObservation) []string {
	out := make([]string, 0, len(runs))
	for _, run := range runs {
		out = append(out, run.Name)
	}
	return out
}

func lastPhase(runs []RunObservation, phase v1alpha1.DenoRunPhase) (RunObservation, bool) {
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Phase == phase {
			return runs[i], true
		}
	}
	return RunObservation{}, false
}

func knownNames(known []string, runs []RunObservation) []string {
	seen := make(map[string]bool, len(known)+len(runs))
	out := make([]string, 0, len(known)+len(runs))
	for _, name := range known {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, run := range runs {
		if run.Name == "" || seen[run.Name] {
			continue
		}
		seen[run.Name] = true
		out = append(out, run.Name)
	}
	return out
}

func nextRunNames(jobName string, known []string, count int32) []string {
	pattern := regexp.MustCompile("^" + regexp.QuoteMeta(jobName) + `-(\d+)$`)
	var max int64
	for _, name := range known {
		m := pattern.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		v, err := strconv.ParseInt(m[1], 10, 64)
		if err == nil && v > max {
			max = v
		}
	}
	out := make([]string, 0, count)
	for i := int32(1); i <= count; i++ {
		out = append(out, fmt.Sprintf("%s-%d", jobName, max+int64(i)))
	}
	return out
}

func deadlineExceeded(job v1alpha1.DenoJob, now time.Time) bool {
	if job.Spec.ActiveDeadlineSeconds == nil || job.Status.StartTime == nil {
		return false
	}
	deadline := job.Status.StartTime.Add(time.Duration(*job.Spec.ActiveDeadlineSeconds) * time.Second)
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
