package vaultmain

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

type mountInfo struct {
	Type    string         `json:"type"`
	Options map[string]any `json:"options"`
}

type api struct {
	addr  string
	token string
	http  *http.Client
}

// ponytail: a second thin client beside internal/openbao because the layout needs writes that
// client does not expose (mount max_lease_ttl, full PKI roles, full AppRoles, sys/mounts and
// sys/auth listing, and reading pki/ca/pem to decide whether the root is already generated);
// fold these methods into internal/openbao when that package stops being frozen for this change
func newAPI(addr, token string, tlsConf *tls.Config) *api {
	hc := http.DefaultClient
	if tlsConf != nil {
		hc = &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConf}}
	}
	return &api{
		addr:  strings.TrimRight(addr, "/"),
		token: token,
		http:  hc,
	}
}

func (a *api) raw(ctx context.Context, method, path string, in any) (int, []byte, error) {
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return 0, nil, fmt.Errorf("socialweb-vault: encode %s /v1/%s: %w", method, path, err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.addr+"/v1/"+path, body)
	if err != nil {
		return 0, nil, fmt.Errorf("socialweb-vault: build %s /v1/%s: %w", method, path, err)
	}
	if a.token != "" {
		req.Header.Set("X-Vault-Token", a.token)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("socialweb-vault: %s /v1/%s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("socialweb-vault: read %s /v1/%s: %w", method, path, err)
	}
	return resp.StatusCode, data, nil
}

func (a *api) mounts(ctx context.Context) (map[string]mountInfo, error) {
	status, body, err := a.raw(ctx, http.MethodGet, "sys/mounts", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("openbao: GET /v1/sys/mounts -> HTTP %d: %s", status, strings.TrimSpace(string(body)))
	}
	var envelope struct {
		Data map[string]mountInfo `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("openbao: sys/mounts returned a body that is not JSON: %v (%s)", err, strings.TrimSpace(string(body)))
	}
	return envelope.Data, nil
}

func (a *api) auths(ctx context.Context) (map[string]mountInfo, error) {
	status, body, err := a.raw(ctx, http.MethodGet, "sys/auth", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("openbao: GET /v1/sys/auth -> HTTP %d: %s", status, strings.TrimSpace(string(body)))
	}
	var envelope struct {
		Data map[string]mountInfo `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("openbao: sys/auth returned a body that is not JSON: %v (%s)", err, strings.TrimSpace(string(body)))
	}
	return envelope.Data, nil
}

func (a *api) createMount(ctx context.Context, m vaultlayout.Mount) error {
	body := map[string]any{"type": mountType(m.Kind)}
	switch m.Kind {
	case vaultlayout.MountKV:
		body["options"] = map[string]any{"version": "2"}
	case vaultlayout.MountPKI:
		if m.MaxLeaseTTL > 0 {
			body["config"] = map[string]any{"max_lease_ttl": m.MaxLeaseTTL.String()}
		}
	}
	path := strings.Trim(m.Path, "/")
	status, resp, err := a.raw(ctx, http.MethodPut, "sys/mounts/"+path, body)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("openbao: PUT /v1/sys/mounts/%s -> HTTP %d: %s", path, status, strings.TrimSpace(string(resp)))
	}
	return nil
}

func (a *api) enableAppRole(ctx context.Context) error {
	status, resp, err := a.raw(ctx, http.MethodPut, "sys/auth/approle", map[string]any{"type": "approle"})
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("openbao: PUT /v1/sys/auth/approle -> HTTP %d: %s", status, strings.TrimSpace(string(resp)))
	}
	return nil
}

func (a *api) pkiRootPresent(ctx context.Context, mount string) (bool, error) {
	path := strings.Trim(mount, "/") + "/ca/pem"
	status, body, err := a.raw(ctx, http.MethodGet, path, nil)
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusOK:
		return len(bytes.TrimSpace(body)) > 0, nil
	case http.StatusNoContent, http.StatusNotFound:
		return false, nil
	default:
		lower := strings.ToLower(string(body))
		if strings.Contains(lower, "no ca") || strings.Contains(lower, "not found") || strings.Contains(lower, "no certificate") {
			return false, nil
		}
		return false, fmt.Errorf("openbao: GET /v1/%s -> HTTP %d: %s", path, status, strings.TrimSpace(string(body)))
	}
}

func (a *api) writePKIRole(ctx context.Context, role vaultlayout.PKIRole) error {
	body := map[string]any{
		"allowed_domains":    role.AllowedDomains,
		"allow_subdomains":   role.AllowSubdomains,
		"allow_bare_domains": role.AllowBareDomains,
		"allow_ip_sans":      role.AllowIPSANs,
		"enforce_hostnames":  role.EnforceHostnames,
		"require_cn":         role.RequireCN,
		"client_flag":        role.ClientAuth,
		"server_flag":        role.ServerAuth,
		"key_usage":          role.KeyUsage,
	}
	if role.KeyType != "" {
		body["key_type"] = pkiKeyType(role.KeyType)
	}
	if role.KeyBits > 0 {
		body["key_bits"] = role.KeyBits
	}
	if role.TTL > 0 {
		body["ttl"] = role.TTL.String()
	}
	if role.MaxTTL > 0 {
		body["max_ttl"] = role.MaxTTL.String()
	}
	path := strings.Trim(role.Mount, "/") + "/roles/" + strings.Trim(role.Name, "/")
	status, resp, err := a.raw(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("openbao: POST /v1/%s -> HTTP %d: %s", path, status, strings.TrimSpace(string(resp)))
	}
	return nil
}

func (a *api) writeAppRole(ctx context.Context, role vaultlayout.AppRole) error {
	body := map[string]any{"token_policies": role.Policies}
	if role.TokenTTL > 0 {
		body["token_ttl"] = role.TokenTTL.String()
	}
	if role.TokenMaxTTL > 0 {
		body["token_max_ttl"] = role.TokenMaxTTL.String()
	}
	if role.SecretIDTTL > 0 {
		body["secret_id_ttl"] = role.SecretIDTTL.String()
	}
	path := "auth/approle/role/" + strings.Trim(role.Name, "/")
	status, resp, err := a.raw(ctx, http.MethodPut, path, body)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("openbao: PUT /v1/%s -> HTTP %d: %s", path, status, strings.TrimSpace(string(resp)))
	}
	return nil
}

func pkiKeyType(layout string) string {
	if strings.EqualFold(layout, "ecdsa") {
		return "ec"
	}
	return strings.ToLower(layout)
}

func mountType(kind vaultlayout.MountKind) string {
	switch kind {
	case vaultlayout.MountKV:
		return "kv"
	case vaultlayout.MountPKI:
		return "pki"
	default:
		return string(kind)
	}
}

func mountMatches(info mountInfo, m vaultlayout.Mount) bool {
	if info.Type != mountType(m.Kind) {
		return false
	}
	if m.Kind == vaultlayout.MountKV {
		version, _ := info.Options["version"].(string)
		return version == "2"
	}
	return true
}
