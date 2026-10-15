package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
)

type nonceStore interface {
	get(origin string) string
	set(origin, nonce string)
}

type nonceMap map[string]string

func (m nonceMap) get(origin string) string { return m[origin] }
func (m nonceMap) set(origin, nonce string) { m[origin] = nonce }

type oauthError struct {
	Status      int
	Code        string
	Description string
}

func (e *oauthError) Error() string {
	msg := fmt.Sprintf("authorization server answered %d", e.Status)
	if e.Code != "" {
		msg += " " + e.Code
	}
	if e.Description != "" {
		msg += ": " + e.Description
	}
	return msg
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Sub          string `json:"sub"`
	ExpiresIn    int64  `json:"expires_in"`
}

type oauthClient struct {
	f      *fetcher
	issuer string
	dpop   *jose.JSONWebKey
	nonces nonceStore
}

func newDPoPKey() (*jose.JSONWebKey, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &jose.JSONWebKey{Key: priv, Algorithm: string(jose.ES256), Use: "sig"}, nil
}

func randomToken(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func signJWT(key *jose.JSONWebKey, opts *jose.SignerOptions, claims map[string]interface{}) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, opts)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	obj, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	return obj.CompactSerialize()
}

func dpopProof(key *jose.JSONWebKey, method, rawURL, nonce, accessToken string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	u.RawQuery, u.Fragment = "", ""
	jti, err := randomToken(16)
	if err != nil {
		return "", err
	}
	claims := map[string]interface{}{"jti": jti, "htm": strings.ToUpper(method), "htu": u.String(), "iat": time.Now().Unix()}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	if accessToken != "" {
		sum := sha256.Sum256([]byte(accessToken))
		claims["ath"] = base64.RawURLEncoding.EncodeToString(sum[:])
	}
	return signJWT(key, (&jose.SignerOptions{EmbedJWK: true}).WithType("dpop+jwt"), claims)
}

func (c *configEntry) clientAssertion(issuer string) (string, error) {
	key, err := c.currentKey().signingKey()
	if err != nil {
		return "", err
	}
	jti, err := randomToken(16)
	if err != nil {
		return "", err
	}
	now := time.Now()
	return signJWT(key, &jose.SignerOptions{}, map[string]interface{}{
		"iss": c.ClientID, "sub": c.ClientID, "aud": issuer, "jti": jti,
		"iat": now.Unix(), "exp": now.Add(clientAssertionTTL).Unix(),
	})
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (o *oauthClient) post(ctx context.Context, endpoint string, form url.Values, accessToken string) ([]byte, error) {
	origin, err := originOf(endpoint)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		assertion, err := o.f.cfg.clientAssertion(o.issuer)
		if err != nil {
			return nil, err
		}
		body := url.Values{}
		for k, v := range form {
			body[k] = v
		}
		body.Set("client_id", o.f.cfg.ClientID)
		body.Set("client_assertion_type", clientAssertionType)
		body.Set("client_assertion", assertion)
		proof, err := dpopProof(o.dpop, http.MethodPost, endpoint, o.nonces.get(origin), accessToken)
		if err != nil {
			return nil, err
		}
		u, err := o.f.checkURL(endpoint)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodPost, u.String(), strings.NewReader(body.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("DPoP", proof)
		resp, raw, err := o.f.do(ctx, req)
		if err != nil {
			return nil, err
		}
		if n := resp.Header.Get("DPoP-Nonce"); n != "" {
			o.nonces.set(origin, n)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return raw, nil
		}
		oe := &oauthError{Status: resp.StatusCode}
		var parsed struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(raw, &parsed) == nil {
			oe.Code, oe.Description = parsed.Error, parsed.Description
		}
		if attempt == 0 && oe.Code == "use_dpop_nonce" && resp.Header.Get("DPoP-Nonce") != "" {
			continue
		}
		return nil, oe
	}
}

func (o *oauthClient) par(ctx context.Context, as *authServer, form url.Values) (string, error) {
	raw, err := o.post(ctx, as.PAREndpoint, form, "")
	if err != nil {
		return "", fmt.Errorf("pushed authorization request: %w", err)
	}
	var out struct {
		RequestURI string `json:"request_uri"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.RequestURI == "" {
		return "", fmt.Errorf("pushed authorization request: no request_uri in the answer")
	}
	return out.RequestURI, nil
}

func (o *oauthClient) token(ctx context.Context, endpoint string, form url.Values) (*tokenResponse, error) {
	raw, err := o.post(ctx, endpoint, form, "")
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	var out tokenResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	if !strings.EqualFold(out.TokenType, "DPoP") {
		return nil, fmt.Errorf("token request: token_type %q, want DPoP", out.TokenType)
	}
	if out.AccessToken == "" || out.RefreshToken == "" {
		return nil, fmt.Errorf("token request: the answer has no access_token or refresh_token")
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 300
	}
	return &out, nil
}

func (o *oauthClient) exchangeCode(ctx context.Context, tokenEndpoint, code, verifier, redirectURI string) (*tokenResponse, error) {
	return o.token(ctx, tokenEndpoint, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirectURI},
	})
}

func (o *oauthClient) refresh(ctx context.Context, tokenEndpoint, refreshToken string) (*tokenResponse, error) {
	return o.token(ctx, tokenEndpoint, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}})
}

func (o *oauthClient) revoke(ctx context.Context, endpoint, token, hint string) error {
	_, err := o.post(ctx, endpoint, url.Values{"token": {token}, "token_type_hint": {hint}}, "")
	return err
}

func thumbprint(key *jose.JSONWebKey) (string, error) {
	t, err := key.Thumbprint(crypto.SHA256)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(t), nil
}
