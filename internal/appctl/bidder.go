package appctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/manifests"
)

const (
	BidderSet       = "market-bidder"
	BidderComponent = "bidder"
	BidderApp       = "market-bidder"
	DefaultPolicy   = "open"
)

func fragment(items []string) (string, error) {
	if len(items) == 0 {
		return "", nil
	}
	var b strings.Builder
	for _, it := range items {
		q, err := json.Marshal(it)
		if err != nil {
			return "", err
		}
		b.WriteString(",")
		b.Write(q)
	}
	return b.String(), nil
}

func portOf(rawURL string) (int, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, err
	}
	_, p, err := net.SplitHostPort(u.Host)
	if err != nil {
		return 0, fmt.Errorf("%s names no port", rawURL)
	}
	var n int
	if _, err := fmt.Sscanf(p, "%d", &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("%s names no port", rawURL)
	}
	return n, nil
}

func BidderValues(spec v1alpha1.MarketBidderSpec, stateDir string, kcp BidderKcp) (manifests.MarketBidder, error) {
	n := spec.Network
	if n == nil {
		return manifests.MarketBidder{}, errors.New("appctl: the MarketBidder names no network value; its composer or its profile sets spec.network")
	}
	if spec.Firecracker == nil && spec.Kcp == nil {
		return manifests.MarketBidder{}, errors.New("appctl: the MarketBidder names no provider (spec.firecracker or spec.kcp)")
	}
	if n.GuestTlsUrl == "" {
		return manifests.MarketBidder{}, errors.New("appctl: the network value names no guestTlsUrl; a bidder hands guests the dispatcher's TLS front")
	}
	tlsPort, err := portOf(n.GuestTlsUrl)
	if err != nil {
		return manifests.MarketBidder{}, fmt.Errorf("appctl: network guestTlsUrl: %w", err)
	}
	v := manifests.MarketBidder{StateDir: stateDir, Handle: spec.Identity.Handle, PdsUrl: spec.Identity.PdsUrl,
		PlcDirectoryUrl: n.PlcDirectoryUrl, IngressProxyHost: n.IngressProxyHost, RelayUrls: strings.Join(n.RelayUrls, ","),
		GuestTLSPort: tlsPort, CABundle: n.CaBundleRef, Policy: spec.Policy}
	if err := providerValues(&v, spec, kcp); err != nil {
		return manifests.MarketBidder{}, err
	}
	if v.Policy == "" {
		v.Policy = DefaultPolicy
	}
	var extra []string
	if len(n.FirehoseUrls) > 0 {
		extra = append(extra, "--firehose-url", strings.Join(n.FirehoseUrls, ","))
	}
	if n.QrOrigin != "" {
		extra = append(extra, "--qr-origin", n.QrOrigin)
	}
	if hosts := NetworkHostsArg(n.Hosts); hosts != "" {
		extra = append(extra, "--network-hosts", hosts)
	}
	opts, err := OptionArgs(BidderApp, spec.Options)
	if err != nil {
		return manifests.MarketBidder{}, err
	}
	extra = append(extra, opts...)
	if v.ExtraArgs, err = fragment(extra); err != nil {
		return manifests.MarketBidder{}, err
	}
	return v, nil
}

func ready(status v1alpha1.ApplicationStatus, msg string) v1alpha1.ApplicationStatus {
	status.Phase, status.Message = v1alpha1.ApplicationPhaseReady, msg
	return status
}

func (ctl *Controller) reconcileBidder(ctx context.Context, u *unstructured.Unstructured) error {
	var o v1alpha1.MarketBidder
	if err := fromUnstructured(u, &o); err != nil {
		return err
	}
	owner := ownerOf(u)
	ws := ctl.c.workspace() + ":" + ComponentWorkspace
	st := v1alpha1.ApplicationStatus{ObservedGeneration: o.Generation, Profile: o.Spec.Profile}
	if u.GetDeletionTimestamp() != nil {
		vms, err := ctl.deleteComputeVMs(ctx)
		if err != nil {
			return err
		}
		if vms > 0 {
			st.Phase, st.Message = v1alpha1.ApplicationPhaseDeleting, fmt.Sprintf("%d ComputeVM(s) releasing", vms)
			return ctl.writeStatus(ctx, MarketBidderGVR, o.Name, st)
		}
		left, err := ctl.deletePods(ctx, ws, owner, nil)
		if err != nil {
			return err
		}
		if left == 0 {
			if left, err = ctl.deleteVaults(ctx, owner); err != nil {
				return err
			}
		}
		if left > 0 {
			st.Phase, st.Message = v1alpha1.ApplicationPhaseDeleting, fmt.Sprintf("%d component(s) stopping", left)
			return ctl.writeStatus(ctx, MarketBidderGVR, o.Name, st)
		}
		delete(ctl.applied, string(o.UID))
		return ctl.setFinalizer(ctx, MarketBidderGVR, u, false)
	}
	if !hasFinalizer(u) {
		if err := ctl.setFinalizer(ctx, MarketBidderGVR, u, true); err != nil {
			return err
		}
	}
	stateDir := ComponentStateDir(ctl.c.Layout.StateDir, BidderComponent)
	if ctl.applied[string(o.UID)] != o.Generation {
		var kcp BidderKcp
		if rec, err := ctl.c.Record(); err == nil {
			kcp = BidderKcpOf(rec)
		}
		kcp.TokenFile, kcp.CAFile = BidderTokenPaths(stateDir)
		values, err := BidderValues(o.Spec, stateDir, kcp)
		if err != nil {
			st.Phase, st.Message = v1alpha1.ApplicationPhaseFailed, err.Error()
			return ctl.writeStatus(ctx, MarketBidderGVR, o.Name, st)
		}
		st.Phase, st.Message = v1alpha1.ApplicationPhaseProvisioning, "applying its own OpenBaoServer and workspace"
		ctl.writeStatus(ctx, MarketBidderGVR, o.Name, st)
		if _, err := ctl.applyProfile(ctx, BidderSet, o.Spec.Profile, owner, false, &values); err != nil {
			st.Message = err.Error()
			return ctl.writeStatus(ctx, MarketBidderGVR, o.Name, st)
		}
		if err := ctl.bidderToken(ctx, values.StateDir); err != nil {
			st.Message = "its kcp token: " + err.Error()
			return ctl.writeStatus(ctx, MarketBidderGVR, o.Name, st)
		}
		vault, err := ctl.bidderVault(ctx, o.Spec, values.StateDir)
		if err != nil {
			st.Message = "its vault and its PDS account: " + err.Error()
			return ctl.writeStatus(ctx, MarketBidderGVR, o.Name, st)
		}
		values.Vault = vault
		st.Message = "applying the " + o.Spec.Profile + " components"
		ctl.writeStatus(ctx, MarketBidderGVR, o.Name, st)
		pods, err := ctl.applyProfile(ctx, BidderSet, o.Spec.Profile, owner, true, &values)
		if err != nil {
			st.Phase, st.Message = v1alpha1.ApplicationPhaseProvisioning, err.Error()
			return ctl.writeStatus(ctx, MarketBidderGVR, o.Name, st)
		}
		if _, err := ctl.deletePods(ctx, ws, owner, pods); err != nil {
			return err
		}
		ctl.applied[string(o.UID)] = o.Generation
		ctl.c.log().Info("applied a MarketBidder's components", "name", o.Name, "profile", o.Spec.Profile, "denopods", pods,
			"plc", o.Spec.Network.PlcDirectoryUrl)
	}
	if err := ctl.bidderToken(ctx, stateDir); err != nil {
		ctl.c.log().Warn("renewing the bidder's kcp token", "name", o.Name, "err", err.Error())
	}
	if err := ctl.rotateSigningKey(ctx, o); err != nil {
		ctl.c.log().Warn("rotating the bidder's OIDC signing key", "name", o.Name, "err", err.Error())
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
		allReady = allReady && v.Ready
		if v.Name == BidderComponent {
			if did, _ := ctl.runStatus(v.RunID)["did"].(string); did != "" {
				st.Did = did
			}
		}
	}
	if o.Spec.Kcp != nil {
		st.Components = append(st.Components, ctl.guestComponents(ctx)...)
	}
	switch {
	case allReady && st.Did != "":
		st = ready(st, "every component is ready")
	default:
		st.Phase, st.Message = v1alpha1.ApplicationPhaseProvisioning, "waiting for the components to report ready and the bidder its did"
	}
	return ctl.writeStatus(ctx, MarketBidderGVR, o.Name, st)
}
