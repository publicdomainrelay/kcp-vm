package appctl

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/manifests"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbaoctl"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

const (
	GatewayRootName       = "gateway"
	RootVaultAsset        = "bao"
	rootVaultAppRole      = "approle"
	rootVaultPKIMount     = "pki"
	rootVaultKVMount      = "secret"
	rootVaultPKIMaxTTL    = 8760 * time.Hour
	ProfileGatewayProd    = "prod"
	ProfileGatewayNetwork = "market-network"
	GatewaySet            = "gateway"
	GatewayApp            = "gateway"
	WebAppComponent       = "socialweb-computer-k8s"
	WebAppWebDir          = "socialweb-computer-k8s/web"
	WebAppVaultClient     = "socialweb-computer-k8s"
	WebAppVaultMount      = "socialweb-computer-k8s"
	NodeGuestCurlHome     = "/root"
)

type WebApp struct {
	FrontKubeconfig string
	ClusterCADir    string
}

func duration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.String()
}

func keyType(kind string) string {
	switch strings.ToLower(kind) {
	case "ecdsa", "ec":
		return "ec"
	}
	return kind
}

func RootVaultWriterRules() []v1alpha1.OpenBaoPolicyRule {
	cu := []string{"create", "update"}
	return []v1alpha1.OpenBaoPolicyRule{
		{Path: rootVaultPKIMount + "/root/generate/internal", Capabilities: cu},
		{Path: rootVaultPKIMount + "/cert/ca", Capabilities: []string{"read"}},
		{Path: rootVaultKVMount + "/data/socialweb/*", Capabilities: []string{"create", "read", "update"}},
	}
}

func RootVaultDeclared() *v1alpha1.OpenBaoServerDeclared {
	layout := vaultlayout.Default()
	d := &v1alpha1.OpenBaoServerDeclared{}
	for _, m := range layout.Mounts {
		if m.Vault != vaultlayout.Root {
			continue
		}
		sm := v1alpha1.OpenBaoSecretMount{Path: strings.Trim(m.Path, "/"), Type: string(m.Kind)}
		if m.Kind == vaultlayout.MountPKI {
			ttl := m.MaxLeaseTTL
			if ttl <= 0 {
				ttl = rootVaultPKIMaxTTL
			}
			sm.MaxLeaseTTL = ttl.String()
		}
		d.SecretMounts = append(d.SecretMounts, sm)
	}
	d.AuthMounts = append(d.AuthMounts, v1alpha1.OpenBaoAuthMount{Path: rootVaultAppRole, Type: "approle"})
	for _, p := range layout.Policies {
		if p.Vault == vaultlayout.Root {
			d.Policies = append(d.Policies, v1alpha1.OpenBaoPolicy{Name: p.Name, HCL: p.HCL})
		}
	}
	d.Policies = append(d.Policies, v1alpha1.OpenBaoPolicy{Name: vaultlayout.PolicyRootWriter, Rules: RootVaultWriterRules()})
	for _, r := range layout.AppRoles {
		if r.Vault == vaultlayout.Root {
			d.AppRoles = append(d.AppRoles, v1alpha1.OpenBaoAppRole{Mount: rootVaultAppRole, Name: r.Name, Policies: r.Policies,
				TokenTTL: duration(r.TokenTTL), TokenMaxTTL: duration(r.TokenMaxTTL), SecretIDTTL: duration(r.SecretIDTTL)})
		}
	}
	for _, r := range layout.PKIRoles {
		if r.Vault != vaultlayout.Root {
			continue
		}
		d.PKIRoles = append(d.PKIRoles, v1alpha1.OpenBaoPKIRole{Mount: strings.Trim(r.Mount, "/"), Name: r.Name,
			AllowedDomains: r.AllowedDomains, AllowSubdomains: r.AllowSubdomains, AllowBareDomains: r.AllowBareDomains,
			AllowIPSANs: r.AllowIPSANs, EnforceHostnames: r.EnforceHostnames, RequireCN: r.RequireCN, ClientFlag: r.ClientAuth,
			ServerFlag: r.ServerAuth, KeyType: keyType(r.KeyType), KeyBits: int32(r.KeyBits), KeyUsage: r.KeyUsage,
			TTL: duration(r.TTL), MaxTTL: duration(r.MaxTTL)})
	}
	return d
}

func RootVaultSpec() v1alpha1.OpenBaoServerSpec {
	return v1alpha1.OpenBaoServerSpec{Asset: &v1alpha1.HostProcessAsset{Name: RootVaultAsset},
		Clients: []v1alpha1.OpenBaoServerClient{{Name: vaultlayout.RootClient, Policies: []string{vaultlayout.PolicyRootWriter}},
			{Name: WebAppVaultClient, KVMount: WebAppVaultMount}},
		Declared: RootVaultDeclared()}
}

func sameSpec(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

func (ctl *Controller) ensureRootVault(ctx context.Context, owner string) (*v1alpha1.OpenBaoServer, error) {
	ri, err := ctl.client(ctl.c.workspace(), OpenBaoServerGVR)
	if err != nil {
		return nil, err
	}
	want := RootVaultSpec()
	for range 5 {
		u, err := ri.Get(ctx, vaultlayout.RootServerName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			o := v1alpha1.OpenBaoServer{ObjectMeta: metav1.ObjectMeta{Name: vaultlayout.RootServerName,
				Labels: map[string]string{OwnerLabel: owner}}, Spec: want}
			nu, err := toUnstructured(&o, "OpenBaoServer")
			if err != nil {
				return nil, err
			}
			if u, err = ri.Create(ctx, nu, metav1.CreateOptions{FieldManager: FieldManager}); err != nil {
				if apierrors.IsAlreadyExists(err) {
					continue
				}
				return nil, err
			}
			ctl.c.log().Info("declared the gateway's root vault", "openbaoserver", vaultlayout.RootServerName)
		} else if err != nil {
			return nil, err
		}
		var o v1alpha1.OpenBaoServer
		if err := fromUnstructured(u, &o); err != nil {
			return nil, err
		}
		if sameSpec(o.Spec, want) && o.Labels[OwnerLabel] == owner {
			return &o, nil
		}
		o.Spec = want
		if o.Labels == nil {
			o.Labels = map[string]string{}
		}
		o.Labels[OwnerLabel] = owner
		nu, err := toUnstructured(&o, "OpenBaoServer")
		if err != nil {
			return nil, err
		}
		if _, err := ri.Update(ctx, nu, metav1.UpdateOptions{FieldManager: FieldManager}); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return nil, err
		}
		ctl.c.log().Info("the gateway's root vault declaration changed", "openbaoserver", vaultlayout.RootServerName)
	}
	return nil, fmt.Errorf("appctl: the OpenBaoServer %s stayed in conflict", vaultlayout.RootServerName)
}

func RootVaultSynced(o *v1alpha1.OpenBaoServer) bool {
	if o == nil || o.Status.ObservedGeneration != o.Generation || o.Status.Phase != v1alpha1.OpenBaoServerPhaseReady {
		return false
	}
	c := meta.FindStatusCondition(o.Status.Conditions, openbaoctl.ConditionSynced)
	return c != nil && c.Status == metav1.ConditionTrue && c.ObservedGeneration == o.Generation
}

func (ctl *Controller) reconcileGateway(ctx context.Context, u *unstructured.Unstructured) error {
	var o v1alpha1.Gateway
	if err := fromUnstructured(u, &o); err != nil {
		return err
	}
	owner := ownerOf(u)
	st := v1alpha1.ApplicationStatus{ObservedGeneration: o.Generation, Profile: o.Spec.Profile, Components: []v1alpha1.ApplicationComponent{}}
	if u.GetDeletionTimestamp() != nil {
		left, err := ctl.deletePods(ctx, ctl.c.workspace()+":"+ComponentWorkspace, owner, nil)
		if err != nil {
			return err
		}
		if left == 0 {
			if err := ctl.revokeWebApp(ctx); err != nil {
				return err
			}
			if left, err = ctl.deleteVaults(ctx, owner); err != nil {
				return err
			}
		}
		if left > 0 {
			st.Phase, st.Message = v1alpha1.ApplicationPhaseDeleting, fmt.Sprintf("%d component(s) stopping", left)
			return ctl.writeStatus(ctx, GatewayGVR, o.Name, st)
		}
		return ctl.setFinalizer(ctx, GatewayGVR, u, false)
	}
	if !hasFinalizer(u) {
		if err := ctl.setFinalizer(ctx, GatewayGVR, u, true); err != nil {
			return err
		}
	}
	switch o.Spec.Profile {
	case ProfileGatewayProd, ProfileGatewayNetwork:
	default:
		st.Phase, st.Message = v1alpha1.ApplicationPhaseFailed, fmt.Sprintf("profile %q: want %s or %s", o.Spec.Profile, ProfileGatewayProd, ProfileGatewayNetwork)
		return ctl.writeStatus(ctx, GatewayGVR, o.Name, st)
	}
	vault, err := ctl.ensureRootVault(ctx, owner)
	if err != nil {
		st.Phase, st.Message = v1alpha1.ApplicationPhaseProvisioning, "its root vault: "+err.Error()
		return ctl.writeStatus(ctx, GatewayGVR, o.Name, st)
	}
	vaultReady := RootVaultSynced(vault)
	st.Components = append(st.Components, v1alpha1.ApplicationComponent{Kind: "OpenBaoServer", Name: vault.Name,
		Workspace: ctl.c.workspace(), Phase: string(vault.Status.Phase), Ready: vaultReady})
	var core []v1alpha1.ApplicationListener
	if ctl.c.Listeners != nil {
		core = ctl.c.Listeners(owner)
	}
	st.Listeners = append(st.Listeners, core...)
	webAppReady := true
	webAppVault, haveVault := WebAppVaultOf(vault)
	if ctl.c.WebApp != nil && !haveVault {
		webAppReady = false
	}
	if ctl.c.WebApp != nil && haveVault {
		ws := ctl.c.workspace() + ":" + ComponentWorkspace
		if ctl.applied[string(o.UID)] != o.Generation || ctl.lostComponent(ctx, owner) != "" {
			values, err := GatewayValues(o, ctl.c.workspace(), ComponentStateDir(ctl.c.Layout.StateDir, WebAppComponent), *ctl.c.WebApp, webAppVault)
			if err != nil {
				st.Phase, st.Message = v1alpha1.ApplicationPhaseFailed, err.Error()
				return ctl.writeStatus(ctx, GatewayGVR, o.Name, st)
			}
			if _, err := ctl.applyProfile(ctx, GatewaySet, o.Spec.Profile, owner, false, &values); err != nil {
				st.Phase, st.Message = v1alpha1.ApplicationPhaseProvisioning, "its workspace: "+err.Error()
				return ctl.writeStatus(ctx, GatewayGVR, o.Name, st)
			}
			id, err := ctl.webAppIdentity(ctx, o, owner, *ctl.c.WebApp)
			if err != nil {
				st.Phase, st.Message = v1alpha1.ApplicationPhaseProvisioning, "the web app's kcp identity: "+err.Error()
				return ctl.writeStatus(ctx, GatewayGVR, o.Name, st)
			}
			values.Kubeconfig, values.KcpDir = id.Kubeconfig, id.Dir
			pods, err := ctl.applyProfile(ctx, GatewaySet, o.Spec.Profile, owner, true, &values)
			if err != nil {
				st.Phase, st.Message = v1alpha1.ApplicationPhaseProvisioning, "its components: "+err.Error()
				return ctl.writeStatus(ctx, GatewayGVR, o.Name, st)
			}
			if _, err := ctl.deletePods(ctx, ws, owner, pods); err != nil {
				return err
			}
			ctl.applied[string(o.UID)] = o.Generation
			ctl.c.log().Info("applied the Gateway's components", "name", o.Name, "profile", o.Spec.Profile, "denopods", pods)
		} else if err := ctl.webAppToken(ctx, WebAppKcpDir(ctl.c.Layout.StateDir)); err != nil {
			return err
		}
		pods, err := ctl.ownedPods(ctx, ws, owner)
		if err != nil {
			return err
		}
		webAppReady = len(pods) > 0
		var components []v1alpha1.ApplicationListener
		for i := range pods {
			v := podViewOf(ws, &pods[i])
			st.Components = append(st.Components, v.component())
			components = append(components, v.Listeners...)
			webAppReady = webAppReady && v.Ready && len(v.Listeners) > 0
		}
		st.Listeners = append(st.Listeners, components...)
		if ctl.c.OnComponents != nil {
			ctl.c.OnComponents(owner, components)
		}
	}
	switch {
	case !vaultReady:
		st.Phase, st.Message = v1alpha1.ApplicationPhaseProvisioning, "waiting for the root vault to be Ready and synced"
	case ctl.c.Listeners != nil && len(core) == 0:
		st.Phase, st.Message = v1alpha1.ApplicationPhaseProvisioning, "the root vault is Ready; waiting for the core's members to report their listeners"
	case !webAppReady:
		st.Phase, st.Message = v1alpha1.ApplicationPhaseProvisioning, "waiting for the "+WebAppComponent+" DenoPod to be ready and report its listener"
	default:
		st = ready(st, "the root vault is Ready and every member and component reported its listener")
	}
	return ctl.writeStatus(ctx, GatewayGVR, o.Name, st)
}

func listJSON(items []string) (string, error) {
	if items == nil {
		items = []string{}
	}
	b, err := json.Marshal(items)
	return string(b), err
}

func WebAppVaultOf(o *v1alpha1.OpenBaoServer) (manifests.GatewayVault, bool) {
	if o == nil || !RootVaultSynced(o) || o.Status.Refs == nil || o.Status.Refs.CA == "" {
		return manifests.GatewayVault{}, false
	}
	for _, c := range o.Status.Clients {
		if c.Name == WebAppVaultClient && c.ClientCert != "" {
			return manifests.GatewayVault{Server: o.Name, Client: c.Name, CA: fileRef(o.Status.Refs.CA),
				ClientDir: filepath.Dir(fileRef(c.ClientCert))}, true
		}
	}
	return manifests.GatewayVault{}, false
}

func GatewayValues(o v1alpha1.Gateway, workspace, stateDir string, w WebApp, vault manifests.GatewayVault) (manifests.Gateway, error) {
	if !filepath.IsAbs(w.FrontKubeconfig) {
		return manifests.Gateway{}, fmt.Errorf("appctl: the %s web app needs an absolute kcp front kubeconfig", WebAppComponent)
	}
	v := manifests.Gateway{StateDir: stateDir, WebDir: manifests.UnderTSRoot(WebAppWebDir),
		GatewayWorkspace: workspace, GatewayName: o.Name, TLS: manifests.TLSNone, Vault: vault,
		IdentityWorkspace: workspace + ":" + ComponentWorkspace}
	var trust, read []string
	var extra []string
	if n := o.Spec.Network; n != nil {
		if n.CaBundleRef != "" {
			v.TLS, trust = manifests.TLSTrust, []string{n.CaBundleRef}
			if o.Spec.Profile == ProfileGatewayNetwork {
				extra = append(extra, "--node-guest-ca-file", n.CaBundleRef, "--node-guest-curl-home", NodeGuestCurlHome)
				read = append(read, n.CaBundleRef)
			}
		}
		if n.PlcDirectoryUrl != "" {
			extra = append(extra, "--plc-directory-url", n.PlcDirectoryUrl)
		}
		if n.IngressProxyHost != "" {
			extra = append(extra, "--ingress-proxy-host", n.IngressProxyHost)
		}
		if len(n.RelayUrls) > 0 {
			extra = append(extra, "--relay-url", strings.Join(n.RelayUrls, ","))
		}
		if hosts := NetworkHostsArg(n.Hosts); hosts != "" {
			extra = append(extra, "--network-hosts", hosts)
		}
	}
	if len(o.Spec.BidderDids) > 0 {
		extra = append(extra, "--node-extra-bidder-dids", strings.Join(o.Spec.BidderDids, ","))
	}
	opts, err := OptionArgs(GatewayApp, o.Spec.Options)
	if err != nil {
		return manifests.Gateway{}, err
	}
	extra = append(extra, opts...)
	if w.ClusterCADir != "" {
		if !filepath.IsAbs(w.ClusterCADir) {
			return manifests.Gateway{}, fmt.Errorf("appctl: the cluster CA dir %q is not absolute", w.ClusterCADir)
		}
		extra = append(extra, "--cluster-ca-dir", w.ClusterCADir)
		read = append(read, w.ClusterCADir)
	}
	if len(read) > 0 {
		if v.ExtraRead, err = fragment(read); err != nil {
			return manifests.Gateway{}, err
		}
	}
	if v.Trust, err = listJSON(trust); err != nil {
		return manifests.Gateway{}, err
	}
	if v.ExtraArgs, err = fragment(extra); err != nil {
		return manifests.Gateway{}, err
	}
	if m := o.Spec.MarketNetwork; m != nil {
		if o.Spec.Profile != ProfileGatewayNetwork {
			return manifests.Gateway{}, fmt.Errorf("appctl: spec.marketNetwork is a value of the %s profile only, not of %s", ProfileGatewayNetwork, o.Spec.Profile)
		}
		var args, read []string
		for _, f := range [][2]string{{"--test-owner-sessions-file", m.OwnerSessionsFile}, {"--test-node-user-data-base-file", m.NodeUserDataBaseFile}} {
			flag, file := f[0], f[1]
			if file == "" {
				continue
			}
			if !filepath.IsAbs(file) {
				return manifests.Gateway{}, fmt.Errorf("appctl: spec.marketNetwork names %q, not an absolute path", file)
			}
			args, read = append(args, flag, file), append(read, file)
		}
		if len(m.NodeGuestHostAliases) > 0 {
			args = append(args, "--test-node-guest-host-aliases", strings.Join(m.NodeGuestHostAliases, ","))
		}
		if v.TestArgs, err = fragment(args); err != nil {
			return manifests.Gateway{}, err
		}
		if v.TestRead, err = fragment(read); err != nil {
			return manifests.Gateway{}, err
		}
	}
	return v, nil
}
