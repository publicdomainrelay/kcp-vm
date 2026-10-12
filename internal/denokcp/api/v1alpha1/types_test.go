package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestDeepCopyDoesNotAliasInputsOutputsOrConditions(t *testing.T) {
	limit := int32(3)
	ttl := int64(30)
	start := metav1.Now()
	original := &PolicyWorkflowRun{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec: PolicyWorkflowRunSpec{
			Workflow:                runtime.RawExtension{Raw: []byte(`{"name":"x"}`)},
			Inputs:                  map[string]string{"a": "1"},
			Args:                    map[string]string{"b": "2"},
			BackoffLimit:            &limit,
			TTLSecondsAfterFinished: &ttl,
		},
		Status: PolicyWorkflowRunStatus{
			Phase:     PolicyWorkflowSucceeded,
			StartTime: &start,
			Outputs:   map[string]string{"allow": "true"},
			Conditions: []metav1.Condition{
				{Type: ConditionComplete, Status: metav1.ConditionTrue},
			},
		},
	}

	copied := original.DeepCopy()

	original.Spec.Inputs["a"] = "changed"
	original.Spec.Args["b"] = "changed"
	original.Status.Outputs["allow"] = "false"
	original.Status.Conditions[0].Status = metav1.ConditionFalse
	*original.Spec.BackoffLimit = 9
	*original.Spec.TTLSecondsAfterFinished = 99

	if copied.Spec.Inputs["a"] != "1" {
		t.Fatalf("Spec.Inputs aliased: %q", copied.Spec.Inputs["a"])
	}
	if copied.Spec.Args["b"] != "2" {
		t.Fatalf("Spec.Args aliased: %q", copied.Spec.Args["b"])
	}
	if copied.Status.Outputs["allow"] != "true" {
		t.Fatalf("Status.Outputs aliased: %q", copied.Status.Outputs["allow"])
	}
	if copied.Status.Conditions[0].Status != metav1.ConditionTrue {
		t.Fatalf("Status.Conditions aliased: %q", copied.Status.Conditions[0].Status)
	}
	if *copied.Spec.BackoffLimit != 3 {
		t.Fatalf("BackoffLimit aliased: %d", *copied.Spec.BackoffLimit)
	}
	if *copied.Spec.TTLSecondsAfterFinished != 30 {
		t.Fatalf("TTLSecondsAfterFinished aliased: %d", *copied.Spec.TTLSecondsAfterFinished)
	}
}

func TestBackoffLimitDistinguishesAbsentFromZero(t *testing.T) {
	absent := PolicyWorkflowRunSpec{}
	if absent.BackoffLimit != nil {
		t.Fatal("absent backoffLimit should be nil")
	}
	zero := int32(0)
	present := PolicyWorkflowRunSpec{BackoffLimit: &zero}
	if present.BackoffLimit == nil || *present.BackoffLimit != 0 {
		t.Fatal("explicit zero backoffLimit should be present and zero")
	}
}
