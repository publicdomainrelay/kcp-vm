package appctl

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

const (
	WebAppAccount        = "socialweb-computer-k8s"
	WebAppAccountNS      = "default"
	WebAppGrant          = "socialweb-computer-k8s-web-app"
	WebAppKcpDirSuffix   = ".kcp"
	WebAppKubeconfigFile = "kubeconfig"
	WebAppTokenFile      = "token"
	WebAppTokenTTL       = 30 * time.Minute
	WebAppTokenRenew     = 10 * time.Minute
	ProviderWorkspace    = "root:socialweb-provider"
	ClustersExport       = "clusters"
	foreignAccountPrefix = "system:kcp:serviceaccount:"
)

var WorkspaceGVR = schema.GroupVersionResource{Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces"}

type WebAppIdentity struct {
	Kubeconfig        string
	Dir               string
	ClustersExportURL string
}

func WebAppKcpDir(instanceState string) string {
	return ComponentStateDir(instanceState, WebAppComponent) + WebAppKcpDirSuffix
}

func ForeignAccount(cluster, namespace, name string) string {
	return foreignAccountPrefix + cluster + ":" + namespace + ":" + name
}

func WebAppRootRules(gateway string) []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{NonResourceURLs: []string{"/"}, Verbs: []string{"access"}},
		{APIGroups: []string{v1alpha1.GroupName}, Resources: []string{"gateways"}, ResourceNames: []string{gateway}, Verbs: []string{"get"}},
		{APIGroups: []string{v1alpha1.GroupName}, Resources: []string{"openbaoservers"}, ResourceNames: []string{vaultlayout.RootServerName}, Verbs: []string{"get"}},
	}
}

func WebAppProviderRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{NonResourceURLs: []string{"/"}, Verbs: []string{"access"}},
		{APIGroups: []string{"apis.kcp.io"}, Resources: []string{"apiexports/content"}, ResourceNames: []string{ClustersExport},
			Verbs: []string{"get", "list", "patch"}},
	}
}

type front struct {
	server     string
	ca         []byte
	serverName string
}

func readFront(kubeconfig string) (front, error) {
	cfg, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil {
		return front{}, err
	}
	kctx, ok := cfg.Contexts[cfg.CurrentContext]
	if !ok {
		return front{}, fmt.Errorf("appctl: %s has no current context", kubeconfig)
	}
	c, ok := cfg.Clusters[kctx.Cluster]
	if !ok {
		return front{}, fmt.Errorf("appctl: %s names no cluster %s", kubeconfig, kctx.Cluster)
	}
	u, err := url.Parse(c.Server)
	if err != nil {
		return front{}, err
	}
	ca := c.CertificateAuthorityData
	if len(ca) == 0 && c.CertificateAuthority != "" {
		if ca, err = os.ReadFile(c.CertificateAuthority); err != nil {
			return front{}, err
		}
	}
	return front{server: u.Scheme + "://" + u.Host, ca: ca, serverName: c.TLSServerName}, nil
}

func (ctl *Controller) logicalCluster(ctx context.Context, parent, name string) (string, error) {
	ri, err := ctl.client(parent, WorkspaceGVR)
	if err != nil {
		return "", err
	}
	u, err := ri.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("appctl: the workspace %s:%s: %w", parent, name, err)
	}
	cluster, _, _ := unstructured.NestedString(u.Object, "spec", "cluster")
	if cluster == "" {
		return "", fmt.Errorf("appctl: the workspace %s:%s has no logical cluster yet", parent, name)
	}
	return cluster, nil
}

func splitPath(path string) (string, string) {
	i := strings.LastIndex(path, ":")
	return path[:max(i, 0)], path[i+1:]
}

func (ctl *Controller) grant(ctx context.Context, ws, owner, user string, rules []rbacv1.PolicyRule) error {
	cs, err := kubernetes.NewForConfig(clusterConfig(ctl.restCfg, ws))
	if err != nil {
		return err
	}
	labels := map[string]string{OwnerLabel: owner}
	role := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: WebAppGrant, Labels: labels}, Rules: rules}
	have, err := cs.RbacV1().ClusterRoles().Get(ctx, WebAppGrant, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if _, err := cs.RbacV1().ClusterRoles().Create(ctx, role, metav1.CreateOptions{FieldManager: FieldManager}); err != nil {
			return fmt.Errorf("appctl: the %s ClusterRole in %s: %w", WebAppGrant, ws, err)
		}
	case err != nil:
		return err
	case !equality.Semantic.DeepEqual(have.Rules, rules):
		have.Rules, have.Labels = rules, labels
		if _, err := cs.RbacV1().ClusterRoles().Update(ctx, have, metav1.UpdateOptions{FieldManager: FieldManager}); err != nil {
			return fmt.Errorf("appctl: the %s ClusterRole in %s: %w", WebAppGrant, ws, err)
		}
	}
	subjects := []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: user}}
	ref := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: WebAppGrant}
	binding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: WebAppGrant, Labels: labels}, RoleRef: ref, Subjects: subjects}
	hb, err := cs.RbacV1().ClusterRoleBindings().Get(ctx, WebAppGrant, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if _, err := cs.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{FieldManager: FieldManager}); err != nil {
			return fmt.Errorf("appctl: the %s ClusterRoleBinding in %s: %w", WebAppGrant, ws, err)
		}
	case err != nil:
		return err
	case !equality.Semantic.DeepEqual(hb.Subjects, subjects):
		hb.Subjects, hb.Labels = subjects, labels
		if _, err := cs.RbacV1().ClusterRoleBindings().Update(ctx, hb, metav1.UpdateOptions{FieldManager: FieldManager}); err != nil {
			return fmt.Errorf("appctl: the %s ClusterRoleBinding in %s: %w", WebAppGrant, ws, err)
		}
	}
	return nil
}

func (ctl *Controller) revokeWebApp(ctx context.Context) error {
	for _, ws := range []string{ctl.c.workspace(), ProviderWorkspace} {
		cs, err := kubernetes.NewForConfig(clusterConfig(ctl.restCfg, ws))
		if err != nil {
			return err
		}
		if err := cs.RbacV1().ClusterRoleBindings().Delete(ctx, WebAppGrant, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if err := cs.RbacV1().ClusterRoles().Delete(ctx, WebAppGrant, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (ctl *Controller) webAppIdentity(ctx context.Context, o v1alpha1.Gateway, owner string, w WebApp) (WebAppIdentity, error) {
	f, err := readFront(w.FrontKubeconfig)
	if err != nil {
		return WebAppIdentity{}, err
	}
	app, err := ctl.logicalCluster(ctx, ctl.c.workspace(), ComponentWorkspace)
	if err != nil {
		return WebAppIdentity{}, err
	}
	user := ForeignAccount(app, WebAppAccountNS, WebAppAccount)
	if err := ctl.grant(ctx, ctl.c.workspace(), owner, user, WebAppRootRules(o.Name)); err != nil {
		return WebAppIdentity{}, err
	}
	if err := ctl.grant(ctx, ProviderWorkspace, owner, user, WebAppProviderRules()); err != nil {
		return WebAppIdentity{}, err
	}
	parent, name := splitPath(ProviderWorkspace)
	provider, err := ctl.logicalCluster(ctx, parent, name)
	if err != nil {
		return WebAppIdentity{}, err
	}
	dir := WebAppKcpDir(ctl.c.Layout.StateDir)
	id := WebAppIdentity{Kubeconfig: filepath.Join(dir, WebAppKubeconfigFile), Dir: dir,
		ClustersExportURL: f.server + "/services/apiexport/" + provider + "/" + ClustersExport}
	if err := ctl.webAppToken(ctx, dir); err != nil {
		return WebAppIdentity{}, err
	}
	kc := clientcmdapi.NewConfig()
	kc.Clusters["kcp"] = &clientcmdapi.Cluster{Server: f.server, CertificateAuthorityData: f.ca, TLSServerName: f.serverName}
	kc.AuthInfos[WebAppAccount] = &clientcmdapi.AuthInfo{TokenFile: filepath.Join(dir, WebAppTokenFile)}
	kc.Contexts["kcp"] = &clientcmdapi.Context{Cluster: "kcp", AuthInfo: WebAppAccount}
	kc.CurrentContext = "kcp"
	body, err := clientcmd.Write(*kc)
	if err != nil {
		return WebAppIdentity{}, err
	}
	if have, err := os.ReadFile(id.Kubeconfig); err != nil || !bytes.Equal(have, body) {
		if err := writeSecret(id.Kubeconfig, body); err != nil {
			return WebAppIdentity{}, err
		}
	}
	ctl.c.log().Info("the web app's own kcp identity", "user", user, "workspace", ctl.c.workspace()+":"+ComponentWorkspace,
		"clustersExport", id.ClustersExportURL, "kubeconfig", id.Kubeconfig)
	return id, nil
}

func (ctl *Controller) webAppToken(ctx context.Context, dir string) error {
	path := filepath.Join(dir, WebAppTokenFile)
	if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) < WebAppTokenRenew {
		return nil
	}
	cs, err := kubernetes.NewForConfig(clusterConfig(ctl.restCfg, ctl.c.workspace()+":"+ComponentWorkspace))
	if err != nil {
		return err
	}
	secs := int64(WebAppTokenTTL / time.Second)
	tr, err := cs.CoreV1().ServiceAccounts(WebAppAccountNS).CreateToken(ctx, WebAppAccount,
		&authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: &secs}}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("appctl: minting the %s token: %w", WebAppAccount, err)
	}
	if err := writeSecret(path, []byte(tr.Status.Token)); err != nil {
		return err
	}
	ctl.c.log().Info("issued the web app's kcp token", "file", path, "expires", tr.Status.ExpirationTimestamp.UTC().Format(time.RFC3339))
	return nil
}
