package scale

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var esoDeployments = []string{"external-secrets", "external-secrets-cert-controller", "external-secrets-webhook"}

func controlledBy(kind, name string) []metav1.OwnerReference {
	return []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: kind, Name: name, Controller: b(true)}}
}

func deployment(ns, name string, replicas int32) appsv1.Deployment {
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(replicas)},
	}
}

func replicaSetOf(d appsv1.Deployment) appsv1.ReplicaSet {
	return appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Namespace: d.Namespace, Name: d.Name + "-5d8f7c", OwnerReferences: controlledBy(KindDeployment, d.Name),
	}}
}

func podOf(rs appsv1.ReplicaSet, suffix string, phase corev1.PodPhase) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: rs.Namespace, Name: rs.Name + "-" + suffix, OwnerReferences: controlledBy(KindReplicaSet, rs.Name),
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func entryFor(t *testing.T, d appsv1.Deployment) BaselineEntry {
	t.Helper()
	h, err := SpecHash(d.Spec)
	if err != nil {
		t.Fatal(err)
	}
	return BaselineEntry{WorkloadKey: WorkloadKey{Kind: KindDeployment, Namespace: d.Namespace, Name: d.Name}, SpecHash: h}
}

func installedOperator(t *testing.T) (Observation, []appsv1.ReplicaSet, Baseline) {
	t.Helper()
	var o Observation
	var rss []appsv1.ReplicaSet
	var base Baseline
	for _, name := range esoDeployments {
		d := deployment("default", name, 1)
		rs := replicaSetOf(d)
		o.Deployments = append(o.Deployments, d)
		o.Pods = append(o.Pods, podOf(rs, "x7k2p", corev1.PodPending))
		rss = append(rss, rs)
		base.Workloads = append(base.Workloads, entryFor(t, d))
	}
	return o, rss, base
}

func TestOurInstalledOperatorAloneIsNothingDeployed(t *testing.T) {
	o, rss, base := installedOperator(t)
	n := Narrow(o, rss, base)
	if Intent(n) {
		t.Fatalf("a cluster holding only the operator we installed reported intent (demand %d): our "+
			"own add-ons must read as nothing deployed, or a cluster can never return to zero nodes",
			Demand(n))
	}
	if d := Decide(n, State{Nodes: 6}, t0, Options{PodsPerNode: 1}); d.Intent {
		t.Fatalf("decided %s/%d with intent; an idle cluster of only our add-ons must start its hold", d.Action, d.Nodes)
	}
}

func TestAPodAndItsDeploymentAreOneUnitOfDemand(t *testing.T) {
	o, rss, _ := installedOperator(t)
	n := Narrow(o, rss, Baseline{})
	if got := Demand(n); got != 3 {
		t.Fatalf("three Deployments of one replica, each with its one Pending pod, demanded %d, want 3: "+
			"a pod whose Deployment is already counted is the same unit of work, not a second one", got)
	}
	if got := Target(n, Options{PodsPerNode: 1}); got != 3 {
		t.Fatalf("target %d at one pod per node, want 3", got)
	}
}

func TestABaselineWorkloadWhoseSpecChangedIsIntent(t *testing.T) {
	o, rss, base := installedOperator(t)
	o.Deployments[0].Spec.Replicas = i32(2)
	o.Pods = append(o.Pods, podOf(rss[0], "q9m4w", corev1.PodPending))
	n := Narrow(o, rss, base)
	if !Intent(n) {
		t.Fatal("our Deployment scaled from 1 to 2 by someone else reported no intent: a changed spec " +
			"is a tenant's request, not our install")
	}
	if got := Demand(n); got != 2 {
		t.Fatalf("the changed Deployment demanded %d, want its 2 replicas and nothing for its pods", got)
	}
}

func TestATenantWorkloadBesideTheBaselineIsIntent(t *testing.T) {
	o, rss, base := installedOperator(t)
	web := deployment("default", "web", 4)
	rs := replicaSetOf(web)
	o.Deployments = append(o.Deployments, web)
	o.Pods = append(o.Pods, podOf(rs, "a", corev1.PodRunning), podOf(rs, "b", corev1.PodPending))
	rss = append(rss, rs)
	n := Narrow(o, rss, base)
	if got := Demand(n); got != 4 {
		t.Fatalf("a four-replica tenant Deployment beside our add-ons demanded %d, want 4", got)
	}
}

func TestABaselineEntryNamesOneNamespace(t *testing.T) {
	o, rss, base := installedOperator(t)
	other := deployment("tenant", esoDeployments[0], 1)
	o.Deployments = append(o.Deployments, other)
	n := Narrow(o, rss, base)
	if got := Demand(n); got != 1 {
		t.Fatalf("a Deployment sharing our add-on's name in another namespace demanded %d, want 1: the "+
			"baseline is keyed on kind, namespace and name together", got)
	}
}

func TestABarePodAndAPodOfAnUnownedReplicaSetStillCount(t *testing.T) {
	bare := pod(corev1.PodPending)
	bare.Name, bare.Namespace = "runner-abc", "gitlab"
	rs := appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "gitlab", Name: "orphan"}}
	o := Observation{Pods: []corev1.Pod{bare, podOf(rs, "z", corev1.PodRunning)}}
	if got := Demand(Narrow(o, []appsv1.ReplicaSet{rs}, Baseline{})); got != 2 {
		t.Fatalf("a bare pod and a pod of a ReplicaSet no Deployment owns demanded %d, want 2: their only "+
			"count is the pod itself", got)
	}
}

func TestPodsOfACountedJobStatefulSetOrDaemonSetAreNotCountedTwice(t *testing.T) {
	job := batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "ci", Name: "build"}}
	sts := appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: "pg"}, Spec: appsv1.StatefulSetSpec{Replicas: i32(2)}}
	ds := appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "obs", Name: "agent"},
		Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 1}}
	owned := func(ns, kind, name string) corev1.Pod {
		return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name + "-0", OwnerReferences: controlledBy(kind, name)},
			Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	}
	o := Observation{
		Jobs:         []batchv1.Job{job},
		StatefulSets: []appsv1.StatefulSet{sts},
		DaemonSets:   []appsv1.DaemonSet{ds},
		Pods:         []corev1.Pod{owned("ci", KindJob, "build"), owned("db", KindStatefulSet, "pg"), owned("obs", KindDaemonSet, "agent")},
	}
	if got := Demand(Narrow(o, nil, Baseline{})); got != 3 {
		t.Fatalf("a Job (1), a two-replica StatefulSet and a DaemonSet on one node, each with a Running pod, "+
			"demanded %d, want 3: the Job and the StatefulSet once each, and the DaemonSet not at all, "+
			"because its want follows the nodes rather than asking for one", got)
	}
}

func TestTheSpecHashIsStableAndSeesAReplicaChange(t *testing.T) {
	a, _ := SpecHash(deployment("default", "x", 1).Spec)
	again, _ := SpecHash(deployment("default", "x", 1).Spec)
	two, _ := SpecHash(deployment("default", "x", 2).Spec)
	if a == "" || a != again {
		t.Fatalf("the same spec hashed to %q then %q", a, again)
	}
	if a == two {
		t.Fatal("replicas 1 and 2 hashed the same")
	}
}
