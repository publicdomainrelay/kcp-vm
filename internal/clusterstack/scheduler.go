package clusterstack

import (
	"errors"
	"fmt"
	"path/filepath"
)

const (
	KubeSchedulerConfigFileName = "kube-scheduler.yaml"

	SchedulerKubeconfigName = "scheduler.kubeconfig"

	MarketSchedulerPodInitialBackoffSeconds = 1
	MarketSchedulerPodMaxBackoffSeconds     = 1
)

var ErrNoSchedulerBackoffForAPlainCluster = errors.New(
	"clusterstack: a kube-scheduler config is rendered for a market cluster only. A market " +
		"cluster is the one that starts from zero nodes, and a pod that was unschedulable while " +
		"it had none is held for kube-scheduler's backoff (1 s doubling to 10 s) after the first " +
		"node arrives. A stack that dials no market has no zero-node window to shorten, and its " +
		"argv names no config file, so a file rendered for it would be read by nobody")

var ErrNoSchedulerDataDir = errors.New(
	"clusterstack: Spec.DataDir must be absolute before a kube-scheduler config can be rendered " +
		"from it. The config names the scheduler's kubeconfig by a path derived from DataDir, and " +
		"k3s reads that path as it stands: a relative one resolves against whichever directory " +
		"the server happened to start in, so the running scheduler would be handed another " +
		"cluster's apiserver")

func SchedulerKubeconfig(dataDir string) string {
	return filepath.Join(CredDir(dataDir), SchedulerKubeconfigName)
}

func KubeSchedulerConfigPath(dataDir string) string {
	return filepath.Join(dataDir, KubeSchedulerConfigFileName)
}

func (s Spec) KubeSchedulerConfig() ([]byte, error) {
	if !s.MarketDialled {
		return nil, ErrNoSchedulerBackoffForAPlainCluster
	}
	if !filepath.IsAbs(s.DataDir) {
		return nil, fmt.Errorf("%w (%q)", ErrNoSchedulerDataDir, s.DataDir)
	}
	return fmt.Appendf(nil, `apiVersion: kubescheduler.config.k8s.io/v1
kind: KubeSchedulerConfiguration
clientConnection:
  kubeconfig: %s
leaderElection:
  leaderElect: false
podInitialBackoffSeconds: %d
podMaxBackoffSeconds: %d
`, SchedulerKubeconfig(s.DataDir), MarketSchedulerPodInitialBackoffSeconds,
		MarketSchedulerPodMaxBackoffSeconds), nil
}

func (s Spec) schedulerArgv() []string {
	if !s.MarketDialled {
		return nil
	}
	return []string{"--kube-scheduler-arg=config=" + KubeSchedulerConfigPath(s.DataDir)}
}
