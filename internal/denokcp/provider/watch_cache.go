package provider

import (
	"context"
	"encoding/json"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/ref"
)

type cacheReader struct {
	mu sync.RWMutex

	indexers map[workKind][]cache.Indexer
}

func newCacheReader() *cacheReader {
	return &cacheReader{indexers: map[workKind][]cache.Indexer{}}
}

func (r *cacheReader) add(kind workKind, indexer cache.Indexer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.indexers[kind] = append(r.indexers[kind], indexer)
}

func (r *cacheReader) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.indexers = map[workKind][]cache.Indexer{}
}

func (r *cacheReader) kindIndexers(kind workKind) []cache.Indexer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]cache.Indexer(nil), r.indexers[kind]...)
}

// ponytail: List() rather than an index lookup, because the caller wants every
// DenoPod in every workspace and no index is keyed that way; the store is what
// holds them all.
func (r *cacheReader) allPods() []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for _, indexer := range r.kindIndexers(workPod) {
		for _, obj := range indexer.List() {
			if u, ok := obj.(*unstructured.Unstructured); ok {
				out = append(out, u)
			}
		}
	}
	return out
}

func (r *cacheReader) get(kind workKind, ref Ref) *unstructured.Unstructured {
	key := ref.Key()
	for _, indexer := range r.kindIndexers(kind) {
		objs, err := indexer.ByIndex(indexByClusterName, key)
		if err != nil || len(objs) == 0 {
			continue
		}
		if u, ok := objs[0].(*unstructured.Unstructured); ok {
			return u
		}
	}
	return nil
}

func (r *cacheReader) list(kind workKind, logicalCluster string) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for _, indexer := range r.kindIndexers(kind) {
		objs, err := indexer.ByIndex(indexByCluster, logicalCluster)
		if err != nil {
			continue
		}
		for _, obj := range objs {
			if u, ok := obj.(*unstructured.Unstructured); ok {
				out = append(out, u)
			}
		}
	}
	return out
}

func cachedTyped[T any](u *unstructured.Unstructured, resource string, ref Ref) (*T, error) {
	if u == nil {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: resource}, ref.Name)
	}
	body, err := json.Marshal(u.Object)
	if err != nil {
		return nil, err
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func cachedTypedList[T any](us []*unstructured.Unstructured) ([]T, error) {
	out := make([]T, 0, len(us))
	for _, u := range us {
		body, err := json.Marshal(u.Object)
		if err != nil {
			return nil, err
		}
		var v T
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (r *cacheReader) Read(_ context.Context, ref Ref) (*v1alpha1.PolicyWorkflowRun, error) {
	return cachedTyped[v1alpha1.PolicyWorkflowRun](r.get(workPolicyRun, ref), "policyworkflowruns", ref)
}

func (r *cacheReader) ReadRun(_ context.Context, ref Ref) (*v1alpha1.DenoRun, error) {
	return cachedTyped[v1alpha1.DenoRun](r.get(workRun, ref), "denoruns", ref)
}

func (r *cacheReader) ListRuns(_ context.Context, logicalCluster string) ([]v1alpha1.DenoRun, error) {
	return cachedTypedList[v1alpha1.DenoRun](r.list(workRun, logicalCluster))
}

func (r *cacheReader) ListRunsForJob(_ context.Context, logicalCluster, namespace, jobName string) ([]v1alpha1.DenoRun, error) {
	key := ref.Key(logicalCluster, namespace, jobName)
	var out []*unstructured.Unstructured
	for _, indexer := range r.kindIndexers(workRun) {
		objs, err := indexer.ByIndex(indexByClusterJob, key)
		if err != nil {
			continue
		}
		for _, obj := range objs {
			if u, ok := obj.(*unstructured.Unstructured); ok {
				out = append(out, u)
			}
		}
	}
	return cachedTypedList[v1alpha1.DenoRun](out)
}

func (r *cacheReader) ReadPod(_ context.Context, ref Ref) (*v1alpha1.DenoPod, error) {
	return cachedTyped[v1alpha1.DenoPod](r.get(workPod, ref), "denopods", ref)
}

func (r *cacheReader) ReadJob(_ context.Context, ref Ref) (*v1alpha1.DenoJob, error) {
	return cachedTyped[v1alpha1.DenoJob](r.get(workJob, ref), "denojobs", ref)
}

func (r *cacheReader) ReadTrigger(_ context.Context, ref Ref) (*v1alpha1.RunTrigger, error) {
	return cachedTyped[v1alpha1.RunTrigger](r.get(workTrigger, ref), "runtriggers", ref)
}

func (r *cacheReader) ReadOpenBao(_ context.Context, ref Ref) (*v1alpha1.OpenBao, error) {
	return cachedTyped[v1alpha1.OpenBao](r.get(workOpenBao, ref), "openbaos", ref)
}

func (r *cacheReader) ReadEngine(_ context.Context, ref Ref) (*v1alpha1.PolicyEngine, error) {
	return cachedTyped[v1alpha1.PolicyEngine](r.get(workEngine, ref), "policyengines", ref)
}

func (r *cacheReader) ReadWorkflowPod(_ context.Context, ref Ref) (*v1alpha1.PolicyWorkflowPod, error) {
	return cachedTyped[v1alpha1.PolicyWorkflowPod](r.get(workWorkflowPod, ref), "policyworkflowpods", ref)
}

func (r *cacheReader) ListWorkflowRuns(_ context.Context, logicalCluster string) ([]v1alpha1.PolicyWorkflowRun, error) {
	return cachedTypedList[v1alpha1.PolicyWorkflowRun](r.list(workPolicyRun, logicalCluster))
}

func (r *cacheReader) triggerNamesForPod(logicalCluster, namespace, podName string) []string {
	key := ref.Key(logicalCluster, namespace, podName)
	var out []string
	for _, indexer := range r.kindIndexers(workTrigger) {
		objs, err := indexer.ByIndex(indexByClusterTriggerPod, key)
		if err != nil {
			continue
		}
		for _, obj := range objs {
			u, ok := obj.(*unstructured.Unstructured)
			if !ok {
				continue
			}
			out = append(out, u.GetName())
		}
	}
	return out
}

func (r *cacheReader) ListWorkflowRunsForPod(_ context.Context, logicalCluster, namespace, podName string) ([]v1alpha1.PolicyWorkflowRun, error) {
	key := ref.Key(logicalCluster, namespace, podName)
	var out []*unstructured.Unstructured
	for _, indexer := range r.kindIndexers(workPolicyRun) {
		objs, err := indexer.ByIndex(indexByClusterPod, key)
		if err != nil {
			continue
		}
		for _, obj := range objs {
			if u, ok := obj.(*unstructured.Unstructured); ok {
				out = append(out, u)
			}
		}
	}
	return cachedTypedList[v1alpha1.PolicyWorkflowRun](out)
}
