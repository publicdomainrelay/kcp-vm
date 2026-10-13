package appctl

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbao"
)

const (
	BidderAccount        = "bidder"
	bidderTokenFile      = "kcp.token"
	bidderKcpCAFile      = "kcp-ca.pem"
	BidderTokenTTL       = time.Hour
	BidderTokenRenew     = 20 * time.Minute
	oidcJwkPath          = "oidc-jwk/"
	SigningKeyBits       = 4096
	SigningKeyPublishFor = 25 * time.Hour
)

func BidderTokenPaths(stateDir string) (string, string) {
	return filepath.Join(stateDir, bidderTokenFile), filepath.Join(stateDir, bidderKcpCAFile)
}

func (ctl *Controller) bidderToken(ctx context.Context, stateDir string) error {
	return ctl.accountToken(ctx, BidderAccount, stateDir)
}

func (ctl *Controller) accountToken(ctx context.Context, account, stateDir string) error {
	tokenPath, caPath := BidderTokenPaths(stateDir)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	if err := writeIfDifferent(caPath, ctl.restCfg.CAData); err != nil {
		return err
	}
	if st, err := os.Stat(tokenPath); err == nil && time.Since(st.ModTime()) < BidderTokenRenew {
		return nil
	}
	cs, err := kubernetes.NewForConfig(clusterConfig(ctl.restCfg, ctl.c.workspace()))
	if err != nil {
		return err
	}
	secs := int64(BidderTokenTTL / time.Second)
	tr, err := cs.CoreV1().ServiceAccounts("default").CreateToken(ctx, account,
		&authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: &secs}}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("minting the %s token: %w", account, err)
	}
	if err := writeSecret(tokenPath, []byte(tr.Status.Token)); err != nil {
		return err
	}
	ctl.c.log().Info("a bidder component's kcp token is minted", "serviceAccount", account, "path", tokenPath,
		"expires", tr.Status.ExpirationTimestamp.UTC().Format(time.RFC3339))
	return nil
}

func writeIfDifferent(path string, data []byte) error {
	if cur, err := os.ReadFile(path); err == nil && string(cur) == string(data) {
		return nil
	}
	return writeSecret(path, data)
}

func signingKeyRole(spec v1alpha1.MarketBidderSpec) string {
	if spec.Kcp != nil {
		return "kcp"
	}
	return "firecracker"
}

func newSigningKeyPEM() (string, error) {
	key, err := rsa.GenerateKey(rand.Reader, SigningKeyBits)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

func publishedKeys(held []any, now time.Time) []any {
	var keep []any
	for _, p := range held {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		until, err := time.Parse(time.RFC3339, fmt.Sprint(m["publishUntil"]))
		if err == nil && until.After(now) {
			keep = append(keep, m)
		}
	}
	return keep
}

func rotationOf(data map[string]any) int64 {
	switch v := data["rotation"].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

func (ctl *Controller) rotateSigningKey(ctx context.Context, o v1alpha1.MarketBidder) error {
	want := int64(o.Spec.SigningKeyRotation)
	if want <= 0 || ctl.rotated[string(o.UID)] >= want {
		return nil
	}
	client, _, c, err := ctl.vaultClient(ctx, BidderVaultServer, BidderVaultClient)
	if err != nil {
		return err
	}
	defer client.RevokeSelf(context.Background())
	role := signingKeyRole(o.Spec)
	path := c.KVMount + "/data/" + oidcJwkPath + role
	data, err := client.GetKV(ctx, path)
	var re *openbao.ResponseError
	if err != nil && !(errors.As(err, &re) && re.Status == http.StatusNotFound) {
		return err
	}
	held := rotationOf(data)
	current, _ := data["pem"].(string)
	switch {
	case held >= want:
		ctl.rotated[string(o.UID)] = held
		return nil
	case current == "":
		ctl.c.log().Info("no OIDC signing key to rotate yet; the bidder's issuer makes its first key when it starts", "name", o.Name, "role", role)
		ctl.rotated[string(o.UID)] = want
		return nil
	}
	next, err := newSigningKeyPEM()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	previous, _ := data["previous"].([]any)
	previous = append([]any{map[string]any{"pem": current, "retiredAt": now.Format(time.RFC3339),
		"publishUntil": now.Add(SigningKeyPublishFor).Format(time.RFC3339)}}, publishedKeys(previous, now)...)
	out := map[string]any{"issuer": data["issuer"], "pem": next, "previous": previous, "rotation": want}
	if err := client.PutKV(ctx, path, out); err != nil {
		return err
	}
	ctl.rotated[string(o.UID)] = want
	ctl.c.log().Info("rotated the bidder's OIDC signing key; the issuer URL stays and the old key stays published",
		"name", o.Name, "role", role, "rotation", want, "published", len(previous)+1, "publishOldUntil", now.Add(SigningKeyPublishFor).Format(time.RFC3339))
	return nil
}
