package scale

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const coldStart = 10 * time.Second

var t0 = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

func i32(v int32) *int32           { return &v }
func b(v bool) *bool               { return &v }
func at(d time.Duration) time.Time { return t0.Add(d) }

func pod(phase corev1.PodPhase) corev1.Pod {
	return corev1.Pod{Status: corev1.PodStatus{Phase: phase}}
}

func TestAnEmptyClusterDecidesZero(t *testing.T) {
	d := Decide(Observation{}, State{}, t0, Options{})
	if d.Intent {
		t.Fatal("an empty cluster reported intent")
	}
	if d.Action != None || d.Nodes != 0 {
		t.Fatalf("empty cluster decided %s/%d, want None/0", d.Action, d.Nodes)
	}
}

func TestAPendingDeploymentDecidesOneNode(t *testing.T) {
	o := Observation{Deployments: []appsv1.Deployment{
		{ObjectMeta: metav1.ObjectMeta{Name: "web"}, Spec: appsv1.DeploymentSpec{Replicas: i32(1)}},
	}}
	if !Intent(o) {
		t.Fatal("a Deployment with replicas=1 and no pods yet reported no intent; the " +
			"predicate keys on spec.replicas precisely so this window is not a gap")
	}
	d := Decide(o, State{}, t0, Options{})
	if d.Action != Provision || d.Nodes != 1 {
		t.Fatalf("decided %s/%d, want Provision/1", d.Action, d.Nodes)
	}
}

func TestAPendingPodDecidesOneNode(t *testing.T) {
	if !Intent(Observation{Pods: []corev1.Pod{pod(corev1.PodPending)}}) {
		t.Fatal("a Pending pod is not intent")
	}
}

func TestARunningPodDecidesOneNode(t *testing.T) {
	if !Intent(Observation{Pods: []corev1.Pod{pod(corev1.PodRunning)}}) {
		t.Fatal("a Running pod is not intent")
	}
}

func TestARunningPodOnAGoneNodeIsStillIntent(t *testing.T) {
	o := Observation{Pods: []corev1.Pod{pod(corev1.PodUnknown)}}
	if !Intent(o) {
		t.Fatal("a pod in phase Unknown is not intent; its node is gone and the pod still wants one")
	}
	if narrowFires(o) {
		t.Fatal("the narrow trigger fired on a pod that is already scheduled; this case is " +
			"here to show it does NOT, which is why it cannot re-wake a cluster")
	}
}

func TestSucceededAndFailedPodsAreNotIntent(t *testing.T) {
	for _, p := range []corev1.PodPhase{corev1.PodSucceeded, corev1.PodFailed} {
		if Intent(Observation{Pods: []corev1.Pod{pod(p)}}) {
			t.Fatalf("a %s pod is intent; a finished pod must not hold a node open", p)
		}
	}
}

func TestACompletedJobDoesNotPinANodeForever(t *testing.T) {
	done := metav1.NewTime(at(-time.Minute))
	o := Observation{
		Jobs: []batchv1.Job{{
			ObjectMeta: metav1.ObjectMeta{Name: "migrate"},
			Status:     batchv1.JobStatus{CompletionTime: &done, Succeeded: 1},
		}},
		Pods: []corev1.Pod{pod(corev1.PodSucceeded)},
	}
	if Intent(o) {
		t.Fatal("a completed Job with a Succeeded pod reported intent")
	}

	st := State{Nodes: 1, IdleSince: at(0)}
	hold := 2 * time.Minute
	if d := Decide(o, st, at(time.Minute), Options{Hold: hold}); d.Action != None {
		t.Fatalf("tore down at 1m with a 2m hold: %s", d.Action)
	}
	d := Decide(o, st, at(hold), Options{Hold: hold})
	if d.Action != Teardown {
		t.Fatalf("a completed Job pinned the node past the hold: %s", d.Action)
	}
	if st.Next(d, at(hold)).Nodes != 0 {
		t.Fatal("the transition did not clear the node count")
	}
}

func TestASuspendedJobIsNotIntent(t *testing.T) {
	o := Observation{Jobs: []batchv1.Job{{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly"},
		Spec:       batchv1.JobSpec{Suspend: b(true)},
	}}}
	if Intent(o) {
		t.Fatal("a suspended Job is intent")
	}
	o.Jobs[0].Spec.Suspend = b(false)
	if !Intent(o) {
		t.Fatal("an unsuspended Job is not intent")
	}
}

func TestARunningStatefulSetDecidesOneNode(t *testing.T) {
	for _, r := range []*int32{nil, i32(3)} {
		o := Observation{StatefulSets: []appsv1.StatefulSet{
			{Spec: appsv1.StatefulSetSpec{Replicas: r}},
		}}
		if !Intent(o) {
			t.Fatalf("a StatefulSet with replicas=%v is not intent", r)
		}
	}
	scaled := Observation{StatefulSets: []appsv1.StatefulSet{
		{Spec: appsv1.StatefulSetSpec{Replicas: i32(0)}},
	}}
	if Intent(scaled) {
		t.Fatal("a StatefulSet scaled to 0 is intent")
	}
}

func TestAnAbsentReplicasMeansOneNotZero(t *testing.T) {
	o := Observation{Deployments: []appsv1.Deployment{
		{ObjectMeta: metav1.ObjectMeta{Name: "web"}},
	}}
	if !Intent(o) {
		t.Fatal("a Deployment with spec.replicas ABSENT reported no intent; the API server " +
			"defaults it to 1, so nil is a running Deployment")
	}
	o.Deployments[0].Spec.Replicas = i32(0)
	if Intent(o) {
		t.Fatal("a Deployment explicitly scaled to 0 is intent")
	}
}

func TestADaemonSetIsNeverIntentAtZeroNodesOrOnceUp(t *testing.T) {
	zero := Observation{DaemonSets: []appsv1.DaemonSet{
		{ObjectMeta: metav1.ObjectMeta{Name: "node-exporter"}},
	}}
	if Intent(zero) || Demand(zero) != 0 {
		t.Fatal("a DaemonSet at zero nodes reported intent or demand")
	}
	running := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "node-exporter-x", OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "apps/v1", Kind: KindDaemonSet, Name: "node-exporter", Controller: b(true)}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	up := Observation{
		DaemonSets: []appsv1.DaemonSet{{
			ObjectMeta: metav1.ObjectMeta{Name: "node-exporter"},
			Status:     appsv1.DaemonSetStatus{DesiredNumberScheduled: 1},
		}},
		Pods: []corev1.Pod{running},
	}
	if Intent(up) || Demand(up) != 0 {
		t.Fatal("a DaemonSet with a node to run on, and its Running pod, reported intent or demand; " +
			"its desired count follows the nodes, so counting it pins any cluster that ever had a " +
			"node above zero for ever (plan 0005 M4-SCALE-IN)")
	}
	orphaned := Observation{Pods: []corev1.Pod{running}}
	if Intent(orphaned) {
		t.Fatal("a DaemonSet's pod whose DaemonSet was not in the same read reported intent; the " +
			"pod's controller says it follows the nodes whether or not the DaemonSet was listed")
	}
}

func TestADaemonSetOnlyClusterScalesToZero(t *testing.T) {
	ds := appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "obs", Name: "agent"},
		Status:     appsv1.DaemonSetStatus{DesiredNumberScheduled: 2, CurrentNumberScheduled: 2},
	}
	o := Observation{DaemonSets: []appsv1.DaemonSet{ds}}
	for _, node := range []string{"c-node1", "c-node2"} {
		o.Pods = append(o.Pods, corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "obs", Name: "agent-" + node, OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: KindDaemonSet, Name: "agent", Controller: b(true)}}},
			Spec:   corev1.PodSpec{NodeName: node},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		})
	}
	o.Placement = Loads([]corev1.Node{node("c-node1", 0), node("c-node2", time.Second)}, o, nil, Baseline{})
	st := State{Nodes: 2}
	d := Decide(o, st, at(0), Options{})
	if d.Intent || d.Action != None {
		t.Fatalf("a cluster holding only a DaemonSet decided %s with intent %v at first sight, want "+
			"None without intent: the hold starts", d.Action, d.Intent)
	}
	st = st.Next(d, at(0))
	d = Decide(o, st, at(DefaultHold), Options{})
	if d.Action != Teardown || d.Nodes != 0 {
		t.Fatalf("a cluster holding only a DaemonSet decided %s/%d past the hold, want Teardown/0: "+
			"%s", d.Action, d.Nodes, d.Reason)
	}
}

func TestTheObservationRangesOverExactlyThePredicatesKinds(t *testing.T) {
	t.Run("no-field-for-an-excluded-kind", func(t *testing.T) {
		want := []string{"DaemonSets", "Deployments", "Jobs", "Placement", "Pods", "StatefulSets",
			"UnreconciledCustomResources"}
		var got []string
		rt := reflect.TypeOf(Observation{})
		for i := 0; i < rt.NumField(); i++ {
			got = append(got, rt.Field(i).Name)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Observation ranges over %v, want %v -- a Service, CronJob, HPA or "+
				"ReplicaSet field is excluded by ADR 0014 and cannot be added here; Placement is the "+
				"per-node load scale-in reads (plan 0005 M4-SCALE-IN) and UnreconciledCustomResources "+
				"the CRs an in-cluster operator of ours has not reconciled (M4-PENDING-CRS)", got, want)
		}
	})
}

func TestABurstOfWritesCollapsesIntoOneProvision(t *testing.T) {
	o := Observation{Pods: []corev1.Pod{pod(corev1.PodPending)}}
	st := State{}
	provisions := 0
	for i := 0; i < 16; i++ {
		d := Decide(o, st, at(time.Duration(i)*time.Millisecond), Options{})
		if d.Action == Provision {
			provisions++
		}
		st = st.Next(d, at(time.Duration(i)*time.Millisecond))
	}
	if provisions != 1 {
		t.Fatalf("16 intent writes during one in-flight provision produced %d provisions; "+
			"ADR 0014 requires exactly 1", provisions)
	}
	if st.Nodes != 1 {
		t.Fatalf("state holds %d nodes", st.Nodes)
	}
}

func TestTheHoldDoesNotDelayScaleUp(t *testing.T) {
	o := Observation{Pods: []corev1.Pod{pod(corev1.PodPending)}}
	st := State{Nodes: 0, IdleSince: at(0)}
	d := Decide(o, st, at(time.Second), Options{Hold: 2 * time.Minute})
	if d.Action != Provision {
		t.Fatalf("decided %s: intent arriving mid-hold must provision at once", d.Action)
	}
}

func TestTheIdleClockStartsAtTheTransitionNotAtThePass(t *testing.T) {
	st := State{Nodes: 1}
	if st = st.Next(Decide(Observation{Pods: []corev1.Pod{pod(corev1.PodRunning)}}, st, at(0), Options{}), at(0)); !st.IdleSince.IsZero() {
		t.Fatalf("a pass with intent started an idle clock: %s", st.IdleSince)
	}
	absent := Observation{}
	d := Decide(absent, st, at(30*time.Second), Options{})
	st = st.Next(d, at(30*time.Second))
	if !st.IdleSince.Equal(at(30 * time.Second)) {
		t.Fatalf("idle clock started at %s, want the transition at 30s", st.IdleSince)
	}
	st = st.Next(Decide(absent, st, at(90*time.Second), Options{}), at(90*time.Second))
	if !st.IdleSince.Equal(at(30 * time.Second)) {
		t.Fatalf("a later pass moved the idle clock to %s", st.IdleSince)
	}
}

func TestNoIntentAndNoNodeStaysAtZero(t *testing.T) {
	st := State{}
	for i := 0; i < 10; i++ {
		now := at(time.Duration(i) * time.Minute)
		st = st.Next(Decide(Observation{}, st, now, Options{}), now)
	}
	if st.Nodes != 0 || !st.IdleSince.IsZero() {
		t.Fatalf("an idle empty cluster accumulated state: %+v", st)
	}
}

func TestTheHoldKnobResolvesTheWayItIsDocumented(t *testing.T) {
	st := State{Nodes: 1, IdleSince: at(0)}

	if d := Decide(Observation{}, st, at(time.Second), Options{Hold: -1}); d.Action != Teardown {
		t.Fatalf("a negative hold did not tear down at once: %s", d.Action)
	}
	if got := (Options{}).hold(); got != DefaultHold {
		t.Fatalf("an unset hold resolved to %s, want the %s default -- zero must not mean "+
			"'no grace'", got, DefaultHold)
	}
	if d := Decide(Observation{}, st, at(time.Second), Options{}); d.Action != None {
		t.Fatalf("an unset hold tore down after one second: %s", d.Action)
	}
}

func TestTheNarrowTriggerCannotScaleToZero(t *testing.T) {
	cases := []struct {
		name string
		o    Observation
	}{
		{
			"a Deployment whose first pod does not exist yet",
			Observation{Deployments: []appsv1.Deployment{{Spec: appsv1.DeploymentSpec{Replicas: i32(1)}}}},
		},
		{
			"a pod left Unknown by a departed node",
			Observation{Pods: []corev1.Pod{pod(corev1.PodUnknown)}},
		},
	}
	for _, tc := range cases {
		if !Intent(tc.o) {
			t.Errorf("%s: the implemented predicate says no intent, and it must say intent", tc.name)
		}
		if narrowFires(tc.o) {
			t.Errorf("%s: the narrow trigger fired, so this is not a disagreement and the "+
				"case cannot support the report's claim", tc.name)
		}
	}

	idle := Observation{}
	if Intent(idle) || narrowFires(idle) {
		t.Fatal("neither predicate may fire on an empty cluster")
	}
}

func narrowFires(o Observation) bool {
	for i := range o.Pods {
		for _, c := range o.Pods[i].Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
				return true
			}
		}
	}
	return false
}

type script struct {
	at     time.Duration
	intent bool
}

type tally struct {
	provisions int
	teardowns  int
	idle       time.Duration
	up         time.Duration
}

func drive(scripts []script, hold time.Duration, until time.Duration) tally {
	var tl tally
	st := State{}
	next := 0
	for now := time.Duration(0); now <= until; now += time.Second {
		for next < len(scripts) && scripts[next].at <= now {
			next++
		}
		intent := next > 0 && scripts[next-1].intent
		o := Observation{}
		if intent {
			o.Pods = []corev1.Pod{pod(corev1.PodPending)}
		}
		d := Decide(o, st, at(now), Options{Hold: hold})
		switch d.Action {
		case Provision:
			tl.provisions++
		case Teardown:
			tl.teardowns++
		}
		if st.Nodes > 0 && !intent {
			tl.idle += time.Second
		}
		if st.Nodes > 0 {
			tl.up += time.Second
		}
		st = st.Next(d, at(now))
	}
	return tl
}

func TestARapidApplyDeleteDoesNotFlap(t *testing.T) {
	const until = 10 * time.Minute
	cycle := 35 * time.Second
	var scripts []script
	for i := 0; i < 3; i++ {
		base := time.Duration(i) * cycle
		scripts = append(scripts,
			script{at: base, intent: true},
			script{at: base + 20*time.Second, intent: false},
		)
	}

	holds := []time.Duration{-1, 5 * time.Second, 15 * time.Second,
		60 * time.Second, DefaultHold, 10 * time.Minute}

	t.Logf("3x (20s Job, 15s gap); one node-day = 86400s; one cold start = %s", coldStart)
	t.Logf("%-10s %-11s %-10s %-11s %s", "hold", "provisions", "teardowns", "idle", "verdict")
	for _, hold := range holds {
		tl := drive(scripts, hold, until)
		name := hold.String()
		if hold < 0 {
			name = "none"
		} else if hold == DefaultHold {
			name = DefaultHold.String() + "*"
		}
		verdict := "no flap"
		if tl.provisions > 1 || tl.teardowns > 1 {
			verdict = fmt.Sprintf("FLAPS: %d wasted cold starts", tl.provisions-1)
		}
		t.Logf("%-10s %-11d %-10d %-11s %s", name, tl.provisions, tl.teardowns, tl.idle, verdict)
	}

	raw := drive(scripts, -1, until)
	if raw.provisions != 3 || raw.teardowns != 3 {
		t.Fatalf("with no hold the loop did %d provisions / %d teardowns, want 3/3 -- this "+
			"test's whole point is that the un-debounced trigger flaps, so if it stopped "+
			"flapping the comparison below is meaningless", raw.provisions, raw.teardowns)
	}

	dflt := drive(scripts, DefaultHold, until)
	if dflt.provisions != 1 || dflt.teardowns != 1 {
		t.Fatalf("at the %s default the loop did %d provisions / %d teardowns, want 1/1",
			DefaultHold, dflt.provisions, dflt.teardowns)
	}

	if short := drive(scripts, 5*time.Second, until); short.provisions != 3 || short.teardowns != 3 {
		t.Fatalf("a 5s hold did %d/%d for a 15s gap, want 3/3: the grace has to be longer "+
			"than the gap to collapse anything", short.provisions, short.teardowns)
	}
	if long := drive(scripts, 60*time.Second, until); long.provisions != 1 || long.teardowns != 1 {
		t.Fatalf("a 60s hold did %d/%d, want 1/1: a hold longer than the gap must collapse it",
			long.provisions, long.teardowns)
	}
}

func TestTheHoldPaysForItself(t *testing.T) {
	alwaysOnBeatsTrigger := func(hold time.Duration) bool {
		return float64(24*time.Hour)/float64(hold) < 100
	}

	if alwaysOnBeatsTrigger(DefaultHold) {
		t.Fatalf("at a %s hold an always-on node costs less than the trigger: the research "+
			"plan's kill criterion has FIRED and the trigger is wrong", DefaultHold)
	}

	flapsToPay := float64(DefaultHold) / float64(coldStart)
	if flapsToPay < 2 {
		t.Fatalf("the %s hold only pays for itself if it prevents %.1f flaps, which is not "+
			"a margin: a single flap must cost more than the whole grace", DefaultHold, flapsToPay)
	}
	t.Logf("%s hold pays for itself by preventing %.0f flaps (%s / %s cold start)",
		DefaultHold, flapsToPay, DefaultHold, coldStart)
	t.Logf("the trigger beats an always-on node until %.0f idle events per day; at %s that "+
		"is %.2f%% of a node-day per idle event",
		float64(24*time.Hour)/float64(DefaultHold), DefaultHold,
		100*float64(DefaultHold)/float64(24*time.Hour))

	if !alwaysOnBeatsTrigger(24 * time.Hour) {
		t.Fatal("the kill criterion does not fire on a 24h hold, so it cannot fire on " +
			"anything and its passing above means nothing")
	}
}

type spy struct {
	provisions int
	teardowns  int
}

func (s *spy) Provision(context.Context) error { s.provisions++; return nil }
func (s *spy) Teardown(context.Context) error  { s.teardowns++; return nil }

func TestTheSeamIsWhatTheDecisionNeeds(t *testing.T) {
	var n Nodes = &spy{}
	o := Observation{Pods: []corev1.Pod{pod(corev1.PodPending)}}
	st := State{}
	d := Decide(o, st, t0, Options{})
	if d.Action != Provision {
		t.Fatalf("decided %s", d.Action)
	}
	if err := n.Provision(context.Background()); err != nil {
		t.Fatal(err)
	}

	st = State{Nodes: 1, IdleSince: at(0)}
	d = Decide(Observation{}, st, at(DefaultHold), Options{})
	if d.Action != Teardown {
		t.Fatalf("decided %s", d.Action)
	}
	if err := n.Teardown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sp := n.(*spy); sp.provisions != 1 || sp.teardowns != 1 {
		t.Fatalf("seam saw %d provisions / %d teardowns", sp.provisions, sp.teardowns)
	}
}

func pods(n int) Observation {
	o := Observation{Pods: make([]corev1.Pod, n)}
	for i := range o.Pods {
		o.Pods[i] = pod(corev1.PodPending)
	}
	return o
}

func TestDemandAgreesWithIntentOnEveryIntentFixture(t *testing.T) {
	ownedPod := pod(corev1.PodPending)
	StampAppliedByTheControlPlane(&ownedPod.ObjectMeta)

	ownedDeployment := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "coredns"},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(3)},
	}
	StampAppliedByTheControlPlane(&ownedDeployment.ObjectMeta)

	done := metav1.NewTime(at(-time.Minute))

	cases := []struct {
		name string
		o    Observation
		want bool
	}{
		{"an empty cluster", Observation{}, false},
		{"a Pending pod", Observation{Pods: []corev1.Pod{pod(corev1.PodPending)}}, true},
		{"a Running pod", Observation{Pods: []corev1.Pod{pod(corev1.PodRunning)}}, true},
		{"a pod left Unknown by a departed node", Observation{Pods: []corev1.Pod{pod(corev1.PodUnknown)}}, true},
		{"a Succeeded pod", Observation{Pods: []corev1.Pod{pod(corev1.PodSucceeded)}}, false},
		{"a Failed pod", Observation{Pods: []corev1.Pod{pod(corev1.PodFailed)}}, false},
		{"a control-plane-owned Pending pod", Observation{Pods: []corev1.Pod{ownedPod}}, false},
		{"a Deployment with replicas=1", Observation{Deployments: []appsv1.Deployment{
			{ObjectMeta: metav1.ObjectMeta{Name: "web"}, Spec: appsv1.DeploymentSpec{Replicas: i32(1)}}}}, true},
		{"a Deployment with replicas absent", Observation{Deployments: []appsv1.Deployment{
			{ObjectMeta: metav1.ObjectMeta{Name: "web"}}}}, true},
		{"a Deployment scaled to 0", Observation{Deployments: []appsv1.Deployment{
			{Spec: appsv1.DeploymentSpec{Replicas: i32(0)}}}}, false},
		{"a control-plane-owned Deployment", Observation{Deployments: []appsv1.Deployment{ownedDeployment}}, false},
		{"a StatefulSet with replicas absent", Observation{StatefulSets: []appsv1.StatefulSet{
			{Spec: appsv1.StatefulSetSpec{}}}}, true},
		{"a StatefulSet with replicas=3", Observation{StatefulSets: []appsv1.StatefulSet{
			{Spec: appsv1.StatefulSetSpec{Replicas: i32(3)}}}}, true},
		{"a StatefulSet scaled to 0", Observation{StatefulSets: []appsv1.StatefulSet{
			{Spec: appsv1.StatefulSetSpec{Replicas: i32(0)}}}}, false},
		{"an unsuspended Job", Observation{Jobs: []batchv1.Job{{
			ObjectMeta: metav1.ObjectMeta{Name: "nightly"},
			Spec:       batchv1.JobSpec{Suspend: b(false)}}}}, true},
		{"a suspended Job", Observation{Jobs: []batchv1.Job{{
			ObjectMeta: metav1.ObjectMeta{Name: "nightly"},
			Spec:       batchv1.JobSpec{Suspend: b(true)}}}}, false},
		{"a completed Job", Observation{
			Jobs: []batchv1.Job{{
				ObjectMeta: metav1.ObjectMeta{Name: "migrate"},
				Status:     batchv1.JobStatus{CompletionTime: &done, Succeeded: 1},
			}},
			Pods: []corev1.Pod{pod(corev1.PodSucceeded)}}, false},
		{"a DaemonSet with no node to match", Observation{DaemonSets: []appsv1.DaemonSet{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-exporter"}}}}, false},
		{"a DaemonSet with a node to run on", Observation{DaemonSets: []appsv1.DaemonSet{{
			ObjectMeta: metav1.ObjectMeta{Name: "node-exporter"},
			Status:     appsv1.DaemonSetStatus{DesiredNumberScheduled: 1}}}}, false},
		{"a CR an in-cluster operator of ours has not reconciled", Observation{
			UnreconciledCustomResources: []CustomResource{{Group: "example.com", Kind: "Widget", Name: "w"}}}, true},
	}
	for _, tc := range cases {
		if got := Intent(tc.o); got != tc.want {
			t.Fatalf("%s: Intent = %v, want %v", tc.name, got, tc.want)
		}
		if got := Demand(tc.o) > 0; got != tc.want {
			t.Fatalf("%s: Demand>0 = %v and Intent = %v; the two definitions of \"is there work\" "+
				"disagree, and a bug in the count must not be able to make zero unreachable or to "+
				"leave intent without a demand", tc.name, got, tc.want)
		}
	}
}

func TestTheDemandCountsTheWorkNotTheNodes(t *testing.T) {
	if got := Demand(pods(17)); got != 17 {
		t.Fatalf("seventeen Pending pods demanded %d, want 17: Demand is the pod work, and Target "+
			"is what divides it into nodes", got)
	}
	o := Observation{Deployments: []appsv1.Deployment{
		{Spec: appsv1.DeploymentSpec{Replicas: i32(4)}},
	}}
	if got := Demand(o); got != 4 {
		t.Fatalf("a Deployment of four replicas with no pods yet demanded %d, want 4", got)
	}
}

func TestTargetReturnsThePlanAcceptance(t *testing.T) {
	cases := []struct {
		name string
		o    Observation
		opts Options
		want int
	}{
		{"no work is no target", Observation{}, Options{}, 0},
		{"one pod, eight per node", pods(1), Options{PodsPerNode: 8}, 1},
		{"seventeen pods, eight per node", pods(17), Options{PodsPerNode: 8, MaxNodes: 3}, 3},
		{"a hundred pods at a ceiling of four", pods(100), Options{MaxNodes: 4}, 4},
		{"seventeen pods at a ceiling of two", pods(17), Options{MaxNodes: 2}, 2},
		{"seventeen pods with no ceiling named still fit under the default", pods(17), Options{}, 3},
		{"a hundred pods with no ceiling named stop at the default", pods(100), Options{}, 10},
		{"a Succeeded pod is no target", Observation{Pods: []corev1.Pod{pod(corev1.PodSucceeded)}}, Options{}, 0},
	}
	for _, tc := range cases {
		if got := Target(tc.o, tc.opts); got != tc.want {
			t.Fatalf("%s: Target = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestDecideAtTheDefaultCeilingReproducesToday(t *testing.T) {
	intent := Observation{Deployments: []appsv1.Deployment{
		{ObjectMeta: metav1.ObjectMeta{Name: "web"}, Spec: appsv1.DeploymentSpec{Replicas: i32(1)}},
	}}
	quiet := Observation{}
	cases := []struct {
		name   string
		o      Observation
		st     State
		now    time.Time
		action Action
		nodes  int
	}{
		{"empty and no node", quiet, State{}, t0, None, 0},
		{"intent and no node", intent, State{}, t0, Provision, 1},
		{"intent and a node", intent, State{Nodes: 1}, t0, None, 1},
		{"no intent and no node", quiet, State{}, t0, None, 0},
		{"no intent and a node inside the hold", quiet, State{Nodes: 1, IdleSince: at(0)}, at(time.Minute), None, 1},
		{"no intent and a node past the hold", quiet, State{Nodes: 1, IdleSince: at(0)}, at(DefaultHold), Teardown, 0},
	}
	for _, tc := range cases {
		d := Decide(tc.o, tc.st, tc.now, Options{})
		if d.Action != tc.action || d.Nodes != tc.nodes || d.Intent != Intent(tc.o) {
			t.Fatalf("%s: Decide = %s/%d, want %s/%d -- at the default ceiling the decision must "+
				"reproduce the one-node behaviour byte for byte", tc.name, d.Action, d.Nodes, tc.action, tc.nodes)
		}
	}
	if got := Decide(intent, State{}, t0, Options{}).Reason; got != "intent and no node" {
		t.Fatalf("the provision reason changed at the default ceiling: %q", got)
	}
	steady := "intent, and a node is already counted for this cluster; a second provision here is " +
		"a second node (ADR 0014: idempotency is per-cluster)"
	if got := Decide(intent, State{Nodes: 1}, t0, Options{}).Reason; got != steady {
		t.Fatalf("the steady-state reason changed at the default ceiling: %q", got)
	}
}

func TestTheDecisionScalesOutToTheTargetAndReleasesNothingItCannotSee(t *testing.T) {
	o := pods(17)
	opts := Options{MaxNodes: 3, PodsPerNode: 8}

	if d := Decide(o, State{}, t0, opts); d.Action != Provision || d.Nodes != 3 {
		t.Fatalf("from zero with seventeen pods the decision was %s/%d, want Provision/3", d.Action, d.Nodes)
	}
	if d := Decide(o, State{Nodes: 1}, t0, opts); d.Action != Provision || d.Nodes != 3 {
		t.Fatalf("from one node with seventeen pods the decision was %s/%d, want Provision/3: the "+
			"target is a total, and the driver reconciles the re-read count up to it", d.Action, d.Nodes)
	}
	if d := Decide(o, State{Nodes: 3}, t0, opts); d.Action != None || d.Nodes != 3 {
		t.Fatalf("at the target the decision was %s/%d, want None/3", d.Action, d.Nodes)
	}
	if d := Decide(o, State{Nodes: 4}, t0, opts); d.Action != None || d.Nodes != 4 {
		t.Fatalf("above the target the decision was %s/%d, want None/4: no node's load was "+
			"observed, so no node has been seen idle for the hold and none is released", d.Action, d.Nodes)
	}
}

func TestATransientlyInflatedObservationIsBoundedByTheCeiling(t *testing.T) {
	opts := Options{MaxNodes: 4, PodsPerNode: 8}
	if d := Decide(pods(100), State{}, t0, opts); d.Nodes != 4 {
		t.Fatalf("a hundred pods at a ceiling of four decided %d nodes", d.Nodes)
	}
	if d := Decide(pods(1), State{Nodes: 4}, t0, opts); d.Action != None || d.Nodes != 4 {
		t.Fatalf("after the inflation passed the decision was %s/%d, want None/4: the target "+
			"falling while intent remains releases only a node seen idle for the whole hold, and "+
			"none was seen", d.Action, d.Nodes)
	}
}
