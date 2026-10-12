package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func (in *PolicyWorkflowRun) DeepCopyInto(out *PolicyWorkflowRun) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *PolicyWorkflowRun) DeepCopy() *PolicyWorkflowRun {
	if in == nil {
		return nil
	}
	out := new(PolicyWorkflowRun)
	in.DeepCopyInto(out)
	return out
}

func (in *PolicyWorkflowRun) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *PolicyWorkflowRunList) DeepCopyInto(out *PolicyWorkflowRunList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]PolicyWorkflowRun, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *PolicyWorkflowRunList) DeepCopy() *PolicyWorkflowRunList {
	if in == nil {
		return nil
	}
	out := new(PolicyWorkflowRunList)
	in.DeepCopyInto(out)
	return out
}

func (in *PolicyWorkflowRunList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *PolicyWorkflowRunSpec) DeepCopyInto(out *PolicyWorkflowRunSpec) {
	*out = *in
	in.Workflow.DeepCopyInto(&out.Workflow)
	if in.Inputs != nil {
		out.Inputs = make(map[string]string, len(in.Inputs))
		for k, v := range in.Inputs {
			out.Inputs[k] = v
		}
	}
	if in.Args != nil {
		out.Args = make(map[string]string, len(in.Args))
		for k, v := range in.Args {
			out.Args[k] = v
		}
	}
	if in.BackoffLimit != nil {
		v := *in.BackoffLimit
		out.BackoffLimit = &v
	}
	if in.ActiveDeadlineSeconds != nil {
		v := *in.ActiveDeadlineSeconds
		out.ActiveDeadlineSeconds = &v
	}
	if in.TTLSecondsAfterFinished != nil {
		v := *in.TTLSecondsAfterFinished
		out.TTLSecondsAfterFinished = &v
	}
}

func (in *PolicyWorkflowRunStatus) DeepCopyInto(out *PolicyWorkflowRunStatus) {
	*out = *in
	if in.StartTime != nil {
		out.StartTime = in.StartTime.DeepCopy()
	}
	if in.CompletionTime != nil {
		out.CompletionTime = in.CompletionTime.DeepCopy()
	}
	if in.Outputs != nil {
		out.Outputs = make(map[string]string, len(in.Outputs))
		for k, v := range in.Outputs {
			out.Outputs[k] = v
		}
	}
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *DenoPod) DeepCopyInto(out *DenoPod) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *DenoPod) DeepCopy() *DenoPod {
	if in == nil {
		return nil
	}
	out := new(DenoPod)
	in.DeepCopyInto(out)
	return out
}

func (in *DenoPod) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *DenoPodList) DeepCopyInto(out *DenoPodList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]DenoPod, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *DenoPodList) DeepCopy() *DenoPodList {
	if in == nil {
		return nil
	}
	out := new(DenoPodList)
	in.DeepCopyInto(out)
	return out
}

func (in *DenoPodList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *DenoPodSpec) DeepCopyInto(out *DenoPodSpec) {
	*out = *in
	in.DenoPodTemplate.DeepCopyInto(&out.DenoPodTemplate)
	if in.ReadinessProbe != nil {
		out.ReadinessProbe = new(ExecProbe)
		in.ReadinessProbe.DeepCopyInto(out.ReadinessProbe)
	}
	if in.LivenessProbe != nil {
		out.LivenessProbe = new(ExecProbe)
		in.LivenessProbe.DeepCopyInto(out.LivenessProbe)
	}
	if in.ActiveDeadlineSeconds != nil {
		v := *in.ActiveDeadlineSeconds
		out.ActiveDeadlineSeconds = &v
	}
	if in.TTLSecondsAfterFinished != nil {
		v := *in.TTLSecondsAfterFinished
		out.TTLSecondsAfterFinished = &v
	}
}

func (in *ExecProbe) DeepCopyInto(out *ExecProbe) {
	*out = *in
	if in.Command != nil {
		out.Command = make([]string, len(in.Command))
		copy(out.Command, in.Command)
	}
	if in.PeriodSeconds != nil {
		v := *in.PeriodSeconds
		out.PeriodSeconds = &v
	}
	if in.FailureThreshold != nil {
		v := *in.FailureThreshold
		out.FailureThreshold = &v
	}
	if in.TimeoutSeconds != nil {
		v := *in.TimeoutSeconds
		out.TimeoutSeconds = &v
	}
}

func (in *DenoPodTemplate) DeepCopyInto(out *DenoPodTemplate) {
	*out = *in
	in.DenoJSON.DeepCopyInto(&out.DenoJSON)
	if in.Permissions != nil {
		out.Permissions = new(DenoPermissions)
		in.Permissions.DeepCopyInto(out.Permissions)
	}
	if in.ServiceAccount != nil {
		v := *in.ServiceAccount
		out.ServiceAccount = &v
	}
	if in.Env != nil {
		out.Env = make(map[string]string, len(in.Env))
		for k, v := range in.Env {
			out.Env[k] = v
		}
	}
}

func (in *DenoPermission) DeepCopyInto(out *DenoPermission) {
	*out = *in
	if in.AllowList != nil {
		out.AllowList = make([]string, len(in.AllowList))
		copy(out.AllowList, in.AllowList)
	}
	if in.DenyList != nil {
		out.DenyList = make([]string, len(in.DenyList))
		copy(out.DenyList, in.DenyList)
	}
}

func (in *DenoPermissions) DeepCopyInto(out *DenoPermissions) {
	*out = *in
	if in.Read != nil {
		out.Read = new(DenoPermission)
		in.Read.DeepCopyInto(out.Read)
	}
	if in.Write != nil {
		out.Write = new(DenoPermission)
		in.Write.DeepCopyInto(out.Write)
	}
	if in.Net != nil {
		out.Net = new(DenoPermission)
		in.Net.DeepCopyInto(out.Net)
	}
	if in.Env != nil {
		out.Env = new(DenoPermission)
		in.Env.DeepCopyInto(out.Env)
	}
	if in.Run != nil {
		out.Run = new(DenoPermission)
		in.Run.DeepCopyInto(out.Run)
	}
	if in.FFI != nil {
		out.FFI = new(DenoPermission)
		in.FFI.DeepCopyInto(out.FFI)
	}
	if in.Sys != nil {
		out.Sys = new(DenoPermission)
		in.Sys.DeepCopyInto(out.Sys)
	}
	if in.Import != nil {
		out.Import = new(DenoPermission)
		in.Import.DeepCopyInto(out.Import)
	}
	if in.IgnoreEnv != nil {
		out.IgnoreEnv = new(DenoPermission)
		in.IgnoreEnv.DeepCopyInto(out.IgnoreEnv)
	}
	if in.AllowScripts != nil {
		out.AllowScripts = make([]string, len(in.AllowScripts))
		copy(out.AllowScripts, in.AllowScripts)
	}
}

func (in *DenoPodStatus) DeepCopyInto(out *DenoPodStatus) {
	*out = *in
	if in.StartTime != nil {
		out.StartTime = in.StartTime.DeepCopy()
	}
	if in.CompletionTime != nil {
		out.CompletionTime = in.CompletionTime.DeepCopy()
	}
	if in.ExitCode != nil {
		v := *in.ExitCode
		out.ExitCode = &v
	}
	if in.Outputs != nil {
		out.Outputs = make(map[string]string, len(in.Outputs))
		for k, v := range in.Outputs {
			out.Outputs[k] = v
		}
	}
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *DenoRun) DeepCopyInto(out *DenoRun) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *DenoRun) DeepCopy() *DenoRun {
	if in == nil {
		return nil
	}
	out := new(DenoRun)
	in.DeepCopyInto(out)
	return out
}

func (in *DenoRun) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *DenoRunList) DeepCopyInto(out *DenoRunList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]DenoRun, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *DenoRunList) DeepCopy() *DenoRunList {
	if in == nil {
		return nil
	}
	out := new(DenoRunList)
	in.DeepCopyInto(out)
	return out
}

func (in *DenoRunList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *DenoRunSpec) DeepCopyInto(out *DenoRunSpec) {
	*out = *in
	in.DenoPodTemplate.DeepCopyInto(&out.DenoPodTemplate)
	if in.Backoff != nil {
		v := *in.Backoff
		out.Backoff = &v
	}
	if in.ActiveDeadlineSeconds != nil {
		v := *in.ActiveDeadlineSeconds
		out.ActiveDeadlineSeconds = &v
	}
	if in.TTLSecondsAfterFinished != nil {
		v := *in.TTLSecondsAfterFinished
		out.TTLSecondsAfterFinished = &v
	}
}

func (in *DenoRunStatus) DeepCopyInto(out *DenoRunStatus) {
	*out = *in
	if in.StartTime != nil {
		out.StartTime = in.StartTime.DeepCopy()
	}
	if in.CompletionTime != nil {
		out.CompletionTime = in.CompletionTime.DeepCopy()
	}
	if in.ExitCode != nil {
		v := *in.ExitCode
		out.ExitCode = &v
	}
	if in.Outputs != nil {
		out.Outputs = make(map[string]string, len(in.Outputs))
		for k, v := range in.Outputs {
			out.Outputs[k] = v
		}
	}
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *DenoJob) DeepCopyInto(out *DenoJob) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *DenoJob) DeepCopy() *DenoJob {
	if in == nil {
		return nil
	}
	out := new(DenoJob)
	in.DeepCopyInto(out)
	return out
}

func (in *DenoJob) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *DenoJobList) DeepCopyInto(out *DenoJobList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]DenoJob, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *DenoJobList) DeepCopy() *DenoJobList {
	if in == nil {
		return nil
	}
	out := new(DenoJobList)
	in.DeepCopyInto(out)
	return out
}

func (in *DenoJobList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *DenoJobSpec) DeepCopyInto(out *DenoJobSpec) {
	*out = *in
	in.Template.DeepCopyInto(&out.Template)
	if in.Completions != nil {
		v := *in.Completions
		out.Completions = &v
	}
	if in.Parallelism != nil {
		v := *in.Parallelism
		out.Parallelism = &v
	}
	if in.BackoffLimit != nil {
		v := *in.BackoffLimit
		out.BackoffLimit = &v
	}
	if in.ActiveDeadlineSeconds != nil {
		v := *in.ActiveDeadlineSeconds
		out.ActiveDeadlineSeconds = &v
	}
	if in.TTLSecondsAfterFinished != nil {
		v := *in.TTLSecondsAfterFinished
		out.TTLSecondsAfterFinished = &v
	}
}

func (in *DenoJobStatus) DeepCopyInto(out *DenoJobStatus) {
	*out = *in
	if in.Runs != nil {
		out.Runs = make([]string, len(in.Runs))
		copy(out.Runs, in.Runs)
	}
	if in.StartTime != nil {
		out.StartTime = in.StartTime.DeepCopy()
	}
	if in.CompletionTime != nil {
		out.CompletionTime = in.CompletionTime.DeepCopy()
	}
	if in.ExitCode != nil {
		v := *in.ExitCode
		out.ExitCode = &v
	}
	if in.Outputs != nil {
		out.Outputs = make(map[string]string, len(in.Outputs))
		for k, v := range in.Outputs {
			out.Outputs[k] = v
		}
	}
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *RunTrigger) DeepCopyInto(out *RunTrigger) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *RunTrigger) DeepCopy() *RunTrigger {
	if in == nil {
		return nil
	}
	out := new(RunTrigger)
	in.DeepCopyInto(out)
	return out
}

func (in *RunTrigger) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *RunTriggerList) DeepCopyInto(out *RunTriggerList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]RunTrigger, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *RunTriggerList) DeepCopy() *RunTriggerList {
	if in == nil {
		return nil
	}
	out := new(RunTriggerList)
	in.DeepCopyInto(out)
	return out
}

func (in *RunTriggerList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *RunTriggerSpec) DeepCopyInto(out *RunTriggerSpec) {
	*out = *in
	if in.Match != nil {
		out.Match = make(map[string]string, len(in.Match))
		for k, v := range in.Match {
			out.Match[k] = v
		}
	}
	in.JobTemplate.DeepCopyInto(&out.JobTemplate)
}

func (in *RunTriggerStatus) DeepCopyInto(out *RunTriggerStatus) {
	*out = *in
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *PolicyEngine) DeepCopyInto(out *PolicyEngine) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *PolicyEngine) DeepCopy() *PolicyEngine {
	if in == nil {
		return nil
	}
	out := new(PolicyEngine)
	in.DeepCopyInto(out)
	return out
}

func (in *PolicyEngine) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *PolicyEngineList) DeepCopyInto(out *PolicyEngineList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]PolicyEngine, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *PolicyEngineList) DeepCopy() *PolicyEngineList {
	if in == nil {
		return nil
	}
	out := new(PolicyEngineList)
	in.DeepCopyInto(out)
	return out
}

func (in *PolicyEngineList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *PolicyEngineSpec) DeepCopyInto(out *PolicyEngineSpec) {
	*out = *in
	in.DenoJSON.DeepCopyInto(&out.DenoJSON)
	if in.Permissions != nil {
		out.Permissions = new(DenoPermissions)
		in.Permissions.DeepCopyInto(out.Permissions)
	}
	if in.ServiceAccount != nil {
		v := *in.ServiceAccount
		out.ServiceAccount = &v
	}
	if in.Env != nil {
		out.Env = make(map[string]string, len(in.Env))
		for k, v := range in.Env {
			out.Env[k] = v
		}
	}
	if in.ReadinessProbe != nil {
		out.ReadinessProbe = new(ExecProbe)
		in.ReadinessProbe.DeepCopyInto(out.ReadinessProbe)
	}
	if in.LivenessProbe != nil {
		out.LivenessProbe = new(ExecProbe)
		in.LivenessProbe.DeepCopyInto(out.LivenessProbe)
	}
}

func (in *PolicyEngineStatus) DeepCopyInto(out *PolicyEngineStatus) {
	*out = *in
	if in.StartTime != nil {
		out.StartTime = in.StartTime.DeepCopy()
	}
	if in.CompletionTime != nil {
		out.CompletionTime = in.CompletionTime.DeepCopy()
	}
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *PolicyWorkflowPod) DeepCopyInto(out *PolicyWorkflowPod) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *PolicyWorkflowPod) DeepCopy() *PolicyWorkflowPod {
	if in == nil {
		return nil
	}
	out := new(PolicyWorkflowPod)
	in.DeepCopyInto(out)
	return out
}

func (in *PolicyWorkflowPod) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *PolicyWorkflowPodList) DeepCopyInto(out *PolicyWorkflowPodList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]PolicyWorkflowPod, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *PolicyWorkflowPodList) DeepCopy() *PolicyWorkflowPodList {
	if in == nil {
		return nil
	}
	out := new(PolicyWorkflowPodList)
	in.DeepCopyInto(out)
	return out
}

func (in *PolicyWorkflowPodList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *PolicyWorkflowPodSpec) DeepCopyInto(out *PolicyWorkflowPodSpec) {
	*out = *in
	in.Workflow.DeepCopyInto(&out.Workflow)
	if in.Inputs != nil {
		out.Inputs = make(map[string]string, len(in.Inputs))
		for k, v := range in.Inputs {
			out.Inputs[k] = v
		}
	}
	if in.Args != nil {
		out.Args = make(map[string]string, len(in.Args))
		for k, v := range in.Args {
			out.Args[k] = v
		}
	}
	if in.MaxConcurrent != nil {
		in, out := &in.MaxConcurrent, &out.MaxConcurrent
		*out = new(int32)
		**out = **in
	}
	if in.RunTTLSecondsAfterFinished != nil {
		in, out := &in.RunTTLSecondsAfterFinished, &out.RunTTLSecondsAfterFinished
		*out = new(int64)
		**out = **in
	}
}

func (in *PolicyWorkflowPodStatus) DeepCopyInto(out *PolicyWorkflowPodStatus) {
	*out = *in
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *OpenBao) DeepCopyInto(out *OpenBao) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	in.Status.DeepCopyInto(&out.Status)
}

func (in *OpenBao) DeepCopy() *OpenBao {
	if in == nil {
		return nil
	}
	out := new(OpenBao)
	in.DeepCopyInto(out)
	return out
}

func (in *OpenBao) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *OpenBaoList) DeepCopyInto(out *OpenBaoList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]OpenBao, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *OpenBaoList) DeepCopy() *OpenBaoList {
	if in == nil {
		return nil
	}
	out := new(OpenBaoList)
	in.DeepCopyInto(out)
	return out
}

func (in *OpenBaoList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *OpenBaoSpec) DeepCopyInto(out *OpenBaoSpec) {
	*out = *in
}

func (in *OpenBaoSpec) DeepCopy() *OpenBaoSpec {
	if in == nil {
		return nil
	}
	out := new(OpenBaoSpec)
	in.DeepCopyInto(out)
	return out
}

func (in *OpenBaoStatus) DeepCopyInto(out *OpenBaoStatus) {
	*out = *in
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *OpenBaoStatus) DeepCopy() *OpenBaoStatus {
	if in == nil {
		return nil
	}
	out := new(OpenBaoStatus)
	in.DeepCopyInto(out)
	return out
}
