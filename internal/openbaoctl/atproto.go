package openbaoctl

import (
	"regexp"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/baosync"
)

const AtprotoPluginAsset = "openbao-plugin-auth-atproto"

var accessorPlaceholder = regexp.MustCompile(`%accessor:([a-z0-9-]+)%`)

func AccessorPlaceholder(mount string) string { return "%accessor:" + strings.Trim(mount, "/") + "%" }

func renderAccessors(hcl string, accessors map[string]string) (string, []string) {
	var missing []string
	out := accessorPlaceholder.ReplaceAllStringFunc(hcl, func(m string) string {
		mount := accessorPlaceholder.FindStringSubmatch(m)[1]
		if a := accessors[mount]; a != "" {
			return a
		}
		missing = append(missing, mount)
		return m
	})
	return out, missing
}

func atprotoObjects(p placedPlugin) []baosync.Object {
	a := p.spec.Atproto
	if a == nil {
		return nil
	}
	mount := strings.Trim(p.spec.MountPath, "/")
	var out []baosync.Object
	if c := a.Config; c.ClientID != "" {
		data := map[string]any{"client_id": c.ClientID, "redirect_uris": c.RedirectURIs}
		put(data, "client_name", c.ClientName)
		put(data, "scope", c.Scope)
		put(data, "plc_directory_url", c.PLCDirectoryURL)
		put(data, "trusted_hosts", c.TrustedHosts)
		o := baosync.Object{Path: "auth/" + mount + "/config", Data: data}
		if c.TrustedCAPEM != "" {
			data["trusted_ca_pem"] = c.TrustedCAPEM
			o.WriteOnly = []string{"trusted_ca_pem"}
			o.Derived = map[string]any{"trusted_ca_set": true}
		}
		out = append(out, o)
	}
	for _, r := range a.Roles {
		data := map[string]any{"token_policies": r.Policies}
		put(data, "bound_dids", r.BoundDIDs)
		put(data, "bound_handles", r.BoundHandles)
		put(data, "token_ttl", r.TokenTTL)
		put(data, "token_max_ttl", r.TokenMaxTTL)
		out = append(out, baosync.Object{Path: "auth/" + mount + "/role/" + r.Name, Data: data})
	}
	return out
}

func atprotoPlugin(p v1alpha1.OpenBaoPlugin) bool { return p.Asset.Name == AtprotoPluginAsset }
