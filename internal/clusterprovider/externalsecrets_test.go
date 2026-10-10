package clusterprovider

import (
	"errors"
	"slices"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

func besideStack() Stack {
	return append(append(Stack{}, DesignedStack...), ExternalSecretsComponents...)
}

func besideLeases() materialiser.Leases {
	l := designedLeases(21003)
	for i, p := range ExternalSecretsPurposes {
		l[p] = portallocator.Lease{Port: 21020 + i, Width: 1, Cluster: "liveweb", Purpose: p}
	}
	return l
}

func besideConfig(t *testing.T) StackConfig {
	t.Helper()
	cfg := stackConfig(t, fakeAdmin())
	cfg.Stack = besideStack()
	cfg.VaultStoreDSN = func(string) (string, error) { return fakeVaultStoreDSN(), nil }
	cfg.VaultTLS = testVaultTLS()
	cfg.ExternalSecrets = &ExternalSecrets{Binary: "/usr/local/bin/external-secrets", Bundle: "/opt/socialweb/eso/external-secrets.yaml"}
	return cfg
}

func TestTheExternalSecretsPairIsLeasedLikeEveryOtherComponent(t *testing.T) {
	purposes, err := PurposesFor(besideStack(), true)
	if err != nil {
		t.Fatalf("PurposesFor: %v", err)
	}
	for _, p := range ExternalSecretsPurposes {
		if !slices.Contains(purposes, p) {
			t.Fatalf("a stack running the External Secrets pair leases %v, missing %s", purposes, p)
		}
		if p.Width() != 1 {
			t.Fatalf("%s has width %d", p, p.Width())
		}
	}
}

func TestTheWebhookStepComesFirstInstallsTheTenantHalfAndBothReadTheMintedKubeconfig(t *testing.T) {
	cfg := besideConfig(t)
	steps, err := StepsFor(cfg)(observed(), besideLeases())
	if err != nil {
		t.Fatalf("StepsFor: %v", err)
	}
	got := names(steps)
	w := slices.Index(got, string(procgroup.ComponentExternalSecretsWebhook))
	c := slices.Index(got, string(procgroup.ComponentExternalSecrets))
	k := slices.Index(got, string(procgroup.ComponentK3s))
	if w < 0 || c < 0 || !(k < w && w < c) {
		t.Fatalf("step order %v: the webhook step installs the CRDs the controller's informers "+
			"need, so it must follow k3s and precede the controller", got)
	}
	if steps[w].Prepare == nil {
		t.Fatal("the webhook step has no Prepare, so nothing mints its credential, its serving pair " +
			"or installs the CRDs before the pair starts")
	}
	dataDir, err := ClusterDataDir(cfg.StateDir, "liveweb")
	if err != nil {
		t.Fatal(err)
	}
	want := "KUBECONFIG=" + clusterstack.ExternalSecretsKubeconfig(dataDir)
	for _, i := range []int{w, c} {
		if !slices.Equal(steps[i].Spec.Env, []string{want}) {
			t.Fatalf("%s env is %v, want exactly %q: the pair inherits nothing from this process, "+
				"least of all a KUBECONFIG naming the admin credential", steps[i].Name, steps[i].Spec.Env, want)
		}
		if steps[i].Ready == nil {
			t.Fatalf("%s has no readiness gate", steps[i].Name)
		}
	}
	if !has(steps[c].Spec.Argv, "--live-addr=127.0.0.1:21020") {
		t.Fatalf("the controller argv %v does not bind its leased health port", steps[c].Spec.Argv)
	}
	if !has(steps[w].Spec.Argv, "--port=21021") || !has(steps[w].Spec.Argv, "--healthz-addr=127.0.0.1:21022") {
		t.Fatalf("the webhook argv %v does not bind its leased ports", steps[w].Spec.Argv)
	}
}

func TestAStackRunningThePairWithNoBinaryOrBundleIsRefused(t *testing.T) {
	cfg := besideConfig(t)
	cfg.ExternalSecrets = nil
	if _, err := StepsFor(cfg)(observed(), besideLeases()); !errors.Is(err, ErrNoExternalSecrets) {
		t.Fatalf("a stack running the External Secrets pair with no config built: %v", err)
	}
	cfg = besideConfig(t)
	cfg.ExternalSecrets.Bundle = ""
	if _, err := StepsFor(cfg)(observed(), besideLeases()); !errors.Is(err, ErrNoExternalSecrets) {
		t.Fatalf("a stack running the External Secrets pair with no bundle built: %v", err)
	}
}

func TestAClusterLeasedBeforeThePairExistedRestartsWithoutItUntilItsNextSpawnLeasesIt(t *testing.T) {
	cfg := besideConfig(t)
	old := designedLeases(21003)
	trimmed := cfg.WithoutUnleased(old)
	for _, c := range ExternalSecretsComponents {
		if trimmed.Stack.Has(c) {
			t.Fatalf("the restart reap derives argvs for %s over leases that hold none of its ports, "+
				"which refuses the restart of every cluster created before the pair", c)
		}
	}
	if !trimmed.Stack.Has(procgroup.ComponentK3s) {
		t.Fatal("trimming the pair took k3s with it")
	}
	if _, err := StepsFor(trimmed)(observed(), old); err != nil {
		t.Fatalf("the trimmed stack does not build over the old leases: %v", err)
	}
	if full := cfg.WithoutUnleased(besideLeases()); !full.Stack.Has(procgroup.ComponentExternalSecrets) {
		t.Fatal("a cluster that holds the pair's leases had the pair trimmed")
	}
}

func TestTheControllerGateWaitsForTheExternalSecretWorkersNotAnyController(t *testing.T) {
	pushOnly := `{"level":"info","msg":"Starting workers","controller":"pushsecret","worker count":1}`
	if externalSecretsWorkersStarted(pushOnly) {
		t.Fatal("the pushsecret controller's workers opened the gate")
	}
	measured := `{"level":"info","ts":"2026-10-07T12:36:18.407-0700","msg":"Starting workers","controller":"externalsecret","controllerGroup":"external-secrets.io","controllerKind":"ExternalSecret","worker count":1}`
	if !externalSecretsWorkersStarted(pushOnly + "\n" + measured + "\n") {
		t.Fatal("the line the v2.11.0 controller logs once its ExternalSecret informer synced did not open the gate")
	}
}
