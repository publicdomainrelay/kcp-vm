package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const (
	configKey            = "config"
	defaultScope         = "atproto"
	defaultPLCDirectory  = "https://plc.directory"
	defaultCallTimeout   = 5 * time.Second
	defaultLoginTimeout  = 15 * time.Second
	maxCallTimeout       = 30 * time.Second
	maxLoginTimeout      = 2 * time.Minute
	maxRefreshLeeway     = time.Hour
	retiredKeyGrace      = 24 * time.Hour
	clientAssertionTTL   = 60 * time.Second
	clientAssertionType  = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	clientMetadataSchema = "private_key_jwt"
)

type clientKey struct {
	KID        string          `json:"kid"`
	PrivateJWK json.RawMessage `json:"private_jwk"`
	CreatedAt  time.Time       `json:"created_at"`
	RetiredAt  time.Time       `json:"retired_at,omitempty"`
}

type configEntry struct {
	ClientID        string            `json:"client_id"`
	RedirectURIs    []string          `json:"redirect_uris"`
	Scope           string            `json:"scope"`
	ClientName      string            `json:"client_name"`
	PLCDirectoryURL string            `json:"plc_directory_url"`
	CallTimeout     time.Duration     `json:"call_timeout"`
	LoginTimeout    time.Duration     `json:"login_timeout"`
	RefreshLeeway   time.Duration     `json:"refresh_leeway"`
	TrustedHosts    map[string]string `json:"trusted_hosts"`
	TrustedCAPEM    string            `json:"trusted_ca_pem"`
	Keys            []clientKey       `json:"keys"`
}

func getConfig(ctx context.Context, s logical.Storage) (*configEntry, error) {
	raw, err := s.Get(ctx, configKey)
	if err != nil || raw == nil {
		return nil, err
	}
	var c configEntry
	if err := raw.DecodeJSON(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

func putConfig(ctx context.Context, s logical.Storage, c *configEntry) error {
	entry, err := logical.StorageEntryJSON(configKey, c)
	if err != nil {
		return err
	}
	return s.Put(ctx, entry)
}

func requireConfig(ctx context.Context, s logical.Storage) (*configEntry, *logical.Response, error) {
	c, err := getConfig(ctx, s)
	if err != nil {
		return nil, nil, err
	}
	if c == nil || len(c.Keys) == 0 {
		return nil, logical.ErrorResponse("the mount has no config yet: write auth/<mount>/config first"), nil
	}
	return c, nil, nil
}

func newClientKey(now time.Time) (clientKey, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return clientKey{}, err
	}
	jwk := jose.JSONWebKey{Key: priv, Algorithm: string(jose.ES256), Use: "sig"}
	thumb, err := jwk.Thumbprint(crypto.SHA256)
	if err != nil {
		return clientKey{}, err
	}
	jwk.KeyID = base64.RawURLEncoding.EncodeToString(thumb)
	raw, err := json.Marshal(jwk)
	if err != nil {
		return clientKey{}, err
	}
	return clientKey{KID: jwk.KeyID, PrivateJWK: raw, CreatedAt: now}, nil
}

func (k clientKey) signingKey() (*jose.JSONWebKey, error) {
	var jwk jose.JSONWebKey
	if err := json.Unmarshal(k.PrivateJWK, &jwk); err != nil {
		return nil, err
	}
	return &jwk, nil
}

func (k clientKey) publicJWK() (map[string]interface{}, error) {
	jwk, err := k.signingKey()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(jwk.Public())
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *configEntry) currentKey() clientKey { return c.Keys[0] }

func (c *configEntry) publishedKeys(now time.Time) []clientKey {
	var out []clientKey
	for i, k := range c.Keys {
		if i == 0 || k.RetiredAt.IsZero() || now.Sub(k.RetiredAt) < retiredKeyGrace {
			out = append(out, k)
		}
	}
	return out
}

func (c *configEntry) pruneKeys(now time.Time) {
	c.Keys = c.publishedKeys(now)
}

func parseTrustedHosts(entries []string) (map[string]string, error) {
	out := map[string]string{}
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		name, addr, _ := strings.Cut(e, "=")
		name, addr = strings.ToLower(strings.TrimSpace(name)), strings.TrimSpace(addr)
		if name == "" || strings.ContainsAny(name, "/@ ") {
			return nil, fmt.Errorf("trusted_hosts entry %q: want host, host:port, host=address or host:port=address", e)
		}
		out[name] = addr
	}
	return out, nil
}

func checkClientURL(label, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("%s %q is not a URL without userinfo and fragment", label, raw)
	}
	return nil
}

func pathConfig(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "config$",
		Fields: map[string]*framework.FieldSchema{
			"client_id":         {Type: framework.TypeString, Description: "The https URL of the client metadata document this mount serves (required at the first write)."},
			"redirect_uris":     {Type: framework.TypeCommaStringSlice, Description: "Redirect URIs of the client (required at the first write)."},
			"scope":             {Type: framework.TypeString, Description: "OAuth scope to ask for. Defaults to atproto."},
			"client_name":       {Type: framework.TypeString, Description: "Human name in the client metadata."},
			"plc_directory_url": {Type: framework.TypeString, Description: "PLC directory base URL. Defaults to https://plc.directory."},
			"call_timeout":      {Type: framework.TypeDurationSecond, Description: "Timeout of one outbound call. Defaults to 5s."},
			"login_timeout":     {Type: framework.TypeDurationSecond, Description: "Timeout of a whole login callback. Defaults to 15s."},
			"refresh_leeway":    {Type: framework.TypeDurationSecond, Description: "A session proof refreshes the access token when it has less than this left. Defaults to 60s."},
			"trusted_hosts":     {Type: framework.TypeCommaStringSlice, Description: "Hosts exempt from the https-only and public-address rules, each host, host:port, host=address or host:port=address (address is where to connect)."},
			"trusted_ca_pem":    {Type: framework.TypeString, Description: "PEM root certificates trusted, besides the system roots, for the trusted hosts only."},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.CreateOperation: &framework.PathOperation{Callback: b.configWrite},
			logical.UpdateOperation: &framework.PathOperation{Callback: b.configWrite},
			logical.ReadOperation:   &framework.PathOperation{Callback: b.configRead},
		},
		ExistenceCheck: func(ctx context.Context, req *logical.Request, _ *framework.FieldData) (bool, error) {
			c, err := getConfig(ctx, req.Storage)
			return c != nil, err
		},
		HelpSynopsis: "Configures the OAuth client of this mount. The client key is made at the first write and never leaves storage.",
	}
}

func (b *backend) configWrite(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	b.lock.Lock()
	defer b.lock.Unlock()
	c, err := getConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if c == nil {
		c = &configEntry{Scope: defaultScope, PLCDirectoryURL: defaultPLCDirectory, CallTimeout: defaultCallTimeout, LoginTimeout: defaultLoginTimeout, RefreshLeeway: refreshLeeway, TrustedHosts: map[string]string{}}
		key, err := newClientKey(now)
		if err != nil {
			return nil, err
		}
		c.Keys = []clientKey{key}
	}
	if v, ok := d.GetOk("client_id"); ok {
		c.ClientID = strings.TrimSpace(v.(string))
	}
	if v, ok := d.GetOk("redirect_uris"); ok {
		c.RedirectURIs = v.([]string)
	}
	if v, ok := d.GetOk("scope"); ok {
		c.Scope = strings.TrimSpace(v.(string))
	}
	if v, ok := d.GetOk("client_name"); ok {
		c.ClientName = strings.TrimSpace(v.(string))
	}
	if v, ok := d.GetOk("plc_directory_url"); ok {
		c.PLCDirectoryURL = strings.TrimRight(strings.TrimSpace(v.(string)), "/")
	}
	if v, ok := d.GetOk("call_timeout"); ok {
		c.CallTimeout = time.Duration(v.(int)) * time.Second
	}
	if v, ok := d.GetOk("login_timeout"); ok {
		c.LoginTimeout = time.Duration(v.(int)) * time.Second
	}
	if v, ok := d.GetOk("refresh_leeway"); ok {
		c.RefreshLeeway = time.Duration(v.(int)) * time.Second
	}
	if v, ok := d.GetOk("trusted_hosts"); ok {
		hosts, err := parseTrustedHosts(v.([]string))
		if err != nil {
			return logical.ErrorResponse(err.Error()), nil
		}
		c.TrustedHosts = hosts
	}
	if v, ok := d.GetOk("trusted_ca_pem"); ok {
		c.TrustedCAPEM = strings.TrimSpace(v.(string))
	}
	if err := c.validate(); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	if err := putConfig(ctx, req.Storage, c); err != nil {
		return nil, err
	}
	return b.configRead(ctx, req, d)
}

func (c *configEntry) validate() error {
	if c.ClientID == "" {
		return errors.New("client_id is required")
	}
	if err := checkClientURL("client_id", c.ClientID); err != nil {
		return err
	}
	if u, _ := url.Parse(c.ClientID); u.Scheme != "https" && !c.trusts(u.Host) {
		return errors.New("client_id must be an https URL")
	}
	if len(c.RedirectURIs) == 0 {
		return errors.New("redirect_uris is required")
	}
	for _, r := range c.RedirectURIs {
		if err := checkClientURL("redirect_uris entry", r); err != nil {
			return err
		}
	}
	if !strings.Contains(" "+c.Scope+" ", " atproto ") {
		return errors.New("scope must include atproto")
	}
	if err := checkClientURL("plc_directory_url", c.PLCDirectoryURL); err != nil {
		return err
	}
	if c.CallTimeout <= 0 || c.CallTimeout > maxCallTimeout {
		return fmt.Errorf("call_timeout must be between 1s and %s", maxCallTimeout)
	}
	if c.LoginTimeout <= 0 || c.LoginTimeout > maxLoginTimeout {
		return fmt.Errorf("login_timeout must be between 1s and %s", maxLoginTimeout)
	}
	if c.RefreshLeeway <= 0 || c.RefreshLeeway > maxRefreshLeeway {
		return fmt.Errorf("refresh_leeway must be between 1s and %s", maxRefreshLeeway)
	}
	if c.TrustedCAPEM != "" {
		if _, err := c.trustedPool(); err != nil {
			return err
		}
	}
	return nil
}

func (c *configEntry) trustedPool() (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if c.TrustedCAPEM != "" && !pool.AppendCertsFromPEM([]byte(c.TrustedCAPEM)) {
		return nil, errors.New("trusted_ca_pem holds no PEM certificate")
	}
	return pool, nil
}

func (b *backend) configRead(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	c, err := getConfig(ctx, req.Storage)
	if err != nil || c == nil {
		return nil, err
	}
	hosts := make([]string, 0, len(c.TrustedHosts))
	for name, addr := range c.TrustedHosts {
		if addr != "" {
			name += "=" + addr
		}
		hosts = append(hosts, name)
	}
	return &logical.Response{Data: map[string]interface{}{
		"client_id":         c.ClientID,
		"redirect_uris":     c.RedirectURIs,
		"scope":             c.Scope,
		"client_name":       c.ClientName,
		"plc_directory_url": c.PLCDirectoryURL,
		"call_timeout":      int64(c.CallTimeout.Seconds()),
		"login_timeout":     int64(c.LoginTimeout.Seconds()),
		"refresh_leeway":    int64(c.RefreshLeeway.Seconds()),
		"trusted_hosts":     hosts,
		"trusted_ca_set":    c.TrustedCAPEM != "",
		"client_key_kid":    c.currentKey().KID,
	}}, nil
}

func pathRotateClientKey(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "config/rotate-client-key$",
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.UpdateOperation: &framework.PathOperation{Callback: b.rotateClientKey},
		},
		HelpSynopsis: "Makes a new client key. The old public key stays in the client metadata for 24 hours.",
	}
}

func (b *backend) rotateClientKey(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	b.lock.Lock()
	defer b.lock.Unlock()
	c, bad, err := requireConfig(ctx, req.Storage)
	if bad != nil || err != nil {
		return bad, err
	}
	now := time.Now().UTC()
	key, err := newClientKey(now)
	if err != nil {
		return nil, err
	}
	c.Keys[0].RetiredAt = now
	c.Keys = append([]clientKey{key}, c.Keys...)
	c.pruneKeys(now)
	if err := putConfig(ctx, req.Storage, c); err != nil {
		return nil, err
	}
	return &logical.Response{Data: map[string]interface{}{"client_key_kid": key.KID, "published_keys": len(c.Keys)}}, nil
}

func pathClientMetadata(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "client-metadata$",
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ReadOperation: &framework.PathOperation{Callback: b.clientMetadata},
		},
		HelpSynopsis: "The atproto OAuth client metadata document, with the public client keys inline as jwks.",
	}
}

func (b *backend) clientMetadata(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	c, bad, err := requireConfig(ctx, req.Storage)
	if bad != nil || err != nil {
		return bad, err
	}
	keys := []interface{}{}
	for _, k := range c.publishedKeys(time.Now().UTC()) {
		pub, err := k.publicJWK()
		if err != nil {
			return nil, err
		}
		keys = append(keys, pub)
	}
	data := map[string]interface{}{
		"client_id":                       c.ClientID,
		"application_type":                "web",
		"grant_types":                     []string{"authorization_code", "refresh_token"},
		"response_types":                  []string{"code"},
		"redirect_uris":                   c.RedirectURIs,
		"scope":                           c.Scope,
		"dpop_bound_access_tokens":        true,
		"token_endpoint_auth_method":      clientMetadataSchema,
		"token_endpoint_auth_signing_alg": "ES256",
		"jwks":                            map[string]interface{}{"keys": keys},
	}
	if c.ClientName != "" {
		data["client_name"] = c.ClientName
	}
	return &logical.Response{Data: data}, nil
}
