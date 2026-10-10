package clusterprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

const AddOnFieldManager = "socialweb-addons"

const AddOnBaselineName = "addon-baseline.json"

const addOnBaselineMode = 0o600

func AddOnBaselinePath(dataDir string) string {
	return filepath.Join(dataDir, AddOnBaselineName)
}

func ReadAddOnBaseline(dataDir string) (scale.Baseline, bool, error) {
	path := AddOnBaselinePath(dataDir)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return scale.Baseline{}, false, nil
	}
	if err != nil {
		return scale.Baseline{}, false, fmt.Errorf("clusterprovider: reading the add-on baseline at %s: %w", path, err)
	}
	var b scale.Baseline
	if err := json.Unmarshal(raw, &b); err != nil {
		return scale.Baseline{}, false, fmt.Errorf("clusterprovider: the add-on baseline at %s is not JSON: %v", path, err)
	}
	return b, true, nil
}

func WriteAddOnBaseline(dataDir string, b scale.Baseline) error {
	path := AddOnBaselinePath(dataDir)
	body, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("clusterprovider: encoding the add-on baseline for %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(dataDir, AddOnBaselineName+".")
	if err != nil {
		return fmt.Errorf("clusterprovider: a temporary file beside %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(addOnBaselineMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("clusterprovider: the mode of %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("clusterprovider: writing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("clusterprovider: closing %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("clusterprovider: renaming %s over %s: %w", tmp.Name(), path, err)
	}
	return nil
}

func appliedBy(meta metav1.ObjectMeta, manager string) bool {
	for _, f := range meta.ManagedFields {
		if f.Manager == manager && f.Operation == metav1.ManagedFieldsOperationApply {
			return true
		}
	}
	return false
}

func MeasureAddOnBaseline(ctx context.Context, cs kubernetes.Interface, manager string, now time.Time) (scale.Baseline, error) {
	b := scale.Baseline{Manager: manager, MeasuredAt: now.UTC()}
	add := func(kind string, meta metav1.ObjectMeta, spec any) error {
		if !appliedBy(meta, manager) {
			return nil
		}
		h, err := scale.SpecHash(spec)
		if err != nil {
			return err
		}
		b.Workloads = append(b.Workloads, scale.BaselineEntry{
			WorkloadKey: scale.WorkloadKey{Kind: kind, Namespace: meta.Namespace, Name: meta.Name},
			SpecHash:    h,
		})
		return nil
	}
	deployments, err := cs.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return scale.Baseline{}, fmt.Errorf("clusterprovider: listing deployments to measure the add-on baseline: %w", err)
	}
	for i := range deployments.Items {
		if err := add(scale.KindDeployment, deployments.Items[i].ObjectMeta, deployments.Items[i].Spec); err != nil {
			return scale.Baseline{}, err
		}
	}
	statefulsets, err := cs.AppsV1().StatefulSets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return scale.Baseline{}, fmt.Errorf("clusterprovider: listing statefulsets to measure the add-on baseline: %w", err)
	}
	for i := range statefulsets.Items {
		if err := add(scale.KindStatefulSet, statefulsets.Items[i].ObjectMeta, statefulsets.Items[i].Spec); err != nil {
			return scale.Baseline{}, err
		}
	}
	daemonsets, err := cs.AppsV1().DaemonSets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return scale.Baseline{}, fmt.Errorf("clusterprovider: listing daemonsets to measure the add-on baseline: %w", err)
	}
	for i := range daemonsets.Items {
		if err := add(scale.KindDaemonSet, daemonsets.Items[i].ObjectMeta, daemonsets.Items[i].Spec); err != nil {
			return scale.Baseline{}, err
		}
	}
	jobs, err := cs.BatchV1().Jobs(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return scale.Baseline{}, fmt.Errorf("clusterprovider: listing jobs to measure the add-on baseline: %w", err)
	}
	for i := range jobs.Items {
		if err := add(scale.KindJob, jobs.Items[i].ObjectMeta, jobs.Items[i].Spec); err != nil {
			return scale.Baseline{}, err
		}
	}
	sort.Slice(b.Workloads, func(i, j int) bool {
		return b.Workloads[i].WorkloadKey.String() < b.Workloads[j].WorkloadKey.String()
	})
	return b, nil
}
