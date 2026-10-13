package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/kcp"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/ref"
)

const defaultProviderWorkspace = "root:deno-provider"

// ponytail: a non-terminal key whose reconciler asked for no requeue waits this long, so a worker never spins on it.
const defaultRequeueAfter = 2 * time.Second

// ponytail: a trigger no longer asks for a requeue, because the run event that completes its candidate run wakes it. This is the backstop for an event that never arrives, so it is long enough to be unmistakably not the discovery path: thirty times the 2s poll it replaced, and still far inside the default 3600s run TTL, so a run that is merely delayed rather than deleted is still picked up.
const triggerBackstop = time.Minute

const (
	indexByCluster           = "by-cluster"
	indexByClusterName       = "by-cluster-name"
	indexByClusterPod        = "by-cluster-pod"
	indexByClusterJob        = "by-cluster-job"
	indexByClusterTriggerPod = "by-cluster-trigger-pod"
)

const (
	exportDenoRuntime       = "denoruntime"
	exportPolicyWorkflowRun = "policyworkflowruns"
)

var (
	gvrDenoPods           = schema.GroupVersionResource{Group: "deno.computer", Version: "v1alpha1", Resource: "denopods"}
	gvrDenoRuns           = schema.GroupVersionResource{Group: "deno.computer", Version: "v1alpha1", Resource: "denoruns"}
	gvrDenoJobs           = schema.GroupVersionResource{Group: "deno.computer", Version: "v1alpha1", Resource: "denojobs"}
	gvrRunTriggers        = schema.GroupVersionResource{Group: "deno.computer", Version: "v1alpha1", Resource: "runtriggers"}
	gvrPolicyEngines      = schema.GroupVersionResource{Group: "deno.computer", Version: "v1alpha1", Resource: "policyengines"}
	gvrPolicyWorkflowPods = schema.GroupVersionResource{Group: "deno.computer", Version: "v1alpha1", Resource: "policyworkflowpods"}
	gvrPolicyWorkflowRuns = schema.GroupVersionResource{Group: "deno.computer", Version: "v1alpha1", Resource: "policyworkflowruns"}
	gvrOpenBaos           = schema.GroupVersionResource{Group: "deno.computer", Version: "v1alpha1", Resource: "openbaos"}
)

type watchResource struct {
	kind workKind

	gvr schema.GroupVersionResource
}

var denoRuntimeResources = []watchResource{
	{kind: workEngine, gvr: gvrPolicyEngines},
	{kind: workWorkflowPod, gvr: gvrPolicyWorkflowPods},
	{kind: workTrigger, gvr: gvrRunTriggers},
	{kind: workRun, gvr: gvrDenoRuns},
	{kind: workJob, gvr: gvrDenoJobs},
	{kind: workPod, gvr: gvrDenoPods},
	{kind: workOpenBao, gvr: gvrOpenBaos},
}

type watchState struct {
	queue workqueue.TypedRateLimitingInterface[workKey]
}

func (p *Provider) RunWatch(ctx context.Context) error {
	endpoints, err := p.awaitWorkspaces(ctx)
	if err != nil {
		return err
	}
	if endpoints == nil {
		return nil
	}

	reader := newCacheReader()
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[workKey]())
	p.reader = reader
	p.watch = &watchState{queue: queue}

	// ponytail: workers start before caches sync; handlers enqueue only after the object is in the store, so a reconcile never reads an unseen object.
	var wg sync.WaitGroup
	for i := 0; i < p.opts.WatchWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.runWatchWorker(ctx, queue)
		}()
	}

	round, err := waitForCaches(ctx,
		func() (*informerRound, error) {
			if fresh, err := p.discoverWorkspaces(ctx); err == nil && endpointsReady(fresh) {
				endpoints = fresh
			}
			return p.startInformers(ctx, endpoints, reader, queue)
		},
		defaultCacheSyncTimeout, cacheSyncRetryPace, p.opts.Log)
	if err != nil {
		queue.ShutDown()
		wg.Wait()
		return err
	}
	defer round.Close()

	<-ctx.Done()
	queue.ShutDown()
	wg.Wait()
	return nil
}

// ponytail: the initial list is bounded so a stalled endpoint costs a retry.
// An unbounded WaitForCacheSync on a factory whose list never returns left the
// provider started and reconciling nothing, with the only symptom an endpoint
// that stayed quiet.
const defaultCacheSyncTimeout = 30 * time.Second

const cacheSyncRetryPace = time.Second

type informerFactory interface {
	Start(stopCh <-chan struct{})
	WaitForCacheSync(stopCh <-chan struct{}) map[schema.GroupVersionResource]bool
}

type informerRound struct {
	factories []informerFactory
	stop      chan struct{}
	once      sync.Once
}

func (r *informerRound) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() { close(r.stop) })
}

func (p *Provider) startInformers(ctx context.Context, endpoints map[string][]string, reader *cacheReader, queue workqueue.TypedRateLimitingInterface[workKey]) (*informerRound, error) {
	round := &informerRound{stop: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			round.Close()
		case <-round.stop:
		}
	}()
	reader.reset()
	for _, url := range endpoints[exportDenoRuntime] {
		factory, err := p.watchFactory(url)
		if err != nil {
			round.Close()
			return nil, err
		}
		for _, res := range denoRuntimeResources {
			p.registerInformer(factory, res, reader, queue)
		}
		factory.Start(round.stop)
		round.factories = append(round.factories, factory)
	}
	for _, url := range endpoints[exportPolicyWorkflowRun] {
		factory, err := p.watchFactory(url)
		if err != nil {
			round.Close()
			return nil, err
		}
		p.registerInformer(factory, watchResource{kind: workPolicyRun, gvr: gvrPolicyWorkflowRuns}, reader, queue)
		factory.Start(round.stop)
		round.factories = append(round.factories, factory)
	}
	return round, nil
}

func awaitCacheRound(ctx context.Context, factories []informerFactory, deadline time.Duration) bool {
	bounded, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	for _, factory := range factories {
		for _, synced := range factory.WaitForCacheSync(bounded.Done()) {
			if !synced {
				return false
			}
		}
	}
	return true
}

func waitForCaches(ctx context.Context, build func() (*informerRound, error), deadline, pace time.Duration, log *slog.Logger) (*informerRound, error) {
	for {
		round, err := build()
		if err != nil {
			return nil, err
		}
		if awaitCacheRound(ctx, round.factories, deadline) {
			log.Info("watch: informer caches have synced", "kinds", len(round.factories))
			return round, nil
		}
		round.Close()
		if ctx.Err() != nil {
			return nil, nil
		}
		log.Info("watch: informer caches did not sync within the deadline, retrying",
			"timeout", deadline, "kinds", len(round.factories))
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(pace):
		}
	}
}

var gvrAPIExportEndpointSlices = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices"}

func (p *Provider) awaitWorkspaces(ctx context.Context) (map[string][]string, error) {
	endpoints, err := p.discoverWorkspaces(ctx)
	if err == nil && endpointsReady(endpoints) {
		return endpoints, nil
	}
	p.logEndpointWait(endpoints, err)
	for {
		// ponytail: the watch is bounded, not open-ended. A watch opened without a
		// resource version starts at the newest object and delivers only future
		// changes, so a slice whose status is already populated and stable produces
		// no event at all -- and a provider that waited on one sat in this loop
		// forever while its export was ready, which reads as a controller that
		// starts and reconciles nothing. The watch stays as the fast path and the
		// discovery on every pass is what ends the wait whether or not anything
		// moves.
		werr := p.watchEndpointSlicesFor(ctx, endpointSlicePoll)
		if werr != nil && !errors.Is(werr, errEndpointSlicesReady) {
			p.opts.Log.Warn("watch: waiting for APIExport virtual workspace endpoints", "err", werr)
		}
		endpoints, err = p.discoverWorkspaces(ctx)
		if err == nil && endpointsReady(endpoints) {
			return endpoints, nil
		}
		if err != nil {
			p.opts.Log.Warn("watch: listing APIExport virtual workspace endpoints", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(time.Second):
		}
	}
}

// ponytail: two seconds is short enough that a provider starting just behind an
// install waits one poll, and long enough that a provider waiting on a cluster
// that never comes is not re-listing in a hot loop.
const endpointSlicePoll = 2 * time.Second

func (p *Provider) watchEndpointSlicesFor(ctx context.Context, wait time.Duration) error {
	bounded, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	return p.watchEndpointSlices(bounded)
}

func endpointsReady(endpoints map[string][]string) bool {
	return len(endpoints[exportDenoRuntime]) > 0 && len(endpoints[exportPolicyWorkflowRun]) > 0
}

func (p *Provider) logEndpointWait(endpoints map[string][]string, err error) {
	if err != nil {
		p.opts.Log.Warn("watch: waiting for APIExport virtual workspace endpoints", "err", err)
		return
	}
	p.opts.Log.Info("watch: waiting for APIExport virtual workspace endpoints",
		"denoruntime", len(endpoints[exportDenoRuntime]),
		"policyworkflowruns", len(endpoints[exportPolicyWorkflowRun]))
}

// ponytail: wait on the endpoint slice watch instead of re-listing every 2s; the watch is bounded by ctx and a single closed-channel retry is immediate.
func (p *Provider) watchEndpointSlices(ctx context.Context) error {
	client, err := p.endpointSliceClient()
	if err != nil {
		return err
	}
	w, err := client.Resource(gvrAPIExportEndpointSlices).Watch(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	defer w.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.ResultChan():
			if !ok {
				return nil
			}
			switch ev.Type {
			case watch.Added, watch.Modified, watch.Deleted:
				endpoints, err := p.discoverWorkspaces(ctx)
				if err == nil && endpointsReady(endpoints) {
					return errEndpointSlicesReady
				}
			}
		}
	}
}

var errEndpointSlicesReady = errors.New("provider: APIExport virtual workspace endpoints are ready")

func (p *Provider) endpointSliceClient() (dynamic.Interface, error) {
	if p.opts.RestConfig == nil {
		return nil, fmt.Errorf("provider: RestConfig is required for the watch driver")
	}
	cfg := rest.CopyConfig(p.opts.RestConfig)
	cfg.Host = strings.TrimSuffix(ref.BaseHost(p.opts.Host), "/") + "/clusters/" + p.opts.ProviderWorkspace
	return dynamic.NewForConfig(cfg)
}

func (p *Provider) discoverWorkspaces(ctx context.Context) (map[string][]string, error) {
	client, err := p.endpointSliceClient()
	if err != nil {
		return nil, err
	}
	list, err := client.Resource(gvrAPIExportEndpointSlices).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for i := range list.Items {
		item := &list.Items[i]
		export, _, _ := unstructured.NestedString(item.Object, "spec", "export", "name")
		if export == "" {
			continue
		}
		endpoints, _, _ := unstructured.NestedSlice(item.Object, "status", "endpoints")
		for _, raw := range endpoints {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			url, _ := entry["url"].(string)
			if url != "" {
				out[export] = append(out[export], url)
			}
		}
	}
	return out, nil
}

func (p *Provider) watchFactory(url string) (dynamicinformer.DynamicSharedInformerFactory, error) {
	cfg := rest.CopyConfig(p.opts.RestConfig)
	cfg.Host = strings.TrimSuffix(url, "/") + "/clusters/*"
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return dynamicinformer.NewFilteredDynamicSharedInformerFactory(client, 0, metav1.NamespaceAll, nil), nil
}

var watchIndexers = cache.Indexers{
	indexByCluster: func(obj any) ([]string, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, nil
		}
		lc := u.GetAnnotations()[kcp.ClusterAnnotation]
		if lc == "" {
			return nil, nil
		}
		return []string{lc}, nil
	},
	indexByClusterName: func(obj any) ([]string, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, nil
		}
		lc := u.GetAnnotations()[kcp.ClusterAnnotation]
		if lc == "" {
			return nil, nil
		}
		return []string{ref.Key(lc, u.GetNamespace(), u.GetName())}, nil
	},
	indexByClusterPod: func(obj any) ([]string, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, nil
		}
		lc := u.GetAnnotations()[kcp.ClusterAnnotation]
		pod := u.GetLabels()[v1alpha1.PolicyWorkflowPodLabel]
		if lc == "" || pod == "" {
			return nil, nil
		}
		return []string{ref.Key(lc, u.GetNamespace(), pod)}, nil
	},
	indexByClusterJob: func(obj any) ([]string, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, nil
		}
		lc := u.GetAnnotations()[kcp.ClusterAnnotation]
		job := u.GetLabels()[JobRunLabel]
		if lc == "" || job == "" {
			return nil, nil
		}
		return []string{ref.Key(lc, u.GetNamespace(), job)}, nil
	},
	indexByClusterTriggerPod: func(obj any) ([]string, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, nil
		}
		lc := u.GetAnnotations()[kcp.ClusterAnnotation]
		pod, _, _ := unstructured.NestedString(u.Object, "spec", "policyWorkflowPod")
		if lc == "" || pod == "" {
			return nil, nil
		}
		return []string{ref.Key(lc, u.GetNamespace(), pod)}, nil
	},
}

// workCacheKey keys a cached object by workspace, namespace and name.
//
// ponytail: the informer's own store keys by namespace and name, so two DenoPods
// named default/pds in two workspaces are one store entry and the later event
// evicts the earlier object, which leaves the evicted workload with no status
// ever written. The reader therefore keeps its own store, keyed by the ref the
// object's kcp.io/cluster annotation plus its namespace and name build, and the
// handlers below fill it.
func workCacheKey(obj any) (string, error) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return "", fmt.Errorf("provider: cache object %T is not unstructured", obj)
	}
	return Ref{
		LogicalCluster: u.GetAnnotations()[kcp.ClusterAnnotation],
		Namespace:      u.GetNamespace(),
		Name:           u.GetName(),
	}.Key(), nil
}

func newCacheStore() cache.Indexer {
	return cache.NewIndexer(workCacheKey, watchIndexers)
}

func untombstone(obj any) any {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		return tombstone.Obj
	}
	return obj
}

func (p *Provider) registerInformer(factory dynamicinformer.DynamicSharedInformerFactory, res watchResource, reader *cacheReader, queue workqueue.TypedRateLimitingInterface[workKey]) {
	informer := factory.ForResource(res.gvr).Informer()
	store := newCacheStore()
	reader.add(res.kind, store)
	_, _ = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			p.recordEvent()
			_ = store.Add(obj)
			enqueueWatchObject(res.kind, obj, reader, queue)
		},
		UpdateFunc: func(oldObj, obj any) {
			p.recordEvent()
			_ = store.Update(obj)
			enqueueWatchUpdate(res.kind, oldObj, obj, reader, queue)
		},
		DeleteFunc: func(obj any) {
			// ponytail: the reader's store is its own, so a delete the informer's
			// store applies for free has to be replayed here; without it a deleted
			// run reads as present forever and its finalizer never runs.
			_ = store.Delete(untombstone(obj))
		},
	})
}

func enqueueWatchObject(kind workKind, obj any, reader *cacheReader, queue workqueue.TypedRateLimitingInterface[workKey]) {
	enqueueOwn(kind, obj, queue)
	switch kind {
	case workRun:
		enqueueJobForRun(obj, queue)
	case workPolicyRun:
		enqueueTriggersForRun(reader, obj, queue)
	}
}

func enqueueWatchUpdate(kind workKind, oldObj, obj any, reader *cacheReader, queue workqueue.TypedRateLimitingInterface[workKey]) {
	enqueueOwn(kind, obj, queue)
	switch kind {
	case workRun:
		if runPhase(oldObj) != runPhase(obj) {
			enqueueJobForRun(obj, queue)
		}
	case workPolicyRun:
		enqueueTriggersForRun(reader, obj, queue)
	}
}

func enqueueOwn(kind workKind, obj any, queue workqueue.TypedRateLimitingInterface[workKey]) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}
	lc := u.GetAnnotations()[kcp.ClusterAnnotation]
	if lc == "" {
		return
	}
	queue.Add(workKey{kind: kind, ref: Ref{LogicalCluster: lc, Namespace: u.GetNamespace(), Name: u.GetName()}})
}

// ponytail: the job derives its counts from child run phases, so only a phase change needs to wake it; other run updates (for example the intermediate running write) do not.
func enqueueJobForRun(obj any, queue workqueue.TypedRateLimitingInterface[workKey]) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}
	lc := u.GetAnnotations()[kcp.ClusterAnnotation]
	job := u.GetLabels()[JobRunLabel]
	if lc == "" || job == "" {
		return
	}
	queue.Add(workKey{kind: workJob, ref: Ref{LogicalCluster: lc, Namespace: u.GetNamespace(), Name: job}})
}

// ponytail: the trigger selects the newest terminal policy run labelled with the pod its spec names, so only a policy run that is terminal can change its verdict, and the phase and the outputs land in the same status write; that makes a terminal phase the only run event the trigger needs. A DenoRun is a different kind and wakes its job instead.
func enqueueTriggersForRun(reader *cacheReader, obj any, queue workqueue.TypedRateLimitingInterface[workKey]) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok || reader == nil {
		return
	}
	if !terminalWorkflowPhase(v1alpha1.PolicyWorkflowPhase(runPhase(u))) {
		return
	}
	lc := u.GetAnnotations()[kcp.ClusterAnnotation]
	pod := u.GetLabels()[v1alpha1.PolicyWorkflowPodLabel]
	if lc == "" || pod == "" {
		return
	}
	for _, name := range reader.triggerNamesForPod(lc, u.GetNamespace(), pod) {
		queue.Add(workKey{kind: workTrigger, ref: Ref{LogicalCluster: lc, Namespace: u.GetNamespace(), Name: name}})
	}
}

func runPhase(obj any) string {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return ""
	}
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	return phase
}
func (p *Provider) runWatchWorker(ctx context.Context, queue workqueue.TypedRateLimitingInterface[workKey]) {
	for {
		key, shutdown := queue.Get()
		if shutdown {
			return
		}
		func() {
			defer queue.Done(key)
			p.reconciles.Add(1)
			p.metrics.reconciles.Add(1)
			start := time.Now()
			after, terminal, err := p.process(ctx, key)
			p.recordReconcile(time.Since(start), err)
			if err != nil {
				if apierrors.IsConflict(err) {
					p.recordConflict()
					queue.Forget(key)
					queue.AddAfter(key, p.opts.MinTransitionPoll)
					return
				}
				p.opts.Log.Error("watch reconcile failed",
					"kind", string(key.kind), "workspace", key.ref.LogicalCluster, "name", key.ref.Name, "err", err)
				queue.AddRateLimited(key)
				return
			}
			queue.Forget(key)
			if terminal && after <= 0 {
				return
			}
			if after <= 0 {
				after = p.opts.Interval
			}

			if !terminal && key.kind == workRun && after > p.opts.MinTransitionPoll {
				after = p.opts.MinTransitionPoll
			}
			queue.AddAfter(key, after)
		}()
	}
}

func (p *Provider) process(ctx context.Context, key workKey) (time.Duration, bool, error) {
	switch key.kind {
	case workPolicyRun:
		run, err := p.readPolicyRun(ctx, key.ref)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return 0, true, nil
			}
			return 0, false, err
		}
		pass, err := p.Reconcile(ctx, key.ref.WithResourceVersion(run.ResourceVersion))
		if err != nil {
			return 0, false, err
		}
		return pass.RequeueAfter, terminalWorkflowPhase(pass.Phase), nil
	case workRun:
		run, err := p.readRun(ctx, key.ref)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return 0, true, nil
			}
			return 0, false, err
		}
		return p.reconcileRun(ctx, key.ref.WithResourceVersion(run.ResourceVersion), run)
	case workPod:
		pod, err := p.readPod(ctx, key.ref)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return 0, true, nil
			}
			return 0, false, err
		}
		return p.reconcilePod(ctx, key.ref.WithResourceVersion(pod.ResourceVersion), pod)
	case workOpenBao:
		obj, err := p.readOpenBao(ctx, key.ref)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return 0, true, nil
			}
			return 0, false, err
		}
		return p.reconcileOpenBao(ctx, key.ref.WithResourceVersion(obj.ResourceVersion), obj)
	case workEngine:
		engine, err := p.readEngine(ctx, key.ref)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return 0, true, nil
			}
			return 0, false, err
		}
		return p.reconcileEngine(ctx, key.ref.WithResourceVersion(engine.ResourceVersion), engine)
	case workWorkflowPod:
		pod, err := p.readWorkflowPod(ctx, key.ref)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return 0, true, nil
			}
			return 0, false, err
		}
		return p.reconcileWorkflowPod(ctx, key.ref.WithResourceVersion(pod.ResourceVersion), pod)
	case workTrigger:
		tr, err := p.readTrigger(ctx, key.ref)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return 0, true, nil
			}
			return 0, false, err
		}
		return p.reconcileTrigger(ctx, key.ref.WithResourceVersion(tr.ResourceVersion), tr)
	case workJob:
		job, err := p.readJob(ctx, key.ref)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return 0, true, nil
			}
			return 0, false, err
		}
		runs, err := p.listRunsForJob(ctx, key.ref.LogicalCluster, job.Namespace, job.Name)
		if err != nil {
			return 0, false, err
		}
		return p.reconcileJob(ctx, key.ref.WithResourceVersion(job.ResourceVersion), job, runs)
	}
	return 0, true, nil
}

func watchWorkers() int {
	n := goruntime.GOMAXPROCS(0)
	if n > 16 {
		n = 16
	}
	if n < 1 {
		n = 1
	}
	return n
}
