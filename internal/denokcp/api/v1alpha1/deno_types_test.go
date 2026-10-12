package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestDenoPodDeepCopyDoesNotAlias(t *testing.T) {
	code := int32(1)
	original := &DenoPod{
		ObjectMeta: metav1.ObjectMeta{Name: "p"},
		Spec: DenoPodSpec{
			DenoPodTemplate: DenoPodTemplate{
				DenoJSON:       runtime.RawExtension{Raw: []byte(`{"imports":{"x":"y"}}`)},
				Env:            map[string]string{"A": "1"},
				Permissions:    &DenoPermissions{Read: &DenoPermission{AllowList: []string{"/etc"}}},
				ServiceAccount: &ServiceAccountRef{Name: "deno", Namespace: "default"},
			},
			ReadinessProbe: &ExecProbe{Command: []string{"true"}, PeriodSeconds: int32Ptr(5)},
		},
		Status: DenoPodStatus{ExitCode: &code, Outputs: map[string]string{"allow": "true"}},
	}

	copied := original.DeepCopy()
	original.Spec.Env["A"] = "changed"
	original.Spec.Permissions.Read.AllowList[0] = "/var"
	original.Status.Outputs["allow"] = "false"
	*original.Status.ExitCode = 9
	original.Spec.ServiceAccount.Name = "other"
	original.Spec.ReadinessProbe.Command[0] = "false"
	*original.Spec.ReadinessProbe.PeriodSeconds = 99

	if copied.Spec.Env["A"] != "1" {
		t.Fatalf("Env aliased: %q", copied.Spec.Env["A"])
	}
	if copied.Spec.Permissions.Read.AllowList[0] != "/etc" {
		t.Fatalf("Permissions aliased: %q", copied.Spec.Permissions.Read.AllowList[0])
	}
	if copied.Status.Outputs["allow"] != "true" {
		t.Fatalf("Outputs aliased: %q", copied.Status.Outputs["allow"])
	}
	if *copied.Status.ExitCode != 1 {
		t.Fatalf("ExitCode aliased: %d", *copied.Status.ExitCode)
	}
	if copied.Spec.ServiceAccount.Name != "deno" {
		t.Fatalf("ServiceAccount aliased: %q", copied.Spec.ServiceAccount.Name)
	}
	if copied.Spec.ReadinessProbe.Command[0] != "true" {
		t.Fatalf("ReadinessProbe.Command aliased: %q", copied.Spec.ReadinessProbe.Command[0])
	}
	if *copied.Spec.ReadinessProbe.PeriodSeconds != 5 {
		t.Fatalf("ReadinessProbe.PeriodSeconds aliased: %d", *copied.Spec.ReadinessProbe.PeriodSeconds)
	}
}

func TestDenoRunDeepCopyDoesNotAlias(t *testing.T) {
	limit := int32(3)
	original := &DenoRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r"},
		Spec: DenoRunSpec{
			DenoPodTemplate: DenoPodTemplate{Script: "x", Env: map[string]string{"A": "1"}},
			Backoff:         &limit,
		},
		Status: DenoRunStatus{Outputs: map[string]string{"allow": "true"}},
	}
	copied := original.DeepCopy()
	original.Spec.Env["A"] = "changed"
	*original.Spec.Backoff = 9
	original.Status.Outputs["allow"] = "false"

	if copied.Spec.Env["A"] != "1" {
		t.Fatalf("Template.Env aliased: %q", copied.Spec.Env["A"])
	}
	if *copied.Spec.Backoff != 3 {
		t.Fatalf("Backoff aliased: %d", *copied.Spec.Backoff)
	}
	if copied.Status.Outputs["allow"] != "true" {
		t.Fatalf("Outputs aliased: %q", copied.Status.Outputs["allow"])
	}
}

func TestDenoJobDeepCopyDoesNotAlias(t *testing.T) {
	limit := int32(3)
	original := &DenoJob{
		ObjectMeta: metav1.ObjectMeta{Name: "j"},
		Spec: DenoJobSpec{
			Template:     DenoRunSpec{DenoPodTemplate: DenoPodTemplate{Script: "x", Env: map[string]string{"A": "1"}}},
			BackoffLimit: &limit,
		},
		Status: DenoJobStatus{Outputs: map[string]string{"allow": "true"}},
	}
	copied := original.DeepCopy()
	original.Spec.Template.Env["A"] = "changed"
	*original.Spec.BackoffLimit = 9
	original.Status.Outputs["allow"] = "false"

	if copied.Spec.Template.Env["A"] != "1" {
		t.Fatalf("Template.Env aliased: %q", copied.Spec.Template.Env["A"])
	}
	if *copied.Spec.BackoffLimit != 3 {
		t.Fatalf("BackoffLimit aliased: %d", *copied.Spec.BackoffLimit)
	}
	if copied.Status.Outputs["allow"] != "true" {
		t.Fatalf("Outputs aliased: %q", copied.Status.Outputs["allow"])
	}
}

func TestRunTriggerDeepCopyDoesNotAlias(t *testing.T) {
	original := &RunTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "t"},
		Spec: RunTriggerSpec{
			PolicyWorkflowPod: "open-policy-pod",
			Match:             map[string]string{"allow": "true"},
			JobTemplate:       DenoJobSpec{Template: DenoRunSpec{DenoPodTemplate: DenoPodTemplate{Script: "x"}}},
		},
	}
	copied := original.DeepCopy()
	original.Spec.Match["allow"] = "false"
	if copied.Spec.Match["allow"] != "true" {
		t.Fatalf("Match aliased: %q", copied.Spec.Match["allow"])
	}
}

func TestServiceAccountAbsentDistinguishesFromZero(t *testing.T) {
	var absent DenoPodSpec
	if absent.ServiceAccount != nil {
		t.Fatal("an absent service account should be nil, so no token is minted")
	}
	present := DenoPodSpec{DenoPodTemplate: DenoPodTemplate{ServiceAccount: &ServiceAccountRef{Name: "deno"}}}
	if present.ServiceAccount == nil || present.ServiceAccount.Name != "deno" {
		t.Fatal("a named service account should be preserved")
	}
}

func int32Ptr(v int32) *int32 {
	return &v
}
