//go:build linux

package providermain

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

func TestTheProcessGroupIsHandedTheExternalSecretsBinaryBundleAndTheOldInstallNamespace(t *testing.T) {
	cfg, err := loadJoin(t, map[string]string{"SOCIALWEB_CLUSTERPROVIDER_ESO_NAMESPACE": "external-secrets"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	es := externalSecretsFor(cfg)
	if es.Binary != cfg.ExternalSecrets || es.Binary == "" {
		t.Fatalf("the pair's binary is %q, want the configured %q", es.Binary, cfg.ExternalSecrets)
	}
	if es.Bundle != cfg.ESOBundle || es.Bundle == "" {
		t.Fatalf("the pair's bundle is %q, want the configured %q", es.Bundle, cfg.ESOBundle)
	}
	if es.LegacyNamespace != "external-secrets" {
		t.Fatalf("the migration deletes the in-tenant install from %q, want the namespace the old "+
			"install was configured into", es.LegacyNamespace)
	}
}

func TestTheBaselineIsReMeasuredEmptyOnceTheOperatorLeavesTheTenant(t *testing.T) {
	dataDir := t.TempDir()
	old := scale.Baseline{Manager: clusterprovider.AddOnFieldManager, MeasuredAt: time.Now().Add(-time.Hour)}
	for _, name := range []string{"external-secrets", "external-secrets-cert-controller", "external-secrets-webhook"} {
		old.Workloads = append(old.Workloads, scale.BaselineEntry{
			WorkloadKey: scale.WorkloadKey{Kind: scale.KindDeployment, Namespace: "default", Name: name},
			SpecHash:    "sha256:before",
		})
	}
	if err := clusterprovider.WriteAddOnBaseline(dataDir, old); err != nil {
		t.Fatal(err)
	}
	if err := measureTenantBaseline(context.Background(), fake.NewSimpleClientset(), dataDir, "c", testLogger()); err != nil {
		t.Fatalf("measureTenantBaseline: %v", err)
	}
	b, measured, err := clusterprovider.ReadAddOnBaseline(dataDir)
	if err != nil || !measured {
		t.Fatalf("the baseline was not written: measured=%t %v", measured, err)
	}
	if len(b.Workloads) != 0 {
		var names []string
		for _, w := range b.Workloads {
			names = append(names, w.WorkloadKey.String())
		}
		t.Fatalf("the baseline still lists %s after the migration took the Deployments out of the "+
			"tenant: the add-on pass must re-measure, not keep the file it found", strings.Join(names, ", "))
	}
}
