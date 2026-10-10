package eso

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

const subsetBundle = "testdata/bundle-v2.11.0-subset.yaml"

func loadSubset(t *testing.T) []unstructured.Unstructured {
	t.Helper()
	objs, err := LoadBundle(subsetBundle)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}
	return objs
}

func keyOf(u unstructured.Unstructured) string {
	if ns := u.GetNamespace(); ns != "" {
		return u.GetKind() + " " + ns + "/" + u.GetName()
	}
	return u.GetKind() + " " + u.GetName()
}

func keysOf(objs []unstructured.Unstructured) []string {
	out := make([]string, 0, len(objs))
	for _, u := range objs {
		out = append(out, keyOf(u))
	}
	sort.Strings(out)
	return out
}

func TestTheTenantKeepsOnlyTheCRDsAndTheRolesAndEverythingThatRanThereRetires(t *testing.T) {
	split, err := SplitBundle(loadSubset(t), "external-secrets")
	if err != nil {
		t.Fatalf("SplitBundle: %v", err)
	}
	wantKept := []string{
		"ClusterRole external-secrets-controller",
		"ClusterRole external-secrets-edit",
		"ClusterRole external-secrets-servicebindings",
		"ClusterRole external-secrets-view",
		"CustomResourceDefinition uuids.generators.external-secrets.io",
	}
	if got := keysOf(split.Kept); !slices.Equal(got, wantKept) {
		t.Fatalf("the tenant keeps %v, want %v", got, wantKept)
	}
	wantRetired := []string{
		"ClusterRole external-secrets-cert-controller",
		"ClusterRoleBinding external-secrets-cert-controller",
		"ClusterRoleBinding external-secrets-controller",
		"Deployment external-secrets/external-secrets",
		"Deployment external-secrets/external-secrets-cert-controller",
		"Deployment external-secrets/external-secrets-webhook",
		"Role external-secrets/external-secrets-leaderelection",
		"RoleBinding external-secrets/external-secrets-leaderelection",
		"Secret external-secrets/external-secrets-webhook",
		"Service external-secrets/external-secrets-webhook",
		"ServiceAccount external-secrets/external-secrets",
		"ServiceAccount external-secrets/external-secrets-cert-controller",
		"ServiceAccount external-secrets/external-secrets-webhook",
	}
	if got := keysOf(split.Retired); !slices.Equal(got, wantRetired) {
		t.Fatalf("the migration retires %v, want %v", got, wantRetired)
	}
	wantWebhooks := []string{
		"ValidatingWebhookConfiguration externalsecret-validate",
		"ValidatingWebhookConfiguration secretstore-validate",
	}
	if got := keysOf(split.Webhooks); !slices.Equal(got, wantWebhooks) {
		t.Fatalf("the webhook registrations are %v, want %v", got, wantWebhooks)
	}
}

func TestABundleKindTheSplitDoesNotKnowIsRefused(t *testing.T) {
	objs := loadSubset(t)
	odd := unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "policy/v1", "kind": "PodDisruptionBudget",
		"metadata": map[string]any{"name": "external-secrets-pdb", "namespace": "default"},
	}}
	if _, err := SplitBundle(append(objs, odd), "default"); err == nil ||
		!strings.Contains(err.Error(), "PodDisruptionBudget") {
		t.Fatalf("a bundle carrying a PodDisruptionBudget was split without naming it: %v", err)
	}
}

func TestTheWebhooksAreRegisteredByLoopbackURLUnderTheMintedCA(t *testing.T) {
	split, err := SplitBundle(loadSubset(t), "default")
	if err != nil {
		t.Fatal(err)
	}
	ca := []byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n")
	got, err := WebhookConfigurations(split.Webhooks, "https://127.0.0.1:20031", ca)
	if err != nil {
		t.Fatalf("WebhookConfigurations: %v", err)
	}
	var urls []string
	for _, vwc := range got {
		hooks, _, _ := unstructured.NestedSlice(vwc.Object, "webhooks")
		if len(hooks) == 0 {
			t.Fatalf("%s carries no webhooks", vwc.GetName())
		}
		for _, h := range hooks {
			hook := h.(map[string]any)
			cc := hook["clientConfig"].(map[string]any)
			if _, ok := cc["service"]; ok {
				t.Fatalf("%s/%s still names a Service: %v. No Service has endpoints in a tenant "+
					"whose webhook is a process beside the control plane", vwc.GetName(), hook["name"], cc)
			}
			if cc["caBundle"] != base64.StdEncoding.EncodeToString(ca) {
				t.Fatalf("%s/%s caBundle is %v, want the minted CA", vwc.GetName(), hook["name"], cc["caBundle"])
			}
			if hook["failurePolicy"] != "Ignore" {
				t.Fatalf("%s/%s failurePolicy is %v, want Ignore", vwc.GetName(), hook["name"], hook["failurePolicy"])
			}
			urls = append(urls, cc["url"].(string))
		}
	}
	sort.Strings(urls)
	want := []string{
		"https://127.0.0.1:20031/validate-external-secrets-io-v1-clustersecretstore",
		"https://127.0.0.1:20031/validate-external-secrets-io-v1-externalsecret",
		"https://127.0.0.1:20031/validate-external-secrets-io-v1-secretstore",
	}
	if !slices.Equal(urls, want) {
		t.Fatalf("the webhook URLs are %v, want %v", urls, want)
	}
}

func TestTheBesideIdentityIsBoundToTheUpstreamControllerRoleAndNothingElse(t *testing.T) {
	b := ControllerBindingObject()
	if b.GetKind() != "ClusterRoleBinding" || b.GetName() != ControllerBinding {
		t.Fatalf("the binding is %s %s", b.GetKind(), b.GetName())
	}
	role, _, _ := unstructured.NestedString(b.Object, "roleRef", "name")
	if role != ControllerClusterRole {
		t.Fatalf("the binding grants %q, want the upstream %q", role, ControllerClusterRole)
	}
	subjects, _, _ := unstructured.NestedSlice(b.Object, "subjects")
	if len(subjects) != 1 {
		t.Fatalf("the binding has %d subjects, want exactly the beside identity", len(subjects))
	}
	s := subjects[0].(map[string]any)
	if s["kind"] != "Group" || s["name"] != ControllerGroup {
		t.Fatalf("the binding's subject is %v, want Group %s: a ServiceAccount subject is one a "+
			"tenant who can create that ServiceAccount inherits", s, ControllerGroup)
	}
}

var crdGVR = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}

func besideDynamic(actions *[]string, existingVWC *unstructured.Unstructured) *fakedynamic.FakeDynamicClient {
	listKinds := map[schema.GroupVersionResource]string{crdGVR: "CustomResourceDefinitionList"}
	dyn := fakedynamic.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	record := func(action clienttesting.Action) {
		name := ""
		switch a := action.(type) {
		case clienttesting.DeleteAction:
			name = a.GetName()
		case clienttesting.PatchAction:
			name = a.GetName()
		case clienttesting.GetAction:
			name = a.GetName()
		}
		ns := action.GetNamespace()
		if ns != "" {
			name = ns + "/" + name
		}
		*actions = append(*actions, action.GetVerb()+" "+action.GetResource().Resource+" "+name)
	}
	dyn.PrependReactor("delete", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		record(action)
		return true, nil, nil
	})
	dyn.PrependReactor("patch", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		record(action)
		return true, &unstructured.Unstructured{}, nil
	})
	dyn.PrependReactor("get", "validatingwebhookconfigurations", func(action clienttesting.Action) (bool, runtime.Object, error) {
		record(action)
		name := action.(clienttesting.GetAction).GetName()
		if existingVWC != nil && existingVWC.GetName() == name {
			return true, existingVWC.DeepCopy(), nil
		}
		return true, nil, notFound("validatingwebhookconfigurations", name)
	})
	dyn.PrependReactor("get", "customresourcedefinitions", func(action clienttesting.Action) (bool, runtime.Object, error) {
		name := action.(clienttesting.GetAction).GetName()
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
			"metadata": map[string]any{"name": name},
			"status": map[string]any{"conditions": []any{
				map[string]any{"type": "Established", "status": "True"},
			}},
		}}
		return true, u, nil
	})
	return dyn
}

func besideOptions() BesideOptions {
	return BesideOptions{
		BundlePath:      subsetBundle,
		LegacyNamespace: "default",
		WebhookURL:      "https://127.0.0.1:20031",
		WebhookCAPEM:    []byte("ca"),
		FieldManager:    "socialweb-addons",
	}
}

func TestTheMigrationDeletesEveryRetiredObjectBeforeAnythingIsApplied(t *testing.T) {
	var actions []string
	if err := InstallBeside(context.Background(), besideDynamic(&actions, nil), besideOptions()); err != nil {
		t.Fatalf("InstallBeside: %v", err)
	}
	firstPatch := slices.IndexFunc(actions, func(a string) bool { return strings.HasPrefix(a, "patch ") })
	if firstPatch < 0 {
		t.Fatalf("nothing was applied: %v", actions)
	}
	for _, want := range []string{
		"delete deployments default/external-secrets",
		"delete deployments default/external-secrets-cert-controller",
		"delete deployments default/external-secrets-webhook",
		"delete serviceaccounts default/external-secrets",
		"delete services default/external-secrets-webhook",
		"delete secrets default/external-secrets-webhook",
		"delete clusterroles external-secrets-cert-controller",
		"delete clusterrolebindings external-secrets-controller",
		"delete clusterrolebindings external-secrets-cert-controller",
		"delete roles default/external-secrets-leaderelection",
		"delete rolebindings default/external-secrets-leaderelection",
	} {
		i := slices.Index(actions, want)
		if i < 0 {
			t.Fatalf("the migration never ran %q; actions %v", want, actions)
		}
		if i > firstPatch {
			t.Fatalf("%q ran after the first apply (%q): the old cert-controller would still be "+
				"rewriting the webhook's caBundle under the new registration", want, actions[firstPatch])
		}
	}
	for _, want := range []string{
		"patch customresourcedefinitions uuids.generators.external-secrets.io",
		"patch clusterroles external-secrets-controller",
		"patch clusterrolebindings " + ControllerBinding,
		"patch validatingwebhookconfigurations secretstore-validate",
		"patch validatingwebhookconfigurations externalsecret-validate",
	} {
		if !slices.Contains(actions, want) {
			t.Fatalf("the install never applied %q; actions %v", want, actions)
		}
	}
	for _, a := range actions {
		if strings.HasPrefix(a, "patch deployments") || strings.HasPrefix(a, "patch serviceaccounts") {
			t.Fatalf("the install applied a workload into the tenant: %q", a)
		}
	}
}

func TestAWebhookRegistrationStillAddressedByServiceIsReplacedNotMerged(t *testing.T) {
	old := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingWebhookConfiguration",
		"metadata": map[string]any{"name": "secretstore-validate"},
		"webhooks": []any{map[string]any{
			"name": "validate.secretstore.external-secrets.io",
			"clientConfig": map[string]any{"service": map[string]any{
				"namespace": "default", "name": "external-secrets-webhook",
			}},
		}},
	}}
	var actions []string
	if err := InstallBeside(context.Background(), besideDynamic(&actions, old), besideOptions()); err != nil {
		t.Fatalf("InstallBeside: %v", err)
	}
	del := slices.Index(actions, "delete validatingwebhookconfigurations secretstore-validate")
	apply := slices.Index(actions, "patch validatingwebhookconfigurations secretstore-validate")
	if del < 0 || apply < 0 || del > apply {
		t.Fatalf("a registration still naming a Service was not deleted before its URL apply: %v. "+
			"A field another manager owns survives a server-side apply, and a webhook carrying both "+
			"service and url is refused by the apiserver", actions)
	}
	if slices.Contains(actions, "delete validatingwebhookconfigurations externalsecret-validate") {
		t.Fatalf("a registration that names no Service was deleted: %v", actions)
	}
}

func notFound(resource, name string) error {
	return apierrors.NewNotFound(schema.GroupResource{Group: "admissionregistration.k8s.io", Resource: resource}, name)
}

func TestTheWebhookCertificateVerifiesForTheLoopbackURLAndIsKeptAcrossSpawns(t *testing.T) {
	dir := t.TempDir()
	paths := WebhookTLSPaths{CertDir: filepath.Join(dir, "certs"), CAKey: filepath.Join(dir, "ca.key")}
	now := time.Now()
	caPEM, err := EnsureWebhookTLS(paths, now)
	if err != nil {
		t.Fatalf("EnsureWebhookTLS: %v", err)
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(paths.CertDir, WebhookCertName), filepath.Join(paths.CertDir, WebhookKeyName))
	if err != nil {
		t.Fatalf("the webhook's pair does not load: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("the returned CA is not PEM")
	}
	for _, name := range []string{"127.0.0.1", WebhookDNSName} {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: name,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			t.Fatalf("the webhook certificate does not verify as %s: %v", name, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "localhost",
		CurrentTime: now.Add(WebhookLookahead + time.Hour)}); err != nil {
		t.Fatalf("the certificate does not outlive the webhook's own lookahead %s: %v, and the "+
			"webhook process exits on its first check", WebhookLookahead, err)
	}
	onDisk, err := os.ReadFile(filepath.Join(paths.CertDir, WebhookCAName))
	if err != nil || string(onDisk) != string(caPEM) {
		t.Fatalf("the cert dir's ca.crt is not the CA returned: %v", err)
	}
	if info, err := os.Stat(paths.CAKey); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the CA key is %v %v, want mode 0600", info, err)
	}
	if _, err := os.Stat(filepath.Join(paths.CertDir, "ca.key")); err == nil {
		t.Fatal("the CA key sits in the cert dir the webhook process reads")
	}

	again, err := EnsureWebhookTLS(paths, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	pair2, err := tls.LoadX509KeyPair(filepath.Join(paths.CertDir, WebhookCertName), filepath.Join(paths.CertDir, WebhookKeyName))
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(caPEM) || string(pair2.Certificate[0]) != string(pair.Certificate[0]) {
		t.Fatal("a second spawn re-minted a CA or leaf that was still good, so the registration's " +
			"caBundle churns on every restart")
	}

	late := leaf.NotAfter.Add(-WebhookRenewBefore + time.Hour)
	renewed, err := EnsureWebhookTLS(paths, late)
	if err != nil {
		t.Fatal(err)
	}
	pair3, err := tls.LoadX509KeyPair(filepath.Join(paths.CertDir, WebhookCertName), filepath.Join(paths.CertDir, WebhookKeyName))
	if err != nil {
		t.Fatal(err)
	}
	if string(pair3.Certificate[0]) == string(pair.Certificate[0]) {
		t.Fatal("a leaf inside its renewal window was kept")
	}
	if string(renewed) != string(caPEM) {
		t.Fatal("renewing the leaf replaced a CA that was still good")
	}
	block, _ := pem.Decode(renewed)
	if block == nil {
		t.Fatal("the renewed CA is not PEM")
	}
}

func TestRetiringWaitsUntilTheOldWorkloadAndItsPodsAreGone(t *testing.T) {
	RetirePoll = time.Millisecond
	RetireTimeout = 2 * time.Second
	t.Cleanup(func() { RetirePoll, RetireTimeout = defaultRetirePoll, defaultRetireTimeout })
	dyn := fakedynamic.NewSimpleDynamicClient(runtime.NewScheme())
	var policies []string
	dyn.PrependReactor("delete", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		opts := action.(clienttesting.DeleteActionImpl).DeleteOptions
		policy := ""
		if opts.PropagationPolicy != nil {
			policy = string(*opts.PropagationPolicy)
		}
		policies = append(policies, policy)
		return true, nil, nil
	})
	gets := 0
	dyn.PrependReactor("get", "deployments", func(action clienttesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets < 3 {
			return true, &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "apps/v1", "kind": "Deployment",
				"metadata": map[string]any{"name": "external-secrets", "namespace": "default"},
			}}, nil
		}
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "apps", Resource: "deployments"}, "external-secrets")
	})
	retired := []unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "external-secrets", "namespace": "default"},
	}}}
	if err := Retire(context.Background(), dyn, retired); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if len(policies) != 1 || policies[0] != "Foreground" {
		t.Fatalf("the old Deployment was deleted with propagation %v, want Foreground: deleted in the "+
			"background, its ReplicaSet's Pending pods outlive it, name no baseline workload, and read "+
			"as tenant demand (prod 2026-10-07T19:57:39Z wrote three claims that way)", policies)
	}
	if gets < 3 {
		t.Fatalf("Retire returned after %d reads, while the Deployment still existed", gets)
	}
}

func TestRetiringRefusesWhenTheOldWorkloadNeverGoes(t *testing.T) {
	RetirePoll = time.Millisecond
	RetireTimeout = 50 * time.Millisecond
	t.Cleanup(func() { RetirePoll, RetireTimeout = defaultRetirePoll, defaultRetireTimeout })
	dyn := fakedynamic.NewSimpleDynamicClient(runtime.NewScheme())
	dyn.PrependReactor("delete", "*", func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, nil })
	dyn.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"name": "external-secrets", "namespace": "default"},
		}}, nil
	})
	retired := []unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "external-secrets", "namespace": "default"},
	}}}
	if err := Retire(context.Background(), dyn, retired); err == nil {
		t.Fatal("Retire reported success while the old Deployment was still there")
	}
}
