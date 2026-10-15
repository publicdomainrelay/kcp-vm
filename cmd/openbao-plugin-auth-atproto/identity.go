package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

var (
	handlePattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
	didPattern    = regexp.MustCompile(`^did:[a-z]+:[a-zA-Z0-9._:%-]*[a-zA-Z0-9._-]$`)
)

type didDocument struct {
	ID          string   `json:"id"`
	AlsoKnownAs []string `json:"alsoKnownAs"`
	Service     []struct {
		ID              string          `json:"id"`
		Type            string          `json:"type"`
		ServiceEndpoint json.RawMessage `json:"serviceEndpoint"`
	} `json:"service"`
}

type identity struct {
	DID    string
	Handle string
	PDS    string
}

type authServer struct {
	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	PAREndpoint           string
	RevocationEndpoint    string
	IssParameterSupported bool
}

func normalizeHandle(h string) (string, error) {
	h = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(h), "@"))
	if len(h) > 253 || !handlePattern.MatchString(h) {
		return "", fmt.Errorf("%q is not a valid handle", h)
	}
	return h, nil
}

func (f *fetcher) resolveHandle(ctx context.Context, handle string) (string, error) {
	var viaDNS, viaHTTP string
	cctx, cancel := context.WithTimeout(ctx, f.cfg.CallTimeout)
	txts, _ := net.DefaultResolver.LookupTXT(cctx, "_atproto."+handle)
	cancel()
	for _, t := range txts {
		if did, ok := strings.CutPrefix(strings.TrimSpace(t), "did="); ok && didPattern.MatchString(did) {
			viaDNS = did
			break
		}
	}
	if body, err := f.get(ctx, "https://"+handle+"/.well-known/atproto-did", "text/plain"); err == nil {
		if did := strings.TrimSpace(string(body)); didPattern.MatchString(did) {
			viaHTTP = did
		}
	}
	switch {
	case viaDNS != "" && viaHTTP != "" && viaDNS != viaHTTP:
		return "", fmt.Errorf("handle %q: DNS says %s and the well-known file says %s", handle, viaDNS, viaHTTP)
	case viaDNS != "":
		return viaDNS, nil
	case viaHTTP != "":
		return viaHTTP, nil
	}
	return "", fmt.Errorf("handle %q resolves to no DID (DNS TXT _atproto and https well-known tried)", handle)
}

func (f *fetcher) resolveDIDDocument(ctx context.Context, did string) (*didDocument, error) {
	if !didPattern.MatchString(did) {
		return nil, fmt.Errorf("%q is not a DID", did)
	}
	var target string
	switch {
	case strings.HasPrefix(did, "did:plc:"):
		target = f.cfg.PLCDirectoryURL + "/" + did
	case strings.HasPrefix(did, "did:web:"):
		parts := strings.Split(strings.TrimPrefix(did, "did:web:"), ":")
		host, err := url.PathUnescape(parts[0])
		if err != nil || host == "" {
			return nil, fmt.Errorf("%q is not a did:web", did)
		}
		path := "/.well-known/did.json"
		if len(parts) > 1 {
			segs := make([]string, len(parts)-1)
			for i, p := range parts[1:] {
				segs[i], _ = url.PathUnescape(p)
			}
			path = "/" + strings.Join(segs, "/") + "/did.json"
		}
		target = "https://" + host + path
	default:
		return nil, fmt.Errorf("DID method of %q is not supported (did:plc and did:web)", did)
	}
	body, err := f.get(ctx, target, "application/did+json, application/json")
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", did, err)
	}
	var doc didDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("resolve %s: %w", did, err)
	}
	if doc.ID != did {
		return nil, fmt.Errorf("resolve %s: the document is for %q", did, doc.ID)
	}
	return &doc, nil
}

func (d *didDocument) pds(f *fetcher) (string, error) {
	for _, s := range d.Service {
		if !strings.HasSuffix(s.ID, "#atproto_pds") || s.Type != "AtprotoPersonalDataServer" {
			continue
		}
		var endpoint string
		if err := json.Unmarshal(s.ServiceEndpoint, &endpoint); err != nil {
			continue
		}
		u, err := f.checkURL(endpoint)
		if err != nil {
			return "", fmt.Errorf("PDS endpoint of %s: %w", d.ID, err)
		}
		if (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
			return "", fmt.Errorf("PDS endpoint %q of %s is not an origin", endpoint, d.ID)
		}
		return u.Scheme + "://" + u.Host, nil
	}
	return "", fmt.Errorf("the DID document of %s has no #atproto_pds service", d.ID)
}

func (d *didDocument) hasHandle(handle string) bool {
	for _, a := range d.AlsoKnownAs {
		if h, ok := strings.CutPrefix(strings.ToLower(a), "at://"); ok && h == handle {
			return true
		}
	}
	return false
}

func (f *fetcher) identityFromHandle(ctx context.Context, handle string) (*identity, error) {
	handle, err := normalizeHandle(handle)
	if err != nil {
		return nil, err
	}
	did, err := f.resolveHandle(ctx, handle)
	if err != nil {
		return nil, err
	}
	doc, err := f.resolveDIDDocument(ctx, did)
	if err != nil {
		return nil, err
	}
	if !doc.hasHandle(handle) {
		return nil, fmt.Errorf("handle %q resolves to %s, whose DID document does not claim that handle", handle, did)
	}
	pds, err := doc.pds(f)
	if err != nil {
		return nil, err
	}
	return &identity{DID: did, Handle: handle, PDS: pds}, nil
}

func (f *fetcher) identityFromDID(ctx context.Context, did string) (*identity, error) {
	doc, err := f.resolveDIDDocument(ctx, did)
	if err != nil {
		return nil, err
	}
	pds, err := doc.pds(f)
	if err != nil {
		return nil, err
	}
	return &identity{DID: did, PDS: pds}, nil
}

func (f *fetcher) discoverAuthServer(ctx context.Context, pds string) (*authServer, error) {
	body, err := f.get(ctx, pds+"/.well-known/oauth-protected-resource", "application/json")
	if err != nil {
		return nil, fmt.Errorf("protected resource metadata of %s: %w", pds, err)
	}
	var resource struct {
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := json.Unmarshal(body, &resource); err != nil {
		return nil, err
	}
	if len(resource.AuthorizationServers) != 1 {
		return nil, fmt.Errorf("protected resource metadata of %s names %d authorization servers, want 1", pds, len(resource.AuthorizationServers))
	}
	issuer := strings.TrimRight(resource.AuthorizationServers[0], "/")
	u, err := f.checkURL(issuer)
	if err != nil {
		return nil, fmt.Errorf("authorization server of %s: %w", pds, err)
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return nil, fmt.Errorf("authorization server %q is not an origin", issuer)
	}
	body, err = f.get(ctx, issuer+"/.well-known/oauth-authorization-server", "application/json")
	if err != nil {
		return nil, fmt.Errorf("authorization server metadata of %s: %w", issuer, err)
	}
	var meta struct {
		Issuer                            string   `json:"issuer"`
		AuthorizationEndpoint             string   `json:"authorization_endpoint"`
		TokenEndpoint                     string   `json:"token_endpoint"`
		PAREndpoint                       string   `json:"pushed_authorization_request_endpoint"`
		RevocationEndpoint                string   `json:"revocation_endpoint"`
		RequirePAR                        bool     `json:"require_pushed_authorization_requests"`
		ResponseTypes                     []string `json:"response_types_supported"`
		GrantTypes                        []string `json:"grant_types_supported"`
		CodeChallengeMethods              []string `json:"code_challenge_methods_supported"`
		TokenEndpointAuthMethods          []string `json:"token_endpoint_auth_methods_supported"`
		TokenEndpointAuthSigningAlgs      []string `json:"token_endpoint_auth_signing_alg_values_supported"`
		DPoPSigningAlgs                   []string `json:"dpop_signing_alg_values_supported"`
		Scopes                            []string `json:"scopes_supported"`
		IssParameterSupported             bool     `json:"authorization_response_iss_parameter_supported"`
		ClientIDMetadataDocumentSupported bool     `json:"client_id_metadata_document_supported"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return nil, err
	}
	if strings.TrimRight(meta.Issuer, "/") != issuer {
		return nil, fmt.Errorf("authorization server metadata of %s says issuer %q", issuer, meta.Issuer)
	}
	needs := []struct {
		ok   bool
		what string
	}{
		{contains(meta.ResponseTypes, "code"), "response type code"},
		{contains(meta.GrantTypes, "authorization_code") && contains(meta.GrantTypes, "refresh_token"), "grant types authorization_code and refresh_token"},
		{contains(meta.CodeChallengeMethods, "S256"), "PKCE S256"},
		{contains(meta.TokenEndpointAuthMethods, "private_key_jwt"), "token endpoint auth private_key_jwt"},
		{contains(meta.TokenEndpointAuthSigningAlgs, "ES256"), "client assertion alg ES256"},
		{contains(meta.DPoPSigningAlgs, "ES256"), "DPoP alg ES256"},
		{contains(meta.Scopes, "atproto"), "scope atproto"},
		{meta.RequirePAR && meta.PAREndpoint != "", "required pushed authorization requests"},
		{meta.ClientIDMetadataDocumentSupported, "client metadata documents as client ids"},
	}
	for _, n := range needs {
		if !n.ok {
			return nil, fmt.Errorf("authorization server %s does not support %s", issuer, n.what)
		}
	}
	for label, endpoint := range map[string]string{"authorization_endpoint": meta.AuthorizationEndpoint, "token_endpoint": meta.TokenEndpoint, "pushed_authorization_request_endpoint": meta.PAREndpoint} {
		if endpoint == "" {
			return nil, fmt.Errorf("authorization server %s has no %s", issuer, label)
		}
		if _, err := f.checkURL(endpoint); err != nil {
			return nil, fmt.Errorf("authorization server %s, %s: %w", issuer, label, err)
		}
	}
	if meta.RevocationEndpoint != "" {
		if _, err := f.checkURL(meta.RevocationEndpoint); err != nil {
			return nil, fmt.Errorf("authorization server %s, revocation_endpoint: %w", issuer, err)
		}
	}
	return &authServer{
		Issuer:                issuer,
		AuthorizationEndpoint: meta.AuthorizationEndpoint,
		TokenEndpoint:         meta.TokenEndpoint,
		PAREndpoint:           meta.PAREndpoint,
		RevocationEndpoint:    meta.RevocationEndpoint,
		IssParameterSupported: meta.IssParameterSupported,
	}, nil
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
