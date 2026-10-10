package kcpclient

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/gateway"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

func TestEnsureBuildsTheMountChildAgainstALiveKcp(t *testing.T) {
	livegate.Short(t, "this test needs a live kcp and -short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	kubeconfig := os.Getenv("SOCIALWEB_LIVE_KUBECONFIG")
	host := os.Getenv("SOCIALWEB_LIVE_APISERVER")
	did := os.Getenv("SOCIALWEB_MOUNT_DID")
	label := os.Getenv("SOCIALWEB_MOUNT_CLUSTER")
	if kubeconfig == "" || host == "" || did == "" || label == "" {
		livegate.Require(t, "NOT RUN: set SOCIALWEB_LIVE_KUBECONFIG, SOCIALWEB_LIVE_APISERVER, "+
			"SOCIALWEB_MOUNT_DID and SOCIALWEB_MOUNT_CLUSTER.\n"+
			"  This is the mount child against a live kcp. Whether the workspace at "+
			"MountPath resolves is a fact about the SERVER -- the ref, the auto-binding and "+
			"the mount controller -- and no fake in this package answers it.")
	}

	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	c, err := New(Options{Host: host, RestConfig: restCfg})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := c.Ensure(ctx, gateway.Caller(did), label); err != nil {
		t.Fatalf("Ensure(%q, %q): %v", did, label, err)
	}

	cr, err := c.readCluster(ctx, HomePath(did, label), label)
	if err != nil {
		t.Fatalf("reading the Cluster CR %s out of %s: %v", label, HomePath(did, label), err)
	}
	if cr.Status.URL == "" {
		livegate.Require(t, "NOT RUN: the Cluster CR %s in %s reports no status.URL, so there "+
			"is no apiserver to address and the mount child cannot be written. Bring the "+
			"cluster up (spec.running=true and its group serving) and run this again",
			label, HomePath(did, label))
	}

	mountCfg, err := ConfigFromContext(kubeconfig, ShardAdminContext)
	if err != nil {
		t.Fatalf("the privileged context %q of %s: %v. spec.URL is refused from any other "+
			"identity with `workspaces.tenancy.kcp.io \"clusters\" is forbidden: spec.URL can "+
			"only be set by system privileged users`, and the mount then serves kcp itself",
			ShardAdminContext, kubeconfig, err)
	}
	m, err := NewMountChild(Options{Host: host, RestConfig: mountCfg})
	if err != nil {
		t.Fatalf("NewMountChild: %v", err)
	}
	if err := m.EnsureMountChild(ctx, HomePath(did, label), label, cr.Status.URL); err != nil {
		t.Fatalf("EnsureMountChild(%s, %s, %q): %v", HomePath(did, label), label, cr.Status.URL, err)
	}

	child, err := c.readWorkspace(ctx, HomePath(did, label), ChildLabel)
	if err != nil {
		t.Fatalf("the mount child %s: %v", MountPath(did, label), err)
	}
	if child.Spec.Mount == nil {
		t.Fatalf("the child at %s carries no spec.mount, so no path through it reaches a "+
			"Cluster CR and a tenant's kubeconfig names a workspace that proxies nothing",
			MountPath(did, label))
	}
	if child.Spec.URL != cr.Status.URL {
		t.Fatalf("the child's spec.URL is %q and the CR reports %q: kcp copies spec.URL out of "+
			"the mounted object ONCE, so the mount is now addressed to an apiserver that is not "+
			"this cluster's", child.Spec.URL, cr.Status.URL)
	}
	ref := child.Spec.Mount.Reference
	if ref.Name != label {
		t.Fatalf("the child at %s mounts %s/%s %q, want the Cluster CR %q in %s",
			MountPath(did, label), ref.APIVersion, ref.Kind, ref.Name, label, HomePath(did, label))
	}

	status, err := json.Marshal(child.Status)
	if err != nil {
		t.Fatalf("marshalling the child's status: %v", err)
	}
	t.Logf("MOUNTCHILD path=%s mountRef=%s %s %q specURL=%q status=%s",
		MountPath(did, label), ref.APIVersion, ref.Kind, ref.Name, child.Spec.URL, status)
}
