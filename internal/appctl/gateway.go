package appctl

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
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
)

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
		Clients:  []v1alpha1.OpenBaoServerClient{{Name: vaultlayout.RootClient, Policies: []string{vaultlayout.PolicyRootWriter}}},
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
		left, err := ctl.deleteVaults(ctx, owner)
		if err != nil {
			return err
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
	if ctl.c.Listeners != nil {
		st.Listeners = ctl.c.Listeners(owner)
	}
	switch {
	case !vaultReady:
		st.Phase, st.Message = v1alpha1.ApplicationPhaseProvisioning, "waiting for the root vault to be Ready and synced"
	case ctl.c.Listeners != nil && len(st.Listeners) == 0:
		st.Phase, st.Message = v1alpha1.ApplicationPhaseProvisioning, "the root vault is Ready; waiting for the core's members to report their listeners"
	default:
		st = ready(st, "the root vault is Ready and every member reported its listener")
	}
	return ctl.writeStatus(ctx, GatewayGVR, o.Name, st)
}
