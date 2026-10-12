package kcpserver

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	ControllerLeaseNamespace = "kube-system"
	ControllerLeaseName      = "kcp-controllers"
	SystemAdminCluster       = "system:admin"
	RootCluster              = "root"
	ProbePoll                = 100 * time.Millisecond
)

type Lease struct {
	Found     bool      `json:"found"`
	Holder    string    `json:"holder"`
	RenewTime time.Time `json:"renewTime,omitzero"`
}

func ReadSharedAdmin(shared string) (Admin, error) {
	return ReadAdmin(filepath.Join(shared, "admin.kubeconfig"))
}

func WriteFrontKubeconfig(path, front string, a Admin) error {
	if a.Token == "" || len(a.CA) == 0 {
		return errors.New("kcpserver: a consumer kubeconfig needs the shard-admin token and the CA")
	}
	c := clientcmdapi.NewConfig()
	c.AuthInfos[ShardAdminUser] = &clientcmdapi.AuthInfo{Token: a.Token}
	for name, path := range map[string]string{RootCluster: "/clusters/root", SystemAdminCluster: "/clusters/system:admin", "base": ""} {
		c.Clusters[name] = &clientcmdapi.Cluster{Server: front + path, CertificateAuthorityData: a.CA, TLSServerName: "127.0.0.1"}
		c.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: ShardAdminUser}
	}
	c.CurrentContext = RootCluster
	return clientcmd.WriteToFile(*c, path)
}

type HTTPAdmin struct {
	Admin Admin
	Front string
	http  *http.Client
}

func NewHTTPAdmin(a Admin, front string) (*HTTPAdmin, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(a.CA) {
		return nil, errors.New("kcpserver: the shared CA does not parse")
	}
	return &HTTPAdmin{Admin: a, Front: strings.TrimSuffix(front, "/"), http: &http.Client{Timeout: 10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"}}}}, nil
}

func (h *HTTPAdmin) Readyz(ctx context.Context, port int) (int, error) {
	code, _, err := Readyz(ctx, port, h.Admin)
	return code, err
}

func (h *HTTPAdmin) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.Front+path, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.Admin.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, nil
}

func leasePath() string {
	return "/clusters/" + SystemAdminCluster + "/apis/coordination.k8s.io/v1/namespaces/" + ControllerLeaseNamespace +
		"/leases/" + ControllerLeaseName
}

func (h *HTTPAdmin) Lease(ctx context.Context) (Lease, error) {
	code, b, err := h.do(ctx, http.MethodGet, leasePath(), nil)
	if err != nil {
		return Lease{}, err
	}
	if code == http.StatusNotFound {
		return Lease{}, nil
	}
	if code != http.StatusOK {
		return Lease{}, fmt.Errorf("kcpserver: reading the controller lease: %d %s", code, b)
	}
	var l struct {
		Spec struct {
			HolderIdentity *string `json:"holderIdentity"`
			RenewTime      *string `json:"renewTime"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(b, &l); err != nil {
		return Lease{}, err
	}
	out := Lease{Found: true}
	if l.Spec.HolderIdentity != nil {
		out.Holder = *l.Spec.HolderIdentity
	}
	if l.Spec.RenewTime != nil {
		out.RenewTime, _ = time.Parse(time.RFC3339Nano, *l.Spec.RenewTime)
	}
	return out, nil
}

func (h *HTTPAdmin) DeleteLease(ctx context.Context) error {
	code, b, err := h.do(ctx, http.MethodDelete, leasePath(), nil)
	if err != nil {
		return err
	}
	if code != http.StatusOK && code != http.StatusNotFound && code != http.StatusAccepted {
		return fmt.Errorf("kcpserver: deleting the controller lease: %d %s", code, b)
	}
	return nil
}

func workspacesPath(parent string) string {
	return "/clusters/" + parent + "/apis/tenancy.kcp.io/v1alpha1/workspaces"
}

func (h *HTTPAdmin) CreateWorkspace(ctx context.Context, parent, name string) error {
	obj := map[string]any{"apiVersion": "tenancy.kcp.io/v1alpha1", "kind": "Workspace", "metadata": map[string]any{"name": name}}
	code, b, err := h.do(ctx, http.MethodPost, workspacesPath(parent), obj)
	if err != nil {
		return err
	}
	if code != http.StatusCreated && code != http.StatusOK && code != http.StatusConflict {
		return fmt.Errorf("kcpserver: creating workspace %s:%s: %d %s", parent, name, code, b)
	}
	return nil
}

func (h *HTTPAdmin) WorkspacePhase(ctx context.Context, parent, name string) (string, error) {
	code, b, err := h.do(ctx, http.MethodGet, workspacesPath(parent)+"/"+name, nil)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("kcpserver: reading workspace %s:%s: %d %s", parent, name, code, b)
	}
	var ws struct {
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	}
	if err := json.Unmarshal(b, &ws); err != nil {
		return "", err
	}
	return ws.Status.Phase, nil
}

func (h *HTTPAdmin) ListWorkspaces(ctx context.Context, parent string) ([]string, error) {
	code, b, err := h.do(ctx, http.MethodGet, workspacesPath(parent), nil)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("kcpserver: listing workspaces of %s: %d %s", parent, code, b)
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	var out []string
	for _, it := range list.Items {
		out = append(out, it.Metadata.Name)
	}
	return out, nil
}

func (h *HTTPAdmin) DeleteWorkspace(ctx context.Context, parent, name string) error {
	code, b, err := h.do(ctx, http.MethodDelete, workspacesPath(parent)+"/"+name, nil)
	if err != nil {
		return err
	}
	if code != http.StatusOK && code != http.StatusAccepted && code != http.StatusNotFound {
		return fmt.Errorf("kcpserver: deleting workspace %s:%s: %d %s", parent, name, code, b)
	}
	return nil
}

func (h *HTTPAdmin) WaitWorkspaceReady(ctx context.Context, parent, name string) error {
	var last error
	for {
		phase, err := h.WorkspacePhase(ctx, parent, name)
		if err == nil && phase == "Ready" {
			return nil
		}
		last = err
		if err == nil {
			last = fmt.Errorf("phase %q", phase)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("kcpserver: workspace %s:%s not Ready: %v (%w)", parent, name, last, ctx.Err())
		case <-time.After(ProbePoll):
		}
	}
}

func (h *HTTPAdmin) ProbeWorkspace(ctx context.Context, name string) error {
	if err := h.CreateWorkspace(ctx, RootCluster, name); err != nil {
		return err
	}
	err := h.WaitWorkspaceReady(ctx, RootCluster, name)
	dctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if derr := h.DeleteWorkspace(dctx, RootCluster, name); derr != nil && err == nil {
		err = derr
	}
	return err
}

func FrontURL(port int) string { return "https://127.0.0.1:" + strconv.Itoa(port) }
