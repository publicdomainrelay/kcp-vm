package main

import (
	"context"
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
	pendingPrefix = "pending/"
	pendingTTL    = 10 * time.Minute
	maxPending    = 256
)

type pendingEntry struct {
	Role               string            `json:"role"`
	DID                string            `json:"did"`
	Handle             string            `json:"handle"`
	PDS                string            `json:"pds"`
	Issuer             string            `json:"issuer"`
	TokenEndpoint      string            `json:"token_endpoint"`
	RevocationEndpoint string            `json:"revocation_endpoint"`
	IssRequired        bool              `json:"iss_required"`
	RedirectURI        string            `json:"redirect_uri"`
	CodeVerifier       string            `json:"code_verifier"`
	DPoPJWK            json.RawMessage   `json:"dpop_jwk"`
	Nonces             map[string]string `json:"nonces"`
	ExpiresAt          time.Time         `json:"expires_at"`
}

func pathLoginStart(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "login/start$",
		Fields: map[string]*framework.FieldSchema{
			"role":         {Type: framework.TypeString, Description: "Name of the role to log in with.", Required: true},
			"handle":       {Type: framework.TypeString, Description: "The atproto handle to sign in. Give handle or did."},
			"did":          {Type: framework.TypeString, Description: "The DID to sign in. Give handle or did."},
			"redirect_uri": {Type: framework.TypeString, Description: "One of the configured redirect URIs. Defaults to the first."},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.UpdateOperation: &framework.PathOperation{Callback: b.loginStart},
		},
		HelpSynopsis: "Starts a sign-in: resolves the identity, pushes the authorization request and returns the URL to send the user to.",
	}
}

func pathLogin(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "login$",
		Fields: map[string]*framework.FieldSchema{
			"state": {Type: framework.TypeString, Description: "The state that login/start returned.", Required: true},
			"code":  {Type: framework.TypeString, Description: "The authorization code of the redirect.", Required: true},
			"iss":   {Type: framework.TypeString, Description: "The iss parameter of the redirect."},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.UpdateOperation: &framework.PathOperation{Callback: b.login},
		},
		HelpSynopsis: "Completes a sign-in with the code of the redirect and returns a token whose entity alias is the DID.",
	}
}

func refused(msg string) (*logical.Response, error) {
	return logical.ErrorResponse(msg), logical.ErrPermissionDenied
}

func (b *backend) pendingCount(ctx context.Context, st logical.Storage) (int, error) {
	keys, err := st.List(ctx, pendingPrefix)
	if err != nil {
		return 0, err
	}
	if len(keys) < maxPending {
		return len(keys), nil
	}
	now := time.Now()
	live := 0
	for _, k := range keys {
		raw, err := st.Get(ctx, pendingPrefix+k)
		if err != nil || raw == nil {
			continue
		}
		var p pendingEntry
		if raw.DecodeJSON(&p) != nil || !now.Before(p.ExpiresAt) {
			_ = st.Delete(ctx, pendingPrefix+k)
			continue
		}
		live++
	}
	return live, nil
}

func (b *backend) loginStart(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	roleName := strings.TrimSpace(d.Get("role").(string))
	handle := strings.TrimSpace(d.Get("handle").(string))
	did := strings.TrimSpace(d.Get("did").(string))
	if roleName == "" || (handle == "") == (did == "") {
		return logical.ErrorResponse("role and exactly one of handle and did are required"), nil
	}
	cfg, bad, err := requireConfig(ctx, req.Storage)
	if bad != nil || err != nil {
		return bad, err
	}
	role, err := getRole(ctx, req.Storage, roleName)
	if err != nil {
		return nil, err
	}
	if role == nil {
		return refused("login refused: unknown role")
	}
	redirectURI := strings.TrimSpace(d.Get("redirect_uri").(string))
	if redirectURI == "" {
		redirectURI = cfg.RedirectURIs[0]
	}
	if !contains(cfg.RedirectURIs, redirectURI) {
		return logical.ErrorResponse(fmt.Sprintf("redirect_uri %q is not one of the configured redirect_uris", redirectURI)), nil
	}
	n, err := b.pendingCount(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if n >= maxPending {
		return logical.ErrorResponse("too many sign-ins are pending; try again in a few minutes"), nil
	}
	f, err := newFetcher(cfg)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, cfg.LoginTimeout)
	defer cancel()
	var id *identity
	if handle != "" {
		id, err = f.identityFromHandle(cctx, handle)
	} else {
		id, err = f.identityFromDID(cctx, did)
	}
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	if err := role.allows(id.DID, id.Handle); err != nil {
		return refused("login refused: " + err.Error())
	}
	as, err := f.discoverAuthServer(cctx, id.PDS)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	dpop, err := newDPoPKey()
	if err != nil {
		return nil, err
	}
	state, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	verifier, err := randomToken(48)
	if err != nil {
		return nil, err
	}
	nonces := map[string]string{}
	oc := &oauthClient{f: f, issuer: as.Issuer, dpop: dpop, nonces: nonceMap(nonces)}
	hint := id.Handle
	if hint == "" {
		hint = id.DID
	}
	requestURI, err := oc.par(cctx, as, url.Values{
		"response_type": {"code"}, "code_challenge": {pkceChallenge(verifier)}, "code_challenge_method": {"S256"},
		"state": {state}, "redirect_uri": {redirectURI}, "scope": {cfg.Scope}, "login_hint": {hint},
	})
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	rawKey, err := json.Marshal(dpop)
	if err != nil {
		return nil, err
	}
	expires := time.Now().UTC().Add(pendingTTL)
	entry, err := logical.StorageEntryJSON(pendingPrefix+state, &pendingEntry{
		Role: roleName, DID: id.DID, Handle: id.Handle, PDS: id.PDS, Issuer: as.Issuer, TokenEndpoint: as.TokenEndpoint,
		RevocationEndpoint: as.RevocationEndpoint, IssRequired: as.IssParameterSupported, RedirectURI: redirectURI,
		CodeVerifier: verifier, DPoPJWK: rawKey, Nonces: nonces, ExpiresAt: expires,
	})
	if err != nil {
		return nil, err
	}
	b.lock.Lock()
	err = req.Storage.Put(ctx, entry)
	b.lock.Unlock()
	if err != nil {
		return nil, err
	}
	authURL, err := url.Parse(as.AuthorizationEndpoint)
	if err != nil {
		return nil, err
	}
	q := authURL.Query()
	q.Set("client_id", cfg.ClientID)
	q.Set("request_uri", requestURI)
	authURL.RawQuery = q.Encode()
	return &logical.Response{Data: map[string]interface{}{
		"authorization_url": authURL.String(),
		"state":             state,
		"expires_at":        expires.Format(time.RFC3339),
		"did":               id.DID,
		"pds":               id.PDS,
	}}, nil
}

func (b *backend) takePending(ctx context.Context, st logical.Storage, state string) (*pendingEntry, error) {
	b.lock.Lock()
	defer b.lock.Unlock()
	raw, err := st.Get(ctx, pendingPrefix+state)
	if err != nil || raw == nil {
		return nil, err
	}
	if err := st.Delete(ctx, pendingPrefix+state); err != nil {
		return nil, err
	}
	var p pendingEntry
	if err := raw.DecodeJSON(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (b *backend) login(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	state := strings.TrimSpace(d.Get("state").(string))
	code := strings.TrimSpace(d.Get("code").(string))
	iss := strings.TrimRight(strings.TrimSpace(d.Get("iss").(string)), "/")
	if state == "" || code == "" {
		return logical.ErrorResponse("state and code are required"), nil
	}
	p, err := b.takePending(ctx, req.Storage, state)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return refused("login refused: unknown or already used state")
	}
	if !time.Now().Before(p.ExpiresAt) {
		return refused("login refused: the state expired")
	}
	if p.IssRequired && iss == "" {
		return refused("login refused: the authorization server announces the iss parameter and the callback has none")
	}
	if iss != "" && iss != p.Issuer {
		return refused(fmt.Sprintf("login refused: iss %q is not the issuer %q the sign-in started with", iss, p.Issuer))
	}
	cfg, bad, err := requireConfig(ctx, req.Storage)
	if bad != nil || err != nil {
		return bad, err
	}
	role, err := getRole(ctx, req.Storage, p.Role)
	if err != nil {
		return nil, err
	}
	if role == nil {
		return refused("login refused: the role is gone")
	}
	f, err := newFetcher(cfg)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, cfg.LoginTimeout)
	defer cancel()
	var key jose.JSONWebKey
	if err := json.Unmarshal(p.DPoPJWK, &key); err != nil {
		return nil, err
	}
	nonces := nonceMap(p.Nonces)
	if nonces == nil {
		nonces = nonceMap{}
	}
	oc := &oauthClient{f: f, issuer: p.Issuer, dpop: &key, nonces: nonces}
	tr, err := oc.exchangeCode(cctx, p.TokenEndpoint, code, p.CodeVerifier, p.RedirectURI)
	if err != nil {
		return refused("login refused: " + err.Error())
	}
	fail := func(msg string) (*logical.Response, error) {
		if p.RevocationEndpoint != "" {
			_ = oc.revoke(cctx, p.RevocationEndpoint, tr.RefreshToken, "refresh_token")
		}
		return refused("login refused: " + msg)
	}
	if tr.Sub != p.DID {
		return fail(fmt.Sprintf("the token response is for %q, the sign-in started for %q", tr.Sub, p.DID))
	}
	if !strings.Contains(" "+tr.Scope+" ", " atproto ") {
		return fail("the granted scope does not include atproto")
	}
	id, err := f.identityFromDID(cctx, tr.Sub)
	if err != nil {
		return fail(err.Error())
	}
	as, err := f.discoverAuthServer(cctx, id.PDS)
	if err != nil {
		return fail(err.Error())
	}
	if as.Issuer != p.Issuer || id.PDS != p.PDS {
		return fail(fmt.Sprintf("the PDS of %s is %s with authorization server %s, not the issuer %s the sign-in used", tr.Sub, id.PDS, as.Issuer, p.Issuer))
	}
	if err := role.allows(p.DID, p.Handle); err != nil {
		return fail(err.Error())
	}
	now := time.Now().UTC()
	rawKey, err := json.Marshal(oc.dpop)
	if err != nil {
		return nil, err
	}
	sess := &sessionEntry{
		DID: p.DID, Handle: p.Handle, PDS: p.PDS, Issuer: p.Issuer, TokenEndpoint: p.TokenEndpoint, RevocationEndpoint: p.RevocationEndpoint,
		Scope: tr.Scope, AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken, ExpiresAt: now.Add(time.Duration(tr.ExpiresIn) * time.Second),
		DPoPJWK: rawKey, Status: statusActive, CreatedAt: now,
	}
	lock := b.sessions.forDID(p.DID)
	lock.Lock()
	err = putSession(ctx, req.Storage, sess)
	lock.Unlock()
	if err != nil {
		return nil, err
	}
	for origin, nonce := range nonces {
		b.nonces.forDID(p.DID).set(origin, nonce)
	}
	auth := &logical.Auth{
		DisplayName:  p.DID,
		InternalData: map[string]interface{}{"role": p.Role, "did": p.DID},
		Metadata:     map[string]string{"did": p.DID, "handle": p.Handle, "pds": p.PDS},
		Alias:        &logical.Alias{Name: p.DID, Metadata: map[string]string{"handle": p.Handle, "pds": p.PDS}},
	}
	if err := role.PopulateTokenAuth(auth, req); err != nil {
		return nil, err
	}
	return &logical.Response{Auth: auth}, nil
}

func (b *backend) loginRenew(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	if req.Auth == nil {
		return nil, errors.New("renew: no auth")
	}
	name, _ := req.Auth.InternalData["role"].(string)
	did, _ := req.Auth.InternalData["did"].(string)
	role, err := getRole(ctx, req.Storage, name)
	if err != nil {
		return nil, err
	}
	if role == nil {
		return refused("renew refused: the role is gone")
	}
	sess, err := getSession(ctx, req.Storage, did)
	if err != nil {
		return nil, err
	}
	if sess == nil || sess.Status != statusActive {
		return refused("renew refused: the session of " + did + " is gone or expired")
	}
	resp := &logical.Response{Auth: req.Auth}
	resp.Auth.TTL = role.TokenTTL
	resp.Auth.MaxTTL = role.TokenMaxTTL
	resp.Auth.Period = role.TokenPeriod
	return resp, nil
}
