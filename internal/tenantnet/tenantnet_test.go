package tenantnet

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestTheNodeReaderRBACIsTheOneMeshdShips(t *testing.T) {
	ours, err := manifests.ReadFile("manifests/node-reader-rbac.yaml")
	if err != nil {
		t.Fatal(err)
	}
	shipped, err := os.ReadFile("../../rust/socialweb-meshd/deploy/node-reader-rbac.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ours, shipped) {
		t.Fatalf("internal/tenantnet/manifests/node-reader-rbac.yaml differs from " +
			"rust/socialweb-meshd/deploy/node-reader-rbac.yaml: meshd's proofs ran against the latter, " +
			"so the tenant must get exactly those bytes")
	}
}

type applied struct {
	Resource  string
	Namespace string
	Name      string
	Manager   string
	Force     bool
	PatchType types.PatchType
}

func recordingDynamic(record *[]applied) *fakedynamic.FakeDynamicClient {
	dyn := fakedynamic.NewSimpleDynamicClient(runtime.NewScheme())
	dyn.PrependReactor("patch", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		p := action.(clienttesting.PatchActionImpl)
		force := p.PatchOptions.Force != nil && *p.PatchOptions.Force
		*record = append(*record, applied{
			Resource:  p.GetResource().Resource,
			Namespace: p.GetNamespace(),
			Name:      p.GetName(),
			Manager:   p.PatchOptions.FieldManager,
			Force:     force,
			PatchType: p.GetPatchType(),
		})
		u := &unstructured.Unstructured{}
		if err := json.Unmarshal(p.GetPatch(), &u.Object); err != nil {
			return true, nil, err
		}
		return true, u, nil
	})
	return dyn
}

func TestApplyServerSideAppliesTheNodeReaderAndThePerNodeDNSUnderTheAddOnManager(t *testing.T) {
	var got []applied
	if err := Apply(context.Background(), recordingDynamic(&got), "socialweb-addons"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := []applied{
		{Resource: "clusterroles", Name: "socialweb-meshd-node-reader"},
		{Resource: "clusterrolebindings", Name: "socialweb-meshd-node-reader"},
		{Resource: "serviceaccounts", Namespace: "kube-system", Name: "socialweb-node-dns"},
		{Resource: "clusterroles", Name: "socialweb-node-dns"},
		{Resource: "clusterrolebindings", Name: "socialweb-node-dns"},
		{Resource: "configmaps", Namespace: "kube-system", Name: "socialweb-node-dns"},
		{Resource: "daemonsets", Namespace: "kube-system", Name: "socialweb-node-dns"},
	}
	if len(got) != len(want) {
		t.Fatalf("applied %d objects %+v, want %d", len(got), got, len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Resource != w.Resource || g.Namespace != w.Namespace || g.Name != w.Name {
			t.Fatalf("object %d applied %s %s/%s, want %s %s/%s", i, g.Resource, g.Namespace, g.Name,
				w.Resource, w.Namespace, w.Name)
		}
		if g.Manager != "socialweb-addons" || !g.Force || g.PatchType != types.ApplyPatchType {
			t.Fatalf("object %d (%s) applied by %q force=%v type=%s: only a server-side apply under "+
				"the add-on manager is measured into the baseline", i, g.Name, g.Manager, g.Force, g.PatchType)
		}
	}
}

func dnsObjects(t *testing.T) map[string]*unstructured.Unstructured {
	t.Helper()
	objs, err := Objects()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*unstructured.Unstructured{}
	for i := range objs {
		out[objs[i].GetKind()+"/"+objs[i].GetName()] = &objs[i]
	}
	return out
}

func TestThePerNodeDNSServesOnTheLinkLocalAddressOnlyOnNodesThatCarryIt(t *testing.T) {
	objs := dnsObjects(t)
	ds, ok := objs["DaemonSet/"+DNSName]
	if !ok {
		t.Fatalf("no DaemonSet %s in %v", DNSName, objs)
	}
	spec := ds.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	if spec["hostNetwork"] != true {
		t.Fatal("the per-node DNS must run on the host network: the link-local address lives on the node, " +
			"so a pod on the node reaches it without crossing to another node")
	}
	if spec["dnsPolicy"] != "Default" {
		t.Fatalf("dnsPolicy %v: the per-node DNS forwards to the node's own resolver, never to itself", spec["dnsPolicy"])
	}
	selector, _ := spec["nodeSelector"].(map[string]any)
	if selector[NodeLabel] != "true" || len(selector) != 1 {
		t.Fatalf("nodeSelector %v, want exactly %s=true: only a node whose user_data put %s on a "+
			"dummy interface and pointed its kubelet there may run it", selector, NodeLabel, Address)
	}
	cm, ok := objs["ConfigMap/"+DNSName]
	if !ok {
		t.Fatal("no Corefile ConfigMap")
	}
	corefile := cm.Object["data"].(map[string]any)["Corefile"].(string)
	for _, line := range []string{
		"bind " + Address,
		"health " + Address + ":8080",
		"ready " + Address + ":8181",
		"kubernetes cluster.local in-addr.arpa ip6.arpa {",
		"forward . /etc/resolv.conf",
	} {
		if !strings.Contains(corefile, line) {
			t.Fatalf("the Corefile lacks %q:\n%s", line, corefile)
		}
	}
	if Address != "169.254.20.10" {
		t.Fatalf("Address = %s: the guest's dummy interface and kubelet cluster-dns name 169.254.20.10", Address)
	}
	containers := spec["containers"].([]any)
	c := containers[0].(map[string]any)
	for _, probe := range []string{"livenessProbe", "readinessProbe"} {
		host := c[probe].(map[string]any)["httpGet"].(map[string]any)["host"]
		if host != Address {
			t.Fatalf("%s probes %v, want %s: the health endpoints are bound there", probe, host, Address)
		}
	}
}

func TestTheDNSImageIsOneTheNodeImageSeeds(t *testing.T) {
	ds := dnsObjects(t)["DaemonSet/"+DNSName]
	spec := ds.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	image := spec["containers"].([]any)[0].(map[string]any)["image"].(string)
	raw, err := os.ReadFile("../../deploy/container-nodeimage.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Images []string `json:"images"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, seeded := range cfg.Images {
		if seeded == image {
			return
		}
	}
	t.Fatalf("the per-node DNS image %s is not one the node image seeds %v: a guest would pull it "+
		"before any name resolves in a pod", image, cfg.Images)
}
