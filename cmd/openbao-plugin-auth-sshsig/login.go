package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
	"golang.org/x/crypto/ssh"
)

func pathBind(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "bind$",
		Fields: map[string]*framework.FieldSchema{
			"role":        {Type: framework.TypeString, Description: "Name of the role to bind.", Required: true},
			"bind_secret": {Type: framework.TypeString, Description: "The one-time bind secret.", Required: true},
			"host_key":    {Type: framework.TypeString, Description: "The ed25519 ssh host public key, in authorized_keys form.", Required: true},
			"signature":   {Type: framework.TypeString, Description: "Armored sshsig over the bind secret by the host key, namespace prove-sshd.", Required: true},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.UpdateOperation: &framework.PathOperation{Callback: b.bind},
		},
		HelpSynopsis: "Binds an ed25519 ssh host key to a role, once, with the role's bind secret.",
	}
}

func pathChallenge(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "challenge$",
		Fields: map[string]*framework.FieldSchema{
			"role": {Type: framework.TypeString, Description: "Name of a bound role.", Required: true},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.UpdateOperation: &framework.PathOperation{Callback: b.challenge},
		},
		HelpSynopsis: "Issues a short-lived, single-use login challenge for a bound role.",
	}
}

func pathLogin(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "login$",
		Fields: map[string]*framework.FieldSchema{
			"role":      {Type: framework.TypeString, Description: "Name of a bound role.", Required: true},
			"challenge": {Type: framework.TypeString, Description: "A challenge issued for the role.", Required: true},
			"signature": {Type: framework.TypeString, Description: "Armored sshsig over the challenge by the bound host key, namespace prove-sshd.", Required: true},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.UpdateOperation: &framework.PathOperation{Callback: b.login},
		},
		HelpSynopsis: "Logs in with an sshsig over a challenge made by the role's bound host key.",
	}
}

func requiredStrings(d *framework.FieldData, names ...string) ([]string, *logical.Response) {
	out := make([]string, len(names))
	for i, n := range names {
		v := strings.TrimSpace(d.Get(n).(string))
		if v == "" {
			return nil, logical.ErrorResponse(fmt.Sprintf("%s is required", n))
		}
		out[i] = v
	}
	return out, nil
}

func refused(msg string) (*logical.Response, error) {
	return logical.ErrorResponse(msg), logical.ErrPermissionDenied
}

func (b *backend) bind(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	in, bad := requiredStrings(d, "role", "bind_secret", "host_key", "signature")
	if bad != nil {
		return bad, nil
	}
	name, secret, hostKeyText, signature := in[0], in[1], in[2], in[3]
	hostKey, err := parseEd25519AuthorizedKey(hostKeyText)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	b.lock.Lock()
	defer b.lock.Unlock()
	r, err := getRole(ctx, req.Storage, name)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return refused("bind refused")
	}
	if r.HostKey != "" || r.BindSecretHash == "" {
		return refused(fmt.Sprintf("bind refused: role %q is already bound", name))
	}
	sum := sha256.Sum256([]byte(secret))
	want, err := hex.DecodeString(r.BindSecretHash)
	if err != nil || subtle.ConstantTimeCompare(sum[:], want) != 1 {
		return refused("bind refused")
	}
	signer, err := verifySSHSig(signature, proveNamespace, []byte(secret))
	if err != nil {
		return refused("bind refused: " + err.Error())
	}
	if !sameKey(signer, hostKey) {
		return refused("bind refused: the signature is not by host_key")
	}
	r.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostKey)))
	r.BindSecretHash = ""
	r.BoundAt = time.Now().UTC()
	r.Challenges = nil
	if err := putRole(ctx, req.Storage, name, r); err != nil {
		return nil, err
	}
	return &logical.Response{Data: map[string]interface{}{
		"role":                 name,
		"host_key":             r.HostKey,
		"host_key_fingerprint": ssh.FingerprintSHA256(hostKey),
		"bound_at":             r.BoundAt.Format(time.RFC3339),
	}}, nil
}

func (b *backend) challenge(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	in, bad := requiredStrings(d, "role")
	if bad != nil {
		return bad, nil
	}
	name := in[0]
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	value := base64.RawURLEncoding.EncodeToString(nonce)
	b.lock.Lock()
	defer b.lock.Unlock()
	r, err := getRole(ctx, req.Storage, name)
	if err != nil {
		return nil, err
	}
	if r == nil || r.HostKey == "" {
		return refused("challenge refused")
	}
	now := time.Now().UTC()
	pruneChallenges(r, now)
	for len(r.Challenges) >= maxChallenges {
		delete(r.Challenges, oldestChallenge(r))
	}
	if r.Challenges == nil {
		r.Challenges = map[string]time.Time{}
	}
	expires := now.Add(r.ChallengeTTL)
	r.Challenges[value] = expires
	if err := putRole(ctx, req.Storage, name, r); err != nil {
		return nil, err
	}
	return &logical.Response{Data: map[string]interface{}{
		"challenge":  value,
		"expires_at": expires.Format(time.RFC3339),
		"ttl":        int64(r.ChallengeTTL.Seconds()),
	}}, nil
}

func pruneChallenges(r *roleEntry, now time.Time) {
	for c, exp := range r.Challenges {
		if !now.Before(exp) {
			delete(r.Challenges, c)
		}
	}
}

func oldestChallenge(r *roleEntry) string {
	var oldest string
	var at time.Time
	for c, exp := range r.Challenges {
		if oldest == "" || exp.Before(at) {
			oldest, at = c, exp
		}
	}
	return oldest
}

func (b *backend) login(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	in, bad := requiredStrings(d, "role", "challenge", "signature")
	if bad != nil {
		return bad, nil
	}
	name, challenge, signature := in[0], in[1], in[2]
	b.lock.Lock()
	defer b.lock.Unlock()
	r, err := getRole(ctx, req.Storage, name)
	if err != nil {
		return nil, err
	}
	if r == nil || r.HostKey == "" {
		return refused("login refused")
	}
	now := time.Now().UTC()
	expires, found := r.Challenges[challenge]
	pruneChallenges(r, now)
	delete(r.Challenges, challenge)
	if err := putRole(ctx, req.Storage, name, r); err != nil {
		return nil, err
	}
	if !found || !now.Before(expires) {
		return refused("login refused: unknown or expired challenge")
	}
	bound, err := r.hostKey()
	if err != nil {
		return nil, err
	}
	signer, err := verifySSHSig(signature, proveNamespace, []byte(challenge))
	if err != nil {
		return refused("login refused: " + err.Error())
	}
	if !sameKey(signer, bound) {
		return refused("login refused: the signature is not by the bound host key")
	}
	fingerprint := ssh.FingerprintSHA256(bound)
	auth := &logical.Auth{
		DisplayName: r.AliasName,
		InternalData: map[string]interface{}{
			"role":                 name,
			"host_key_fingerprint": fingerprint,
		},
		Metadata: map[string]string{
			"role":                 name,
			"host_key_fingerprint": fingerprint,
		},
		Alias: &logical.Alias{
			Name: r.AliasName,
			Metadata: map[string]string{
				"role": name,
			},
		},
	}
	if err := r.PopulateTokenAuth(auth, req); err != nil {
		return nil, err
	}
	return &logical.Response{Auth: auth}, nil
}

func (b *backend) loginRenew(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	if req.Auth == nil {
		return nil, errors.New("renew: no auth")
	}
	name, _ := req.Auth.InternalData["role"].(string)
	fingerprint, _ := req.Auth.InternalData["host_key_fingerprint"].(string)
	r, err := getRole(ctx, req.Storage, name)
	if err != nil {
		return nil, err
	}
	if r == nil || fingerprint == "" || r.hostKeyFingerprint() != fingerprint {
		return refused("renew refused: the role or its bound host key changed")
	}
	resp := &logical.Response{Auth: req.Auth}
	resp.Auth.TTL = r.TokenTTL
	resp.Auth.MaxTTL = r.TokenMaxTTL
	resp.Auth.Period = r.TokenPeriod
	return resp, nil
}
