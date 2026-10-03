package vaultcert

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbao"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

const (
	PKIMount    = "pki"
	PKIRoleNode = "node-client"

	CertLoginPath = "auth/cert/login"

	NodeCertFile = "vault-client.crt"
	NodeKeyFile  = "vault-client.key"
	NodeCAFile   = "vault-ca.crt"
	NodeMetaFile = "vault-client.json"

	nodeCertTTL  = 24 * time.Hour
	nodeMaxTTL   = 168 * time.Hour
	nodeTokenTTL = "15m"
	nodeTokenMax = "1h"

	nodeFileMode = 0o600
)

var ErrNoVaultClientCA = errors.New(
	"vaultcert: the cluster vault holds no imported client CA at pki/ca/pem, so a certificate it " +
		"issues would chain to nothing and the CERT auth role would trust no issuer. Import the " +
		"cluster's client-ca into the cluster vault before minting, which is what custody does")

var ErrCertLoginRefused = errors.New("vaultcert: the CERT login was refused")

type ClientMetadata struct {
	Identity string `json:"identity"`

	Role string `json:"role"`

	VaultAddr string `json:"vaultAddr"`
}

type ClientCertificate struct {
	Metadata ClientMetadata

	CertPEM []byte

	KeyPEM []byte

	ServerCAPEM []byte

	Serial string
}

func (c ClientCertificate) WriteTo(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("vaultcert: creating the certificate directory %s: %w", dir, err)
	}
	meta, err := json.Marshal(c.Metadata)
	if err != nil {
		return fmt.Errorf("vaultcert: encoding the certificate metadata: %w", err)
	}
	for _, f := range []struct {
		name string
		data []byte
	}{
		{NodeCertFile, c.CertPEM},
		{NodeKeyFile, c.KeyPEM},
		{NodeCAFile, c.ServerCAPEM},
		{NodeMetaFile, meta},
	} {
		if err := os.WriteFile(filepath.Join(dir, f.name), f.data, nodeFileMode); err != nil {
			return fmt.Errorf("vaultcert: writing %s: %w", filepath.Join(dir, f.name), err)
		}
	}
	return nil
}

func Load(dir string) (ClientCertificate, error) {
	var out ClientCertificate
	for _, f := range []struct {
		name string
		dst  *[]byte
	}{
		{NodeCertFile, &out.CertPEM},
		{NodeKeyFile, &out.KeyPEM},
		{NodeCAFile, &out.ServerCAPEM},
	} {
		raw, err := os.ReadFile(filepath.Join(dir, f.name))
		if err != nil {
			return ClientCertificate{}, fmt.Errorf("vaultcert: reading %s: %w", filepath.Join(dir, f.name), err)
		}
		*f.dst = raw
	}
	meta, err := os.ReadFile(filepath.Join(dir, NodeMetaFile))
	if err != nil {
		return ClientCertificate{}, fmt.Errorf("vaultcert: reading %s: %w", filepath.Join(dir, NodeMetaFile), err)
	}
	if err := json.Unmarshal(meta, &out.Metadata); err != nil {
		return ClientCertificate{}, fmt.Errorf("vaultcert: the metadata at %s is not JSON: %w", filepath.Join(dir, NodeMetaFile), err)
	}
	return out, nil
}

func MintNodeCertificate(ctx context.Context, adminAddr, rootToken, nodeName, vaultAddr string, serverCAPEM []byte) (ClientCertificate, error) {
	if err := vaultlayout.ValidateCertIdentity(nodeName); err != nil {
		return ClientCertificate{}, err
	}
	return mint(ctx, adminAddr, rootToken, vaultAddr, serverCAPEM, nodeName,
		vaultlayout.NodeCertRoleName(nodeName), vaultlayout.NodePolicyName(nodeName),
		vaultlayout.NodeCertPolicyHCL(nodeName))
}

func MintESOCertificate(ctx context.Context, adminAddr, rootToken, identity, vaultAddr string, serverCAPEM []byte) (ClientCertificate, error) {
	if err := vaultlayout.ValidateCertIdentity(identity); err != nil {
		return ClientCertificate{}, err
	}
	return mint(ctx, adminAddr, rootToken, vaultAddr, serverCAPEM, identity,
		vaultlayout.RoleESO, vaultlayout.PolicyESO, vaultlayout.ESOCertPolicyHCL())
}

func mint(ctx context.Context, adminAddr, rootToken, vaultAddr string, serverCAPEM []byte, commonName, role, policy, policyHCL string) (ClientCertificate, error) {
	if len(serverCAPEM) == 0 {
		return ClientCertificate{}, errors.New(
			"vaultcert: no vault server CA was given, so the certificate would carry no way to " +
				"verify the vault it dials and every read would need server verification switched off")
	}
	pool, err := openbao.CertPoolFromPEM(serverCAPEM)
	if err != nil {
		return ClientCertificate{}, fmt.Errorf("vaultcert: the vault server CA to trust: %w", err)
	}
	clientCA, err := VaultClientCAPEM(ctx, adminAddr, rootToken, pool)
	if err != nil {
		return ClientCertificate{}, err
	}
	if err := EnsureCertAuth(ctx, adminAddr, rootToken, clientCA, pool); err != nil {
		return ClientCertificate{}, err
	}
	client := openbao.New(adminAddr, rootToken, openbao.WithCACertPool(pool))
	if err := client.PutPolicy(ctx, policy, policyHCL); err != nil {
		return ClientCertificate{}, fmt.Errorf("vaultcert: writing policy %s: %w", policy, err)
	}
	if err := writeCertRole(ctx, newRawClient(adminAddr, rootToken, adminHTTPClient(pool)), clientCA, role, commonName, policy); err != nil {
		return ClientCertificate{}, err
	}
	issued, err := client.IssueCert(ctx, PKIMount, PKIRoleNode, openbao.CertRequest{
		CommonName: commonName,
		TTL:        vaultTTLString(nodeCertTTL),
	})
	if err != nil {
		return ClientCertificate{}, fmt.Errorf("vaultcert: issuing the certificate for %s from %s: %w", commonName, adminAddr, err)
	}
	if issued.Certificate == "" || issued.PrivateKey == "" {
		return ClientCertificate{}, fmt.Errorf("vaultcert: the vault issued an incomplete certificate for %s", commonName)
	}
	leaf, err := parseLeaf([]byte(issued.Certificate))
	if err != nil {
		return ClientCertificate{}, err
	}
	if leaf.Subject.CommonName != commonName {
		return ClientCertificate{}, fmt.Errorf(
			"vaultcert: the vault issued a certificate whose common name is %q, not %q, so the "+
				"cert would authenticate as a different identity than the one that was minted",
			leaf.Subject.CommonName, commonName)
	}
	return ClientCertificate{
		Metadata:    ClientMetadata{Identity: commonName, Role: role, VaultAddr: vaultAddr},
		CertPEM:     []byte(issued.Certificate),
		KeyPEM:      []byte(issued.PrivateKey),
		ServerCAPEM: serverCAPEM,
		Serial:      issued.Serial,
	}, nil
}

func VaultClientCAPEM(ctx context.Context, adminAddr, rootToken string, pool *x509.CertPool) ([]byte, error) {
	status, body, err := newRawClient(adminAddr, rootToken, adminHTTPClient(pool)).do(ctx, http.MethodGet, "pki/ca/pem", nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound || len(bytes.TrimSpace(body)) == 0 {
		return nil, fmt.Errorf("%w (GET %s/v1/pki/ca/pem -> HTTP %d)", ErrNoVaultClientCA, adminAddr, status)
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("vaultcert: GET %s/v1/pki/ca/pem -> HTTP %d: %s", adminAddr, status, strings.TrimSpace(string(body)))
	}
	certs, err := parseCertificates(body)
	if err != nil || len(certs) == 0 {
		return nil, fmt.Errorf("%w: pki/ca/pem holds no parsable certificate: %v", ErrNoVaultClientCA, err)
	}
	foundCA := false
	for _, c := range certs {
		if c.IsCA {
			foundCA = true
		}
	}
	if !foundCA {
		return nil, fmt.Errorf("%w: pki/ca/pem holds %d certificate(s) and not one is a CA", ErrNoVaultClientCA, len(certs))
	}
	return body, nil
}

func EnsureCertAuth(ctx context.Context, adminAddr, rootToken string, clientCAPEM []byte, pool *x509.CertPool) error {
	if len(clientCAPEM) == 0 {
		return ErrNoVaultClientCA
	}
	admin := newRawClient(adminAddr, rootToken, adminHTTPClient(pool))
	auths, err := admin.listData(ctx, "sys/auth")
	if err != nil {
		return err
	}
	if _, ok := auths[vaultlayout.CertAuthPath+"/"]; !ok {
		status, body, err := admin.do(ctx, http.MethodPost, "sys/auth/"+vaultlayout.CertAuthPath, map[string]any{"type": "cert"})
		if err != nil {
			return err
		}
		if status < 200 || status > 299 {
			return fmt.Errorf("vaultcert: enabling auth/%s -> HTTP %d: %s", vaultlayout.CertAuthPath, status, strings.TrimSpace(string(body)))
		}
	}
	return writeNodePKIRole(ctx, admin)
}

func writeNodePKIRole(ctx context.Context, admin *rawClient) error {
	status, body, err := admin.do(ctx, http.MethodPost, "pki/roles/"+PKIRoleNode, map[string]any{
		"allow_any_name":    true,
		"enforce_hostnames": false,
		"require_cn":        true,
		"client_flag":       true,
		"server_flag":       false,
		"key_type":          "ec",
		"key_bits":          256,
		"ttl":               vaultTTLString(nodeCertTTL),
		"max_ttl":           vaultTTLString(nodeMaxTTL),
	})
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("vaultcert: writing PKI role %s -> HTTP %d: %s", PKIRoleNode, status, strings.TrimSpace(string(body)))
	}
	return nil
}

func writeCertRole(ctx context.Context, admin *rawClient, clientCAPEM []byte, role, commonName, policy string) error {
	status, body, err := admin.do(ctx, http.MethodPost, "auth/cert/certs/"+role, map[string]any{
		"certificate":             string(clientCAPEM),
		"allowed_common_names":    commonName,
		"display_name":            commonName,
		"token_policies":          []string{policy},
		"token_ttl":               nodeTokenTTL,
		"token_max_ttl":           nodeTokenMax,
		"token_no_default_policy": true,
	})
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("vaultcert: writing CERT role %s -> HTTP %d: %s", role, status, strings.TrimSpace(string(body)))
	}
	return nil
}

func WriteNodeSecret(ctx context.Context, adminAddr, rootToken, nodeName string, data map[string]any, serverCAPEM []byte) error {
	if err := vaultlayout.ValidateCertIdentity(nodeName); err != nil {
		return err
	}
	pool, err := openbao.CertPoolFromPEM(serverCAPEM)
	if err != nil {
		return fmt.Errorf("vaultcert: the vault server CA to trust: %w", err)
	}
	return openbao.New(adminAddr, rootToken, openbao.WithCACertPool(pool)).PutKV(ctx, vaultlayout.NodeSecretPath(nodeName), data)
}

func WriteESODemoSecret(ctx context.Context, adminAddr, rootToken string, data map[string]any, serverCAPEM []byte) error {
	pool, err := openbao.CertPoolFromPEM(serverCAPEM)
	if err != nil {
		return fmt.Errorf("vaultcert: the vault server CA to trust: %w", err)
	}
	return openbao.New(adminAddr, rootToken, openbao.WithCACertPool(pool)).PutKV(ctx, vaultlayout.PathESODemo, data)
}

func (c ClientCertificate) Login(ctx context.Context) (string, error) {
	hc, err := c.tlsClient()
	if err != nil {
		return "", err
	}
	status, body, err := newRawClient(c.Metadata.VaultAddr, "", hc).do(ctx, http.MethodPost, CertLoginPath, map[string]any{
		"name": c.Metadata.Role,
	})
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", fmt.Errorf("%w: %s -> HTTP %d: %s", ErrCertLoginRefused, c.Metadata.VaultAddr, status, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("vaultcert: the CERT login answered a body that is not JSON: %v (%s)", err, strings.TrimSpace(string(body)))
	}
	if payload.Auth.ClientToken == "" {
		return "", fmt.Errorf("vaultcert: the CERT login answered without a client token (%s)", strings.TrimSpace(string(body)))
	}
	return payload.Auth.ClientToken, nil
}

func (c ClientCertificate) ReadKV(ctx context.Context, path string) (map[string]any, error) {
	token, err := c.Login(ctx)
	if err != nil {
		return nil, err
	}
	hc, err := c.tlsClient()
	if err != nil {
		return nil, err
	}
	status, body, err := newRawClient(c.Metadata.VaultAddr, token, hc).do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, &openbao.ResponseError{Method: http.MethodGet, Path: path, Status: status, Body: strings.TrimSpace(string(body))}
	}
	var env struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("vaultcert: GET %s/v1/%s was not JSON: %v", c.Metadata.VaultAddr, path, err)
	}
	return env.Data.Data, nil
}

func (c ClientCertificate) FetchOwnSecret(ctx context.Context) (map[string]any, error) {
	if c.Metadata.Identity == "" {
		return nil, errors.New("vaultcert: this certificate carries no identity, so it names no per-node path to read")
	}
	return c.ReadKV(ctx, vaultlayout.NodeSecretPath(c.Metadata.Identity))
}

func (c ClientCertificate) tlsClient() (*http.Client, error) {
	pair, err := tls.X509KeyPair(c.CertPEM, c.KeyPEM)
	if err != nil {
		return nil, fmt.Errorf("vaultcert: the certificate and key are not a pair: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(c.ServerCAPEM) {
		return nil, errors.New("vaultcert: the vault server CA carries no CERTIFICATE PEM block")
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:      pool,
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS12,
	}}}, nil
}

type rawClient struct {
	addr  string
	token string
	http  *http.Client
}

func newRawClient(addr, token string, hc *http.Client) *rawClient {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &rawClient{addr: strings.TrimRight(addr, "/"), token: token, http: hc}
}

func adminHTTPClient(pool *x509.CertPool) *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:    pool,
		MinVersion: tls.VersionTLS12,
	}}}
}

func (c *rawClient) do(ctx context.Context, method, path string, in any) (int, []byte, error) {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return 0, nil, fmt.Errorf("vaultcert: encoding %s /v1/%s: %w", method, path, err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.addr+"/v1/"+strings.Trim(path, "/"), body)
	if err != nil {
		return 0, nil, fmt.Errorf("vaultcert: building %s /v1/%s: %w", method, path, err)
	}
	if c.token != "" {
		req.Header.Set("X-Vault-Token", c.token)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("vaultcert: %s /v1/%s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("vaultcert: reading %s /v1/%s: %w", method, path, err)
	}
	return resp.StatusCode, data, nil
}

func (c *rawClient) listData(ctx context.Context, path string) (map[string]json.RawMessage, error) {
	status, body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("vaultcert: GET /v1/%s -> HTTP %d: %s", path, status, strings.TrimSpace(string(body)))
	}
	var env struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("vaultcert: GET /v1/%s was not JSON: %v", path, err)
	}
	return env.Data, nil
}

func vaultTTLString(d time.Duration) string {
	return fmt.Sprintf("%dh", int(d.Hours()))
}

func parseLeaf(certPEM []byte) (*x509.Certificate, error) {
	certs, err := parseCertificates(certPEM)
	if err != nil {
		return nil, err
	}
	if len(certs) == 0 {
		return nil, errors.New("vaultcert: the certificate holds no PEM block")
	}
	return certs[0], nil
}

func parseCertificates(certPEM []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	for block, rest := pem.Decode(certPEM); block != nil; block, rest = pem.Decode(rest) {
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("vaultcert: a PEM block is not a certificate: %w", err)
		}
		out = append(out, cert)
	}
	return out, nil
}
