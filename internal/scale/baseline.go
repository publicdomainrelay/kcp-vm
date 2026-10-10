package scale

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	KindDeployment  = "Deployment"
	KindStatefulSet = "StatefulSet"
	KindDaemonSet   = "DaemonSet"
	KindJob         = "Job"
	KindReplicaSet  = "ReplicaSet"
)

type WorkloadKey struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

func (k WorkloadKey) String() string {
	return k.Kind + "/" + k.Namespace + "/" + k.Name
}

type BaselineEntry struct {
	WorkloadKey
	SpecHash string `json:"specHash"`
}

type Baseline struct {
	Manager    string          `json:"manager"`
	MeasuredAt time.Time       `json:"measuredAt"`
	Workloads  []BaselineEntry `json:"workloads"`
}

func SpecHash(spec any) (string, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("scale: encoding a workload spec to hash it: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (b Baseline) Holds(k WorkloadKey, spec any) bool {
	for _, e := range b.Workloads {
		if e.WorkloadKey != k {
			continue
		}
		h, err := SpecHash(spec)
		return err == nil && h == e.SpecHash
	}
	return false
}

func Narrow(o Observation, replicaSets []appsv1.ReplicaSet, b Baseline) Observation {
	present := map[WorkloadKey]bool{}
	var out Observation
	for _, d := range o.Deployments {
		k := WorkloadKey{Kind: KindDeployment, Namespace: d.Namespace, Name: d.Name}
		present[k] = true
		if !b.Holds(k, d.Spec) {
			out.Deployments = append(out.Deployments, d)
		}
	}
	for _, s := range o.StatefulSets {
		k := WorkloadKey{Kind: KindStatefulSet, Namespace: s.Namespace, Name: s.Name}
		present[k] = true
		if !b.Holds(k, s.Spec) {
			out.StatefulSets = append(out.StatefulSets, s)
		}
	}
	for _, d := range o.DaemonSets {
		k := WorkloadKey{Kind: KindDaemonSet, Namespace: d.Namespace, Name: d.Name}
		present[k] = true
		if !b.Holds(k, d.Spec) {
			out.DaemonSets = append(out.DaemonSets, d)
		}
	}
	for _, j := range o.Jobs {
		k := WorkloadKey{Kind: KindJob, Namespace: j.Namespace, Name: j.Name}
		present[k] = true
		if !b.Holds(k, j.Spec) {
			out.Jobs = append(out.Jobs, j)
		}
	}
	deploymentOf := map[WorkloadKey]WorkloadKey{}
	for i := range replicaSets {
		rs := &replicaSets[i]
		if c := metav1.GetControllerOf(rs); c != nil && c.Kind == KindDeployment {
			deploymentOf[WorkloadKey{Kind: KindReplicaSet, Namespace: rs.Namespace, Name: rs.Name}] =
				WorkloadKey{Kind: KindDeployment, Namespace: rs.Namespace, Name: c.Name}
		}
	}
	for _, p := range o.Pods {
		if k, ok := workloadOf(&p, deploymentOf); ok && present[k] {
			continue
		}
		out.Pods = append(out.Pods, p)
	}
	return out
}

func workloadOf(p *corev1.Pod, deploymentOf map[WorkloadKey]WorkloadKey) (WorkloadKey, bool) {
	c := metav1.GetControllerOf(p)
	if c == nil {
		return WorkloadKey{}, false
	}
	k := WorkloadKey{Kind: c.Kind, Namespace: p.Namespace, Name: c.Name}
	if c.Kind == KindReplicaSet {
		d, ok := deploymentOf[k]
		return d, ok
	}
	return k, true
}
