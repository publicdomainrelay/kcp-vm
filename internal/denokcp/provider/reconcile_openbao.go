package provider

import (
	"context"
	"fmt"
	"reflect"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

const openBaoInterval = 30 * time.Second

func (p *Provider) reconcileOpenBao(ctx context.Context, ref Ref, obj *v1alpha1.OpenBao) (time.Duration, bool, error) {
	ref.ResourceVersion = obj.ResourceVersion
	if obj.DeletionTimestamp != nil {
		return p.deleteOpenBao(ctx, ref, obj)
	}
	if p.pki == nil {
		return p.writeOpenBaoStatus(ctx, ref, obj, v1alpha1.OpenBaoStatus{
			Namespace: obj.Spec.Namespace,
			Ready:     false,
			Message:   "the provider has no OpenBao address, so no namespace can be created",
		}, metav1.Condition{
			Type:    v1alpha1.OpenBaoConditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  "NoOpenBao",
			Message: "start the provider with --openbao-addr and --openbao-token",
		})
	}
	// ponytail: two objects naming one Kubernetes namespace leave a workload with
	// no way to tell which authority it should be issued from, so neither is
	// provisioned and the ambiguity is reported rather than resolved by picking.
	// The list comes from the API, like the one the pod path makes, so the report
	// and the refusal cannot disagree about how many there are.
	others, err := p.opts.Runtime.ListOpenBaos(ctx, ref.LogicalCluster, ref.Namespace)
	if err != nil {
		return 0, false, err
	}
	if len(others) > 1 {
		return p.writeOpenBaoStatus(ctx, ref, obj, v1alpha1.OpenBaoStatus{
			Namespace: obj.Spec.Namespace,
			Ready:     false,
			Message:   fmt.Sprintf("%d OpenBao objects name this namespace; exactly one may", len(others)),
		}, metav1.Condition{
			Type:    v1alpha1.OpenBaoConditionAmbiguous,
			Status:  metav1.ConditionTrue,
			Reason:  "MultipleAuthorities",
			Message: "a workload is issued from the single OpenBao object in its namespace",
		})
	}
	authority, err := p.pki.EnsureAuthority(ctx, obj.Spec.Namespace)
	if err != nil {
		p.opts.Log.Warn("openbao: provisioning failed", "namespace", obj.Spec.Namespace, "err", err)
		status := v1alpha1.OpenBaoStatus{
			Namespace: obj.Spec.Namespace,
			Ready:     false,
			Message:   err.Error(),
		}
		// ponytail: a provisioning failure is reported and retried on the normal
		// interval rather than returned, because a vault that is down should leave
		// a condition saying so on the object, not a reconcile error in a log.
		return p.writeOpenBaoStatus(ctx, ref, obj, status, metav1.Condition{
			Type:    v1alpha1.OpenBaoConditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  "ProvisionFailed",
			Message: err.Error(),
		})
	}

	if !hasFinalizer(obj.Finalizers, v1alpha1.FinalizerOpenBao) {
		if err := p.opts.Runtime.WriteOpenBaoFinalizer(ctx, ref, append(append([]string(nil), obj.Finalizers...), v1alpha1.FinalizerOpenBao)); err != nil {
			return 0, false, err
		}
	}
	return p.writeOpenBaoStatus(ctx, ref, obj, v1alpha1.OpenBaoStatus{
		Namespace: authority.Namespace,
		Serial:    authority.Serial,
		Chain:     authority.Chain,
		Ready:     true,
		Message:   "the namespace holds an intermediate CA signed by the root",
	}, metav1.Condition{
		Type:    v1alpha1.OpenBaoConditionReady,
		Status:  metav1.ConditionTrue,
		Reason:  "Provisioned",
		Message: "intermediate " + authority.CommonName + " serial " + authority.Serial,
	})
}

func (p *Provider) deleteOpenBao(ctx context.Context, ref Ref, obj *v1alpha1.OpenBao) (time.Duration, bool, error) {
	if !hasFinalizer(obj.Finalizers, v1alpha1.FinalizerOpenBao) {
		return 0, true, nil
	}
	if p.pki != nil {
		if err := p.pki.Delete(ctx, obj.Spec.Namespace); err != nil {
			return 0, false, err
		}
	}
	if err := p.opts.Runtime.RemoveOpenBaoFinalizer(ctx, ref); err != nil {
		return 0, false, err
	}
	return 0, true, nil
}

func (p *Provider) openBaoRequeueAfter() time.Duration {
	if p.opts.Interval > openBaoInterval {
		return p.opts.Interval
	}
	return openBaoInterval
}

func (p *Provider) writeOpenBaoStatus(ctx context.Context, ref Ref, obj *v1alpha1.OpenBao, status v1alpha1.OpenBaoStatus, condition metav1.Condition) (time.Duration, bool, error) {
	condition.ObservedGeneration = obj.Generation
	now := metav1.NewTime(p.opts.Now())
	if previous := conditionOf(obj.Status.Conditions, condition.Type); previous != nil && previous.Status == condition.Status {
		condition.LastTransitionTime = previous.LastTransitionTime
	} else {
		condition.LastTransitionTime = now
	}
	status.Conditions = []metav1.Condition{condition}
	if !p.opts.WriteStatus {
		return p.openBaoRequeueAfter(), false, nil
	}
	if reflect.DeepEqual(status, obj.Status) {
		return p.openBaoRequeueAfter(), false, nil
	}
	if err := p.opts.Runtime.WriteOpenBaoStatus(ctx, ref, status); err != nil {
		return 0, false, err
	}
	return p.openBaoRequeueAfter(), false, nil
}

// authorityFor is the authority a workload in this namespace is issued from.
//
// ponytail: it reads the API rather than the informer cache, unlike every other
// reader in this provider. The cache is filled per APIExport virtual workspace,
// and a list in one workspace can come back holding one cluster's objects and
// not another's -- measured with three bound workspaces, where the pod cache
// held all three and this kind's held one, and the answer flipped as the
// reflector re-listed. A certificate is issued once, at a pod's start, so the
// round trip is affordable and a wrong answer is not.
func (p *Provider) authorityFor(ctx context.Context, logicalCluster, namespace string) (*v1alpha1.OpenBao, error) {
	objs, err := p.opts.Runtime.ListOpenBaos(ctx, logicalCluster, namespace)
	if err != nil {
		return nil, err
	}
	switch len(objs) {
	case 0:
		return nil, fmt.Errorf("namespace %s in %s holds no OpenBao object", namespace, logicalCluster)
	case 1:
		return &objs[0], nil
	default:
		return nil, fmt.Errorf("namespace %s in %s holds %d OpenBao objects, and exactly one may name the authority", namespace, logicalCluster, len(objs))
	}
}

func conditionOf(conditions []metav1.Condition, kind string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == kind {
			return &conditions[i]
		}
	}
	return nil
}

func hasFinalizer(finalizers []string, want string) bool {
	for _, f := range finalizers {
		if f == want {
			return true
		}
	}
	return false
}
