package eso

import (
	"encoding/base64"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

func kindsOf(objs []unstructured.Unstructured) []string {
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.GetKind())
	}
	return out
}
