package appctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/irohdial"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/manifests"
)

const (
	SshGatewaySet            = "ssh-gateway"
	SshGatewayApp            = "ssh-gateway"
	SshGatewayComponent      = "ssh-gateway"
	SshGatewayVaultServer    = "ssh-gateway"
	SshGatewayVaultClient    = "ssh-gateway"
	SshGatewayAccount        = "ssh-gateway"
	SshGatewayIrohDial       = "iroh-dial"
	SshGatewayIrohSocket     = "iroh-dial.sock"
	SshGatewayWebDir         = "ts/socialweb-computer-ssh/web"
	SshGatewayProfileNetwork = "market-network"
	SshGatewayProfileDefault = "default"
	DumbpipeExecutable       = "dumbpipe"
)

var (
	SshGatewayGVR      = schema.GroupVersionResource{Group: v1alpha1.GroupName, Version: v1alpha1.Version, Resource: "sshgateways"}
	SshGatewayProfiles = []string{SshGatewayProfileNetwork, SshGatewayProfileDefault}
	SshGatewayEnvAllow = []string{"KCP_SERVICE_DOMAIN", "KCP_DNS_TABLE", "KCP_DNS_TABLE_FILE", "KCP_TOKENS", "KCP_SERVER",
		"KCP_WORKSPACE", "KCP_NAMESPACE", "KCP_SERVICE_NAME", "PORT_FILE", "LISTENERS_FILE", "STATUS_FILE", "MIN_LOG_LEVEL", "ATPROTO_DID", "BUNDLED_ACTIONS_DIR", "POLICY_ENGINE_LOG_LEVEL", "PATH"}
)

func SshGatewayValues(spec v1alpha1.SshGatewaySpec, stateDir string, l devinstance.Layout, rec devinstance.Record) (manifests.SshGateway, error) {
	v := manifests.SshGateway{StateDir: stateDir}
	if !slices.Contains(SshGatewayProfiles, spec.Profile) {
		return v, fmt.Errorf("appctl: SshGateway profile %q: want one of %s", spec.Profile, strings.Join(SshGatewayProfiles, ", "))
	}
	v.WebDir = manifests.UnderTSRoot(SshGatewayWebDir)
	dumbpipe := rec.Executables[DumbpipeExecutable].Path
	if dumbpipe == "" {
		return v, errors.New("appctl: the instance has no dumbpipe executable for the iroh dialer (dev up --dumbpipe-path in a dev build)")
	}
	v.IrohDial = manifests.IrohDial{Unit: l.Names().HostProcessPrefix() + SshGatewayIrohDial + ".service",
		Socket: filepath.Join(stateDir, SshGatewayIrohSocket), Dumbpipe: dumbpipe}
	if err := (irohdial.Config{Socket: v.IrohDial.Socket, Dumbpipe: dumbpipe}).Check(); err != nil {
		return v, err
	}
	env, err := json.Marshal(SshGatewayEnvAllow)
	if err != nil {
		return v, err
	}
	v.EnvAllow = string(env)
	var extra, read []string
	if n := spec.Network; n != nil {
		v.CABundle = n.CaBundleRef
		for _, kv := range [][2]string{
			{"--plc-directory-url", n.PlcDirectoryUrl},
			{"--ingress-proxy-host", n.IngressProxyHost},
			{"--relay-url", strings.Join(n.RelayUrls, ",")},
			{"--network-hosts", NetworkHostsArg(n.Hosts)},
		} {
			if kv[1] != "" {
				extra = append(extra, kv[0], kv[1])
			}
		}
		if spec.Profile == SshGatewayProfileNetwork {
			aliases, err := guestAliases(n)
			if err != nil {
				return v, err
			}
			if len(aliases) > 0 {
				extra = append(extra, "--guest-host-aliases", strings.Join(aliases, ","))
			}
		}
	}
	if spec.Profile == SshGatewayProfileNetwork && (spec.Network == nil || v.CABundle == "") {
		return v, errors.New("appctl: the market-network profile needs the network value with its caBundleRef; the MarketNetwork composer copies it")
	}
	if len(spec.BidderDids) > 0 {
		extra = append(extra, "--bidder-dids", strings.Join(spec.BidderDids, ","))
	}
	if f := spec.OwnerSessionsFile; f != "" {
		if spec.Profile != SshGatewayProfileNetwork {
			return v, fmt.Errorf("appctl: ownerSessionsFile is a value of the %s profile only, not of %s", SshGatewayProfileNetwork, spec.Profile)
		}
		if !filepath.IsAbs(f) {
			return v, fmt.Errorf("appctl: ownerSessionsFile %q is not an absolute path", f)
		}
		extra, read = append(extra, "--test-owner-sessions-file", f), append(read, f)
	}
	opts, err := OptionArgs(SshGatewayApp, spec.Options)
	if err != nil {
		return v, err
	}
	extra = append(extra, opts...)
	if v.ExtraArgs, err = fragment(extra); err != nil {
		return v, err
	}
	if v.ExtraRead, err = fragment(read); err != nil {
		return v, err
	}
	return v, nil
}

func (ctl *Controller) sshGatewayKcp(ctx context.Context, stateDir string) (manifests.KcpRef, manifests.BidderVault, error) {
	if _, _, _, err := ctl.vaultRefs(ctx, SshGatewayVaultServer, SshGatewayVaultClient); err != nil {
		return manifests.KcpRef{}, manifests.BidderVault{}, err
	}
	vault := manifests.BidderVault{Server: SshGatewayVaultServer, Client: SshGatewayVaultClient}
	return manifests.KcpRef{Workspace: ctl.c.workspace()}, vault, nil
}

func (ctl *Controller) ownedHostProcesses(ctx context.Context, owner string) ([]unstructured.Unstructured, error) {
	ri, err := ctl.client(ctl.c.workspace(), HostProcessGVR)
	if err != nil {
		return nil, err
	}
	list, err := ri.List(ctx, metav1.ListOptions{LabelSelector: OwnerLabel + "=" + owner})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (ctl *Controller) deleteHostProcesses(ctx context.Context, owner string) (int, error) {
	items, err := ctl.ownedHostProcesses(ctx, owner)
	if err != nil {
		return 0, err
	}
	ri, err := ctl.client(ctl.c.workspace(), HostProcessGVR)
	if err != nil {
		return 0, err
	}
	for _, o := range items {
		if o.GetDeletionTimestamp() != nil {
			continue
		}
		if err := ri.Delete(ctx, o.GetName(), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return len(items), err
		}
	}
	return len(items), nil
}

func (ctl *Controller) reconcileSshGateway(ctx context.Context, u *unstructured.Unstructured) error {
	var o v1alpha1.SshGateway
	if err := fromUnstructured(u, &o); err != nil {
		return err
	}
	owner := ownerOf(u)
	ws := ctl.c.workspace() + ":" + ComponentWorkspace
	st := v1alpha1.ApplicationStatus{ObservedGeneration: o.Generation, Profile: o.Spec.Profile}
	if u.GetDeletionTimestamp() != nil {
		left, err := ctl.deletePods(ctx, ws, owner, nil)
		if err != nil {
			return err
		}
		if left == 0 {
			if left, err = ctl.deleteHostProcesses(ctx, owner); err != nil {
				return err
			}
		}
		if left == 0 {
			if left, err = ctl.deleteVaults(ctx, owner); err != nil {
				return err
			}
		}
		if left > 0 {
			st.Phase, st.Message = v1alpha1.ApplicationPhaseDeleting, fmt.Sprintf("%d component(s) stopping", left)
			return ctl.writeStatus(ctx, SshGatewayGVR, o.Name, st)
		}
		delete(ctl.applied, string(o.UID))
		ctl.forgetPlaced(owner)
		return ctl.setFinalizer(ctx, SshGatewayGVR, u, false)
	}
	if !hasFinalizer(u) {
		if err := ctl.setFinalizer(ctx, SshGatewayGVR, u, true); err != nil {
			return err
		}
	}
	stateDir := ComponentStateDir(ctl.c.Layout.StateDir, SshGatewayComponent)
	if ctl.applied[string(o.UID)] != o.Generation || ctl.lostComponent(ctx, owner) != "" {
		rec, err := ctl.c.Record()
		if err != nil {
			return err
		}
		values, err := SshGatewayValues(o.Spec, stateDir, ctl.c.Layout, rec)
		if err != nil {
			st.Phase, st.Message = v1alpha1.ApplicationPhaseFailed, err.Error()
			return ctl.writeStatus(ctx, SshGatewayGVR, o.Name, st)
		}
		st.Phase, st.Message = v1alpha1.ApplicationPhaseProvisioning, "applying its own OpenBaoServer, its iroh dialer and its workspace"
		ctl.writeStatus(ctx, SshGatewayGVR, o.Name, st)
		if _, err := ctl.applyProfile(ctx, SshGatewaySet, o.Spec.Profile, owner, false, &values); err != nil {
			st.Message = err.Error()
			return ctl.writeStatus(ctx, SshGatewayGVR, o.Name, st)
		}
		if values.Kcp, values.Vault, err = ctl.sshGatewayKcp(ctx, stateDir); err != nil {
			st.Message = "its vault and its kcp account: " + err.Error()
			return ctl.writeStatus(ctx, SshGatewayGVR, o.Name, st)
		}
		st.Message = "applying the " + o.Spec.Profile + " components"
		ctl.writeStatus(ctx, SshGatewayGVR, o.Name, st)
		pods, err := ctl.applyProfile(ctx, SshGatewaySet, o.Spec.Profile, owner, true, &values)
		if err != nil {
			st.Message = err.Error()
			return ctl.writeStatus(ctx, SshGatewayGVR, o.Name, st)
		}
		if _, err := ctl.deletePods(ctx, ws, owner, pods); err != nil {
			return err
		}
		ctl.applied[string(o.UID)] = o.Generation
		ctl.c.log().Info("applied an SshGateway's components", "name", o.Name, "profile", o.Spec.Profile, "denopods", pods)
	}
	pods, err := ctl.ownedPods(ctx, ws, owner)
	if err != nil {
		return err
	}
	allReady := len(pods) > 0
	for i := range pods {
		v := podViewOf(ws, &pods[i])
		st.Components = append(st.Components, v.component())
		st.Listeners = append(st.Listeners, v.Listeners...)
		allReady = allReady && v.Ready && len(v.Listeners) > 0
	}
	hps, err := ctl.ownedHostProcesses(ctx, owner)
	if err != nil {
		return err
	}
	dialer := false
	for _, h := range hps {
		phase, _, _ := unstructured.NestedString(h.Object, "status", "phase")
		running := phase == string(v1alpha1.HostProcessPhaseRunning)
		st.Components = append(st.Components, v1alpha1.ApplicationComponent{Kind: "HostProcess", Name: h.GetName(),
			Workspace: ctl.c.workspace(), Phase: phase, Ready: running})
		dialer = dialer || (h.GetName() == SshGatewayIrohDial && running)
	}
	switch {
	case allReady && dialer:
		st = ready(st, "the ssh gateway listens and its iroh dialer runs")
	case !dialer:
		st.Phase, st.Message = v1alpha1.ApplicationPhaseProvisioning, "waiting for the iroh dialer HostProcess to run"
	default:
		st.Phase, st.Message = v1alpha1.ApplicationPhaseProvisioning, "waiting for the ssh gateway DenoPod to report ready and its listeners"
	}
	return ctl.writeStatus(ctx, SshGatewayGVR, o.Name, st)
}
