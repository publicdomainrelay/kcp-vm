package esovault

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/eso"
)

func TestAStoreIsNeverAppliedToATenantThatServesNoneOfTheOperatorsKinds(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1", Resource: "clustersecretstores"}
	dyn := fakedynamic.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "ClusterSecretStoreList"})
	dyn.PrependReactor("list", "clustersecretstores", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(gvr.GroupResource(), "")
	})
	var writes []string
	dyn.PrependReactor("patch", "*", func(a clienttesting.Action) (bool, runtime.Object, error) {
		writes = append(writes, a.GetResource().Resource)
		return true, nil, nil
	})
	_, err := ApplyVaultStore(context.Background(), dyn, VaultStore{
		Cluster: "c", DataDir: t.TempDir(), VaultPort: 1, RootToken: "root", ManifestDir: "../../deploy/eso", Namespace: "users",
	})
	if !errors.Is(err, eso.ErrNoOperator) {
		t.Fatalf("a tenant serving no %s was handed a store: %v", eso.OperatorGroupVersion, err)
	}
	if len(writes) != 0 {
		t.Fatalf("the store pass wrote %v into a tenant with no operator", writes)
	}
}

func TestTheStoreDialsTheClusterVaultOnLoopback(t *testing.T) {
	if got := StoreVaultURL(20001); got != "https://127.0.0.1:20001" {
		t.Fatalf("the store's vault URL is %s: the controller is a process beside the control plane, "+
			"so the vault it dials is the loopback listener on every substrate", got)
	}
}
