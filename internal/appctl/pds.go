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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/manifests"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbao"
)

const (
	BidderVaultServer  = "bidder"
	BidderVaultClient  = "bidder"
	BidderVaultAccount = "bidder-vault"
	identityKeysPath   = "identity/bidder/keys"
	passwordBytes      = 24
	vaultTokenFile     = "vault-kcp.token"
	vaultKcpCAFile     = "vault-kcp-ca.pem"
	vaultTokenTTL      = 30 * 24 * time.Hour
	vaultTokenRenew    = 7 * 24 * time.Hour
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
	client, st, c, err := ctl.vaultClient(ctx, BidderVaultServer, BidderVaultClient)
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
		if err := client.PutKV(ctx, path, map[string]any{"password": password}); err != nil {
			return "", manifests.BidderVault{}, err
		}
		ctl.c.log().Info("the bidder's account password is in its own OpenBao", "server", BidderVaultServer, "path", path)
	}
	return password, manifests.BidderVault{Server: BidderVaultServer, Client: BidderVaultClient, CA: fileRef(st.Refs.CA),
		ClientDir: filepath.Dir(fileRef(c.ClientCert))}, nil
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

func (ctl *Controller) vaultReaderToken(ctx context.Context, dir, account string) (string, string, error) {
	tokenPath, caPath := filepath.Join(dir, vaultTokenFile), filepath.Join(dir, vaultKcpCAFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	if err := writeSecret(caPath, ctl.restCfg.CAData); err != nil {
		return "", "", err
	}
	if st, err := os.Stat(tokenPath); err == nil && time.Since(st.ModTime()) < vaultTokenRenew {
		return tokenPath, caPath, nil
	}
	cs, err := kubernetes.NewForConfig(clusterConfig(ctl.restCfg, ctl.c.workspace()))
	if err != nil {
		return "", "", err
	}
	secs := int64(vaultTokenTTL / time.Second)
	tr, err := cs.CoreV1().ServiceAccounts("default").CreateToken(ctx, account,
		&authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: &secs}}, metav1.CreateOptions{})
	if err != nil {
		return "", "", fmt.Errorf("minting the %s token: %w", account, err)
	}
	return tokenPath, caPath, writeSecret(tokenPath, []byte(tr.Status.Token))
}

func (ctl *Controller) bidderVault(ctx context.Context, spec v1alpha1.MarketBidderSpec, stateDir string) (manifests.BidderVault, error) {
	password, vault, err := ctl.bidderPassword(ctx)
	if err != nil {
		return vault, err
	}
	did, err := EnsurePdsAccount(ctx, spec.Identity.PdsUrl, spec.Network.CaBundleRef, spec.Identity.Handle, password)
	if err != nil {
		return vault, err
	}
	ctl.c.log().Info("the bidder's PDS account", "handle", spec.Identity.Handle, "pds", spec.Identity.PdsUrl, "did", did)
	tokenPath, caPath, err := ctl.vaultReaderToken(ctx, stateDir, BidderVaultAccount)
	if err != nil {
		return vault, err
	}
	vault.KcpServer = strings.TrimSuffix(ctl.restCfg.Host, "/") + "/clusters/" + ctl.c.workspace()
	vault.TokenFile, vault.KcpCAFile = tokenPath, caPath
	return vault, nil
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
