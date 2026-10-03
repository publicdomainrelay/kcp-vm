package clusterprovider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/identitybundle"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbao"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

const (
	VaultUnsealFileName = "unseal.json"

	VaultPKIPath = "pki"

	VaultPKIRoleTenant = "tenant-superuser"

	vaultUnsealMode = 0o600
)

type VaultSeal struct {
	Port      int      `json:"port"`
	Keys      []string `json:"keys"`
	RootToken string   `json:"root_token"`
}

func VaultAddr(port int) string {
	return fmt.Sprintf("https://127.0.0.1:%d", port)
}

func vaultCA(dataDir string) (*x509.CertPool, error) {
	path := clusterstack.OpenBaoTLSCAPath(dataDir)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("clusterprovider: reading the cluster vault's TLS CA at %s: %w", path, err)
	}
	pool, err := openbao.CertPoolFromPEM(raw)
	if err != nil {
		return nil, fmt.Errorf("clusterprovider: the cluster vault's TLS CA at %s: %w", path, err)
	}
	return pool, nil
}

func vaultClient(dataDir string, port int, token string) (*openbao.Client, error) {
	pool, err := vaultCA(dataDir)
	if err != nil {
		return nil, err
	}
	return openbao.New(VaultAddr(port), token, openbao.WithCACertPool(pool)), nil
}

func VaultUnsealPath(dataDir string) string {
	return filepath.Join(clusterstack.OpenBaoDir(dataDir), VaultUnsealFileName)
}

func ReadVaultSeal(dataDir string) (VaultSeal, error) {
	path := VaultUnsealPath(dataDir)
	raw, err := os.ReadFile(path)
	if err != nil {
		return VaultSeal{}, fmt.Errorf("clusterprovider: reading the vault unseal material at %s: %w", path, err)
	}
	var seal VaultSeal
	if err := json.Unmarshal(raw, &seal); err != nil {
		return VaultSeal{}, fmt.Errorf("clusterprovider: the vault unseal material at %s is not JSON: %v", path, err)
	}
	if seal.RootToken == "" || len(seal.Keys) == 0 {
		return VaultSeal{}, fmt.Errorf(
			"clusterprovider: the vault unseal material at %s holds %d key(s) and a root token "+
				"of length %d; an initialised vault with no key cannot be unsealed and with no "+
				"root token cannot be applied", path, len(seal.Keys), len(seal.RootToken))
	}
	return seal, nil
}

func writeVaultSeal(dataDir string, seal VaultSeal) error {
	path := VaultUnsealPath(dataDir)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("clusterprovider: creating %s for the vault unseal material: %w", dir, err)
	}
	raw, err := json.Marshal(seal)
	if err != nil {
		return fmt.Errorf("clusterprovider: encoding the vault unseal material: %w", err)
	}
	tmp, err := os.CreateTemp(dir, VaultUnsealFileName+".*")
	if err != nil {
		return fmt.Errorf("clusterprovider: a temporary file beside %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(vaultUnsealMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("clusterprovider: the mode of %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("clusterprovider: writing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("clusterprovider: closing %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("clusterprovider: renaming %s over %s: %w", tmp.Name(), path, err)
	}
	return nil
}

func waitVaultProcess(ctx context.Context, dataDir string, port int) error {
	ctx, cancel := context.WithTimeout(ctx, GateTimeout)
	defer cancel()
	client, err := vaultClient(dataDir, port, "")
	if err != nil {
		return err
	}
	last := "no answer"
	return poll(ctx, func(ctx context.Context) (bool, error) {
		if _, err := client.Health(ctx); err == nil {
			return true, nil
		} else {
			last = err.Error()
			return false, nil
		}
	}, func() error {
		return &errNotReady{what: "the openbao process on " + VaultAddr(port), last: last}
	})
}

func EnsureVault(ctx context.Context, dataDir string, port int) (VaultSeal, error) {
	if err := waitVaultProcess(ctx, dataDir, port); err != nil {
		return VaultSeal{}, err
	}
	addr := VaultAddr(port)
	anon, err := vaultClient(dataDir, port, "")
	if err != nil {
		return VaultSeal{}, err
	}
	health, err := anon.Health(ctx)
	if err != nil {
		return VaultSeal{}, fmt.Errorf("clusterprovider: reading the vault's health at %s: %w", addr, err)
	}

	var seal VaultSeal
	if !health.Initialized {
		result, err := anon.Init(ctx, 1, 1)
		if err != nil {
			return VaultSeal{}, fmt.Errorf("clusterprovider: initialising the vault at %s: %w", addr, err)
		}
		if result.RootToken == "" || len(result.Keys) == 0 {
			return VaultSeal{}, fmt.Errorf(
				"clusterprovider: the vault at %s initialised without a root token or a key, so "+
					"no later pass could unseal or apply it", addr)
		}
		seal = VaultSeal{Port: port, Keys: result.Keys, RootToken: result.RootToken}
		if err := writeVaultSeal(dataDir, seal); err != nil {
			return VaultSeal{}, err
		}
	} else {
		seal, err = ReadVaultSeal(dataDir)
		if err != nil {
			return VaultSeal{}, err
		}
		seal.Port = port
	}

	client, err := vaultClient(dataDir, port, seal.RootToken)
	if err != nil {
		return seal, err
	}
	for _, key := range seal.Keys {
		sealed, err := client.Sealed(ctx)
		if err != nil {
			return seal, fmt.Errorf("clusterprovider: reading the seal state of the vault at %s: %w", addr, err)
		}
		if !sealed {
			break
		}
		if _, err := client.Unseal(ctx, key); err != nil {
			return seal, fmt.Errorf("clusterprovider: unsealing the vault at %s: %w", addr, err)
		}
	}
	sealed, err := client.Sealed(ctx)
	if err != nil {
		return seal, fmt.Errorf("clusterprovider: reading the seal state of the vault at %s: %w", addr, err)
	}
	if sealed {
		return seal, fmt.Errorf(
			"clusterprovider: the vault at %s is still sealed after all %d recorded unseal key(s), "+
				"so the material at %s does not unseal the store at %s",
			addr, len(seal.Keys), VaultUnsealPath(dataDir), clusterstack.OpenBaoStorageDir(dataDir))
	}
	return seal, nil
}

type vaultAdmin struct {
	addr  string
	token string
	http  *http.Client
}

func newVaultAdmin(addr, token string, pool *x509.CertPool) *vaultAdmin {
	httpClient := http.DefaultClient
	if pool != nil {
		httpClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			MinVersion: tls.VersionTLS12,
		}}}
	}
	return &vaultAdmin{addr: strings.TrimRight(addr, "/"), token: token, http: httpClient}
}

func (v *vaultAdmin) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var in io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("clusterprovider: encoding the vault request %s /v1/%s: %w", method, path, err)
		}
		in = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, v.addr+"/v1/"+path, in)
	if err != nil {
		return 0, nil, fmt.Errorf("clusterprovider: building the vault request %s /v1/%s: %w", method, path, err)
	}
	if v.token != "" {
		req.Header.Set("X-Vault-Token", v.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := v.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("clusterprovider: the vault request %s /v1/%s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("clusterprovider: reading the vault response %s /v1/%s: %w", method, path, err)
	}
	return resp.StatusCode, data, nil
}

func (v *vaultAdmin) list(ctx context.Context, path string) (map[string]json.RawMessage, error) {
	status, body, err := v.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("clusterprovider: GET /v1/%s -> HTTP %d: %s", path, status, strings.TrimSpace(string(body)))
	}
	var env struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("clusterprovider: GET /v1/%s was not JSON: %v", path, err)
	}
	return env.Data, nil
}

func (v *vaultAdmin) listNames(ctx context.Context, path string) ([]string, error) {
	status, body, err := v.do(ctx, http.MethodGet, path+"?list=true", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("clusterprovider: LIST /v1/%s -> HTTP %d: %s", path, status, strings.TrimSpace(string(body)))
	}
	var env struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("clusterprovider: LIST /v1/%s was not JSON: %v", path, err)
	}
	return env.Data.Keys, nil
}

func (v *vaultAdmin) mountExists(ctx context.Context, path string) (bool, error) {
	mounts, err := v.list(ctx, "sys/mounts")
	if err != nil {
		return false, err
	}
	_, ok := mounts[strings.Trim(path, "/")+"/"]
	return ok, nil
}

func (v *vaultAdmin) authExists(ctx context.Context, path string) (bool, error) {
	auths, err := v.list(ctx, "sys/auth")
	if err != nil {
		return false, err
	}
	_, ok := auths[strings.Trim(path, "/")+"/"]
	return ok, nil
}

func (v *vaultAdmin) post(ctx context.Context, path string, body map[string]any) error {
	status, raw, err := v.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("clusterprovider: POST /v1/%s -> HTTP %d: %s", path, status, strings.TrimSpace(string(raw)))
	}
	return nil
}

func vaultTTL(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

func vaultKeyType(kind string) string {
	switch strings.ToLower(kind) {
	case "ecdsa", "ec":
		return "ec"
	case "rsa":
		return "rsa"
	case "ed25519":
		return "ed25519"
	default:
		return kind
	}
}

// ponytail: apply is a straight replay of the layout on every bring-up; a cached marker for the structural half is the upgrade if a vault apply ever costs real time.
func ApplyClusterVault(ctx context.Context, dataDir string, seal VaultSeal) error {
	pool, err := vaultCA(dataDir)
	if err != nil {
		return err
	}
	addr := VaultAddr(seal.Port)
	client := openbao.New(addr, seal.RootToken, openbao.WithCACertPool(pool))
	admin := newVaultAdmin(addr, seal.RootToken, pool)
	layout := vaultlayout.Default()

	for _, m := range layout.Mounts {
		if m.Vault != vaultlayout.Cluster {
			continue
		}
		exists, err := admin.mountExists(ctx, m.Path)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		switch m.Kind {
		case vaultlayout.MountKV:
			if err := client.EnableKV(ctx, strings.Trim(m.Path, "/"), 2); err != nil {
				return fmt.Errorf("clusterprovider: enabling the %s KV mount in the cluster vault: %w", m.Path, err)
			}
		case vaultlayout.MountPKI:
			if err := client.MountPKI(ctx, strings.Trim(m.Path, "/")); err != nil {
				return fmt.Errorf("clusterprovider: enabling the %s PKI mount in the cluster vault: %w", m.Path, err)
			}
		}
	}

	exists, err := admin.mountExists(ctx, VaultPKIPath)
	if err != nil {
		return err
	}
	if !exists {
		if err := client.MountPKI(ctx, VaultPKIPath); err != nil {
			return fmt.Errorf("clusterprovider: enabling the %s PKI mount in the cluster vault: %w", VaultPKIPath, err)
		}
	}

	for _, p := range layout.Policies {
		if p.Vault != vaultlayout.Cluster {
			continue
		}
		if err := client.PutPolicy(ctx, p.Name, p.HCL); err != nil {
			return fmt.Errorf("clusterprovider: writing policy %s to the cluster vault: %w", p.Name, err)
		}
	}

	exists, err = admin.authExists(ctx, "approle")
	if err != nil {
		return err
	}
	if !exists {
		if err := client.EnableAppRole(ctx); err != nil {
			return fmt.Errorf("clusterprovider: enabling AppRole in the cluster vault: %w", err)
		}
	}
	for _, r := range layout.AppRoles {
		if r.Vault != vaultlayout.Cluster {
			continue
		}
		if err := client.WriteAppRole(ctx, r.Name, r.Policies, vaultTTL(r.TokenTTL)); err != nil {
			return fmt.Errorf("clusterprovider: writing AppRole %s to the cluster vault: %w", r.Name, err)
		}
	}

	for _, r := range layout.PKIRoles {
		if err := writeLayoutPKIRole(ctx, admin, VaultPKIPath, r); err != nil {
			return err
		}
	}
	return writeTenantPKIRole(ctx, admin, VaultPKIPath)
}

func writeLayoutPKIRole(ctx context.Context, admin *vaultAdmin, mount string, r vaultlayout.PKIRole) error {
	body := map[string]any{
		"allowed_domains":    r.AllowedDomains,
		"allow_subdomains":   r.AllowSubdomains,
		"allow_bare_domains": r.AllowBareDomains,
		"allow_ip_sans":      r.AllowIPSANs,
		"enforce_hostnames":  r.EnforceHostnames,
		"require_cn":         r.RequireCN,
		"client_flag":        r.ClientAuth,
		"server_flag":        r.ServerAuth,
	}
	if r.KeyType != "" {
		body["key_type"] = vaultKeyType(r.KeyType)
	}
	if r.KeyBits > 0 {
		body["key_bits"] = r.KeyBits
	}
	if len(r.KeyUsage) > 0 {
		body["key_usage"] = strings.Join(r.KeyUsage, ",")
	}
	if ttl := vaultTTL(r.TTL); ttl != "" {
		body["ttl"] = ttl
	}
	if max := vaultTTL(r.MaxTTL); max != "" {
		body["max_ttl"] = max
	}
	path := strings.Trim(mount, "/") + "/roles/" + r.Name
	if err := admin.post(ctx, path, body); err != nil {
		return fmt.Errorf("clusterprovider: writing the layout PKI role %s: %w", r.Name, err)
	}
	return nil
}

// ponytail: the tenant superuser CN is not under the layout role's socialweb.local domain, so it needs its own role; fold it into the layout if vaultlayout ever grows a superuser PKI role.
func writeTenantPKIRole(ctx context.Context, admin *vaultAdmin, mount string) error {
	body := map[string]any{
		"allow_any_name":    true,
		"enforce_hostnames": false,
		"client_flag":       true,
		"server_flag":       false,
		"require_cn":        true,
		"key_type":          "ec",
		"key_bits":          256,
		"organization":      SuperuserOrganization,
		"ttl":               "8760h",
		"max_ttl":           "87600h",
	}
	path := strings.Trim(mount, "/") + "/roles/" + VaultPKIRoleTenant
	if err := admin.post(ctx, path, body); err != nil {
		return fmt.Errorf("clusterprovider: writing the tenant PKI role %s: %w", VaultPKIRoleTenant, err)
	}
	return nil
}

func ImportClientCA(ctx context.Context, dataDir string, seal VaultSeal) error {
	tlsDir := clusterstack.TLSDir(dataDir)
	certPEM, err := os.ReadFile(filepath.Join(tlsDir, clientCACertName))
	if err != nil {
		return fmt.Errorf("clusterprovider: reading the cluster's client CA for import into the vault: %w", err)
	}
	keyPEM, err := os.ReadFile(filepath.Join(tlsDir, clientCAKeyName))
	if err != nil {
		return fmt.Errorf("clusterprovider: reading the cluster's client CA key for import into the vault: %w", err)
	}
	bundle := strings.TrimRight(string(certPEM), "\n") + "\n" + strings.TrimRight(string(keyPEM), "\n") + "\n"
	pool, err := vaultCA(dataDir)
	if err != nil {
		return err
	}
	admin := newVaultAdmin(VaultAddr(seal.Port), seal.RootToken, pool)
	path := strings.Trim(VaultPKIPath, "/") + "/config/ca"
	if err := admin.post(ctx, path, map[string]any{"pem_bundle": bundle}); err != nil {
		return fmt.Errorf("clusterprovider: importing the cluster's client CA into the vault: %w", err)
	}
	return nil
}

func WriteBundleToVault(ctx context.Context, dataDir string, seal VaultSeal, b *identitybundle.Bundle) error {
	data := make(map[string]any, len(b.Entries))
	for _, e := range b.Entries {
		data[e.Path] = base64.StdEncoding.EncodeToString(e.Bytes)
	}
	client, err := vaultClient(dataDir, seal.Port, seal.RootToken)
	if err != nil {
		return err
	}
	if err := client.PutKV(ctx, vaultlayout.PathClusterIdentity, data); err != nil {
		return fmt.Errorf("clusterprovider: writing the identity bundle into the cluster vault: %w", err)
	}
	return nil
}

func ReadBundleFromVault(ctx context.Context, dataDir string, seal VaultSeal) (*identitybundle.Bundle, error) {
	client, err := vaultClient(dataDir, seal.Port, seal.RootToken)
	if err != nil {
		return nil, err
	}
	data, err := client.GetKV(ctx, vaultlayout.PathClusterIdentity)
	if err != nil {
		return nil, fmt.Errorf("clusterprovider: reading the identity bundle from the cluster vault: %w", err)
	}
	b := &identitybundle.Bundle{Entries: make([]identitybundle.Entry, 0, len(identitybundle.Paths))}
	for _, path := range identitybundle.Paths {
		value, ok := data[path].(string)
		if !ok {
			return nil, fmt.Errorf(
				"clusterprovider: the identity bundle in the cluster vault holds no %q, so it is "+
					"not the 13-entry bundle k3s authored", path)
		}
		raw, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("clusterprovider: the identity bundle entry %q in the cluster vault is not base64: %v", path, err)
		}
		b.Entries = append(b.Entries, identitybundle.Entry{Path: path, Bytes: raw, ModTime: time.Now()})
	}
	return b, nil
}

type CustodyOptions struct {
	StateDir string

	Cluster string

	VaultPort int

	PostgresDSN string
}

func CustodyClusterVault(ctx context.Context, o CustodyOptions) error {
	if o.VaultPort <= 0 {
		return fmt.Errorf(
			"clusterprovider: identity custody for %s was asked for with no vault port, so the "+
				"bundle k3s just authored would be persisted to the file tree and NOT to the vault. "+
				"The vault is only custodied for a stack that leases one (PurposesFor)", o.Cluster)
	}
	dataDir, err := ClusterDataDir(o.StateDir, o.Cluster)
	if err != nil {
		return err
	}
	seal, err := ReadVaultSeal(dataDir)
	if err != nil {
		return err
	}
	if err := ImportClientCA(ctx, dataDir, seal); err != nil {
		return err
	}
	b, err := PersistedBundle(o.StateDir, o.Cluster)
	if err != nil {
		return err
	}
	if err := WriteBundleToVault(ctx, dataDir, seal, b); err != nil {
		return err
	}

	client, err := vaultClient(dataDir, o.VaultPort, seal.RootToken)
	if err != nil {
		return err
	}
	if token, err := os.ReadFile(TokenPath(dataDir)); err == nil {
		if err := client.PutKV(ctx, vaultlayout.PathClusterInstanceToken, map[string]any{
			"token": base64.StdEncoding.EncodeToString(token),
		}); err != nil {
			return fmt.Errorf("clusterprovider: writing the instance token into the cluster vault: %w", err)
		}
	}
	if o.PostgresDSN != "" {
		if err := writePostgresToVault(ctx, client, o.PostgresDSN); err != nil {
			return err
		}
	}
	return nil
}

func writePostgresToVault(ctx context.Context, client *openbao.Client, dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("clusterprovider: the postgres DSN %q for the vault: %w", dsn, err)
	}
	username := ""
	password := ""
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}
	database := strings.TrimPrefix(u.Path, "/")
	if username == "" || password == "" || database == "" {
		return fmt.Errorf("clusterprovider: the postgres DSN %q carries no complete username, password and database", dsn)
	}
	if err := client.PutKV(ctx, vaultlayout.PathClusterPostgres, map[string]any{
		"username": username,
		"password": password,
		"database": database,
	}); err != nil {
		return fmt.Errorf("clusterprovider: writing the postgres credential into the cluster vault: %w", err)
	}
	return nil
}

var ErrNoVaultBundle = errors.New(
	"clusterprovider: the cluster vault holds no identity bundle to materialise, so this wake has " +
		"no vault to restore from and the file tree is the store it falls back to")

func SeedBundleFromVault(ctx context.Context, stateDir, cluster string) error {
	dataDir, err := ClusterDataDir(stateDir, cluster)
	if err != nil {
		return err
	}
	seal, err := ReadVaultSeal(dataDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s holds no vault unseal material: %v", ErrNoVaultBundle,
				VaultUnsealPath(dataDir), err)
		}
		return fmt.Errorf(
			"clusterprovider: this cluster's vault unseal material is unreadable, so a bundle it "+
				"may hold cannot be restored and falling back to the file tree would let k3s author "+
				"NEW CAs under the same name: %w", err)
	}
	if seal.Port <= 0 {
		return fmt.Errorf("%w: the vault custody for %s names no port", ErrNoVaultBundle, cluster)
	}
	addr := VaultAddr(seal.Port)
	b, err := ReadBundleFromVault(ctx, dataDir, seal)
	if err != nil {
		if errors.Is(err, openbao.ErrNotFound) {
			return fmt.Errorf("%w: the cluster vault at %s holds no %s", ErrNoVaultBundle, addr,
				vaultlayout.PathClusterIdentity)
		}
		return err
	}
	root, err := bundleRoot(stateDir, cluster)
	if err != nil {
		return err
	}
	if err := b.Seed(root); err != nil {
		return fmt.Errorf("clusterprovider: seeding the identity bundle for %s from the vault into %s: %w", cluster, root, err)
	}
	dir, err := BundleDir(stateDir, cluster)
	if err != nil {
		return err
	}
	if err := b.Seed(dir); err != nil {
		return fmt.Errorf("clusterprovider: materialising the identity bundle for %s from the vault into %s: %w", cluster, dir, err)
	}
	return nil
}
