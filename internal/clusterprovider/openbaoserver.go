package clusterprovider

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbao"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbaoctl"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

const (
	VaultClient      = "clusterprovider"
	VaultAccessFile  = "access.json"
	VaultSyncTimeout = time.Minute
	vaultTokenReuse  = 10 * time.Minute
	vaultAccessMode  = 0o600
)

var ErrNoClusterVault = errors.New("clusterprovider: the cluster's OpenBaoServer is not placed")

type VaultAccess struct {
	Server     string `json:"server"`
	Address    string `json:"address"`
	CAFile     string `json:"caFile"`
	ClientCert string `json:"clientCert"`
	ClientKey  string `json:"clientKey"`
	CertMount  string `json:"certMount"`
	CertRole   string `json:"certRole"`
}

func VaultAccessPath(dataDir string) string {
	return filepath.Join(clusterstack.OpenBaoDir(dataDir), VaultAccessFile)
}

func ReadVaultAccess(dataDir string) (VaultAccess, error) {
	var a VaultAccess
	raw, err := os.ReadFile(VaultAccessPath(dataDir))
	if err != nil {
		return a, fmt.Errorf("clusterprovider: reading how to reach the cluster vault at %s: %w", VaultAccessPath(dataDir), err)
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, fmt.Errorf("clusterprovider: %s is not JSON: %w", VaultAccessPath(dataDir), err)
	}
	if a.Address == "" || a.CAFile == "" || a.ClientCert == "" || a.ClientKey == "" || a.CertRole == "" {
		return a, fmt.Errorf("clusterprovider: %s names no complete way to the cluster vault", VaultAccessPath(dataDir))
	}
	return a, nil
}

func writeVaultAccess(dataDir string, a VaultAccess) error {
	path := VaultAccessPath(dataDir)
	raw, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	if cur, err := os.ReadFile(path); err == nil && string(cur) == string(raw) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, vaultAccessMode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type cachedToken struct {
	token string
	addr  string
	until time.Time
}

var vaultTokens = struct {
	sync.Mutex
	m map[string]cachedToken
}{m: map[string]cachedToken{}}

func forgetVaultToken(dataDir string) {
	vaultTokens.Lock()
	delete(vaultTokens.m, dataDir)
	vaultTokens.Unlock()
}

func (a VaultAccess) Port() int {
	u, err := url.Parse(a.Address)
	if err != nil {
		return 0
	}
	p, _ := strconv.Atoi(u.Port())
	return p
}

func ClusterVaultClient(ctx context.Context, dataDir string) (*openbao.Client, VaultAccess, error) {
	return clusterVault(ctx, dataDir)
}

func clusterVault(ctx context.Context, dataDir string) (*openbao.Client, VaultAccess, error) {
	a, err := ReadVaultAccess(dataDir)
	if err != nil {
		return nil, a, err
	}
	raw, err := os.ReadFile(a.CAFile)
	if err != nil {
		return nil, a, fmt.Errorf("clusterprovider: the cluster vault's TLS CA: %w", err)
	}
	pool, err := openbao.CertPoolFromPEM(raw)
	if err != nil {
		return nil, a, err
	}
	pair, err := tls.LoadX509KeyPair(a.ClientCert, a.ClientKey)
	if err != nil {
		return nil, a, fmt.Errorf("clusterprovider: the client certificate of %s: %w", VaultClient, err)
	}
	base := openbao.New(a.Address, "", openbao.WithCACertPool(pool), openbao.WithClientCertificate(pair))
	key := dataDir
	vaultTokens.Lock()
	t, ok := vaultTokens.m[key]
	vaultTokens.Unlock()
	if ok && t.addr == a.Address && time.Now().Before(t.until) {
		return base.WithToken(t.token), a, nil
	}
	tok, err := base.LoginCert(ctx, a.CertMount, a.CertRole)
	if err != nil {
		return nil, a, fmt.Errorf("clusterprovider: logging in to the cluster vault at %s as %s: %w", a.Address, a.CertRole, err)
	}
	vaultTokens.Lock()
	vaultTokens.m[key] = cachedToken{token: tok, addr: a.Address, until: time.Now().Add(vaultTokenReuse)}
	vaultTokens.Unlock()
	return base.WithToken(tok), a, nil
}

func (h *UnitsHost) VaultServerName(key string) string {
	return ObjectName(key, MemberName(procgroup.ComponentOpenBao))
}

func (h *UnitsHost) vaultServer(ctx context.Context, key string) (*v1alpha1.OpenBaoServer, error) {
	u, err := h.obs.Get(ctx, h.VaultServerName(key), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o, err := decodeObj[v1alpha1.OpenBaoServer](u)
	return &o, err
}

func vaultSynced(o *v1alpha1.OpenBaoServer) bool {
	if o == nil || o.Status.ObservedGeneration != o.Generation || o.Status.Phase != v1alpha1.OpenBaoServerPhaseReady {
		return false
	}
	c := meta.FindStatusCondition(o.Status.Conditions, openbaoctl.ConditionSynced)
	return c != nil && c.Status == metav1.ConditionTrue && c.ObservedGeneration == o.Generation
}

func vaultState(o *v1alpha1.OpenBaoServer) string {
	if o == nil {
		return "no OpenBaoServer"
	}
	s := fmt.Sprintf("%s %s", o.Status.Phase, o.Status.Message)
	if c := meta.FindStatusCondition(o.Status.Conditions, openbaoctl.ConditionSynced); c != nil {
		s += fmt.Sprintf("; Synced=%s %s", c.Status, c.Message)
	}
	return strings.TrimSpace(s)
}

func (h *UnitsHost) vaultSpec(key string, st procgroup.Step, declared *v1alpha1.OpenBaoServerDeclared) (v1alpha1.OpenBaoServerSpec, error) {
	dataDir, err := ClusterDataDir(h.c.StateDir, key)
	if err != nil {
		return v1alpha1.OpenBaoServerSpec{}, err
	}
	store, ok := h.Store(key)
	if !ok {
		return v1alpha1.OpenBaoServerSpec{}, fmt.Errorf("%w (cluster %s)", procgroup.ErrPostgresUnavailable, key)
	}
	if len(st.Spec.Argv) == 0 || st.Spec.Listen == "" {
		return v1alpha1.OpenBaoServerSpec{}, fmt.Errorf("clusterprovider: the vault step of %s names no executable or no listen address", key)
	}
	exe := st.Spec.Argv[0]
	if !filepath.IsAbs(exe) {
		p, err := h.c.Resolve(exe)
		if err != nil {
			return v1alpha1.OpenBaoServerSpec{}, fmt.Errorf("clusterprovider: the vault of %s runs %s, which is not on this host: %w", key, exe, err)
		}
		if exe, err = filepath.Abs(p); err != nil {
			return v1alpha1.OpenBaoServerSpec{}, err
		}
	}
	deps := memberDeps(h, key, procgroup.ComponentOpenBao, "")
	return v1alpha1.OpenBaoServerSpec{Exec: exe, Listen: st.Spec.Listen, RestartPolicy: v1alpha1.HostProcessRestartOnFailure,
		Unit: h.Unit(key, MemberName(procgroup.ComponentOpenBao)), Slice: h.Slice(key), After: deps, Wants: deps, Log: st.Spec.Log,
		Storage: &v1alpha1.OpenBaoServerStorage{Postgres: &v1alpha1.OpenBaoServerPostgres{SocketDir: store.SocketDir, User: store.User,
			Database: PostgresVaultDatabase}},
		TLS: &v1alpha1.OpenBaoServerTLS{CertFile: clusterstack.OpenBaoTLSCertPath(dataDir), KeyFile: clusterstack.OpenBaoTLSKeyPath(dataDir),
			CAFile: clusterstack.OpenBaoTLSCAPath(dataDir), ClientCertOptional: true},
		Clients:  []v1alpha1.OpenBaoServerClient{{Name: VaultClient, Policies: []string{VaultClientPolicy}}},
		Declared: ClusterVaultDeclared(declared)}, nil
}

func sameJSON(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

func (h *UnitsHost) placeVault(ctx context.Context, key string, st procgroup.Step) (*v1alpha1.OpenBaoServer, error) {
	name := h.VaultServerName(key)
	for attempt := 0; ; attempt++ {
		cur, err := h.vaultServer(ctx, key)
		if err != nil {
			return nil, err
		}
		if cur != nil && cur.DeletionTimestamp != nil {
			return nil, fmt.Errorf("clusterprovider: the OpenBaoServer %s of %s is being deleted", name, key)
		}
		var prev *v1alpha1.OpenBaoServerDeclared
		if cur != nil {
			prev = cur.Spec.Declared
		}
		want, err := h.vaultSpec(key, st, prev)
		if err != nil {
			return nil, err
		}
		if cur == nil {
			o := v1alpha1.OpenBaoServer{ObjectMeta: metav1.ObjectMeta{Name: name,
				Labels: map[string]string{LabelCluster: key, LabelMember: memberLabel(MemberName(procgroup.ComponentOpenBao))}}, Spec: want}
			u, err := encodeObj(&o, "OpenBaoServer")
			if err != nil {
				return nil, err
			}
			if _, err := h.obs.Create(ctx, u, metav1.CreateOptions{}); err != nil {
				if apierrors.IsAlreadyExists(err) && attempt < 5 {
					continue
				}
				return nil, fmt.Errorf("clusterprovider: creating the OpenBaoServer of %s: %w", key, err)
			}
			h.c.Log.Info("cluster provider: the cluster's vault is an OpenBaoServer", "cluster", key, "openbaoserver", name,
				"unit", want.Unit, "slice", want.Slice)
			break
		}
		if sameJSON(cur.Spec, want) {
			break
		}
		cur.Spec = want
		u, err := encodeObj(cur, "OpenBaoServer")
		if err != nil {
			return nil, err
		}
		if _, err := h.obs.Update(ctx, u, metav1.UpdateOptions{}); err != nil {
			if apierrors.IsConflict(err) && attempt < 5 {
				continue
			}
			return nil, err
		}
		h.c.Log.Info("cluster provider: the cluster's OpenBaoServer spec changed", "cluster", key, "openbaoserver", name)
		break
	}
	o, err := h.waitVault(ctx, key, h.c.PlaceTimeout)
	if err != nil {
		return nil, err
	}
	return o, h.recordVaultAccess(key, o)
}

func (h *UnitsHost) recordVaultAccess(key string, o *v1alpha1.OpenBaoServer) error {
	dataDir, err := ClusterDataDir(h.c.StateDir, key)
	if err != nil {
		return err
	}
	a := VaultAccess{Server: o.Name, Address: o.Status.Address, CertMount: openbaoctl.CertAuthMount, CertRole: VaultClient}
	if o.Spec.TLS != nil {
		a.CAFile = o.Spec.TLS.CAFile
	}
	for _, c := range o.Status.Clients {
		if c.Name == VaultClient {
			a.ClientCert, a.ClientKey = strings.TrimPrefix(c.ClientCert, "file:"), strings.TrimPrefix(c.ClientKey, "file:")
			if c.CertAuthMount != "" {
				a.CertMount = c.CertAuthMount
			}
		}
	}
	if a.ClientCert == "" {
		return fmt.Errorf("clusterprovider: the OpenBaoServer %s reports no client %s", o.Name, VaultClient)
	}
	return writeVaultAccess(dataDir, a)
}

func (h *UnitsHost) waitVault(ctx context.Context, key string, timeout time.Duration) (*v1alpha1.OpenBaoServer, error) {
	deadline := time.Now().Add(timeout)
	for {
		o, err := h.vaultServer(ctx, key)
		if err != nil {
			return nil, err
		}
		if o == nil {
			return nil, fmt.Errorf("%w (cluster %s)", ErrNoClusterVault, key)
		}
		if vaultSynced(o) {
			return o, nil
		}
		if o.Status.Phase == v1alpha1.OpenBaoServerPhaseFailed && o.Status.ObservedGeneration == o.Generation {
			return nil, fmt.Errorf("clusterprovider: the OpenBaoServer %s of %s failed: %s", o.Name, key, vaultState(o))
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("clusterprovider: the OpenBaoServer %s of %s is not Ready and synced to generation %d after %s: %s",
				o.Name, key, o.Generation, timeout, vaultState(o))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(unitPoll):
		}
	}
}

func (h *UnitsHost) UpdateVaultDeclared(ctx context.Context, key string, mutate func(*v1alpha1.OpenBaoServerDeclared)) error {
	for attempt := 0; ; attempt++ {
		cur, err := h.vaultServer(ctx, key)
		if err != nil {
			return err
		}
		if cur == nil || cur.DeletionTimestamp != nil {
			return fmt.Errorf("%w (cluster %s)", ErrNoClusterVault, key)
		}
		next := &v1alpha1.OpenBaoServerDeclared{}
		if cur.Spec.Declared != nil {
			raw, _ := json.Marshal(cur.Spec.Declared)
			if err := json.Unmarshal(raw, next); err != nil {
				return err
			}
		}
		mutate(next)
		if sameJSON(next, cur.Spec.Declared) {
			break
		}
		cur.Spec.Declared = next
		u, err := encodeObj(cur, "OpenBaoServer")
		if err != nil {
			return err
		}
		if _, err := h.obs.Update(ctx, u, metav1.UpdateOptions{}); err != nil {
			if apierrors.IsConflict(err) && attempt < 10 {
				continue
			}
			return err
		}
		break
	}
	_, err := h.waitVault(ctx, key, VaultSyncTimeout)
	return err
}

func (h *UnitsHost) vaultSick(ctx context.Context, key string) string {
	o, err := h.vaultServer(ctx, key)
	switch {
	case err != nil:
		return err.Error()
	case o == nil:
		return "no OpenBaoServer"
	case o.DeletionTimestamp != nil:
		return "stopping"
	case o.Status.Phase != v1alpha1.OpenBaoServerPhaseReady:
		return vaultState(o)
	}
	return ""
}

func (h *UnitsHost) reapVault(ctx context.Context, key string) error {
	name := h.VaultServerName(key)
	if err := h.obs.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	forgetVaultToken(func() string { d, _ := ClusterDataDir(h.c.StateDir, key); return d }())
	deadline := time.Now().Add(h.c.ReapTimeout)
	for {
		_, err := h.hps.Get(ctx, openbaoctl.ChildName(name), metav1.GetOptions{})
		_, oerr := h.obs.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) && apierrors.IsNotFound(oerr) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("clusterprovider: the vault of %s is still placed after %s", key, h.c.ReapTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(unitPoll):
		}
	}
}
