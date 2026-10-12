package appctl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbao"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/stack/denoruntime"
)

const (
	NetworkDir      = "network"
	OpenBaoDir      = "openbao"
	OpenBaoName     = "network-openbao"
	OpenBaoAsset    = "bao"
	addressFile     = "address"
	tokenFile       = "token"
	keysFile        = "unseal-keys.json"
	configFile      = "bao.hcl"
	OpenBaoWaitPoll = 200 * time.Millisecond
)

const openBaoConfig = `disable_mlock = true
disable_clustering = true
ui = false
storage "inmem" {}
listener "tcp" {
  address     = "127.0.0.1:0"
  tls_disable = true
}
`

func NetworkStateDir(instanceState string) string { return filepath.Join(instanceState, NetworkDir) }

func openBaoDir(instanceState string) string {
	return filepath.Join(NetworkStateDir(instanceState), OpenBaoDir)
}

func NetworkOpenBao(instanceState string) func(context.Context) (denoruntime.OpenBao, error) {
	dir := openBaoDir(instanceState)
	return func(ctx context.Context) (denoruntime.OpenBao, error) {
		for {
			addr, aerr := os.ReadFile(filepath.Join(dir, addressFile))
			tok, terr := os.ReadFile(filepath.Join(dir, tokenFile))
			if aerr == nil && terr == nil {
				ob := denoruntime.OpenBao{Address: strings.TrimSpace(string(addr)), Token: strings.TrimSpace(string(tok))}
				if h, err := openbao.New(ob.Address, ob.Token).Health(ctx); err == nil && h.Initialized && !h.Sealed {
					return ob, nil
				}
			}
			select {
			case <-ctx.Done():
				return denoruntime.OpenBao{}, ctx.Err()
			case <-time.After(OpenBaoWaitPoll):
			}
		}
	}
}

type unsealKeys struct {
	Keys      []string `json:"keys"`
	RootToken string   `json:"rootToken"`
}

func writeSecret(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (n *networks) openBaoSpec(dir string) v1alpha1.HostProcessSpec {
	return v1alpha1.HostProcessSpec{Unit: n.ctl.c.Layout.Names().HostProcessPrefix() + OpenBaoName + ".service",
		Asset: &v1alpha1.HostProcessAsset{Name: OpenBaoAsset}, Argv: []string{"server", "-config", filepath.Join(dir, configFile)},
		WorkDir: dir, RestartPolicy: v1alpha1.HostProcessRestartOnFailure,
		Output: &v1alpha1.HostProcessOutput{Stdout: filepath.Join(dir, "bao.log"), Stderr: filepath.Join(dir, "bao.log")},
		Stop:   &v1alpha1.HostProcessStop{Signal: "SIGTERM", GraceSeconds: 10}, Placement: v1alpha1.HostProcessPlacementSupervisor}
}

func (n *networks) ensureOpenBao(ctx context.Context) (denoruntime.OpenBao, error) {
	dir := openBaoDir(n.ctl.c.Layout.StateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return denoruntime.OpenBao{}, err
	}
	if err := writeSecret(filepath.Join(dir, configFile), []byte(openBaoConfig)); err != nil {
		return denoruntime.OpenBao{}, err
	}
	ri, err := n.ctl.client(n.ctl.c.workspace(), HostProcessGVR)
	if err != nil {
		return denoruntime.OpenBao{}, err
	}
	cur, err := ri.Get(ctx, OpenBaoName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		u, err := toUnstructured(&v1alpha1.HostProcess{ObjectMeta: metav1.ObjectMeta{Name: OpenBaoName}, Spec: n.openBaoSpec(dir)}, "HostProcess")
		if err != nil {
			return denoruntime.OpenBao{}, err
		}
		if cur, err = ri.Create(ctx, u, metav1.CreateOptions{FieldManager: FieldManager}); err != nil {
			return denoruntime.OpenBao{}, err
		}
		n.ctl.c.log().Info("declared the network's OpenBao", "hostprocess", OpenBaoName)
	} else if err != nil {
		return denoruntime.OpenBao{}, err
	}
	var hp v1alpha1.HostProcess
	if err := fromUnstructured(cur, &hp); err != nil {
		return denoruntime.OpenBao{}, err
	}
	if hp.Status.Phase != v1alpha1.HostProcessPhaseRunning || len(hp.Status.Listeners) == 0 {
		return denoruntime.OpenBao{}, fmt.Errorf("the network's OpenBao is %s, not yet listening", hp.Status.Phase)
	}
	addr := "http://" + hp.Status.Listeners[0].Address
	client := openbao.New(addr, "")
	h, err := client.Health(ctx)
	if err != nil {
		return denoruntime.OpenBao{}, err
	}
	keysPath := filepath.Join(dir, keysFile)
	var k unsealKeys
	if !h.Initialized {
		res, err := client.Init(ctx, 1, 1)
		if err != nil {
			return denoruntime.OpenBao{}, err
		}
		k = unsealKeys{Keys: res.Keys, RootToken: res.RootToken}
		b, _ := json.Marshal(k)
		if err := writeSecret(keysPath, b); err != nil {
			return denoruntime.OpenBao{}, err
		}
		n.ctl.c.log().Info("initialised the network's OpenBao (in-memory storage)", "address", addr)
	} else {
		b, err := os.ReadFile(keysPath)
		if err != nil {
			return denoruntime.OpenBao{}, fmt.Errorf("the network's OpenBao is initialised and %s is unreadable: %w", keysPath, err)
		}
		if err := json.Unmarshal(b, &k); err != nil || len(k.Keys) == 0 {
			return denoruntime.OpenBao{}, fmt.Errorf("%s holds no unseal key", keysPath)
		}
	}
	if h.Sealed || !h.Initialized {
		if _, err := client.Unseal(ctx, k.Keys[0]); err != nil {
			return denoruntime.OpenBao{}, err
		}
	}
	if err := writeSecret(filepath.Join(dir, tokenFile), []byte(k.RootToken+"\n")); err != nil {
		return denoruntime.OpenBao{}, err
	}
	if err := writeSecret(filepath.Join(dir, addressFile), []byte(addr+"\n")); err != nil {
		return denoruntime.OpenBao{}, err
	}
	return denoruntime.OpenBao{Address: addr, Token: k.RootToken}, nil
}

func (n *networks) deleteOpenBao(ctx context.Context) error {
	ri, err := n.ctl.client(n.ctl.c.workspace(), HostProcessGVR)
	if err != nil {
		return err
	}
	if err := ri.Delete(ctx, OpenBaoName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func pkiCA(ctx context.Context, ob denoruntime.OpenBao) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ob.Address+"/v1/pki/ca/pem", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", ob.Token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "BEGIN CERTIFICATE") {
		return nil, fmt.Errorf("the network's PKI root CA: %d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return b, nil
}
