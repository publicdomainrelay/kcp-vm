package eso

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func testOptions() Options {
	return Options{
		ManifestDir:   "../../deploy/eso",
		Namespace:     "users",
		VaultServer:   "https://10.42.7.1:20004",
		ServerCAPEM:   []byte("ca-pem"),
		ClientCertPEM: []byte("cert-pem"),
		ClientKeyPEM:  []byte("key-pem"),
	}
}

func TestTheManifestsLoadAndResolveToTheClusterVault(t *testing.T) {
	o := testOptions()
	objs, err := Load(o.ManifestDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("Load returned %d objects, want the ClusterSecretStore and the ExternalSecret", len(objs))
	}
	var store, external *unstructured.Unstructured
	for i := range objs {
		if err := Resolve(&objs[i], o); err != nil {
			t.Fatalf("Resolve(%s): %v", objs[i].GetKind(), err)
		}
		switch objs[i].GetKind() {
		case "ClusterSecretStore":
			store = &objs[i]
		case "ExternalSecret":
			external = &objs[i]
		}
	}
	if store == nil || external == nil {
		t.Fatalf("the manifest set is missing the store or the ExternalSecret: kinds %v", kindsOf(objs))
	}

	server, _, _ := unstructured.NestedString(store.Object, "spec", "provider", "vault", "server")
	if server != o.VaultServer {
		t.Fatalf("the store's vault server is %q, want the cluster's %q: a store left on its "+
			"placeholder would point every tenant secret at the wrong vault", server, o.VaultServer)
	}
	ca, _, _ := unstructured.NestedString(store.Object, "spec", "provider", "vault", "caBundle")
	if ca != base64.StdEncoding.EncodeToString(o.ServerCAPEM) {
		t.Fatalf("the store's caBundle is %q, want the base64 of the vault server CA", ca)
	}
	certName, _, _ := unstructured.NestedString(store.Object, "spec", "provider", "vault", "auth", "cert", "clientCert", "name")
	keyName, _, _ := unstructured.NestedString(store.Object, "spec", "provider", "vault", "auth", "cert", "secretRef", "name")
	if certName != ClientSecretName || keyName != ClientSecretName {
		t.Fatalf("the store reads its certificate from (%q, %q), want both %q", certName, keyName, ClientSecretName)
	}
	certNamespace, _, _ := unstructured.NestedString(store.Object, "spec", "provider", "vault", "auth", "cert", "clientCert", "namespace")
	if certNamespace != o.Namespace {
		t.Fatalf("the store reads the certificate from namespace %q, want the tenant namespace %q", certNamespace, o.Namespace)
	}

	if external.GetNamespace() != o.Namespace {
		t.Fatalf("the ExternalSecret is in namespace %q, want the tenant namespace %q", external.GetNamespace(), o.Namespace)
	}
	storeRef, _, _ := unstructured.NestedString(external.Object, "spec", "secretStoreRef", "name")
	if storeRef != StoreName {
		t.Fatalf("the ExternalSecret names store %q, want %q", storeRef, StoreName)
	}
	items, found, err := unstructured.NestedSlice(external.Object, "spec", "data")
	if err != nil || !found || len(items) == 0 {
		t.Fatalf("the ExternalSecret carries no data entries: found=%t err=%v", found, err)
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("the ExternalSecret's first data entry is %T", items[0])
	}
	remoteRef, ok := first["remoteRef"].(map[string]any)
	if !ok {
		t.Fatalf("the ExternalSecret's first remoteRef is %T", first["remoteRef"])
	}
	if remoteRef["key"] != "k3s/eso-demo" {
		t.Fatalf("the ExternalSecret reads key %q, want k3s/eso-demo so the demo value has a vault "+
			"home and never a k3s home", remoteRef["key"])
	}
}

func TestOptionsAreValidated(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(o *Options)
	}{
		{"no manifest directory", func(o *Options) { o.ManifestDir = "" }},
		{"no namespace", func(o *Options) { o.Namespace = "" }},
		{"no vault server", func(o *Options) { o.VaultServer = "" }},
		{"plaintext vault server", func(o *Options) { o.VaultServer = "http://10.42.7.1:20004" }},
		{"hostless vault server", func(o *Options) { o.VaultServer = "https://" }},
		{"no vault server CA", func(o *Options) { o.ServerCAPEM = nil }},
		{"no client certificate", func(o *Options) { o.ClientCertPEM = nil }},
		{"no client key", func(o *Options) { o.ClientKeyPEM = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := testOptions()
			tc.mut(&o)
			if err := o.validate(); !errors.Is(err, ErrIncomplete) {
				t.Fatalf("Options with %s validated: %v", tc.name, err)
			}
		})
	}
	if err := testOptions().validate(); err != nil {
		t.Fatalf("a complete Options value was refused: %v", err)
	}
}

var storeGVR = schema.GroupVersionResource{
	Group: "external-secrets.io", Version: "v1", Resource: "clustersecretstores",
}

var unknownStore = &apierrors.StatusError{ErrStatus: metav1.Status{
	Status:  metav1.StatusFailure,
	Code:    404,
	Reason:  metav1.StatusReasonNotFound,
	Message: "the server could not find the requested resource",
}}

func storeDynamic(served bool, order *[]string) *fakedynamic.FakeDynamicClient {
	listKinds := map[schema.GroupVersionResource]string{
		storeGVR: "ClusterSecretStoreList",
		{Group: "external-secrets.io", Version: "v1", Resource: "externalsecrets"}: "ExternalSecretList",
		{Group: "", Version: "v1", Resource: "secrets"}:                            "SecretList",
	}
	dyn := fakedynamic.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	if !served {
		dyn.PrependReactor("list", "clustersecretstores", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, unknownStore
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

func TestTheOperatorIsReadOffTheStoreResource(t *testing.T) {
	served, err := Installed(context.Background(), storeDynamic(true, nil))
	if err != nil {
		t.Fatalf("Installed with the store resource served: %v", err)
	}
	if !served {
		t.Fatalf("the cluster serves the store resource and Installed says it does not, so every " +
			"pass would reinstall the operator")
	}
	absent, err := Installed(context.Background(), storeDynamic(false, nil))
	if err != nil {
		t.Fatalf("Installed with the store resource absent: %v", err)
	}
	if absent {
		t.Fatalf("the cluster answers the store resource with %v and Installed says the operator "+
			"is there", unknownStore)
	}
}

func TestAnUnreadableStoreResourceIsRefusedRatherThanReadAsAbsent(t *testing.T) {
	mine := errors.New("the apiserver is unreachable")
	dyn := storeDynamic(true, nil)
	dyn.PrependReactor("list", "clustersecretstores", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, mine
	})
	if _, err := Installed(context.Background(), dyn); !errors.Is(err, mine) {
		t.Fatalf("a failed read was taken as an answer about the operator: %v", err)
	}
}

func TestAnAbsentOperatorIsInstalledBeforeTheStoreIsApplied(t *testing.T) {
	ctx := context.Background()
	var order []string
	served := false
	dyn := storeDynamic(true, &order)
	dyn.PrependReactor("list", "clustersecretstores", func(clienttesting.Action) (bool, runtime.Object, error) {
		if served {
			return false, nil, nil
		}
		return true, nil, unknownStore
	})
	install := func(context.Context) error {
		order = append(order, "install")
		served = true
		return nil
	}
	if err := InstallOperator(ctx, dyn, install); err != nil {
		t.Fatalf("InstallOperator: %v", err)
	}
	if err := Apply(ctx, dyn, testOptions()); err != nil {
		t.Fatalf("Apply after the operator install: %v", err)
	}
	want := []string{"install", "secrets", "clustersecretstores", "externalsecrets"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("the pass did %v, want %v: the store is applied to a cluster whose %s nothing "+
			"has installed yet", order, want, OperatorGroupVersion)
	}
}

func TestAPresentOperatorIsNotInstalledAgain(t *testing.T) {
	installs := 0
	install := func(context.Context) error {
		installs++
		return nil
	}
	if err := InstallOperator(context.Background(), storeDynamic(true, nil), install); err != nil {
		t.Fatalf("InstallOperator with the operator present: %v", err)
	}
	if installs != 0 {
		t.Fatalf("the store resource is served and the installer ran %d time(s), so every pass "+
			"pays for an install it already has", installs)
	}
}

func TestAFailedInstallNamesTheOperatorAndTheCause(t *testing.T) {
	mine := errors.New("curl: (6) could not resolve host: github.com")
	err := InstallOperator(context.Background(), storeDynamic(false, nil),
		func(context.Context) error { return mine })
	if !errors.Is(err, mine) {
		t.Fatalf("the install failure is not named: %v", err)
	}
	if !strings.Contains(err.Error(), OperatorGroupVersion) {
		t.Fatalf("the refusal does not name %s: %v", OperatorGroupVersion, err)
	}
}

func TestAnInstallThatLeavesTheGroupUnservedIsRefused(t *testing.T) {
	err := InstallOperator(context.Background(), storeDynamic(false, nil),
		func(context.Context) error { return nil })
	if !errors.Is(err, ErrNoOperator) {
		t.Fatalf("an installer that leaves the store resource unknown was accepted: %v", err)
	}
}

func kindsOf(objs []unstructured.Unstructured) []string {
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.GetKind())
	}
	return out
}
