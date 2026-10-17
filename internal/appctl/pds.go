package appctl

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/manifests"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbao"
)

const (
	BidderVaultServer = "bidder"
	BidderVaultClient = "bidder"
	identityKeysPath  = "identity/bidder/keys"
	RepoKeyField      = "repoKeyHex"
	passwordBytes     = 24
)

var OpenBaoServerGVR = schema.GroupVersionResource{Group: v1alpha1.GroupName, Version: v1alpha1.Version, Resource: "openbaoservers"}

func fileRef(ref string) string { return strings.TrimPrefix(ref, "file:") }

func (ctl *Controller) vaultClient(ctx context.Context, server, clientName string) (*openbao.Client, v1alpha1.OpenBaoServerStatus, v1alpha1.OpenBaoServerClientRefs, error) {
	ri, err := ctl.client(ctl.c.workspace(), OpenBaoServerGVR)
	if err != nil {
		return nil, v1alpha1.OpenBaoServerStatus{}, v1alpha1.OpenBaoServerClientRefs{}, err
	}
	return ServerClient(ctx, ri, server, clientName)
}

func ServerClient(ctx context.Context, ri dynamic.ResourceInterface, server, clientName string) (*openbao.Client, v1alpha1.OpenBaoServerStatus, v1alpha1.OpenBaoServerClientRefs, error) {
	u, err := ri.Get(ctx, server, metav1.GetOptions{})
	if err != nil {
		return nil, v1alpha1.OpenBaoServerStatus{}, v1alpha1.OpenBaoServerClientRefs{}, err
	}
	var o v1alpha1.OpenBaoServer
	if err := fromUnstructured(u, &o); err != nil {
		return nil, v1alpha1.OpenBaoServerStatus{}, v1alpha1.OpenBaoServerClientRefs{}, err
	}
	st := o.Status
	if st.Address == "" || st.Refs == nil {
		return nil, st, v1alpha1.OpenBaoServerClientRefs{}, fmt.Errorf("the OpenBaoServer %s reports no address yet (phase %s)", server, st.Phase)
	}
	var c v1alpha1.OpenBaoServerClientRefs
	for _, x := range st.Clients {
		if x.Name == clientName {
			c = x
		}
	}
	if c.ClientCert == "" {
		return nil, st, c, fmt.Errorf("the OpenBaoServer %s reports no client %s yet", server, clientName)
	}
	caPEM, err := os.ReadFile(fileRef(st.Refs.CA))
	if err != nil {
		return nil, st, c, err
	}
	pool, err := openbao.CertPoolFromPEM(caPEM)
	if err != nil {
		return nil, st, c, err
	}
	cert, err := tls.LoadX509KeyPair(fileRef(c.ClientCert), fileRef(c.ClientKey))
	if err != nil {
		return nil, st, c, err
	}
	client := openbao.New(st.Address, "", openbao.WithCACertPool(pool), openbao.WithClientCertificate(cert))
	token, err := client.LoginCert(ctx, c.CertAuthMount, c.CertAuthRole)
	if err != nil {
		return nil, st, c, err
	}
	return client.WithToken(token), st, c, nil
}

func (ctl *Controller) bidderPassword(ctx context.Context) (string, manifests.BidderVault, error) {
	client, _, c, err := ctl.vaultClient(ctx, BidderVaultServer, BidderVaultClient)
	if err != nil {
		return "", manifests.BidderVault{}, err
	}
	defer client.RevokeSelf(context.Background())
	path := c.KVMount + "/data/" + identityKeysPath
	data, err := client.GetKV(ctx, path)
	var re *openbao.ResponseError
	if err != nil && !(errors.As(err, &re) && re.Status == http.StatusNotFound) {
		return "", manifests.BidderVault{}, err
	}
	password, _ := data["password"].(string)
	if password == "" {
		buf := make([]byte, passwordBytes)
		if _, err := rand.Read(buf); err != nil {
			return "", manifests.BidderVault{}, err
		}
		password = hex.EncodeToString(buf)
		if data == nil {
			data = map[string]any{}
		}
		data["password"] = password
		if err := client.PutKV(ctx, path, data); err != nil {
			return "", manifests.BidderVault{}, err
		}
		ctl.c.log().Info("the bidder's account password is in its own OpenBao", "server", BidderVaultServer, "path", path)
	}
	return password, manifests.BidderVault{Server: BidderVaultServer, Client: BidderVaultClient}, nil
}

func pdsPost(ctx context.Context, pdsURL string, caFile string, nsid string, body any) (map[string]any, int, error) {
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, 0, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, 0, fmt.Errorf("the network CA bundle %s does not parse", caFile)
	}
	c := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(pdsURL, "/")+"/xrpc/"+nsid, bytes.NewReader(b))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out map[string]any
	json.Unmarshal(raw, &out)
	if out == nil {
		out = map[string]any{"raw": strings.TrimSpace(string(raw))}
	}
	return out, resp.StatusCode, nil
}

func EnsurePdsAccount(ctx context.Context, pdsURL, caFile, handle, password string) (string, error) {
	domain := handle[strings.Index(handle, ".")+1:]
	sess, scode, err := pdsPost(ctx, pdsURL, caFile, "com.atproto.server.createSession", map[string]string{"identifier": handle, "password": password})
	if err != nil {
		return "", err
	}
	if did, _ := sess["did"].(string); scode == http.StatusOK && did != "" {
		return did, nil
	}
	out, code, err := pdsPost(ctx, pdsURL, caFile, "com.atproto.server.createAccount", map[string]string{"handle": handle, "email": handle + "@" + domain, "password": password})
	if err != nil {
		return "", err
	}
	if did, _ := out["did"].(string); code == http.StatusOK && did != "" {
		return did, nil
	}
	return "", fmt.Errorf("appctl: neither signing in to (%d %v) nor creating (%d %v) %s on %s worked", scode, sess, code, out, handle, pdsURL)
}

func (ctl *Controller) bidderVault(ctx context.Context, spec v1alpha1.MarketBidderSpec, stateDir string) (manifests.BidderVault, error) {
	var vault manifests.BidderVault
	if spec.Identity.Mode == v1alpha1.MarketIdentityModePdsAccount || spec.Profile == BidderProfileNetwork {
		password, v, err := ctl.bidderPassword(ctx)
		if err != nil {
			return v, err
		}
		did, err := EnsurePdsAccount(ctx, spec.Identity.PdsUrl, spec.Network.CaBundleRef, spec.Identity.Handle, password)
		if err != nil {
			return v, err
		}
		ctl.c.log().Info("the bidder's PDS account", "handle", spec.Identity.Handle, "pds", spec.Identity.PdsUrl, "did", did)
		vault = v
	} else {
		if _, _, _, err := ctl.vaultRefs(ctx, BidderVaultServer, BidderVaultClient); err != nil {
			return vault, err
		}
		vault = manifests.BidderVault{Server: BidderVaultServer, Client: BidderVaultClient}
	}
	if spec.Identity.Mode == v1alpha1.MarketIdentityModeLocalRepo {
		if err := ctl.ensureRepoKey(ctx); err != nil {
			return vault, err
		}
	}
	vault.KcpServer = strings.TrimSuffix(ctl.restCfg.Host, "/") + "/clusters/" + ctl.c.workspace()
	return vault, nil
}

var secp256k1N, _ = new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141", 16)

func NewRepoKeyHex() (string, error) {
	for {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		k := new(big.Int).SetBytes(buf)
		if k.Sign() > 0 && k.Cmp(secp256k1N) < 0 {
			return hex.EncodeToString(buf), nil
		}
	}
}

func (ctl *Controller) ensureRepoKey(ctx context.Context) error {
	client, _, c, err := ctl.vaultClient(ctx, BidderVaultServer, BidderVaultClient)
	if err != nil {
		return err
	}
	defer client.RevokeSelf(context.Background())
	path := c.KVMount + "/data/" + identityKeysPath
	data, err := client.GetKV(ctx, path)
	var re *openbao.ResponseError
	if err != nil && !(errors.As(err, &re) && re.Status == http.StatusNotFound) {
		return err
	}
	if k, _ := data[RepoKeyField].(string); k != "" {
		return nil
	}
	key, err := NewRepoKeyHex()
	if err != nil {
		return err
	}
	if data == nil {
		data = map[string]any{}
	}
	data[RepoKeyField] = key
	if err := client.PutKV(ctx, path, data); err != nil {
		return err
	}
	ctl.c.log().Info("the bidder's repo key is in its own OpenBao", "server", BidderVaultServer, "path", path)
	return nil
}

func (ctl *Controller) deleteVaults(ctx context.Context, owner string) (int, error) {
	ri, err := ctl.client(ctl.c.workspace(), OpenBaoServerGVR)
	if err != nil {
		return 0, err
	}
	list, err := ri.List(ctx, metav1.ListOptions{LabelSelector: OwnerLabel + "=" + owner})
	if apierrors.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	for _, o := range list.Items {
		if o.GetDeletionTimestamp() == nil {
			if err := ri.Delete(ctx, o.GetName(), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return len(list.Items), err
			}
		}
	}
	return len(list.Items), nil
}
