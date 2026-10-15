package appctl

import (
	"fmt"
	"os"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbaoctl"
)

const (
	AtprotoPluginName         = "atproto"
	AtprotoMountPath          = "atproto"
	AtprotoUserRole           = "user"
	AtprotoSelfPolicy         = "atproto-self"
	AtprotoSessionProxyPolicy = "atproto-session-proxy"
	AtprotoClientMetadataPath = "/oauth-client-metadata.json"
	AtprotoCallbackPath       = "/oauth/callback"
	AtprotoUserTokenTTL       = "1h"
	AtprotoUserTokenMaxTTL    = "720h"
	atprotoClientName         = "socialweb-computer"
	publicOriginOption        = "public-origin"
)

func optionValue(opts []v1alpha1.ApplicationOption, name string) string {
	for _, o := range opts {
		if o.Name == name {
			return strings.TrimRight(strings.TrimSpace(o.Value), "/")
		}
	}
	return ""
}

func AtprotoPolicies() []v1alpha1.OpenBaoPolicy {
	own := "auth/" + AtprotoMountPath + "/session/{{identity.entity.aliases." + openbaoctl.AccessorPlaceholder(AtprotoMountPath) + ".name}}"
	anyDID := "auth/" + AtprotoMountPath + "/session/+"
	cu := []string{"create", "update"}
	return []v1alpha1.OpenBaoPolicy{
		{Name: AtprotoSelfPolicy, Rules: []v1alpha1.OpenBaoPolicyRule{
			{Path: own, Capabilities: []string{"read", "delete"}},
			{Path: own + "/proof", Capabilities: cu},
		}},
		{Name: AtprotoSessionProxyPolicy, Rules: []v1alpha1.OpenBaoPolicyRule{
			{Path: anyDID, Capabilities: []string{"read"}},
			{Path: anyDID + "/proof", Capabilities: cu},
		}},
	}
}

func atprotoTrust(n *v1alpha1.MarketNetworkValue) (plc string, hosts []string, caPEM string, err error) {
	if n == nil {
		return "", nil, "", nil
	}
	for _, h := range n.Hosts {
		switch {
		case strings.HasPrefix(h.Name, "plc."):
			plc = "https://" + h.Name
			hosts = append(hosts, h.Name+"="+h.Address)
		case strings.HasPrefix(h.Name, "pds."):
			hosts = append(hosts, h.Name+"="+h.Address)
		}
	}
	if plc == "" && n.PlcDirectoryUrl != "" {
		plc = n.PlcDirectoryUrl
		if rest, ok := strings.CutPrefix(plc, "https://"); ok {
			hosts = append(hosts, strings.TrimRight(rest, "/"))
		}
	}
	if n.CaBundleRef != "" && len(hosts) > 0 {
		raw, rerr := os.ReadFile(n.CaBundleRef)
		if rerr != nil {
			return "", nil, "", fmt.Errorf("appctl: the network CA bundle for the atproto plugin: %w", rerr)
		}
		caPEM = strings.TrimSpace(string(raw))
	}
	return plc, hosts, caPEM, nil
}

func AtprotoPlugin(opts []v1alpha1.ApplicationOption, n *v1alpha1.MarketNetworkValue) (v1alpha1.OpenBaoPlugin, error) {
	plc, hosts, caPEM, err := atprotoTrust(n)
	if err != nil {
		return v1alpha1.OpenBaoPlugin{}, err
	}
	a := &v1alpha1.OpenBaoAtprotoPlugin{Roles: []v1alpha1.OpenBaoAtprotoRole{{Name: AtprotoUserRole, BoundDIDs: []string{"*"},
		Policies: []string{AtprotoSelfPolicy}, TokenTTL: AtprotoUserTokenTTL, TokenMaxTTL: AtprotoUserTokenMaxTTL}}}
	if origin := optionValue(opts, publicOriginOption); origin != "" {
		a.Config = v1alpha1.OpenBaoAtprotoConfig{ClientID: origin + AtprotoClientMetadataPath, RedirectURIs: []string{origin + AtprotoCallbackPath},
			ClientName: atprotoClientName, PLCDirectoryURL: plc, TrustedHosts: hosts, TrustedCAPEM: caPEM}
	}
	return v1alpha1.OpenBaoPlugin{Name: AtprotoPluginName, Type: "auth", Asset: v1alpha1.HostProcessAsset{Name: openbaoctl.AtprotoPluginAsset},
		MountPath: AtprotoMountPath, Atproto: a}, nil
}
