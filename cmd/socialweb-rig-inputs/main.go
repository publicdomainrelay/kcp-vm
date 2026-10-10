package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/eso"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

func main() {
	store := flag.String("store", "", "the image store socialweb-nodeimage published into")
	fcConfig := flag.String("config", "", "the firecracker image configuration")
	qemuConfig := flag.String("config-qemu", "", "the qemu image configuration")
	fcBin := flag.String("firecracker", "/usr/local/bin/firecracker", "the firecracker binary")
	qemuBin := flag.String("qemu", "/usr/local/bin/qemu-system-x86_64", "the qemu-system-x86_64 binary")
	k3s := flag.String("k3s", "/usr/local/bin/k3s", "the k3s binary")
	kubectl := flag.String("kubectl", "/usr/local/bin/kubectl", "the kubectl binary")
	kcp := flag.String("kcp", "/usr/local/bin/kcp", "the kcp binary")
	scratch := flag.String("scratch", "/var/lib/socialweb/rig-scratch", "the subnet proof's scratch dir")
	kcpKubeconfig := flag.String("kcp-kubeconfig", "/var/lib/socialweb/kcp/admin.kubeconfig", "the kcp admin kubeconfig the running product wrote")
	kcpHost := flag.String("kcp-host", "https://127.0.0.1:6443", "the kcp apiserver the product listens on")
	cluster := flag.String("cluster", "socialweb", "the Cluster CR the deployment bootstrapped")
	registryWorkspace := flag.String("registry-workspace", "root:socialweb-provider", "the workspace holding the device-key binding rows")
	registryNamespace := flag.String("registry-namespace", "default", "the namespace holding the device-key binding rows")
	clusterproviderState := flag.String("clusterprovider-state", "/var/lib/socialweb/clusterprovider", "the clusterprovider's state directory")
	grant := flag.String("grant", "/opt/socialweb/deploy/tenant-grant.yaml", "the tenant grant the bootstrap applies")
	esoNamespace := flag.String("eso-namespace", eso.DefaultControllerNamespace, "the namespace the External Secrets controller is installed into")
	flag.Parse()
	if err := run(*store, *fcConfig, *qemuConfig, *fcBin, *qemuBin, *k3s, *kubectl, *kcp, *scratch); err != nil {
		fmt.Fprintf(os.Stderr, "socialweb-rig-inputs: %v\n", err)
		os.Exit(1)
	}
	for _, v := range derive(*kcpKubeconfig, *kcpHost, *cluster, *registryWorkspace, *registryNamespace, *clusterproviderState, *grant, *esoNamespace) {
		fmt.Printf("export %s=%s\n", v[0], shellQuote(v[1]))
	}
}

func run(store, fcConfig, qemuConfig, fcBin, qemuBin, k3s, kubectl, kcp, scratch string) error {
	if store == "" || fcConfig == "" || qemuConfig == "" {
		return fmt.Errorf("-store, -config and -config-qemu are required: the store is what the " +
			"product published into and the two configurations are what name the firecracker and " +
			"qemu images inside it, so neither the image a node boots nor its VMM can be derived " +
			"without both")
	}
	fcImage, _, err := nodefirecracker.LoadImage(store, fcConfig)
	if err != nil {
		return err
	}
	build, err := nodefirecracker.LoadBuildConfig(fcConfig)
	if err != nil {
		return err
	}
	qemuImage, _, err := nodefirecracker.LoadImage(store, qemuConfig)
	if err != nil {
		return err
	}
	tenant := ""
	for _, ref := range build.Images {
		if strings.Contains(ref, "busybox") {
			tenant = ref
		}
	}
	if tenant == "" {
		return fmt.Errorf("the configuration at %s seeds no image whose reference names busybox, "+
			"and the four-step proof runs its tenant workload in an image the guest already "+
			"carries and that stays up with no arguments", fcConfig)
	}
	agent := build.AgentBinary
	vars := [][2]string{
		{"SOCIALWEB_NODEFIRECRACKER_IMAGE", filepath.Dir(fcImage.Kernel)},
		{"SOCIALWEB_NODEFIRECRACKER_BIN", fcBin},
		{"SOCIALWEB_NODEFIRECRACKER_AGENT", agent},
		{"SOCIALWEB_NODEFIRECRACKER_K3S", k3s},
		{"SOCIALWEB_NODEFIRECRACKER_KUBECTL", kubectl},
		{"SOCIALWEB_NODEQEMU_IMAGE", filepath.Dir(qemuImage.Kernel)},
		{"SOCIALWEB_NODEQEMU_AGENT", agent},
		{"SOCIALWEB_NODEQEMU_BIN", qemuBin},
		{"SOCIALWEB_LIVE_SUBNET_IMAGE_OUT_DIR", store},
		{"SOCIALWEB_LIVE_SUBNET_IMAGE_CONFIG", fcConfig},
		{"SOCIALWEB_LIVE_SUBNET_FIRECRACKER", fcBin},
		{"SOCIALWEB_LIVE_SUBNET_AGENT", agent},
		{"SOCIALWEB_LIVE_SUBNET_SCRATCH", scratch},
		{"SOCIALWEB_CLUSTERPROVIDER_LIVE_IMAGE_OUT_DIR", store},
		{"SOCIALWEB_CLUSTERPROVIDER_LIVE_IMAGE_CONFIG", fcConfig},
		{"SOCIALWEB_CLUSTERPROVIDER_LIVE_QEMU_IMAGE_CONFIG", qemuConfig},
		{"SOCIALWEB_CLUSTERPROVIDER_LIVE_FIRECRACKER", fcBin},
		{"SOCIALWEB_CLUSTERPROVIDER_LIVE_QEMU", qemuBin},
		{"SOCIALWEB_CLUSTERPROVIDER_LIVE_NODE_AGENT", agent},
		{"SOCIALWEB_CLUSTERPROVIDER_LIVE_K3S", k3s},
		{"SOCIALWEB_CLUSTERPROVIDER_LIVE_KUBECTL", kubectl},
		{"SOCIALWEB_CLUSTERPROVIDER_LIVE_TENANT_IMAGE", tenant},
		{"KCP_BIN", kcp},
	}
	for _, v := range vars {
		fmt.Printf("export %s=%s\n", v[0], shellQuote(v[1]))
	}
	return nil
}

func derive(kcpKubeconfig, kcpHost, cluster, registryWorkspace, registryNamespace, clusterproviderState, grantPath, esoNamespace string) [][2]string {
	if _, err := os.Stat(kcpKubeconfig); err != nil {
		return nil
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kcpKubeconfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "socialweb-rig-inputs: reading %s: %v\n", kcpKubeconfig, err)
		return nil
	}
	reg, err := clusterprovider.NewRegistry(clusterprovider.RegistryOptions{Host: kcpHost, RestConfig: cfg})
	if err != nil {
		fmt.Fprintf(os.Stderr, "socialweb-rig-inputs: %v\n", err)
		return nil
	}
	refs, err := reg.Refs(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "socialweb-rig-inputs: listing the Cluster CRs in kcp: %v\n", err)
		return nil
	}
	var ref *clusterprovider.Ref
	for i := range refs {
		if cluster == "" || refs[i].Name == cluster {
			ref = &refs[i]
			break
		}
	}
	if ref == nil {
		return nil
	}
	vars := [][2]string{
		{"SOCIALWEB_LIVE_DID", ref.DID},
		{"SOCIALWEB_LIVE_CLUSTER", ref.Name},
		{"SOCIALWEB_MOUNT_DID", ref.DID},
		{"SOCIALWEB_MOUNT_CLUSTER", ref.Name},
		{"SOCIALWEB_LIVE_REGISTRY_WORKSPACE", registryWorkspace},
		{"SOCIALWEB_LIVE_REGISTRY_NAMESPACE", registryNamespace},
	}
	key, keyErr := ref.Key()
	if keyErr != nil {
		fmt.Fprintf(os.Stderr, "socialweb-rig-inputs: %v\n", keyErr)
		return nil
	}
	if dir, err := clusterprovider.ClusterDataDir(clusterproviderState, key); err == nil {
		tenant := filepath.Join(dir, "server", "cred", "admin.kubeconfig")
		if _, err := os.Stat(tenant); err == nil {
			vars = append(vars,
				[2]string{"SOCIALWEB_LIVE_TENANT_KUBECONFIG", tenant},
				[2]string{"CLUSTERPROVIDER_LIVE_TENANT_KUBECONFIG", tenant})
		}
	}
	if sa := grantServiceAccount(grantPath, ref.DID, esoNamespace); sa != "" {
		vars = append(vars, [2]string{"SOCIALWEB_LIVE_GRANTED_SA", sa})
	}
	return vars
}

func grantServiceAccount(path, ownerDID, esoNamespace string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	grant, err := kcpclient.RenderTenantGrant(string(raw), kcpclient.GrantScope{
		OwnerDID:     ownerDID,
		ESONamespace: esoNamespace,
	})
	if err != nil {
		return ""
	}
	objs, err := clusterprovider.GrantObjects([]byte(grant))
	if err != nil {
		return ""
	}
	for i := range objs {
		if objs[i].GetKind() != "Role" {
			continue
		}
		rules, ok, err := unstructured.NestedSlice(objs[i].Object, "rules")
		if err != nil || !ok {
			continue
		}
		for _, rule := range rules {
			r, _ := rule.(map[string]any)
			resources, _ := r["resources"].([]any)
			namesTheToken := false
			for _, res := range resources {
				if s, _ := res.(string); s == "serviceaccounts/token" {
					namesTheToken = true
				}
			}
			if !namesTheToken {
				continue
			}
			names, _ := r["resourceNames"].([]any)
			for _, n := range names {
				if name, _ := n.(string); name != "" {
					return name
				}
			}
		}
	}
	return ""
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
