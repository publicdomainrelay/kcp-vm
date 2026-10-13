package computevm

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

const (
	BidTTL     = 24 * time.Hour
	BidGCEvery = time.Minute
)

var BidGVR = schema.GroupVersionResource{Group: v1alpha1.GroupName, Version: v1alpha1.Version, Resource: "bids"}

func bidFinishedAt(o *unstructured.Unstructured, vms map[string]bool) (time.Time, bool) {
	phase, _, _ := unstructured.NestedString(o.Object, "status", "phase")
	var stamp string
	switch v1alpha1.BidPhase(phase) {
	case v1alpha1.BidPhaseLost, v1alpha1.BidPhaseExpired, v1alpha1.BidPhaseFailed:
		stamp, _, _ = unstructured.NestedString(o.Object, "status", "timings", "finishedAt")
	case v1alpha1.BidPhaseWon:
		vm, _, _ := unstructured.NestedString(o.Object, "status", "computeVM")
		if vm != "" && vms[vm] {
			return time.Time{}, false
		}
		stamp, _, _ = unstructured.NestedString(o.Object, "status", "timings", "acceptedAt")
	default:
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return o.GetCreationTimestamp().Time, true
	}
	return at, true
}

func (ct *Controller) collectBids(ctx context.Context, vms []unstructured.Unstructured) {
	ct.mu.Lock()
	due := time.Since(ct.bidsAt) >= BidGCEvery
	if due {
		ct.bidsAt = time.Now()
	}
	ct.mu.Unlock()
	if !due {
		return
	}
	log := ct.c.log()
	list, err := ct.bids.List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) {
		return
	}
	if err != nil {
		log.Warn("listing Bids to remove the finished ones", "err", err.Error())
		return
	}
	live := map[string]bool{}
	for i := range vms {
		live[vms[i].GetName()] = true
	}
	var removed []string
	for i := range list.Items {
		o := &list.Items[i]
		at, done := bidFinishedAt(o, live)
		if !done || time.Since(at) < BidTTL {
			continue
		}
		if err := ct.bids.Delete(ctx, o.GetName(), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			log.Warn("removing a finished Bid", "bid", o.GetName(), "err", err.Error())
			continue
		}
		removed = append(removed, o.GetName())
	}
	if len(removed) > 0 {
		log.Info("Bids finished more than the TTL ago are removed", "removed", removed, "ttl", BidTTL.String())
	}
}
