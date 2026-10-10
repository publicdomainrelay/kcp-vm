//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/eso"
)

var tenantAddOnStoreGVR = schema.GroupVersionResource{
	Group: "external-secrets.io", Version: "v1", Resource: "clustersecretstores",
}

var tenantAddOnUnknownStore = &apierrors.StatusError{ErrStatus: metav1.Status{
	Status:  metav1.StatusFailure,
	Code:    404,
	Reason:  metav1.StatusReasonNotFound,
	Message: "the server could not find the requested resource",
}}

func tenantAddOnDynamic(served bool, order *[]string) *fakedynamic.FakeDynamicClient {
	listKinds := map[schema.GroupVersionResource]string{
		tenantAddOnStoreGVR: "ClusterSecretStoreList",
		{Group: "external-secrets.io", Version: "v1", Resource: "externalsecrets"}: "ExternalSecretList",
		{Group: "", Version: "v1", Resource: "secrets"}:                            "SecretList",
	}
	dyn := fakedynamic.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	if !served {
		dyn.PrependReactor("list", "clustersecretstores", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, tenantAddOnUnknownStore
		})
	}
	if order != nil {
		dyn.PrependReactor("patch", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
			*order = append(*order, action.GetResource().Resource)
			return true, &unstructured.Unstructured{}, nil
		})
	}
	return dyn
}

func tenantAddOnOptions() eso.Options {
	return eso.Options{
		ManifestDir:   "../../deploy/eso",
		Namespace:     "users",
		VaultServer:   "https://10.42.7.1:20004",
		ServerCAPEM:   []byte("ca-pem"),
		ClientCertPEM: []byte("cert-pem"),
		ClientKeyPEM:  []byte("key-pem"),
	}
}

func TestTheOperatorIsInstalledBeforeTheStoreIsApplied(t *testing.T) {
	ctx := context.Background()
	var order []string
	served := false
	dyn := tenantAddOnDynamic(true, &order)
	dyn.PrependReactor("list", "clustersecretstores", func(clienttesting.Action) (bool, runtime.Object, error) {
		if served {
			return false, nil, nil
		}
		return true, nil, tenantAddOnUnknownStore
	})
	install := func(context.Context) error {
		order = append(order, "install")
		served = true
		return nil
	}
	if err := applyTenantAddOns(ctx, dyn, install, tenantAddOnOptions()); err != nil {
		t.Fatalf("applyTenantAddOns: %v", err)
	}
	want := []string{"install", "secrets", "clustersecretstores", "externalsecrets"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("the tenant add-on pass did %v, want %v: a store applied to a cluster whose %s "+
			"nothing has installed is the unknown kind this pass was failing on", order, want,
			eso.OperatorGroupVersion)
	}
}

func TestAServedOperatorIsNotInstalledAndTheStoreIsStillApplied(t *testing.T) {
	var order []string
	install := func(context.Context) error {
		order = append(order, "install")
		return nil
	}
	dyn := tenantAddOnDynamic(true, &order)
	if err := applyTenantAddOns(context.Background(), dyn, install, tenantAddOnOptions()); err != nil {
		t.Fatalf("applyTenantAddOns with the operator served: %v", err)
	}
	if strings.Join(order, ",") != "secrets,clustersecretstores,externalsecrets" {
		t.Fatalf("the pass did %v, want the store and nothing else: the operator is served, so "+
			"there is nothing to install", order)
	}
}

func TestTheInstallerIsHandedAKubeconfigForTheTenantCluster(t *testing.T) {
	dir := t.TempDir()
	seen := filepath.Join(dir, "seen-kubeconfig")
	script := filepath.Join(dir, "install.sh")
	body := "#!/usr/bin/env bash\nset -eu\ncp \"$KUBECONFIG\" " + seen + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("writing the installer: %v", err)
	}
	creds := &rest.Config{
		Host: "https://10.100.0.1:6443",
		TLSClientConfig: rest.TLSClientConfig{
			CAData:   []byte("ca-pem"),
			CertData: []byte("cert-pem"),
			KeyData:  []byte("key-pem"),
		},
	}
	if err := installExternalSecrets(context.Background(), creds, dir); err != nil {
		t.Fatalf("installExternalSecrets: %v", err)
	}
	read, err := clientcmd.BuildConfigFromFlags("", seen)
	if err != nil {
		t.Fatalf("the installer was handed no kubeconfig it could read: %v", err)
	}
	if read.Host != creds.Host {
		t.Fatalf("the installer dialled %q, want the tenant cluster %q", read.Host, creds.Host)
	}
	if len(read.TLSClientConfig.CertData) == 0 || len(read.TLSClientConfig.KeyData) == 0 {
		t.Fatalf("the installer was handed a kubeconfig with no client credential, so the CRDs it " +
			"applies would go out anonymous")
	}
}

func TestAFailedInstallerIsReportedWithItsOwnOutput(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "install.sh")
	body := "#!/usr/bin/env bash\nset -eu\necho 'curl: (6) could not resolve host: github.com' >&2\nexit 7\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("writing the installer: %v", err)
	}
	creds := &rest.Config{Host: "https://10.100.0.1:6443", BearerToken: "a-token"}
	err := installExternalSecrets(context.Background(), creds, dir)
	if err == nil {
		t.Fatal("an installer that exited 7 was reported as a success, so the pass would apply the " +
			"store into a cluster with no CRDs")
	}
	if !strings.Contains(err.Error(), "could not resolve host") {
		t.Fatalf("the installer's own output is not in the refusal: %v", err)
	}
}

func TestAFailedInstallLeavesTheStoreUnapplied(t *testing.T) {
	mine := errors.New("curl: (6) could not resolve host: github.com")
	var order []string
	err := applyTenantAddOns(context.Background(), tenantAddOnDynamic(false, &order),
		func(context.Context) error { return mine }, tenantAddOnOptions())
	if !errors.Is(err, mine) {
		t.Fatalf("the install failure is not named: %v", err)
	}
	if !strings.Contains(err.Error(), eso.OperatorGroupVersion) {
		t.Fatalf("the refusal does not name %s: %v", eso.OperatorGroupVersion, err)
	}
	if len(order) != 0 {
		t.Fatalf("the store was applied over a cluster with no operator: %v", order)
	}
}

func TestTheInstallDoesNotWaitForTheOperatorToRoll(t *testing.T) {
	dir := t.TempDir()
	args := filepath.Join(dir, "kubectl-args")
	stdin := filepath.Join(dir, "kubectl-stdin")
	fake := filepath.Join(dir, "kubectl")
	body := "#!/usr/bin/env bash\nset -eu\nprintf '%s\\n' \"$@\" > " + args + "\ncat > " + stdin + "\n"
	if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
		t.Fatalf("writing the fake kubectl: %v", err)
	}
	bundle := filepath.Join(dir, "external-secrets.yaml")
	webhooks := "apiVersion: admissionregistration.k8s.io/v1\n" +
		"kind: ValidatingWebhookConfiguration\n" +
		"metadata:\n  name: external-secrets\n" +
		"webhooks:\n  - name: secretstore\n    failurePolicy: Fail\n"
	if err := os.WriteFile(bundle, []byte(webhooks), 0o600); err != nil {
		t.Fatalf("writing the bundle: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ESO_BUNDLE", bundle)
	creds := &rest.Config{Host: "https://10.100.0.1:6443", BearerToken: "a-token"}
	if err := installExternalSecrets(context.Background(), creds, "../../deploy/eso"); err != nil {
		t.Fatalf("installExternalSecrets: %v", err)
	}
	recorded, err := os.ReadFile(args)
	if err != nil {
		t.Fatalf("the installer ran no kubectl at all: %v", err)
	}
	got := strings.Fields(string(recorded))
	if len(got) == 0 || got[0] != "apply" {
		t.Fatalf("the installer's kubectl was %v, want an apply first", got)
	}
	for _, a := range got {
		if a == "rollout" || strings.Contains(a, "--for=condition") {
			t.Fatalf("the installer waited on %q: a tenant cluster with zero nodes has no pod to "+
				"roll out, so this is the wait the node-scale pass runs after, and the probe is what "+
				"decides", a)
		}
	}
	applied, err := os.ReadFile(stdin)
	if err != nil {
		t.Fatalf("the installer applied nothing on stdin: %v", err)
	}
	if strings.Contains(string(applied), "failurePolicy: Fail") {
		t.Fatalf("the applied bundle still fails its webhooks closed, so on a cluster with no node "+
			"the apiserver would reject the store with no endpoints available: %s", applied)
	}
	if !strings.Contains(string(applied), "failurePolicy: Ignore") {
		t.Fatalf("the applied bundle is not the patched one, so the webhook rewrite was lost: %s", applied)
	}
}

func TestAnInstallThatLeavesTheOperatorUnservedFailsThePassByName(t *testing.T) {
	var order []string
	err := applyTenantAddOns(context.Background(), tenantAddOnDynamic(false, &order),
		func(context.Context) error { return nil }, tenantAddOnOptions())
	if !errors.Is(err, eso.ErrNoOperator) {
		t.Fatalf("an installer that left the operator's CRDs unserved was accepted: %v", err)
	}
	if !strings.Contains(err.Error(), eso.OperatorGroupVersion) {
		t.Fatalf("the refusal does not name %s: %v", eso.OperatorGroupVersion, err)
	}
	if len(order) != 0 {
		t.Fatalf("the store was applied over a cluster with no operator: %v", order)
	}
}
